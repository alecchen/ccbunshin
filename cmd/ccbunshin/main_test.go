package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	for _, name := range []string{"init", "create", "launch", "local", "model", "list", "status", "doctor", "delete", "proxy", "update", "help"} {
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
