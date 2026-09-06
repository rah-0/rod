package main

import (
	"bytes"
	"testing"
)

func TestNetwork(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "Initial request: X-Example=configured, session=example\n" +
		"Browser cookie: session=example (HttpOnly=true)\nJavaScript cookies: \"\"\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
