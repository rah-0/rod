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
	want := "Authenticated manager page: Managed browser\nManager process and profile: cleaned\n"
	if output.String() != want {
		t.Fatalf("result = %q, want %q", output.String(), want)
	}
}
