package main

import (
	"bytes"
	"testing"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output, "../../fixtures/chrome-extension"); err != nil {
		t.Fatal(err)
	}
	const want = "Extension title: test-extension\n"
	if got := output.String(); got != want {
		t.Fatalf("example result = %q, want %q", got, want)
	}
}
