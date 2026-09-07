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
		fmt.Printf("%s\tmodel=%s\n", name, profileModel(path.Join(profilesDir(), entry.Name())))
	}
	return nil
}

func initCLI() error { return os.MkdirAll(profilesDir(), 0700) }
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
func launchProfile(name string, args []string) error {
	file, err := profilePath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(file); err != nil {
		return err
	}
	command := exec.Command("claude", append([]string{"--settings", file}, args...)...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
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
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Println("usage: ccbunshin <init|create|launch|model|list|status|doctor|delete|uninstall|proxy>")
		return
	}
	var err error
	switch args[0] {
	case "launch":
		if len(args) < 2 {
			err = fmt.Errorf("launch requires a profile")
		} else {
			err = launchProfile(args[1], args[2:])
		}
	case "init":
		err = initCLI()
	case "create":
		if len(args) < 2 {
			err = fmt.Errorf("create requires a profile")
		} else {
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
			err = profileCreate(args[1], source, force)
		}
	case "model":
		if len(args) != 3 {
			err = fmt.Errorf("model requires a profile and model")
		} else {
			err = setProfileModel(args[1], args[2])
		}
	case "list":
		err = listProfiles()
	case "status":
		if len(args) != 2 {
			err = fmt.Errorf("status requires a profile")
		} else {
			err = statusProfile(args[1])
		}
	case "doctor":
		if len(args) != 2 {
			err = fmt.Errorf("doctor requires a profile")
		} else {
			err = doctorProfile(args[1])
		}
	case "delete":
		if len(args) != 2 {
			err = fmt.Errorf("delete requires a profile")
		} else {
			err = deleteProfile(args[1])
		}
	case "uninstall":
		err = uninstallCLI()
	case "proxy":
		if len(args) < 2 {
			err = fmt.Errorf("proxy requires start, stop, or status")
		} else if args[1] == "start" {
			err = proxyStart()
		} else if args[1] == "stop" {
			err = proxyStop()
		} else if args[1] == "status" {
			err = proxyStatus()
		} else {
			err = fmt.Errorf("unknown proxy command")
		}
	default:
		err = fmt.Errorf("command %q is not implemented in unified Go CLI yet", args[0])
	}
	if err != nil {
		log.Fatal(err)
	}
}
