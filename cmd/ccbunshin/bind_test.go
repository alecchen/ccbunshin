package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateBind(t *testing.T) {
	// Empty is the documented "every interface", and both IP forms and a bare
	// hostname are legal.
	for _, bind := range []string{"", "127.0.0.1", "0.0.0.0", "::1", "::", "localhost", "my-host.local"} {
		if err := validateBind(bind); err != nil {
			t.Errorf("validateBind(%q) = %v, want it accepted", bind, err)
		}
	}
	for _, bind := range []string{"127.0.0.1:8080", ":8080", "localhost:3456", "127.0.0.1 ", "bad host"} {
		if err := validateBind(bind); err == nil {
			t.Errorf("validateBind(%q) accepted an address that would fail at bind time", bind)
		}
	}
}

// A port in bind is the mistake the check exists for, so the message has to say so.
func TestLoadConfigRejectsBindWithPort(t *testing.T) {
	_, err := loadConfig(authConfig(t, `"bind": "127.0.0.1:8080",`))
	if err == nil {
		t.Fatal("a bind carrying a port was accepted")
	}
	if !strings.Contains(err.Error(), "without a port") {
		t.Errorf("error %q does not explain the mistake", err)
	}
}

// An unset token variable rejects everything, so the line is written once rather than
// once per request, or a launcher that retries floods the log.
func TestEmptyTokenVariableLogsOnce(t *testing.T) {
	var buffer bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buffer)
	defer log.SetOutput(original)

	p := authProxy(t, "")
	p.config.clientTokenEnv = "CCBUNSHIN_TEST_UNSET_TOKEN"
	request := httptest.NewRequest(http.MethodGet, "http://proxy.test/x", nil)
	for i := 0; i < 5; i++ {
		if p.authorized(request) {
			t.Fatal("an unset token variable opened the gate")
		}
	}
	if count := strings.Count(buffer.String(), "is empty"); count != 1 {
		t.Errorf("logged the empty-variable warning %d times, want 1:\n%s", count, buffer.String())
	}
}
