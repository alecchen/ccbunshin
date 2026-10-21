package main

import (
	"strings"
	"testing"
)

// Re-encoding the profile must not change a value the profile stated. A number that
// does not fit a float64 silently rounds unless the parse keeps literals.
func TestExpandProfileDataPreservesLargeNumbers(t *testing.T) {
	const big = "9007199254740993"
	data := []byte(`{"env": {}, "timeout": ` + big + `}`)
	out, err := expandProfileData(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), big) {
		t.Fatalf("re-encoded profile lost the exact number:\n%s", out)
	}
}
