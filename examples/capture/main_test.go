package main

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	directory := t.TempDir()
	var output bytes.Buffer
	if err := run(t.Context(), &output, directory); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Desktop viewport: 800x600 @1x", "Mobile viewport: 375x667 @2x", "Artifacts: " + directory} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q:\n%s", want, output.String())
		}
	}
	for _, expected := range []struct {
		Name   string
		Width  int
		Height int
	}{
		{"page.png", 800, 600},
		{"element.png", 320, 160},
		{"mobile.png", 750, 1334},
	} {
		data, err := os.ReadFile(filepath.Join(directory, expected.Name))
		if err != nil {
			t.Fatal(err)
		}
		image, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("decode %s: %v", expected.Name, err)
		}
		if image.Bounds().Dx() != expected.Width || image.Bounds().Dy() != expected.Height {
			t.Errorf("%s dimensions: %v, want %dx%d", expected.Name, image.Bounds(), expected.Width, expected.Height)
		}
		if expected.Name == "element.png" {
			r, g, b, a := image.At(0, 0).RGBA()
			if r != 0x1717 || g != 0x4242 || b != 0x6b6b || a != 0xffff {
				t.Errorf("element capture did not start at the card's background: %x %x %x %x", r, g, b, a)
			}
		}
	}
	pdf, err := os.ReadFile(filepath.Join(directory, "page.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.Contains(pdf, []byte("/Type /Page")) || !bytes.Contains(pdf, []byte("startxref")) || !bytes.HasSuffix(bytes.TrimSpace(pdf), []byte("%%EOF")) {
		t.Fatal("page.pdf is missing its PDF header, page object, or completed trailer")
	}
}
