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
	want := "HTTP app: Notebook (2 items, 720px)\nInline page: Inline fixture\n"
	if output.String() != want {
		t.Fatalf("unexpected browser results:\ngot:  %q\nwant: %q", output.String(), want)
	}
}
