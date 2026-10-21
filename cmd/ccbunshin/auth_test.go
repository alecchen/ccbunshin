package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// authConfig writes a proxy config with one provider and one route.
func authConfig(t *testing.T, body string) string {
	t.Helper()
	return writeConfig(t, `{
	  "port": 3456,
	  `+body+`
	  "providers": {"p": {"upstream": "https://gw.example.invalid"}},
	  "routes": [{"pattern": "claude-*", "provider": "p"}]
	}`)
}

func TestLoadConfigDefaultsBindToLoopback(t *testing.T) {
	cfg, err := loadConfig(authConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.bind != "127.0.0.1" {
		t.Errorf("bind = %q, want loopback when the key is absent", cfg.bind)
	}
}

// An existing config that listens on the network keeps working, but only by asking.
func TestLoadConfigBindAllInterfacesWhenRequested(t *testing.T) {
	for _, value := range []string{"", "0.0.0.0", "::"} {
		cfg, err := loadConfig(authConfig(t, `"bind": "`+value+`",`))
		if err != nil {
			t.Fatalf("bind %q: %v", value, err)
		}
		if cfg.bind != value {
			t.Errorf("bind = %q, want %q", cfg.bind, value)
		}
	}
}

// The proxy token is the caller's credential, so forwarding it upstream would leak it
// to every gateway. A provider with no credential of its own is refused at load.
func TestLoadConfigRejectsProviderWithoutCredentialUnderClientToken(t *testing.T) {
	_, err := loadConfig(authConfig(t, `"client_token_env": "CCBUNSHIN_PROXY_TOKEN",`))
	if err == nil {
		t.Fatal("a credential-less provider was accepted while client_token_env is set")
	}
	for _, want := range []string{"p", "auth_token_env"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadConfigAcceptsProviderWithOwnCredentialUnderClientToken(t *testing.T) {
	for name, extra := range map[string]string{
		"auth_token_env": `"auth_token_env": "UPSTREAM_TOKEN"`,
		"auth_headers":   `"auth_headers": {"x-api-key": "UPSTREAM_TOKEN"}`,
		"no_auth":        `"no_auth": true`,
	} {
		file := writeConfig(t, `{
		  "port": 3456,
		  "client_token_env": "CCBUNSHIN_PROXY_TOKEN",
		  "providers": {"p": {"upstream": "https://gw.example.invalid", `+extra+`}},
		  "routes": [{"pattern": "claude-*", "provider": "p"}]
		}`)
		if _, err := loadConfig(file); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// With no client_token_env the config behaves exactly as before, so a provider that
// names nothing still forwards the caller's credential.
func TestLoadConfigWithoutClientTokenAcceptsCredentiallessProvider(t *testing.T) {
	if _, err := loadConfig(authConfig(t, "")); err != nil {
		t.Fatal(err)
	}
}

func authProxy(t *testing.T, token string) *proxy {
	t.Helper()
	if token != "" {
		t.Setenv("CCBUNSHIN_TEST_CLIENT_TOKEN", token)
	}
	return &proxy{config: loadedConfig{
		clientTokenEnv: "CCBUNSHIN_TEST_CLIENT_TOKEN",
		providers:      map[string]loadedProvider{},
		routes:         []loadedRoute{},
	}}
}

func TestAuthorizedAcceptsBearerAndApiKey(t *testing.T) {
	for name, header := range map[string][2]string{
		"bearer":  {"Authorization", "Bearer sekrit"},
		"scheme":  {"Authorization", "bearer sekrit"},
		"api key": {"X-Api-Key", "sekrit"},
	} {
		p := authProxy(t, "sekrit")
		request := httptest.NewRequest(http.MethodGet, "http://proxy.test/x", nil)
		request.Header.Set(header[0], header[1])
		if !p.authorized(request) {
			t.Errorf("%s: a valid token was rejected", name)
		}
	}
}

func TestAuthorizedRejectsMissingAndWrongToken(t *testing.T) {
	p := authProxy(t, "sekrit")
	for name, header := range map[string][2]string{
		"missing": {"", ""},
		"wrong":   {"Authorization", "Bearer nope"},
		"prefix":  {"Authorization", "Bearer sekri"},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://proxy.test/x", nil)
		if header[0] != "" {
			request.Header.Set(header[0], header[1])
		}
		if p.authorized(request) {
			t.Errorf("%s: an invalid token was accepted", name)
		}
	}
}

// No key configured means no gate, which is the behavior every older config relies on.
func TestAuthorizedIsOpenWithoutClientTokenEnv(t *testing.T) {
	p := &proxy{config: loadedConfig{}}
	if !p.authorized(httptest.NewRequest(http.MethodGet, "http://proxy.test/x", nil)) {
		t.Fatal("a config with no client_token_env rejected a request")
	}
}

// /healthz stays open so a liveness check needs no secret. /ccbunshin/resolve does not:
// it publishes the whole route table.
func TestServeHTTPGatesResolveButNotHealth(t *testing.T) {
	p := authProxy(t, "sekrit")
	p.config.routes = []loadedRoute{{pattern: "claude-*", provider: "p"}}

	health := httptest.NewRecorder()
	p.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "http://proxy.test/healthz", nil))
	if health.Code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200 without a token", health.Code)
	}

	resolve := httptest.NewRecorder()
	p.ServeHTTP(resolve, httptest.NewRequest(http.MethodGet, "http://proxy.test/ccbunshin/resolve", nil))
	if resolve.Code != http.StatusUnauthorized {
		t.Errorf("/ccbunshin/resolve = %d, want 401 without a token", resolve.Code)
	}
	if strings.Contains(resolve.Body.String(), "routes") {
		t.Error("the route table leaked in the rejection body")
	}
}

// End to end: the proxy token authenticates the caller and never reaches the upstream.
func TestProxyStripsClientTokenBeforeUpstream(t *testing.T) {
	var seenAuth, seenKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenKey = r.Header.Get("X-Api-Key")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()

	t.Setenv("CCBUNSHIN_TEST_CLIENT_TOKEN", "proxy-secret")
	t.Setenv("CCBUNSHIN_TEST_UPSTREAM_TOKEN", "upstream-secret")
	provider := openAITestProvider(t, upstream, map[string]string{"claude-opus-5": "oss-model"})
	provider.authTokenEnv = "CCBUNSHIN_TEST_UPSTREAM_TOKEN"
	handler := &proxy{
		config: loadedConfig{
			clientTokenEnv: "CCBUNSHIN_TEST_CLIENT_TOKEN",
			providers:      map[string]loadedProvider{"p": provider},
			routes:         []loadedRoute{testRoute("claude-*", "p")},
		},
		client: upstream.Client(),
	}
	request := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages",
		strings.NewReader(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer proxy-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if seenAuth != "Bearer upstream-secret" || seenKey != "upstream-secret" {
		t.Errorf("upstream saw auth=%q key=%q, want only the provider's credential", seenAuth, seenKey)
	}
}

// The proxy token is refused as a provider credential on the pass-through path too,
// where the caller's headers are copied verbatim.
func TestProxyStripsClientTokenOnAnthropicPath(t *testing.T) {
	var seenAuth, seenKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenKey = r.Header.Get("X-Api-Key")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	t.Setenv("CCBUNSHIN_TEST_CLIENT_TOKEN", "proxy-secret")
	handler := &proxy{
		config: loadedConfig{
			clientTokenEnv: "CCBUNSHIN_TEST_CLIENT_TOKEN",
			providers:      map[string]loadedProvider{"p": noAuthProvider(t, upstream)},
			routes:         []loadedRoute{testRoute("claude-*", "p")},
		},
		client: upstream.Client(),
	}
	request := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages",
		strings.NewReader(`{"model":"claude-opus-5"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer proxy-secret")
	request.Header.Set("X-Api-Key", "proxy-secret")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if seenAuth != "" || seenKey != "" {
		t.Errorf("upstream saw auth=%q key=%q, want the proxy token stripped", seenAuth, seenKey)
	}
}

// A config with no client token still forwards the caller's credential, byte for byte,
// which is what every config written before this key expects.
func TestProxyForwardsCallerCredentialWithoutClientToken(t *testing.T) {
	var seenAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	handler := &proxy{
		config: loadedConfig{
			providers: map[string]loadedProvider{"p": testProvider(t, upstream, nil)},
			routes:    []loadedRoute{testRoute("claude-*", "p")},
		},
		client: upstream.Client(),
	}
	request := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages",
		strings.NewReader(`{"model":"claude-opus-5"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer callers-own")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if seenAuth != "Bearer callers-own" {
		t.Errorf("Authorization = %q, want the caller's credential forwarded", seenAuth)
	}
}

// noAuthProvider is a provider that sends no credential, which is the only kind allowed
// on the pass-through path when the caller's header is the proxy token.
func noAuthProvider(t *testing.T, server *httptest.Server) loadedProvider {
	t.Helper()
	provider := testProvider(t, server, nil)
	provider.noAuth = true
	return provider
}
