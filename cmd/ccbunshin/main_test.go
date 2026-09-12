package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
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
		routes: []loadedRoute{
			{pattern: "claude-*", provider: "paid"},
			{pattern: "qwen-3.8-27b", provider: "free"},
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
		routes: []loadedRoute{{pattern: "opus", provider: "provider1"}},
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
	handler := &proxy{config: loadedConfig{routes: []loadedRoute{}}, client: http.DefaultClient}
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

// --- logging ---

func TestParseLogLevel(t *testing.T) {
	cases := map[string]logLevel{
		"":        levelInfo,
		"info":    levelInfo,
		"INFO":    levelInfo,
		" info ":  levelInfo,
		"debug":   levelDebug,
		"Debug":   levelDebug,
		"warn":    levelWarn,
		"warning": levelWarn,
		"error":   levelError,
	}
	for value, want := range cases {
		got, err := parseLogLevel(value)
		if err != nil || got != want {
			t.Errorf("parseLogLevel(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	if _, err := parseLogLevel("verbose"); err == nil {
		t.Fatal("an unknown level was accepted instead of reported")
	}
}

// capturer installs a collecting logger for one test and restores the old one, so the
// threshold and the output both start from a known state.
func capturer(t *testing.T, level logLevel) *bytes.Buffer {
	t.Helper()
	previousOutput, previousFlags, previousThreshold := log.Writer(), log.Flags(), logThreshold
	buffer := &bytes.Buffer{}
	log.SetOutput(buffer)
	log.SetFlags(0)
	logThreshold = level
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
		logThreshold = previousThreshold
	})
	return buffer
}

func TestLogThresholdFiltersLines(t *testing.T) {
	buffer := capturer(t, levelWarn)
	logf(levelDebug, "debug line")
	logf(levelInfo, "info line")
	logf(levelWarn, "warn line")
	logf(levelError, "error line")

	output := buffer.String()
	for _, dropped := range []string{"debug line", "info line"} {
		if strings.Contains(output, dropped) {
			t.Errorf("%q was written below the threshold: %s", dropped, output)
		}
	}
	for _, kept := range []string{"WARN warn line", "ERROR error line"} {
		if !strings.Contains(output, kept) {
			t.Errorf("%q was not written: %s", kept, output)
		}
	}
}

// The default level keeps the proxy's own startup line, which is what an unconfigured
// run has always logged.
func TestDefaultLevelKeepsStartupLine(t *testing.T) {
	buffer := capturer(t, levelInfo)
	logf(levelInfo, "proxy listening on port %d", 3456)
	if !strings.Contains(buffer.String(), "INFO proxy listening on port 3456") {
		t.Fatalf("log = %q", buffer.String())
	}
}

func TestRequestLogLineNamesModelProviderAndDialect(t *testing.T) {
	upstream, err := url.Parse("https://gw.example.invalid/v1")
	if err != nil {
		t.Fatal(err)
	}
	plan := requestPlan{
		requestedModel: "claude-opus-5",
		targetModel:    "oss-model",
		dialect:        dialectOpenAIChat,
		providerName:   "provider2",
		provider:       loadedProvider{upstream: upstream},
	}
	line := requestLogLine(plan, http.StatusOK, 1234, 1500*time.Millisecond)
	for _, want := range []string{"claude-opus-5 -> oss-model", "buffered", "provider=provider2", "dialect=openai-chat", "status=200", "bytes=1234", "elapsed=1.5s"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q is missing %q", line, want)
		}
	}
}

// A credential in the upstream URL must not reach the log, which is the one line
// carrying the upstream address.
func TestRequestLogLineRedactsCredentials(t *testing.T) {
	upstream, err := url.Parse("https://user:sekret@gw.example.invalid/v1")
	if err != nil {
		t.Fatal(err)
	}
	plan := requestPlan{requestedModel: "m", targetModel: "m", provider: loadedProvider{upstream: upstream}}
	if line := requestLogLine(plan, http.StatusOK, 0, time.Second); strings.Contains(line, "sekret") {
		t.Fatalf("credential leaked into the log line: %s", line)
	}
}

// A completed request is logged even at the default level, which is the point of the
// whole exercise: the old log said nothing about a request at all.
func TestRequestIsLoggedAtDefaultLevel(t *testing.T) {
	buffer := capturer(t, levelInfo)
	handler := &proxy{}
	handler.logRequest(requestPlan{requestedModel: "m", targetModel: "m"}, http.StatusOK, 10, time.Now())
	if !strings.Contains(buffer.String(), "INFO proxy:") {
		t.Fatalf("log = %q", buffer.String())
	}
}

