package main

import (
	"bytes"
	"testing"
)

func TestJSONEvaluation(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "Items: Apple, Pear; total: 8\nDiscarded result, status: ready\nUndefined property rejected\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
