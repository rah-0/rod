package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneration(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is required for helper generation")
	}
	dir := t.TempDir()
	source, output := filepath.Join(dir, "helper.js"), filepath.Join(dir, "helper.go")
	if err := os.WriteFile(source, []byte(`const functions = { value() { return 1 }, twice() { return functions.value() * 2 } }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generate(source, output); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "[]*Function{Value}") || !strings.Contains(string(first), `Name:         "twice"`) {
		t.Fatalf("helper/dependency missing: %s", first)
	}
	if err := generate(source, output); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("nondeterministic helpers: %v", err)
	}
	if err := os.WriteFile(source, []byte("const functions = {"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generate(source, output); err == nil {
		t.Fatal("invalid JavaScript accepted")
	}
	second, err = os.ReadFile(output)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("failed generation damaged helpers: %v", err)
	}
}
