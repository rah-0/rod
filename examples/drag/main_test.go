package main

import (
	"bytes"
	"testing"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "Dropped: hello from Rod\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
