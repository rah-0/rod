//go:build !windows

package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rah-0/rod/lib/launcher/flags"
)

func TestOwnedProfileIsPrivateTemporaryChild(t *testing.T) {
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	// Another local user could create the formerly shared parent in advance.
	planted := t.TempDir()
	if err := os.Symlink(planted, filepath.Join(temporary, "rod")); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	record := filepath.Join(dir, "profile-mode")
	browser := filepath.Join(dir, "browser")
	const source = `#!/bin/sh
for arg; do
	case "$arg" in --user-data-dir=*) profile=${arg#--user-data-dir=} ;; esac
done
ls -ld "$profile" > "$ROD_PROFILE_RECORD"
exit 1
`
	if err := os.WriteFile(browser, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}

	l := New().Bin(browser).Env(append(os.Environ(), "ROD_PROFILE_RECORD="+record)...)
	profile := l.Get(flags.UserDataDir)
	if filepath.Dir(profile) != temporary {
		t.Fatalf("generated profile %q is not a direct child of %q", profile, temporary)
	}
	if other := New().Get(flags.UserDataDir); other == profile {
		t.Fatal("generated profile names repeat")
	}
	if _, err := l.LaunchNew(t.Context()); err == nil {
		t.Fatal("test browser unexpectedly produced a DevTools URL")
	}

	mode, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mode), "drwx------") {
		t.Fatalf("generated profile is not private: %s", mode)
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated profile remains: %v", err)
	}
	if entries, err := os.ReadDir(planted); err != nil || len(entries) != 0 {
		t.Fatalf("launch wrote through a planted directory: %v, %v", entries, err)
	}
}

func TestOwnedProfileRejectsPlantedSymlink(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	victim := t.TempDir()
	sentinel := filepath.Join(victim, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	l := New().Bin("unused-browser")
	profile := l.Get(flags.UserDataDir)
	if err := os.Symlink(victim, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := l.LaunchNew(t.Context()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("planted symlink = %v, want os.ErrExist", err)
	}
	if err := l.CleanupContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(profile); err != nil || target != victim {
		t.Fatalf("planted symlink changed: %q, %v", target, err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("symlink target changed: %v", err)
	}
	entries, err := os.ReadDir(victim)
	if err != nil || len(entries) != 1 {
		t.Fatalf("launch wrote through a planted symlink: %v, %v", entries, err)
	}
}
