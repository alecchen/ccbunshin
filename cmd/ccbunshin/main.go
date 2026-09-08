package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type fileConfig struct {
	Port      int                 `json:"port"`
	Providers map[string]provider `json:"providers"`
	Routes    []route             `json:"routes"`
}
type provider struct {
	Upstream string            `json:"upstream"`
	Timeout  string            `json:"timeout,omitempty"`
	Models   map[string]string `json:"models,omitempty"`
}
type route struct {
	Pattern  string `json:"pattern"`
	Provider string `json:"provider"`
}
type loadedConfig struct {
	port      int
	providers map[string]loadedProvider
	routes    []route
}
type loadedProvider struct {
	upstream *url.URL
	timeout  time.Duration
	models   map[string]string
}
type proxy struct {
	config loadedConfig
	client *http.Client
}

func loadConfig(filename string) (loadedConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return loadedConfig{}, err
	}
	var raw fileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return loadedConfig{}, fmt.Errorf("invalid proxy config: %w", err)
	}
	if raw.Port < 1 || raw.Port > 65535 {
		return loadedConfig{}, fmt.Errorf("port must be between 1 and 65535")
	}
	if len(raw.Providers) == 0 {
		return loadedConfig{}, fmt.Errorf("at least one provider is required")
	}
	providers := make(map[string]loadedProvider, len(raw.Providers))
	for name, value := range raw.Providers {
		if name == "" || value.Upstream == "" {
			return loadedConfig{}, fmt.Errorf("provider %q requires an upstream", name)
		}
		upstream, err := url.Parse(value.Upstream)
		if err != nil || upstream.Scheme == "" || upstream.Host == "" {
			return loadedConfig{}, fmt.Errorf("provider %q upstream must be an absolute URL", name)
		}
		timeout := 60 * time.Second
		if value.Timeout != "" {
			timeout, err = time.ParseDuration(value.Timeout)
			if err != nil || timeout <= 0 {
				return loadedConfig{}, fmt.Errorf("provider %q timeout must be positive", name)
			}
		}
		providers[name] = loadedProvider{upstream: upstream, timeout: timeout, models: value.Models}
	}
	seen := map[string]bool{}
	for _, item := range raw.Routes {
		if item.Pattern == "" || item.Provider == "" {
			return loadedConfig{}, fmt.Errorf("routes require pattern and provider")
		}
		if _, ok := providers[item.Provider]; !ok {
			return loadedConfig{}, fmt.Errorf("route %q references unknown provider %q", item.Pattern, item.Provider)
		}
		if _, err := path.Match(item.Pattern, "model"); err != nil {
			return loadedConfig{}, fmt.Errorf("invalid route pattern %q: %w", item.Pattern, err)
		}
		if !strings.Contains(item.Pattern, "*") {
			if seen[item.Pattern] {
				return loadedConfig{}, fmt.Errorf("duplicate exact route %q", item.Pattern)
			}
			seen[item.Pattern] = true
		}
	}
	return loadedConfig{port: raw.Port, providers: providers, routes: raw.Routes}, nil
}

func (c loadedConfig) providerFor(model string) (loadedProvider, bool) {
	for _, item := range c.routes {
		matched, _ := path.Match(item.Pattern, model)
		if matched {
			p, ok := c.providers[item.Provider]
			return p, ok
		}
	}
	return loadedProvider{}, false
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request", http.StatusBadRequest)
		return
	}
	model, err := requestModel(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	provider, ok := p.config.providerFor(model)
	if !ok {
		http.Error(w, "no provider route for model", http.StatusBadRequest)
		return
	}
	body = rewriteModel(body, provider.models)
	target := *provider.upstream
	target.Path = strings.TrimRight(target.Path, "/") + "/" + strings.TrimLeft(r.URL.Path, "/")
	target.RawQuery = r.URL.RawQuery
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "failed to create upstream request", http.StatusBadGateway)
		return
	}
	copyHeaders(request.Header, r.Header)
	request.Header.Del("Host")
	client := p.client
	if provider.timeout > 0 {
		client = &http.Client{Timeout: provider.timeout}
	}
	response, err := client.Do(request)
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	copyHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if flusher, ok := w.(http.Flusher); ok {
		_, _ = io.Copy(flushingWriter{w, flusher}, response.Body)
		return
	}
	_, _ = io.Copy(w, response.Body)
}