// A 5xx is the signal that something is wrong with the gateway, so it is the one
// request line that survives CCBUNSHIN_LOG=error.
func TestFailedRequestIsLoggedAtErrorLevel(t *testing.T) {
	buffer := capturer(t, levelError)
	handler := &proxy{}
	handler.logRequest(requestPlan{requestedModel: "m", targetModel: "m"}, http.StatusBadGateway, 0, time.Now())
	if !strings.Contains(buffer.String(), "ERROR proxy:") {
		t.Fatalf("log = %q", buffer.String())
	}
}

// A pass-through upstream can answer 5xx without any other failure to report, so the
// request line is the only thing a reader gets. It must carry the status.
func TestPassThroughUpstreamErrorIsLogged(t *testing.T) {
	buffer := capturer(t, levelInfo)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gateway exploded", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	handler := &proxy{
		config: loadedConfig{
			providers: map[string]loadedProvider{"provider1": testProvider(t, upstream, nil)},
			routes:    []loadedRoute{{pattern: "opus", provider: "provider1"}},
		},
		client: upstream.Client(),
	}
	request := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages", strings.NewReader(`{"model":"opus"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	logged := buffer.String()
	for _, want := range []string{"ERROR proxy: opus -> opus", "provider=provider1", "status=500"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log %q is missing %q", logged, want)
		}
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

