package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testProvider(t *testing.T, server *httptest.Server, models map[string]string) loadedProvider {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return loadedProvider{upstream: parsed, timeout: time.Second, models: models}
}

func TestProviderForModel(t *testing.T) {
	cfg := loadedConfig{
		providers: map[string]loadedProvider{
			"paid": {},
			"free": {},
		},
		routes: []route{
			{Pattern: "claude-*", Provider: "paid"},
			{Pattern: "qwen-3.8-27b", Provider: "free"},
		},
	}
	if _, ok := cfg.providerFor("claude-sonnet-4-5"); !ok {
		t.Fatal("claude model did not match prefix route")
	}
	if _, ok := cfg.providerFor("qwen-3.8-27b"); !ok {
		t.Fatal("qwen model did not match exact route")
	}
	if _, ok := cfg.providerFor("unknown"); ok {
		t.Fatal("unknown model matched a route")
	}
}

func TestProxyForwardsRoutedRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer client-secret" {
			t.Errorf("authorization was not forwarded")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"provider-model"`) {
			t.Errorf("model was not mapped: %s", body)
		}
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("response"))
	}))
	defer upstream.Close()

	cfg := loadedConfig{
		providers: map[string]loadedProvider{
			"provider1": testProvider(t, upstream, map[string]string{"opus": "provider-model"}),
		},
		routes: []route{{Pattern: "opus", Provider: "provider1"}},
	}
	handler := &proxy{config: cfg, client: upstream.Client()}
	request := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages?x=1", strings.NewReader(`{"model":"opus"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer client-secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("X-Upstream") != "yes" {
		t.Fatal("upstream header was not forwarded")
	}
	if response.Body.String() != "response" {
		t.Fatalf("body = %q", response.Body.String())
	}
}

func TestProxyRejectsUnroutedModel(t *testing.T) {
	handler := &proxy{config: loadedConfig{routes: []route{}}, client: http.DefaultClient}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages", strings.NewReader(`{"model":"unknown"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestHealthz(t *testing.T) {
	handler := &proxy{config: loadedConfig{}, client: http.DefaultClient}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://proxy.test/healthz", nil))
	if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
		t.Fatalf("health response = %d %q", response.Code, response.Body.String())
	}
}

