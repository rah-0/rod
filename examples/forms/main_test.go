package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Initial alerts suppressed: 1\n",
		"Submitted by POST: Release notes\n",
		"Uploaded: note.txt (24 bytes)\n",
		"Content: A file uploaded by Rod.\n",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q:\n%s", want, output.String())
		}
	}
}
