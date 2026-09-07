package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRewriteModel(t *testing.T) {
	got := rewriteModel([]byte(`{"model":"opus","stream":true}`), map[string]string{"opus": "provider-model"})
	if !strings.Contains(string(got), `"model":"provider-model"`) {
		t.Fatalf("model was not rewritten: %s", got)
	}
}

func TestProxyForwardsRequestAndAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization was not replaced")
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

	parsed, _ := url.Parse(upstream.URL)
	handler := &proxy{config: config{upstream: parsed, apiKey: "secret", modelMap: map[string]string{"opus": "provider-model"}}, client: upstream.Client()}
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

func TestHealthz(t *testing.T) {
	handler := &proxy{config: config{}, client: http.DefaultClient}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://proxy.test/healthz", nil))
	if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
		t.Fatalf("health response = %d %q", response.Code, response.Body.String())
	}
}
