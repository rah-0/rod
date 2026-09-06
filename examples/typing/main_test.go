package main

import (
	"bytes"
	"testing"
)

func TestTyping(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "Rejected text: value=existing:, focused=false, keys=0/0\n" +
		"Typed: existing:Go!, keys=4/4, submitted=true\n" +
		"Inserted: こんにちは 🦊, keys=4/4\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
