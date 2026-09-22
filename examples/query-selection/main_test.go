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
	want := `Optional notice: false
CSS items: 2
Text match: Beta
Scoped button: Start
Search result: Alpha
Frame text: Frame content
Shadow text: Shadow content
Race outcome: Ready
Missing element: deadline exceeded
`
	if output.String() != want {
		t.Fatalf("unexpected query results:\ngot:  %q\nwant: %q", output.String(), want)
	}
}
