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
	"strings"
	"time"
)

type config struct {
	name     string
	listen   string
	upstream *url.URL
	apiKey   string
	modelMap map[string]string
	timeout  time.Duration
}

type proxy struct {
	config config
	client *http.Client
}

func loadConfig(prefix, name string) (config, error) {
	listen := getenv(prefix+"_LISTEN", "127.0.0.1:3456")
	upstreamValue := os.Getenv(prefix + "_UPSTREAM")
	if upstreamValue == "" {
		return config{}, fmt.Errorf("%s_UPSTREAM is required", prefix)
	}
	upstream, err := url.Parse(upstreamValue)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return config{}, fmt.Errorf("%s_UPSTREAM must be an absolute URL", prefix)
	}
	timeout := 60 * time.Second
	if value := os.Getenv(prefix + "_TIMEOUT"); value != "" {
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return config{}, fmt.Errorf("%s_TIMEOUT must be a positive duration", prefix)
		}
	}
	return config{
		name:     name,
		listen:   listen,
		upstream: upstream,
		apiKey:   os.Getenv(prefix + "_API_KEY"),
		modelMap: parseModelMap(os.Getenv(prefix + "_MODEL_MAP")),
		timeout:  timeout,
	}, nil
}

func getenv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func parseModelMap(value string) map[string]string {
	result := make(map[string]string)
	for _, item := range strings.Split(value, ",") {
		parts := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			result[parts[0]] = parts[1]
		}
	}
	return result
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
	if len(body) > 0 && strings.HasSuffix(r.Header.Get("Content-Type"), "json") {
		body = rewriteModel(body, p.config.modelMap)
	}

	target := *p.config.upstream
	target.Path = strings.TrimRight(target.Path, "/") + "/" + strings.TrimLeft(r.URL.Path, "/")
	target.RawQuery = r.URL.RawQuery
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "failed to create upstream request", http.StatusBadGateway)
		return
	}
	copyHeaders(request.Header, r.Header)
	request.Header.Del("Host")
	if p.config.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.config.apiKey)
	}

	response, err := p.client.Do(request)
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	copyHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if flusher, ok := w.(http.Flusher); ok {
		_, _ = io.Copy(flushingWriter{writer: w, flusher: flusher}, response.Body)
		return
	}
	_, _ = io.Copy(w, response.Body)
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
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Host") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func rewriteModel(body []byte, mappings map[string]string) []byte {
	if len(mappings) == 0 {
		return body
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return body
	}
	model, ok := payload["model"].(string)
	if !ok {
		return body
	}
	mapped, ok := mappings[model]
	if !ok {
		return body
	}
	payload["model"] = mapped
	result, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return result
}

func run(ctx context.Context, cfg config) error {
	server := &http.Server{Addr: cfg.listen, Handler: &proxy{config: cfg, client: &http.Client{Timeout: cfg.timeout}}}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	log.Printf("%s proxy listening on %s -> %s", cfg.name, cfg.listen, cfg.upstream)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func main() {
	provider1, err := loadConfig("CCBUNSHIN_PROVIDER1", "provider1")
	if err != nil {
		log.Fatal(err)
	}
	provider2, err := loadConfig("CCBUNSHIN_PROVIDER2", "provider2")
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 2)
	go func() { result <- run(ctx, provider1) }()
	go func() { result <- run(ctx, provider2) }()
	if err := <-result; err != nil {
		log.Fatal(err)
	}
	cancel()
	if err := <-result; err != nil {
		log.Fatal(err)
	}
}
