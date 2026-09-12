package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
)

// dialect names the wire format a route's upstream speaks.
//
// anthropic passes the caller's request through byte-for-byte, which is what every
// config written before dialects existed expects. openai-chat translates an Anthropic
// /v1/messages call onto an OpenAI-compatible /chat/completions call.
type dialect string

const (
	dialectAnthropic  dialect = "anthropic"
	dialectOpenAIChat dialect = "openai-chat"
)

// parseDialect reads an optional config value. An empty value is the pass-through
// dialect, because existing configs at ~/.config/ccbunshin/proxy.json and
// /etc/ccbunshin/proxy.json have no dialect key and proxyStart refuses to start a
// config that fails validation.
func parseDialect(where, value string) (dialect, error) {
	switch value {
	case "", string(dialectAnthropic):
		return dialectAnthropic, nil
	case string(dialectOpenAIChat):
		return dialectOpenAIChat, nil
	}
	return "", fmt.Errorf("%s dialect must be %q or %q", where, dialectAnthropic, dialectOpenAIChat)
}

// parseEffort reads an optional reasoning_effort default. Unlike dialect, an unset value
// stays empty rather than normalizing to a default: "" is what marks "the caller decides",
// and the request path needs to tell that apart from a configured value.
func parseEffort(where, value string) (string, error) {
	switch value {
	case "", "low", "medium", "high", "xhigh", "max":
		return value, nil
	}
	return "", fmt.Errorf("%s effort must be one of \"low\", \"medium\", \"high\", \"xhigh\", \"max\"", where)
}

// requestPlan is the outcome of resolving a request model: which model goes upstream,
// in which shape, and through which provider.
type requestPlan struct {
	requestedModel string // what the caller asked for; echoed back in the response
	targetModel    string // what goes upstream
	dialect        dialect
	provider       loadedProvider
	isStreaming    bool
	inputTokens    int    // local estimate, surfaced before the upstream reports usage
	effortDefault  string // reasoning_effort applied when the caller sent none; "" for none
}

// planFor resolves a request model against the ordered routes, then decides the target
// model and dialect. Precedence is most-specific-first.
//
// Target model: route models, then provider models, then the provider default, then the
// requested id unchanged.
//
// Dialect: the per-target-model override, then the route, then the provider, then
// anthropic. The override keys on the target id because that is the id the upstream is
// asked for, and therefore the thing that actually has a dialect.
func (c loadedConfig) planFor(model string) (requestPlan, bool) {
	for _, item := range c.routes {
		matched, _ := path.Match(item.pattern, model)
		if !matched {
			continue
		}
		p, ok := c.providers[item.provider]
		if !ok {
			return requestPlan{}, false
		}
		target := item.models[model]
		if target == "" {
			target = p.models[model]
		}
		if target == "" {
			target = p.defaultModel
		}
		if target == "" {
			target = model
		}
		// Only a value the config actually stated may override the layer below it.
		// parseDialect normalizes an unset value to anthropic, so comparing the
		// route's resolved dialect against "" would never match, and every route
		// would silently mask its provider's dialect.
		d := p.dialect
		if item.configured {
			d = item.dialect
		}
		if override, ok := item.modelDialects[target]; ok {
			d = override
		}
		if d == "" {
			d = dialectAnthropic
		}
		// Effort is a default, never an override. Claude Code states an effort on every
		// effort-capable request (/effort is session-level and re-sent each turn), so a
		// value configured here cannot displace a choice the user made - the caller's
		// value always wins in translateAnthropicRequest. What this reaches is the tiers
		// that state nothing, which is why it is worth configuring at all.
		effort := p.effort
		if item.effort != "" {
			effort = item.effort
		}
		return requestPlan{
			requestedModel: model,
			targetModel:    target,
			dialect:        d,
			provider:       p,
			effortDefault:  effort,
		}, true
	}
	return requestPlan{}, false
}

// isAnthropicOnlyHeader reports whether a request header describes the Anthropic
// protocol and must not be forwarded to an OpenAI-chat upstream.
func isAnthropicOnlyHeader(key string) bool {
	switch strings.ToLower(key) {
	case "x-api-key", "anthropic-version", "anthropic-beta",
		"anthropic-dangerous-direct-browser-access", "host":
		return true
	}
	return false
}

// applyOpenAIChatHeaders fills dst from an inbound Anthropic request for an OpenAI-chat
// upstream. The caller's own credential is forwarded: an existing Bearer token as-is, an
// x-api-key promoted to Bearer. Nothing is read from config, so no secret lives there.
func applyOpenAIChatHeaders(dst, src http.Header) {
	for key, values := range src {
		if isAnthropicOnlyHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	// The body is re-encoded, so anything describing the inbound encoding is now wrong.
	dst.Del("Content-Length")
	dst.Del("Accept-Encoding")
	dst.Set("Content-Type", "application/json")
	if auth := src.Get("Authorization"); auth != "" {
		dst.Set("Authorization", auth)
	} else if key := src.Get("X-Api-Key"); key != "" {
		dst.Set("Authorization", "Bearer "+key)
	} else {
		dst.Del("Authorization")
	}
}

// applyTranslatedResponseHeaders replaces the upstream header copy for a translated
// response. Nothing is carried over: the body no longer matches what the upstream
// described, so an upstream Content-Length or Content-Encoding would be a lie, and Go's
// server sets chunked framing itself when Content-Length is absent.
func applyTranslatedResponseHeaders(dst http.Header, streaming bool) {
	if streaming {
		dst.Set("Content-Type", "text/event-stream")
		dst.Set("Cache-Control", "no-cache")
		dst.Set("X-Accel-Buffering", "no")
		return
	}
	dst.Set("Content-Type", "application/json")
}

// anthropicErrorTypeForStatus maps an HTTP status to the Anthropic error type a client
// expects. Claude Code's error classifier keys on the Anthropic envelope, so an
// upstream's OpenAI-shaped error body must be re-wrapped rather than forwarded.
func anthropicErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable, 529:
		return "overloaded_error"
	}
	return "api_error"
}

type anthropicErrorBody struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// writeTranslatedError renders an Anthropic error envelope. It is only usable before a
// status is committed; once streaming has started, the stream translator owns errors.
func writeTranslatedError(w http.ResponseWriter, status int, errorType, message string) {
	var body anthropicErrorBody
	body.Type = "error"
	body.Error.Type = errorType
	body.Error.Message = message
	encoded, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "translation error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}