func requestModel(body []byte) (string, error) {
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return "", fmt.Errorf("request body must be valid JSON")
	}
	if payload.Model == "" {
		return "", fmt.Errorf("request body requires a model")
	}
	return payload.Model, nil
}

type flushingWriter struct {
	writer  io.Writer
	flusher http.Flusher
}

func (w flushingWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.flusher.Flush()
	return n, err
}
func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Host") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
func rewriteModel(body []byte, mappings map[string]string) []byte {
	model, err := requestModel(body)
	if err != nil {
		return body
	}
	mapped, ok := mappings[model]
	if !ok {
		return body
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return body
	}
	payload["model"] = mapped
	result, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return result
}

func runServer(ctx context.Context, cfg loadedConfig) error {
	server := &http.Server{Addr: ":" + strconv.Itoa(cfg.port), Handler: &proxy{config: cfg, client: &http.Client{}}}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("proxy listening on port %d", cfg.port)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func home() string {
	value := os.Getenv("HOME")
	if value == "" {
		value, _ = os.UserHomeDir()
	}
	return value
}
func profilesDir() string {
	if value := os.Getenv("CCBUNSHIN_PROFILES_DIR"); value != "" {
		return value
	}
	return path.Join(home(), ".claude-profiles")
}
func profilePath(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("invalid profile name")
	}
	return path.Join(profilesDir(), name+".json"), nil
}

const localProfileFile = ".ccbunshin-profile"

func localProfilePath(dir string) string { return path.Join(dir, localProfileFile) }

func readLocalProfile(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if name == "" || strings.ContainsAny(name, "\r\n") {
		return "", fmt.Errorf("invalid local profile in %s", file)
	}
	if _, err := profilePath(name); err != nil {
		return "", fmt.Errorf("invalid local profile in %s: %w", file, err)
	}
	return name, nil
}

func findLocalProfile(dir string) (string, string, error) {
	for {
		file := localProfilePath(dir)
		name, err := readLocalProfile(file)
		if err == nil {
			return name, file, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		parent := path.Dir(dir)
		if parent == dir {
			return "", "", os.ErrNotExist
		}
		dir = parent
	}
}

func setLocalProfile(name string) error {
	if _, err := profilePath(name); err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	return os.WriteFile(localProfilePath(dir), []byte(name+"\n"), 0600)
}

func unsetLocalProfile() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	err = os.Remove(localProfilePath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func localCLI(args []string) error {
	if len(args) == 0 {
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		name, file, err := findLocalProfile(dir)
		if err != nil {
			return usageError("local", "no local profile found")
		}
		fmt.Printf("profile: %s\nsource: %s\n", name, file)
		return nil
	}
	if len(args) == 1 && args[0] == "--unset" {
		return unsetLocalProfile()
	}
	if len(args) != 1 {
		return usageError("local", "local requires a profile or --unset")
	}
	return setLocalProfile(args[0])
}

func resolveLaunch(args []string) (string, []string, error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:], nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", nil, err
	}
	name, _, err := findLocalProfile(dir)
	if err != nil {
		return "", nil, fmt.Errorf("launch requires a profile or local .ccbunshin-profile")
	}
	return name, args, nil
}

// resolveProvider returns the nearest .ccbunshin-profile provider for dir, or
// "" when dir is not inside a ccbunshin project (no marker, or a malformed one).
func resolveProvider(dir string) string {
	name, _, err := findLocalProfile(dir)
	if err != nil {
		return ""
	}
	return name
}

func resolveProviderCLI() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if name := resolveProvider(dir); name != "" {
		fmt.Println(name)
	}
	return nil
}

