package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rah-0/rod/lib/jsonvalue"
)

func TestPinnedGeneration(t *testing.T) {
	output := filepath.Join(t.TempDir(), "list.go")
	if err := generate("devices.json", output); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := generate("devices.json", output); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("device generation is nondeterministic")
	}
	badSource := filepath.Join(t.TempDir(), "devices.json")
	if err := os.WriteFile(badSource, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generate(badSource, output); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	second, err = os.ReadFile(output)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("failed generation damaged output: %v", err)
	}
}

func TestUserAgentPolicy(t *testing.T) {
	for _, source := range []string{"", "Chrome/%s"} {
		got := getUserAgent(jsonvalue.New(map[string]any{"user-agent": source}))
		if !strings.Contains(got, "Chrome/"+chromeVersion) {
			t.Fatalf("user agent = %q", got)
		}
	}
	if got := normalizeName("iPad Mini/Plus"); got != "IPadMiniorPlus" {
		t.Fatalf("name = %q", got)
	}
}