func TestFindLocalProfileWalksParents(t *testing.T) {
	root := t.TempDir()
	nested := root + "/nested/child"
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localProfilePath(root), []byte("paid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	name, file, err := findLocalProfile(nested)
	if err != nil || name != "paid" || file != localProfilePath(root) {
		t.Fatalf("findLocalProfile() = %q, %q, %v", name, file, err)
	}
}

func TestLocalProfileCommands(t *testing.T) {
	root := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	if err := setLocalProfile("paid"); err != nil {
		t.Fatal(err)
	}
	name, _, err := findLocalProfile(root)
	if err != nil || name != "paid" {
		t.Fatalf("local profile = %q, %v", name, err)
	}
	if err := unsetLocalProfile(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := findLocalProfile(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("findLocalProfile after unset = %v", err)
	}
}

func TestReadLocalProfileRejectsMultipleLines(t *testing.T) {
	file := t.TempDir() + "/" + localProfileFile
	if err := os.WriteFile(file, []byte("paid\nfree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalProfile(file); err == nil {
		t.Fatal("readLocalProfile accepted multiple lines")
	}
}

func chdirT(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func sameArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestResolveLaunchExplicitName(t *testing.T) {
	name, claudeArgs, err := resolveLaunch([]string{"provider1", "-p", "hello"})
	if err != nil || name != "provider1" {
		t.Fatalf("resolveLaunch() = %q, %v", name, err)
	}
	if !sameArgs(claudeArgs, []string{"-p", "hello"}) {
		t.Fatalf("claude args = %q", claudeArgs)
	}
}

func TestResolveLaunchDashFirstUsesLocal(t *testing.T) {
	root := t.TempDir()
	chdirT(t, root)
	if err := os.WriteFile(localProfilePath(root), []byte("paid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	name, claudeArgs, err := resolveLaunch([]string{"-p", "hello world", "--settings", "foo.json"})
	if err != nil || name != "paid" {
		t.Fatalf("resolveLaunch() = %q, %v", name, err)
	}
	if !sameArgs(claudeArgs, []string{"-p", "hello world", "--settings", "foo.json"}) {
		t.Fatalf("claude args = %q", claudeArgs)
	}
}

func TestResolveLaunchNoNameNoMarkerFails(t *testing.T) {
	chdirT(t, t.TempDir())
	if _, _, err := resolveLaunch([]string{"-p", "hi"}); err == nil {
		t.Fatal("resolveLaunch accepted dash args without a local profile")
	}
	if _, _, err := resolveLaunch(nil); err == nil {
		t.Fatal("resolveLaunch accepted no args without a local profile")
	}
}

func TestResolveLaunchNoArgsUsesLocal(t *testing.T) {
	root := t.TempDir()
	chdirT(t, root)
	if err := os.WriteFile(localProfilePath(root), []byte("paid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	name, claudeArgs, err := resolveLaunch(nil)
	if err != nil || name != "paid" || len(claudeArgs) != 0 {
		t.Fatalf("resolveLaunch() = %q, %q, %v", name, claudeArgs, err)
	}
}

func TestBuildClaudeCommandForwardsArgs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCBUNSHIN_PROFILES_DIR", dir)
	file := filepath.Join(dir, "provider1.json")
	if err := os.WriteFile(file, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	forwarded := []string{"-p", "hello world", "", "--resume", "abc", "--settings", "foo.json", "--unknown-future-option", "value"}
	command, err := buildClaudeCommand("provider1", forwarded)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{"claude", "--settings", file}, forwarded...)
	if !sameArgs(command.Args, want) {
		t.Fatalf("argv = %q", command.Args)
	}
}

func TestResolveProvider(t *testing.T) {
	root := t.TempDir()
	marker := func(dir, name string) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(localProfilePath(dir), []byte(name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	proj := filepath.Join(root, "proj")
	marker(proj, "provider1")
	nested := filepath.Join(proj, "a", "b")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	marker(filepath.Join(nested, "nested"), "provider2")

	cases := []struct {
		dir  string
		want string
	}{
		{proj, "provider1"},
		{nested, "provider1"},                                  // marker several levels above
		{filepath.Join(proj, "a", "b", "c"), "provider1"},      // deep child
		{filepath.Join(nested, "nested"), "provider2"},         // nearest wins
		{filepath.Join(nested, "nested", "deep"), "provider2"}, // inside nested project
		{filepath.Join(root, "plain"), ""},                     // no profile
	}
	for _, tc := range cases {
		if got := resolveProvider(tc.dir); got != tc.want {
			t.Errorf("resolveProvider(%s) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.0.4", "v0.0.3", 1},
		{"v0.0.3", "v0.0.4", -1},
		{"v0.0.3", "v0.0.3", 0},
		{"v0.10.0", "v0.9.9", 1},
		{"v0.0.3", "v0.0.3.1", -1},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "ccbunshin-linux-amd64"},
		{"linux", "arm64", "ccbunshin-linux-arm64"},
		{"darwin", "amd64", "ccbunshin-darwin-amd64"},
		{"darwin", "arm64", "ccbunshin-darwin-arm64"},
		{"windows", "amd64", ""},
		{"darwin", "386", ""},
	}
	for _, tc := range cases {
		got, ok := assetName(tc.goos, tc.goarch)
		if got != tc.want {
			t.Errorf("assetName(%s/%s) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
		if (tc.want != "") != ok {
			t.Errorf("assetName(%s/%s) ok = %v", tc.goos, tc.goarch, ok)
		}
	}
}

func TestCommandHelpCoversUsageCommands(t *testing.T) {
	for _, name := range []string{"init", "create", "launch", "local", "model", "list", "status", "doctor", "delete", "proxy", "update", "version", "help"} {
		text, ok := commandHelp(name)
		if !ok || !strings.Contains(text, "usage: ccbunshin "+name) {
			t.Errorf("commandHelp(%q) missing usage", name)
		}
		if strings.Count(text, "\n") < 3 {
			t.Errorf("commandHelp(%q) has no description body", name)
		}
	}
	for _, name := range []string{"resolve-provider", "run"} {
		if _, ok := commandHelp(name); !ok {
			t.Errorf("commandHelp(%q) missing internal help", name)
		}
	}
	if _, ok := commandHelp("bogus"); ok {
		t.Error("commandHelp(bogus) accepted unknown command")
	}
}

func TestRunCLIUsageErrors(t *testing.T) {
	chdirT(t, t.TempDir())
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	cases := [][]string{
		{"create"},
		{"model"},
		{"model", "only-one"},
		{"status"},
		{"doctor"},
		{"delete"},
		{"proxy"},
		{"proxy", "bogus"},
		{"resolve-provider", "extra"},
		{"list", "extra"},
		{"init", "a", "b"},
		{"init", "fish"},
		{"local", "a", "b"},
		{"local"},
		{"launch"},
		{"bogus"},
		{"help", "bogus"},
	}
	for _, args := range cases {
		err := runCLI(args)
		if err == nil {
			t.Errorf("runCLI(%q) = nil, want usage error", args)
			continue
		}
		if !strings.Contains(err.Error(), "usage:") {
			t.Errorf("runCLI(%q) = %q, want usage text", args, err)
		}
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestListEmptyModel(t *testing.T) {
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	if err := profileCreate("provider1", "", false); err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		if err := runCLI([]string{"list"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "provider1\tmodel=(no model set)") {
		t.Errorf("list output = %q, want (no model set) marker", output)
	}
	if err := setProfileModel("provider1", "sonnet"); err != nil {
		t.Fatal(err)
	}
	output = captureStdout(t, func() {
		if err := runCLI([]string{"list"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "provider1\tmodel=sonnet") {
		t.Errorf("list output = %q, want model=sonnet", output)
	}
}

func TestInitAutoInstallsShellHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())

	bashrc := home + "/.bashrc"
	zshrc := home + "/.zshrc"
	tcshrc := home + "/.tcshrc"
	if err := os.WriteFile(bashrc, []byte("# existing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// zshrc missing, should be skipped
	// tcshrc exists
	if err := os.WriteFile(tcshrc, []byte("# existing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		if err := initCLI(); err != nil {
			t.Fatal(err)
		}
	})

	for _, line := range []string{"installed: " + bashrc, "skip: " + zshrc, "installed: " + tcshrc} {
		if !strings.Contains(output, line) {
			t.Errorf("stdout missing %q; got %q", line, output)
		}
	}

	bashrcData, err := os.ReadFile(bashrc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bashrcData), "eval \"$(ccbunshin init bash)\"") {
		t.Errorf("bashrc missing bash hook:\n%s", bashrcData)
	}

	tcshrcData, err := os.ReadFile(tcshrc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tcshrcData), "eval `ccbunshin init tcsh`") {
		t.Errorf("tcshrc missing tcsh hook:\n%s", tcshrcData)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())

	bashrc := home + "/.bashrc"
	if err := os.WriteFile(bashrc, []byte("# existing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// run init twice
	if err := initCLI(); err != nil {
		t.Fatal(err)
	}
	if err := initCLI(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bashrc)
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(data), "ccbunshin init bash")
	if count != 1 {
		t.Errorf("bashrc has %d hooks, want 1:\n%s", count, data)
	}
}

func TestTcshInitIsBackquoteSafe(t *testing.T) {
	script, err := shellInit("tcsh")
	if err != nil {
		t.Fatal(err)
	}
	// `eval `+"`ccbunshin init tcsh`"+` flattens newlines: the output must be a
	// single alias line, or tcsh fails with "Badly placed ()'s".
	lines := strings.Split(strings.TrimSpace(script), "\n")
	if len(lines) != 1 {
		t.Errorf("tcsh init must be one line, got %d:\n%s", len(lines), script)
	}
	if !strings.HasPrefix(strings.TrimSpace(script), "alias claude") {
		t.Errorf("tcsh init must define the claude alias, got:\n%s", script)
	}
	for _, bad := range []string{"if (", "endif", "#"} {
		if strings.Contains(script, bad) {
			t.Errorf("tcsh init contains %q, which breaks backquote eval:\n%s", bad, script)
		}
	}
}

func TestProxyInitWritesTemplate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CCBUNSHIN_PROXY_CONFIG", home+"/proxy.json")

	if err := runCLI([]string{"proxy", "init"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(home + "/proxy.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(home + "/proxy.json"); err != nil {
		t.Fatalf("template failed validation: %v\n%s", err, data)
	}
	if err := runCLI([]string{"proxy", "init"}); err == nil {
		t.Fatal("second init without --force succeeded")
	}
	if err := runCLI([]string{"proxy", "init", "--force"}); err != nil {
		t.Fatal(err)
	}
	if err := runCLI([]string{"proxy", "init", "extra"}); err == nil {
		t.Fatal("init with extra arg succeeded")
	}
}

// proxyStub writes an executable that stands in for the proxy daemon: keepAlive
// leaves it running until it is signalled, otherwise it exits at once with the
// given status.
func proxyStub(t *testing.T, keepAlive bool, status int) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "stub.sh")
	body := "#!/bin/sh\nexit " + strconv.Itoa(status) + "\n"
	if keepAlive {
		body = "#!/bin/sh\nsleep 30\n"
	}
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return script
}

func writeProxyTestConfig(t *testing.T, file string) {
	t.Helper()
	config := `{"port": 3456, "providers": {"provider1": {"upstream": "https://gateway.example.invalid"}}}`
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
}

// The state directory must not follow CCBUNSHIN_PROXY_CONFIG: a relative value
// would resolve against the working directory, and an absolute one can be a
// read-only system location like /etc/ccbunshin.
func TestProxyStatePathsIgnoreConfigLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".config", "ccbunshin")
	for _, config := range []string{
		"proxy.json",
		filepath.Join(home, "repo", "proxy.json"),
		"/etc/ccbunshin/proxy.json",
	} {
		t.Setenv("CCBUNSHIN_PROXY_CONFIG", config)
		for _, got := range []string{pidPath(), logPath()} {
			if !filepath.IsAbs(got) {
				t.Errorf("config %q gave relative state path %q", config, got)
			}
			if filepath.Dir(got) != want {
				t.Errorf("config %q put state in %q, want %q", config, filepath.Dir(got), want)
			}
		}
	}
}

// A relative config path used to make the pid and log land in the working
// directory, where status and stop run from anywhere else cannot see them.
func TestProxyStartKeepsStateOutOfWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	chdirT(t, work)
	writeProxyTestConfig(t, filepath.Join(work, "proxy.json"))
	t.Setenv("CCBUNSHIN_PROXY_CONFIG", "proxy.json")
	t.Setenv("CCBUNSHIN_PROXY_BIN", proxyStub(t, true, 0))

	if err := proxyStart(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxyStop() })

	for _, name := range []string{"proxy.pid", "proxy.log"} {
		if _, err := os.Stat(filepath.Join(work, name)); err == nil {
			t.Errorf("proxy start wrote %s into the working directory", name)
		}
	}
	if _, err := os.Stat(pidPath()); err != nil {
		t.Fatalf("state not written to %s: %v", pidPath(), err)
	}
	output := captureStdout(t, func() {
		if err := proxyStatus(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "proxy running") {
		t.Errorf("proxyStatus = %q, want running", output)
	}
}

// A config outside the user's own writable space (the systemd unit uses
// /etc/ccbunshin/proxy.json) must not stop the proxy from starting.
func TestProxyStartWithReadOnlyConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := t.TempDir()
	config := filepath.Join(configDir, "proxy.json")
	writeProxyTestConfig(t, config)
	if err := os.Chmod(configDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0700) })
	t.Setenv("CCBUNSHIN_PROXY_CONFIG", config)
	t.Setenv("CCBUNSHIN_PROXY_BIN", proxyStub(t, true, 0))

	if err := proxyStart(); err != nil {
		t.Fatalf("proxy start with a read-only config directory: %v", err)
	}
	t.Cleanup(func() { _ = proxyStop() })
	if _, err := os.Stat(pidPath()); err != nil {
		t.Fatalf("state not written to %s: %v", pidPath(), err)
	}
}

// A daemon that dies during startup (port in use, unusable upstream) used to be
// reported as a successful start, leaving a pid file for a dead process.
func TestProxyStartReportsDaemonThatExitsImmediately(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, "proxy.json")
	writeProxyTestConfig(t, config)
	t.Setenv("CCBUNSHIN_PROXY_CONFIG", config)
	t.Setenv("CCBUNSHIN_PROXY_BIN", proxyStub(t, false, 1))

	err := proxyStart()
	if err == nil {
		t.Fatal("proxy start reported success although the daemon exited immediately")
	}
	if !strings.Contains(err.Error(), logPath()) {
		t.Errorf("error %q does not point at the log %s", err, logPath())
	}
	if _, statErr := os.Stat(pidPath()); statErr == nil {
		t.Error("pid file left behind for a daemon that already exited")
	}
}

// A pid file for a process that is gone (crash, reboot, SIGKILL) must not block
// the next start, and start and status must agree about what is running.
func TestProxyStartRecoversFromStalePidFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, "proxy.json")
	writeProxyTestConfig(t, config)
	t.Setenv("CCBUNSHIN_PROXY_CONFIG", config)
	t.Setenv("CCBUNSHIN_PROXY_BIN", proxyStub(t, true, 0))

	dead := exec.Command("/bin/sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(proxyStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidPath(), []byte(strconv.Itoa(dead.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}

	if err := proxyStart(); err != nil {
		t.Fatalf("stale pid file blocked start: %v", err)
	}
	t.Cleanup(func() { _ = proxyStop() })
	output := captureStdout(t, func() {
		if err := proxyStatus(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "proxy running") {
		t.Errorf("proxyStatus after start = %q, want running", output)
	}
}

// State moved from ~/.cache/ccbunshin to ~/.config/ccbunshin, so a proxy left
// running by an older binary has to stay reachable.
func TestProxyStopFindsProxyFromLegacyStateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyDir := filepath.Join(home, ".cache", "ccbunshin")
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	daemon := exec.Command(proxyStub(t, true, 0))
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Kill()
		_ = daemon.Wait()
	})
	legacyPid := filepath.Join(legacyDir, "proxy.pid")
	if err := os.WriteFile(legacyPid, []byte(strconv.Itoa(daemon.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}

	output := captureStdout(t, func() {
		if err := proxyStatus(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "proxy running") {
		t.Errorf("proxyStatus with a legacy pid file = %q, want running", output)
	}

	output = captureStdout(t, func() {
		if err := proxyStop(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "proxy stopped") {
		t.Errorf("proxyStop = %q, want stopped", output)
	}
	if _, err := os.Stat(legacyPid); err == nil {
		t.Error("legacy pid file left behind after stop")
	}
	done := make(chan error, 1)
	go func() { done <- daemon.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("legacy proxy still running after stop")
	}
}
