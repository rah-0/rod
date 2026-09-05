package rod

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveFileDefaultPaths(t *testing.T) {
	for _, test := range []struct {
		name string
		kind saveFileType
		dir  string
		ext  string
	}{
		{"screenshot", saveFileTypeScreenshot, "screenshots", ".png"},
		{"PDF", saveFileTypePDF, "pdf", ".pdf"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			content := []byte("output")
			if err := saveFile(test.kind, content, []string{""}); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join("tmp", test.dir, "*"+test.ext))
			if err != nil || len(files) != 1 {
				t.Fatalf("default output files: %v, error: %v", files, err)
			}
			got, err := os.ReadFile(files[0])
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("saved content: %q, error: %v", got, err)
			}
		})
	}
}
