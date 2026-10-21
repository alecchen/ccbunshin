package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func profileEnv(t *testing.T, data string) (map[string]any, error) {
	t.Helper()
	return parseProfile([]byte(data))
}

func expandWith(t *testing.T, payload map[string]any, env map[string]string) error {
	t.Helper()
	return expandProfileEnv(payload, func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	})
}

func envValue(t *testing.T, payload map[string]any, key string) string {
	t.Helper()
	block, ok := payload["env"].(map[string]any)
	if !ok {
		t.Fatalf("env block is %T, want an object", payload["env"])
	}
	value, ok := block[key].(string)
	if !ok {
		t.Fatalf("env %s is %T, want a string", key, block[key])
	}
	return value
}

func TestExpandProfileEnvHandlesBothForms(t *testing.T) {
	payload, err := profileEnv(t, `{"env": {"A": "$TOKEN", "B": "Bearer ${TOKEN}", "C": "no-vars"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := expandWith(t, payload, map[string]string{"TOKEN": "sekrit"}); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"A": "sekrit", "B": "Bearer sekrit", "C": "no-vars"} {
		if got := envValue(t, payload, key); got != want {
			t.Errorf("env %s = %q, want %q", key, got, want)
		}
	}
}

// An unset variable is an error: expanding it to the empty string would send a blank
// credential, which the gateway reports as 401 and reads as a provider fault.
func TestExpandProfileEnvRejectsUnsetVariable(t *testing.T) {
	payload, err := profileEnv(t, `{"env": {"ANTHROPIC_AUTH_TOKEN": "${MISSING_TOKEN}"}}`)
	if err != nil {
		t.Fatal(err)
	}
	err = expandWith(t, payload, map[string]string{})
	if err == nil {
		t.Fatal("an unset variable expanded silently")
	}
	for _, want := range []string{"ANTHROPIC_AUTH_TOKEN", "MISSING_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// $$ is the escape for a value that genuinely contains a dollar sign, and a dollar
// followed by no valid name start stays literal.
func TestExpandProfileEnvKeepsLiteralDollars(t *testing.T) {
	payload, err := profileEnv(t, `{"env": {"A": "pa$$word", "B": "cost is $100", "C": "trailing $"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := expandWith(t, payload, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"A": "pa$word", "B": "cost is $100", "C": "trailing $"} {
		if got := envValue(t, payload, key); got != want {
			t.Errorf("env %s = %q, want %q", key, got, want)
		}
	}
}

// Only env is expanded. A dollar sign under another top-level key is Claude Code's or a
// shell's, and rewriting it would corrupt a working profile.
func TestExpandProfileEnvLeavesOtherKeysAlone(t *testing.T) {
	payload, err := profileEnv(t, `{"env": {}, "model": "$NOT_A_VAR", "hooks": {"x": "$ALSO_NOT"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := expandWith(t, payload, map[string]string{"NOT_A_VAR": "wrong"}); err != nil {
		t.Fatal(err)
	}
	if got := payload["model"]; got != "$NOT_A_VAR" {
		t.Errorf("model = %q, want it untouched", got)
	}
	hooks, _ := payload["hooks"].(map[string]any)
	if got := hooks["x"]; got != "$ALSO_NOT" {
		t.Errorf("hooks.x = %q, want it untouched", got)
	}
}

// The profile file keeps its placeholders. Decision 2 makes an env block beat the
// process environment, so a value written back would freeze a rotated secret.
func TestExpandProfileDataDoesNotTouchTheFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "p.json")
	original := `{"env": {"ANTHROPIC_AUTH_TOKEN": "${EXPAND_TEST_TOKEN}"}}`
	if err := os.WriteFile(file, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXPAND_TEST_TOKEN", "sekrit")
	expanded, err := expandProfileData([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != original {
		t.Fatalf("profile on disk was rewritten to %s", onDisk)
	}
	var payload map[string]any
	if err := json.Unmarshal(expanded, &payload); err != nil {
		t.Fatal(err)
	}
	if got := envValue(t, payload, "ANTHROPIC_AUTH_TOKEN"); got != "sekrit" {
		t.Errorf("expanded token = %q, want the environment value", got)
	}
}

// The temporary copy holds a secret, so it must be owner-only and must not survive the
// launch.
func TestWriteLaunchSettingsIsPrivateAndRemoved(t *testing.T) {
	path, cleanup, err := writeLaunchSettings([]byte(`{"env":{"A":"sekrit"}}`))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("mode = %o, want 600", mode)
	}
	defer cleanup()
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("settings file survived cleanup: %v", err)
	}
}

func TestExpandProfileEnvRejectsNonObjectEnv(t *testing.T) {
	payload, err := profileEnv(t, `{"env": "not-an-object"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := expandWith(t, payload, map[string]string{}); err == nil {
		t.Fatal("a non-object env block was accepted")
	}
}
