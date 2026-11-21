package utils_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rah-0/rod/lib/utils"
)

func TestOutputFileExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "captures", "capture.png")
	if err := utils.OutputFileExclusive(path, []byte("private")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		file, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		dir, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if file.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
			t.Fatalf("modes: file %o, directory %o", file.Mode().Perm(), dir.Mode().Perm())
		}
	}
	if err := utils.OutputFileExclusive(path, []byte("overwrite")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "private" {
		t.Fatalf("original changed: %q, %v", data, err)
	}
	if err := utils.OutputFileExclusive(filepath.Dir(path), nil); err == nil {
		t.Fatal("directory accepted")
	}
	link := filepath.Join(filepath.Dir(path), "link.png")
	if err := os.Symlink(path, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation unavailable")
		}
		t.Fatal(err)
	}
	if err := utils.OutputFileExclusive(link, []byte("overwrite")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("symlink: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "private" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}