func proxyStateDir() string { return path.Join(home(), ".cache", "ccbunshin") }
func proxyConfigPath() string {
	if value := os.Getenv("CCBUNSHIN_PROXY_CONFIG"); value != "" {
		return value
	}
	return path.Join(home(), ".config", "ccbunshin", "proxy.json")
}
func proxyBinary() string {
	if value := os.Getenv("CCBUNSHIN_PROXY_BIN"); value != "" {
		return value
	}
	return os.Args[0]
}
func pidPath() string { return path.Join(proxyStateDir(), "proxy.pid") }
func logPath() string { return path.Join(proxyStateDir(), "proxy.log") }

// proxyInitTemplate is the starting point written by `ccbunshin proxy init`.
// Upstreams use reserved example.invalid hosts; replace them with real URLs.
const proxyInitTemplate = `{
  "port": 3456,
  "providers": {
    "provider1": {
      "upstream": "https://sdc-gateway.example.invalid",
      "models": {
        "claude-opus-4-1": "claude-opus-4-1"
      }
    },
    "provider2": {
      "upstream": "https://free-gateway.example.invalid"
    }
  },
  "routes": [
    {
      "pattern": "claude-*",
      "provider": "provider1"
    },
    {
      "pattern": "deepseek-v4-flash",
      "provider": "provider2"
    },
    {
      "pattern": "qwen-3.8-27b",
      "provider": "provider2"
    },
    {
      "pattern": "gpt-oss-120b",
      "provider": "provider2"
    }
  ]
}
`

func proxyInit(force bool) error {
	cfg := proxyConfigPath()
	if !force {
		if _, err := os.Stat(cfg); err == nil {
			return fmt.Errorf("proxy config exists: %s (use --force to overwrite)", cfg)
		}
	}
	if err := os.MkdirAll(filepath.Dir(cfg), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(cfg, []byte(proxyInitTemplate), 0600); err != nil {
		return err
	}
	fmt.Printf("wrote proxy template: %s\n", cfg)
	return nil
}

func profileModel(file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return ""
	}
	return payload.Model
}

func setProfileModel(name, model string) error {
	file, err := profilePath(name)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	payload["model"] = model
	updated, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	updated = append(updated, '\n')
	return os.WriteFile(file, updated, 0600)
}

func listProfiles() error {
	entries, err := os.ReadDir(profilesDir())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		model := profileModel(path.Join(profilesDir(), entry.Name()))
		if model == "" {
			model = "(no model set)"
		}
		fmt.Printf("%s\tmodel=%s\n", name, model)
	}
	return nil
}

// claudeInit is the claude wrapper shared by bash and zsh. It resolves the
// nearest project provider lazily at call time, so cd/pushd/popd work without
// any directory-change hooks. Redefining the function makes re-eval idempotent.
const claudeInit = `# ccbunshin: route claude through the nearest .ccbunshin-profile project
claude() {
    local provider
    provider="$(ccbunshin resolve-provider 2>/dev/null)"
    if [ -n "$provider" ]; then
        ccbunshin launch "$provider" "$@"
    else
        command claude "$@"
    fi
}
`

// tcshInit wraps claude in an alias. tcsh cannot express a conditional alias
// (no functions, single-line if/then/else/endif rejected), so the alias simply
// delegates to `ccbunshin run`, which resolves the nearest project provider and
// falls back to the original claude binary outside projects. The guard makes
// repeated eval a no-op.
const tcshInit = "# ccbunshin: route claude through the nearest .ccbunshin-profile project\n" +
	"if ( ! $?ccbunshin_loaded ) then\n" +
	"    set ccbunshin_loaded\n" +
	"    alias claude 'ccbunshin run \\!*'\n" +
	"endif\n"

func shellInit(shell string) (string, error) {
	switch shell {
	case "bash", "zsh":
		return claudeInit, nil
	case "tcsh":
		return tcshInit, nil
	}
	return "", fmt.Errorf("unsupported shell %q (use bash, zsh, or tcsh)", shell)
}

func initCLI() error {
	dir := profilesDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	fmt.Printf("Initialized profile directory: %s\n", dir)
	return installShellHooks()
}

