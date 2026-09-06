package main

import (
	"bytes"
	"testing"
)

func TestProxyAuth(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "Authenticated proxy: challenge=true, forwarded=true\n" +
		"Origin received proxy marker: true\nProxy credentials forwarded to origin: false\nPage: Authenticated proxy\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