func TestReadProfileNameRejectsMultipleLines(t *testing.T) {
	file := t.TempDir() + "/" + localProfileFile
	if err := os.WriteFile(file, []byte("paid\nfree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProfileName(file); err == nil {
		t.Fatal("readProfileName accepted multiple lines")
	}
}

func TestGlobalProfileCommands(t *testing.T) {
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	if _, err := readProfileName(globalProfilePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("global profile before set = %v", err)
	}
	if err := setGlobalProfile("paid"); err != nil {
		t.Fatal(err)
	}
	name, file, err := findGlobalProfile()
	if err != nil || name != "paid" || file != globalProfilePath() {
		t.Fatalf("findGlobalProfile() = %q, %q, %v", name, file, err)
	}
	if err := unsetGlobalProfile(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := findGlobalProfile(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("findGlobalProfile after unset = %v", err)
	}
	if err := unsetGlobalProfile(); err != nil {
		t.Fatalf("unsetGlobalProfile is not idempotent: %v", err)
	}
}

// TestFindProfilePrefersLocalOverGlobal pins the pyenv-style layering: a local
// marker wins for its directory and descendants, the global applies elsewhere.
func TestFindProfilePrefersLocalOverGlobal(t *testing.T) {
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	if err := setGlobalProfile("free"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(proj, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localProfilePath(proj), []byte("paid\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ dir, want string }{
		{proj, "paid"},
		{filepath.Join(proj, "deep"), "paid"},
		{root, "free"},
	}
	for _, tc := range cases {
		name, _, err := findProfile(tc.dir)
		if err != nil || name != tc.want {
			t.Errorf("findProfile(%s) = %q, %v; want %q", tc.dir, name, err, tc.want)
		}
	}
}

func TestFindProfileWithoutLocalOrGlobalFails(t *testing.T) {
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	if _, _, err := findProfile(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("findProfile with no selection = %v", err)
	}
}

func TestGlobalCLIStatusAndUsage(t *testing.T) {
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	if err := globalCLI([]string{"paid"}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := globalCLI(nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "profile: paid") || !strings.Contains(out, globalProfilePath()) {
		t.Fatalf("global status = %q", out)
	}
	if err := globalCLI([]string{"--unset"}); err != nil {
		t.Fatal(err)
	}
	if err := globalCLI(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("global with no selection = %v, want usage error", err)
	}
	if err := globalCLI([]string{"a", "b"}); err == nil {
		t.Fatal("global accepted two arguments")
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
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	if _, _, err := resolveLaunch([]string{"-p", "hi"}); err == nil {
		t.Fatal("resolveLaunch accepted dash args without a local or global profile")
	}
	if _, _, err := resolveLaunch(nil); err == nil {
		t.Fatal("resolveLaunch accepted no args without a local or global profile")
	}
}

func TestResolveLaunchUsesGlobalOutsideProject(t *testing.T) {
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	if err := setGlobalProfile("free"); err != nil {
		t.Fatal(err)
	}
	chdirT(t, t.TempDir())
	name, claudeArgs, err := resolveLaunch([]string{"-p", "hi"})
	if err != nil || name != "free" || !sameArgs(claudeArgs, []string{"-p", "hi"}) {
		t.Fatalf("resolveLaunch() = %q, %q, %v", name, claudeArgs, err)
	}
}

// TestResolveLaunchGlobalNameSelectsGlobal pins the reserved "global" argument:
// the global selection file is <profiles>/global with no .json suffix, so
// treating the word as an ordinary profile name would look for global.json and
// fail even though a global profile is set.
func TestResolveLaunchGlobalNameSelectsGlobal(t *testing.T) {
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	if err := setGlobalProfile("free"); err != nil {
		t.Fatal(err)
	}
	chdirT(t, t.TempDir())
	name, claudeArgs, err := resolveLaunch([]string{globalProfileName, "-p", "hi"})
	if err != nil || name != "free" || !sameArgs(claudeArgs, []string{"-p", "hi"}) {
		t.Fatalf("resolveLaunch(global) = %q, %q, %v", name, claudeArgs, err)
	}
}

// An explicit profile name still beats the global selection, and "global" is
// the only name reserved for the selector.
func TestResolveLaunchGlobalNameBeatsLocalMarker(t *testing.T) {
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	if err := setGlobalProfile("free"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	chdirT(t, root)
	if err := os.WriteFile(localProfilePath(root), []byte("paid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	name, _, err := resolveLaunch([]string{globalProfileName})
	if err != nil || name != "free" {
		t.Fatalf("resolveLaunch(global) = %q, %v; want the global selection", name, err)
	}
}

func TestResolveLaunchGlobalNameWithNoSelectionFails(t *testing.T) {
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	chdirT(t, t.TempDir())
	if _, _, err := resolveLaunch([]string{globalProfileName}); err == nil {
		t.Fatal("resolveLaunch(global) accepted an unset global selection")
	}
}

func TestResolveLaunchLocalBeatsGlobal(t *testing.T) {
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
	if err := setGlobalProfile("free"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	chdirT(t, root)
	if err := os.WriteFile(localProfilePath(root), []byte("paid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	name, _, err := resolveLaunch(nil)
	if err != nil || name != "paid" {
		t.Fatalf("resolveLaunch() = %q, %v; want local profile to win", name, err)
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

// TestResolveProvider pins the no-selection case: with no global profile set,
// resolveProvider stays "" outside a project, which is what keeps the shell
// wrapper falling back to the plain claude binary.
func TestResolveProvider(t *testing.T) {
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())
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

func TestFormatLocalVersion(t *testing.T) {
	cases := []struct {
		sha, stamp, want string
	}{
		{"abc1234", "20260911-1130", "abc1234-20260911-1130"},
		{"abc1234", "", "abc1234"},
		{"", "20260911-1130", "20260911-1130"},
		{"", "", ""},
		{"  abc1234  ", "  20260911-1130  ", "abc1234-20260911-1130"},
	}
	for _, tc := range cases {
		if got := formatLocalVersion(tc.sha, tc.stamp); got != tc.want {
			t.Errorf("formatLocalVersion(%q, %q) = %q, want %q", tc.sha, tc.stamp, got, tc.want)
		}
	}
}

// TestBuildCommitIsShortened guards the reported commit against the toolchain's
// full 40-character revision, which is too long to read in a version string.
func TestBuildCommitIsShortened(t *testing.T) {
	commit := buildCommit()
	if commit == "" {
		t.Skip("no VCS stamping in this build (go test does not stamp)")
	}
	if len(commit) > shortCommitLength {
		t.Errorf("buildCommit() = %q, want at most %d characters", commit, shortCommitLength)
	}
}

// TestVersionStaysAReleaseTag pins the invariant updateCLI depends on: version
// is compared against release tags verbatim, so a local build identifier must
// never be written into it.
func TestVersionStaysAReleaseTag(t *testing.T) {
	if version == "" {
		return
	}
	if _, err := time.Parse(buildTimeLayout, version); err == nil {
		t.Errorf("version %q parses as a build stamp; local identifiers belong to printVersion", version)
	}
}

func TestPrintVersionFallsBackToLocalBuild(t *testing.T) {
	savedVersion, savedTime := version, buildTime
	t.Cleanup(func() { version, buildTime = savedVersion, savedTime })

	// Go only stamps VCS metadata when building a main package from a checkout,
	// so a test binary may have no commit at all. Assert against whatever the
	// environment provides rather than assuming the SHA is present.
	version = ""
	buildTime = "20260911-1130"
	want := formatLocalVersion(buildCommit(), buildTime)
	out := strings.TrimSpace(captureStdout(t, printVersion))
	if out != want {
		t.Fatalf("printVersion with a stamp = %q, want %q", out, want)
	}

	// A release tag wins over the local identifier, stamp or not.
	version = "v9.9.9"
	out = strings.TrimSpace(captureStdout(t, printVersion))
	if out != "v9.9.9" {
		t.Fatalf("printVersion with a release tag = %q, want v9.9.9", out)
	}

	// With no tag and no stamp the placeholder still appears rather than blank.
	version = ""
	buildTime = ""
	out = strings.TrimSpace(captureStdout(t, printVersion))
	if out == "" {
		t.Fatal("printVersion printed nothing with no tag or stamp")
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
	for _, name := range []string{"init", "create", "launch", "local", "global", "model", "list", "status", "doctor", "delete", "proxy", "update", "version", "help"} {
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
	t.Cleanup(func() { _ = unsetGlobalProfile() })
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
		{"global", "a", "b"},
		{"global"},
		{"launch"},
		{"update", "--bogus"},
		{"update", "--force", "extra"},
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

// uninstall must reverse init exactly: the hook, its comment, and the blank line
// init added all go, and nothing the user wrote does.
func TestUninstallRemovesOnlyItsOwnHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CCBUNSHIN_PROFILES_DIR", t.TempDir())

	original := "# my bashrc\nexport EDITOR=vim\n"
	bashrc := home + "/.bashrc"
	if err := os.WriteFile(bashrc, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := initCLI(); err != nil {
		t.Fatal(err)
	}
	if err := uninstallCLI(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bashrc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("bashrc after uninstall =\n%q\nwant\n%q", data, original)
	}
	// The profiles the user created are not uninstall's to delete.
	if _, err := os.Stat(profilesDir()); err != nil {
		t.Errorf("uninstall removed the profile directory: %v", err)
	}
}

// A hand-written hook is left alone: uninstall matches the line init wrote, so it
// cannot tell a manual hook from a generated one and must not guess.
func TestUninstallLeavesHandWrittenHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manual := "# mine\neval \"$(ccbunshin init bash --custom)\"\n"
	bashrc := home + "/.bashrc"
	if err := os.WriteFile(bashrc, []byte(manual), 0600); err != nil {
		t.Fatal(err)
	}
	if err := uninstallCLI(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bashrc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != manual {
		t.Errorf("bashrc after uninstall =\n%q\nwant it untouched:\n%q", data, manual)
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

// TestUpdateRefusesUnreplaceableBuild covers the branch a source build hits:
// no release tag and no binary on disk to rename over (what "go run" sees), so
// update reports the latest tag and changes nothing unless --force is given.
func TestUpdateRefusesUnreplaceableBuild(t *testing.T) {
	savedVersion, savedTime, savedArgs0 := version, buildTime, os.Args[0]
	t.Cleanup(func() { version, buildTime, os.Args[0] = savedVersion, savedTime, savedArgs0 })

	fakeGitHub(t, `{"tag_name":"v9.9.9"}`)
	version = ""
	buildTime = "20260912-1200"
	os.Args[0] = filepath.Join(t.TempDir(), "ccbunshin-that-does-not-exist")

	output := captureStdout(t, func() {
		if err := runCLI([]string{"update"}); err != nil {
			t.Fatalf("update: %v", err)
		}
	})
	if !strings.Contains(output, "not a release build") || !strings.Contains(output, "v9.9.9") {
		t.Errorf("update output = %q, want the not-a-release-build notice naming v9.9.9", output)
	}
	if !strings.Contains(output, "--force") {
		t.Errorf("update output = %q, want it to mention --force", output)
	}

	// --force proceeds past the guard and fails at the download, not earlier.
	err := runCLI([]string{"update", "--force"})
	if err == nil {
		t.Fatal("update --force = nil, want a download failure from the stub server")
	}
	if strings.Contains(err.Error(), "not a release build") {
		t.Errorf("update --force stopped at the guard: %v", err)
	}
}

func TestReplaceableRejectsMissingFile(t *testing.T) {
	if replaceable(filepath.Join(t.TempDir(), "absent")) {
		t.Error("replaceable(absent) = true, want false")
	}
	real := filepath.Join(t.TempDir(), "ccbunshin")
	if err := os.WriteFile(real, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	if !replaceable(real) {
		t.Errorf("replaceable(%s) = false, want true", real)
	}
	if replaceable(t.TempDir()) {
		t.Error("replaceable(dir) = true, want false")
	}
}

// fakeGitHub points latestReleaseTag at a stub serving one release payload.
func fakeGitHub(t *testing.T, payload string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, payload)
	}))
	t.Cleanup(server.Close)
	// latestReleaseTag hard-codes api.github.com, so redirect through a test
	// override of the base URL rather than DNS.
	githubAPIBase = server.URL
	t.Cleanup(func() { githubAPIBase = "https://api.github.com" })
}
