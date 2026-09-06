package main

import (
	"bytes"
	"testing"
)

func TestDownload(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "Downloaded report.csv:\nitem,quantity\nnotebook,2\npencil,3\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
