package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