func installShellHooks() error {
	hooks := []struct{ file, shell, line string }{
		{".bashrc", "bash", `eval "$(ccbunshin init bash)"`},
		{".zshrc", "zsh", `eval "$(ccbunshin init zsh)"`},
		{".tcshrc", "tcsh", "eval `ccbunshin init tcsh`"},
		{".cshrc", "tcsh", "eval `ccbunshin init tcsh`"},
	}
	for _, hook := range hooks {
		rc := path.Join(home(), hook.file)
		if _, err := os.Stat(rc); errors.Is(err, os.ErrNotExist) {
			fmt.Printf("skip: %s not found\n", rc)
			continue
		}
		data, err := os.ReadFile(rc)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "ccbunshin init "+hook.shell) {
			fmt.Printf("ok: %s already hooks %s\n", rc, hook.shell)
			continue
		}
		file, err := os.OpenFile(rc, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(file, "\n# ccbunshin project-aware claude wrapper\n%s\n", hook.line)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Printf("installed: %s hooks %s\n", rc, hook.shell)
	}
	return nil
}
func deleteProfile(name string) error {
	file, err := profilePath(name)
	if err != nil {
		return err
	}
	return os.Remove(file)
}
func uninstallCLI() error { return nil }

func doctorProfile(name string) error {
	file, err := profilePath(name)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	for _, key := range []string{"env", "hooks", "model"} {
		if _, ok := payload[key]; !ok {
			fmt.Printf("WARN %s: missing key %s\n", name, key)
		}
	}
	return nil
}

