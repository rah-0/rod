package main

import (
	"bytes"
	"testing"
)

func TestOwnedLaunchExample(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "title: Owned browser\n" +
		"expired operation: deadline exceeded\n" +
		"owned process: stopped\n" +
		"temporary profile: removed\n"
	if got := output.String(); got != want {
		t.Fatalf("example result:\n%s\nwant:\n%s", got, want)
	}
}
