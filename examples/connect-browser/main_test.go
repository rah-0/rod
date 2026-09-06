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
	want := "Attached page: Attached browser\nCaller-owned launcher: cleanup complete\n"
	if output.String() != want {
		t.Fatalf("result = %q, want %q", output.String(), want)
	}
}
