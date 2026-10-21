package main

import (
	"fmt"
	"os"
	"strings"
)

// expandProfileEnv expands ${VAR} and $VAR in every string value of a profile's env
// block, using the process environment. It exists because Claude Code treats a
// settings env value as a literal: a profile carrying "$TOKEN" sends the dollar sign
// and the name on the wire, not the secret.
//
// Only env is touched. Other top-level keys (hooks, permissions, statusLine) can
// contain a dollar sign that means something to Claude Code or to a shell, and
// expanding them would corrupt working configs.
//
// The profile file is never rewritten: decision 2 makes a settings env block beat the
// process environment, so a value baked into the file would freeze a stale secret
// until someone edited it. Expansion happens on a copy, at launch.
//
// $$ is the escape for a literal dollar sign. A dollar that starts no valid name is
// left alone, so a value like "100%" or "a$b" passes through unchanged. An unset
// variable is an error: an empty credential surfaces as a 401 from the gateway, which
// reads as a provider fault instead of a typo in the profile.
func expandProfileEnv(payload map[string]any, lookup func(string) (string, bool)) error {
	raw, ok := payload["env"]
	if !ok {
		return nil
	}
	block, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("env must be a JSON object")
	}
	expanded := make(map[string]string, len(block))
	for key, value := range block {
		text, isString := value.(string)
		if !isString {
			continue
		}
		result, err := expandEnvString(text, lookup)
		if err != nil {
			return fmt.Errorf("env %s: %w", key, err)
		}
		expanded[key] = result
	}
	for key, value := range expanded {
		block[key] = value
	}
	return nil
}

// expandEnvString expands $VAR and ${VAR} in one value. It mirrors os.Expand's
// grammar, which it cannot use directly: os.Expand reads the real environment and
// reports nothing when a variable is missing, and this needs to fail loudly instead.
func expandEnvString(text string, lookup func(string) (string, bool)) (string, error) {
	if !strings.ContainsRune(text, '$') {
		return text, nil
	}
	var builder strings.Builder
	builder.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != '$' || i+1 >= len(text) {
			builder.WriteByte(text[i])
			continue
		}
		next := text[i+1]
		if next == '$' {
			builder.WriteByte('$')
			i++
			continue
		}
		name, end, ok := envNameAt(text, i+1)
		if !ok {
			builder.WriteByte('$')
			continue
		}
		value, found := lookup(name)
		if !found {
			return "", fmt.Errorf("$%s is not set", name)
		}
		builder.WriteString(value)
		i = end - 1
	}
	return builder.String(), nil
}

// envNameAt reads a variable name starting at index start, which is either
// "{NAME}" or a bare NAME. It returns the name and the index just past the
// expansion, so the caller can resume after it.
func envNameAt(text string, start int) (name string, end int, ok bool) {
	if start >= len(text) {
		return "", start, false
	}
	if text[start] == '{' {
		close := strings.IndexByte(text[start:], '}')
		if close < 0 {
			return "", start, false
		}
		name := text[start+1 : start+close]
		if !validEnvName(name) {
			return "", start, false
		}
		return name, start + close + 1, true
	}
	stop := start
	for stop < len(text) && isEnvNameByte(text[stop], stop == start) {
		stop++
	}
	if stop == start {
		return "", start, false
	}
	return text[start:stop], stop, true
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isEnvNameByte(name[i], i == 0) {
			return false
		}
	}
	return true
}

// isEnvNameByte reports whether c may appear in a variable name. The first byte
// cannot be a digit, which is what keeps "$1" and "$100" literal.
func isEnvNameByte(c byte, first bool) bool {
	switch {
	case c == '_':
		return true
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return !first
	}
	return false
}

// expandProfileData parses a profile, expands its env block, and re-encodes it. The
// result is what Claude Code is handed in place of the profile file.
func expandProfileData(data []byte) ([]byte, error) {
	payload, err := parseProfile(data)
	if err != nil {
		return nil, err
	}
	if err := expandProfileEnv(payload, os.LookupEnv); err != nil {
		return nil, err
	}
	return encodeProfile(payload)
}