func statusProfile(name string) error {
	file, err := profilePath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(file); err != nil {
		return err
	}
	fmt.Printf("profile: %s\nmodel: %s\n", file, profileModel(file))
	return nil
}
func profileCreate(name, source string, force bool) error {
	file, err := profilePath(name)
	if err != nil {
		return err
	}
	if !force {
		if _, err := os.Stat(file); err == nil {
			return fmt.Errorf("profile exists: %s", name)
		}
	}
	if err := os.MkdirAll(profilesDir(), 0700); err != nil {
		return err
	}
	var data []byte
	if source != "" {
		data, err = os.ReadFile(source)
	} else {
		data = []byte("{\n  \"env\": {},\n  \"hooks\": {},\n  \"model\": \"\"\n}\n")
	}
	if err != nil {
		return err
	}
	if !json.Valid(data) {
		return fmt.Errorf("invalid JSON")
	}
	return os.WriteFile(file, data, 0600)
}
func buildClaudeCommand(name string, args []string) (*exec.Cmd, error) {
	file, err := profilePath(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(file); err != nil {
		return nil, err
	}
	command := exec.Command("claude", append([]string{"--settings", file}, args...)...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command, nil
}

func runExec(command *exec.Cmd) error {
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		return err
	}
	return nil
}

func runClaude(name string, args []string) error {
	command, err := buildClaudeCommand(name, args)
	if err != nil {
		return err
	}
	return runExec(command)
}

// runProjectClaude launches claude with the nearest project provider, or the
// plain claude binary when the directory is not inside a project. tcsh cannot
// express a conditional alias, so its wrapper delegates to this command.
func runProjectClaude(args []string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if name := resolveProvider(dir); name != "" {
		return runClaude(name, args)
	}
	command := exec.Command("claude", args...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return runExec(command)
}

func proxyStart() error {
	if _, err := os.Stat(pidPath()); err == nil {
		return fmt.Errorf("proxy may already be running; use status")
	}
	cfg := proxyConfigPath()
	if _, err := loadConfig(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(proxyStateDir(), 0700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	command := exec.Command(proxyBinary(), "--proxy-server")
	command.Env = append(os.Environ(), "CCBUNSHIN_PROXY_CONFIG="+cfg)
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	return os.WriteFile(pidPath(), []byte(strconv.Itoa(command.Process.Pid)), 0600)
}
func proxyPID() (int, error) {
	data, err := os.ReadFile(pidPath())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid < 1 {
		return 0, fmt.Errorf("invalid proxy pid")
	}
	return pid, nil
}
func proxyStatus() error {
	pid, err := proxyPID()
	if err != nil {
		fmt.Println("proxy stopped")
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil || process.Signal(syscall.Signal(0)) != nil {
		fmt.Println("proxy stopped")
		return nil
	}
	fmt.Printf("proxy running (pid %d)\n", pid)
	return nil
}
func proxyStop() error {
	pid, err := proxyPID()
	if err != nil {
		fmt.Println("proxy stopped")
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(pidPath())
		return nil
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	_ = os.Remove(pidPath())
	fmt.Println("proxy stopped")
	return nil
}

// version is stamped at build time via -ldflags "-X main.version=<tag>".
// Release binaries carry their release tag; locally built binaries are empty.
var version = ""

func printVersion() {
	current := strings.TrimSpace(version)
	if current == "" {
		fmt.Println("unknown (not a release build)")
		return
	}
	fmt.Println(current)
}

func updateRepo() string {
	if value := os.Getenv("CCBUNSHIN_REPO"); value != "" {
		return value
	}
	return "alecchen/ccbunshin"
}

func assetName(goos, goarch string) (string, bool) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return "ccbunshin-linux-amd64", true
	case "linux/arm64":
		return "ccbunshin-linux-arm64", true
	case "darwin/amd64":
		return "ccbunshin-darwin-amd64", true
	case "darwin/arm64":
		return "ccbunshin-darwin-arm64", true
	}
	return "", false
}

func versionParts(tag string) []int {
	tag = strings.TrimPrefix(strings.TrimSpace(tag), "v")
	parts := []int{}
	for _, field := range strings.Split(tag, ".") {
		number, err := strconv.Atoi(field)
		if err != nil {
			return parts
		}
		parts = append(parts, number)
	}
	return parts
}

// compareVersions orders dotted numeric tags; it returns -1, 0, or 1.
func compareVersions(a, b string) int {
	left, right := versionParts(a), versionParts(b)
	for i := 0; i < len(left) || i < len(right); i++ {
		la, rb := 0, 0
		if i < len(left) {
			la = left[i]
		}
		if i < len(right) {
			rb = right[i]
		}
		if la < rb {
			return -1
		}
		if la > rb {
			return 1
		}
	}
	return 0
}

func latestReleaseTag(repo string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Get("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching latest release: status %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.TagName == "" {
		return "", fmt.Errorf("latest release has no tag")
	}
	return payload.TagName, nil
}

func downloadRelease(repo, tag, file string) error {
	asset, ok := assetName(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("no release binary for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	url := "https://github.com/" + repo + "/releases/download/" + tag + "/" + asset
	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: status %d", url, response.StatusCode)
	}
	output, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, response.Body)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func updateCLI() error {
	repo := updateRepo()
	current := strings.TrimSpace(version)
	latest, err := latestReleaseTag(repo)
	if err != nil {
		return err
	}
	if current == "" {
		fmt.Printf("installed version is unknown (not a release build); latest is %s - reinstall with install.sh\n", latest)
		return nil
	}
	if compareVersions(current, latest) >= 0 {
		fmt.Printf("already up to date (current %s)\n", current)
		return nil
	}
	fmt.Printf("current is %s, latest is %s, updating to %s\n", current, latest, latest)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(executable), ".ccbunshin-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	if err := downloadRelease(repo, latest, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0755); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, executable); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	fmt.Printf("updated to %s\n", latest)
	return nil
}

const usageText = `usage: ccbunshin <command> [args]

Commands:
  init [bash|zsh|tcsh]             create the profile directory, or print a shell wrapper
  create <name> [--from <file>] [--force]
                                   create a profile
  launch [<name>] [claude args...] run Claude Code with a profile
  local [<name>|--unset]           show, set, or clear the directory-local profile
  model <name> <model>             set the default model for a profile
  list                             list profiles
  status <name>                    show a profile path and model
  doctor <name>                    check a profile for missing keys
  delete <name>                    delete a profile
  proxy <init|start|stop|status>   manage the model-routed proxy
  update                           update to the latest release
  version                          print the binary version
  help [<command>]                 show help, or help for one command

Run "ccbunshin <command> --help" for details.
`

func commandHelp(name string) (string, bool) {
	switch name {
	case "init":
		return `usage: ccbunshin init [bash|zsh|tcsh]

With no argument, create ~/.claude-profiles (mode 700), append the wrapper
hook to each of ~/.bashrc, ~/.zshrc, ~/.tcshrc, ~/.cshrc that exists (missing
files are skipped, already-hooked files are left alone), and print per-file
status to stdout.
With a shell name, print a wrapper that routes "claude" through the nearest
.ccbunshin-profile project; eval it from your shell rc:

  eval "$(ccbunshin init bash)"
  eval "$(ccbunshin init zsh)"
` + "  eval `ccbunshin init tcsh`\n", true
	case "create":
		return `usage: ccbunshin create <name> [--from <file>] [--force]

Create ~/.claude-profiles/<name>.json (mode 600). --from copies an existing
JSON file (for example examples/provider2.json); without it a blank template
is used. --force overwrites an existing profile.
`, true
	case "launch":
		return `usage: ccbunshin launch [<name>] [claude args...]

Run "claude --settings ~/.claude-profiles/<name>.json". Extra arguments are
forwarded to Claude unchanged and its exit status is returned. With no name,
use the nearest .ccbunshin-profile marker in this directory or a parent; an
explicit name takes precedence.
`, true
	case "local":
		return `usage: ccbunshin local [<name>|--unset]

With no argument, print the nearest .ccbunshin-profile marker (searching this
directory upward) and its profile. With a name, write .ccbunshin-profile in
the current directory. --unset removes it.
`, true
	case "model":
		return `usage: ccbunshin model <name> <model>

Set the "model" key in the profile for future sessions:

  ccbunshin model provider1 claude-sonnet-4-5
`, true
	case "list":
		return `usage: ccbunshin list

List profiles as "<name>\tmodel=<model>" ("(no model set)" when unset).
`, true
	case "status":
		return `usage: ccbunshin status <name>

Print the profile file path and its model.
`, true
	case "doctor":
		return `usage: ccbunshin doctor <name>

Warn about missing "env", "hooks", or "model" keys in the profile.
`, true
	case "delete":
		return `usage: ccbunshin delete <name>

Remove ~/.claude-profiles/<name>.json.
`, true
	case "proxy":
		return `usage: ccbunshin proxy <init|start|stop|status> [--force]

Manage the model-routed proxy in the background. The config comes from
$CCBUNSHIN_PROXY_CONFIG, default ~/.config/ccbunshin/proxy.json; PID and log
are stored in ~/.cache/ccbunshin/. init writes a proxy.json template;
--force overwrites an existing file.
`, true
	case "update":
		return `usage: ccbunshin update

Check the latest GitHub release (repo from $CCBUNSHIN_REPO, default
alecchen/ccbunshin) and replace this binary in place when a newer tag exists.
`, true
	case "version":
		return `usage: ccbunshin version

Print the binary version (release tag stamped at build time, "unknown (not a
release build)" for local builds; "ccbunshin --version" works too).
`, true
	case "resolve-provider":
		return `usage: ccbunshin resolve-provider

Print the nearest .ccbunshin-profile provider for the current directory, or
nothing outside a project. Used by the bash/zsh wrapper.
`, true
	case "run":
		return `usage: ccbunshin run [claude args...]

Run Claude Code with the nearest project provider, or plain "claude" outside
a project. Used by the tcsh wrapper.
`, true
	case "help":
		return `usage: ccbunshin help [<command>]

Show the command list, or detailed help for one command.
`, true
	}
	return "", false
}

// usageError pairs a bad-invocation message with the command help, so
// argument errors print usage instead of a bare one-liner.
func usageError(command, message string) error {
	if text, ok := commandHelp(command); ok {
		return fmt.Errorf("%s\n\n%s", message, text)
	}
	return errors.New(message)
}

func runCLI(args []string) error {
	if len(args) == 0 {
		fmt.Print(usageText)
		return nil
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Print(usageText)
		return nil
	}
	if args[0] == "--version" || args[0] == "-V" {
		printVersion()
		return nil
	}
	if args[0] == "help" {
		if len(args) == 1 {
			fmt.Print(usageText)
			return nil
		}
		if args[1] == "--help" || args[1] == "-h" {
			args[1] = "help"
		}
		if text, ok := commandHelp(args[1]); ok {
			fmt.Print(text)
			return nil
		}
		return fmt.Errorf("unknown command %q\n\n%s", args[1], usageText)
	}
	// "<cmd> --help" prints command help, except for run: the tcsh wrapper
	// routes "claude --help" through run, so its flags must reach Claude.
	if len(args) > 1 && (args[1] == "--help" || args[1] == "-h") && args[0] != "run" {
		if text, ok := commandHelp(args[0]); ok {
			fmt.Print(text)
			return nil
		}
	}
	switch args[0] {
	case "launch":
		name, claudeArgs, resolveErr := resolveLaunch(args[1:])
		if resolveErr != nil {
			return usageError("launch", resolveErr.Error())
		}
		return runClaude(name, claudeArgs)
	case "local":
		return localCLI(args[1:])
	case "init":
		if len(args) == 1 {
			return initCLI()
		}
		if len(args) == 2 {
			script, err := shellInit(args[1])
			if err != nil {
				return usageError("init", err.Error())
			}
			fmt.Print(script)
			return nil
		}
		return usageError("init", "init takes no argument or a shell: bash, zsh, or tcsh")
	case "create":
		if len(args) < 2 {
			return usageError("create", "create requires a profile")
		}
		source := ""
		force := false
		for i := 2; i < len(args); i++ {
			if args[i] == "--force" {
				force = true
			} else if args[i] == "--from" && i+1 < len(args) {
				i++
				source = args[i]
			}
		}
		return profileCreate(args[1], source, force)
	case "model":
		if len(args) != 3 {
			return usageError("model", "model requires a profile and model")
		}
		return setProfileModel(args[1], args[2])
	case "list":
		if len(args) != 1 {
			return usageError("list", "list takes no arguments")
		}
		return listProfiles()
	case "status":
		if len(args) != 2 {
			return usageError("status", "status requires a profile")
		}
		return statusProfile(args[1])
	case "doctor":
		if len(args) != 2 {
			return usageError("doctor", "doctor requires a profile")
		}
		return doctorProfile(args[1])
	case "delete":
		if len(args) != 2 {
			return usageError("delete", "delete requires a profile")
		}
		return deleteProfile(args[1])
	case "uninstall":
		return uninstallCLI()
	case "proxy":
		if len(args) < 2 {
			return usageError("proxy", "proxy requires init, start, stop, or status")
		}
		switch args[1] {
		case "init":
			force := len(args) > 2 && args[2] == "--force"
			if len(args) > 2 && !force || len(args) > 3 {
				return usageError("proxy", "proxy init takes only --force")
			}
			return proxyInit(force)
		case "start":
			return proxyStart()
		case "stop":
			return proxyStop()
		case "status":
			return proxyStatus()
		}
		return usageError("proxy", fmt.Sprintf("unknown proxy command %q", args[1]))
	case "resolve-provider":
		if len(args) != 1 {
			return usageError("resolve-provider", "resolve-provider takes no arguments")
		}
		return resolveProviderCLI()
	case "run":
		return runProjectClaude(args[1:])
	case "update":
		return updateCLI()
	case "version":
		if len(args) != 1 {
			return usageError("version", "version takes no arguments")
		}
		printVersion()
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usageText)
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--proxy-server" {
		cfg, err := loadConfig(proxyConfigPath())
		if err != nil {
			log.Fatal(err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runServer(ctx, cfg); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := runCLI(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
