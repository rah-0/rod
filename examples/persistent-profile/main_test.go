package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentProfileExample(t *testing.T) {
	profile := t.TempDir()
	sentinel := filepath.Join(profile, "caller-owned")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var output bytes.Buffer
		if err := run(t.Context(), &output, profile); err != nil {
			t.Fatal(err)
		}
		if want := "Persistent automation profile: " + profile + "\n"; output.String() != want {
			t.Fatalf("example output = %q, want %q", output.String(), want)
		}
		data, err := os.ReadFile(sentinel)
		if err != nil || string(data) != "keep" {
			t.Fatalf("cleanup changed caller-owned profile: %q, %v", data, err)
		}
	}
}
