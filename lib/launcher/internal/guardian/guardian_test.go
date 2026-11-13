//go:build !windows

package guardian

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// The private invocation needs the marker variable, its exact arguments, and a
// pipe as both standard input and descriptor 3. Without any one of them the
// executable runs as usual: it reports nothing on descriptor 3 and leaves the
// directory that the invocation names as the profile to remove.
func TestPrivateInvocationRequirements(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	private := func(profile string) []string { return []string{Argument, profile, ""} }
	const pipe, regular, closed = "pipe", "regular file", "closed"
	for _, test := range []struct {
		name          string
		marker        string
		arguments     func(profile string) []string
		input, status string
		exit          int
	}{
		{"ordinary arguments", "1", func(string) []string { return []string{"-test.run=^$"} }, pipe, pipe, 0},
		{"no marker", "", private, pipe, pipe, 2},
		{"another marker value", "true", private, pipe, pipe, 2},
		{"too few arguments", "1", func(profile string) []string { return []string{Argument, profile} }, pipe, pipe, 2},
		{"standard input is no pipe", "1", private, closed, pipe, 2},
		{"descriptor 3 is closed", "1", private, pipe, closed, 2},
		{"descriptor 3 is a file", "1", private, pipe, regular, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			profile := filepath.Join(dir, "profile")
			if err := os.Mkdir(profile, 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, test.arguments(profile)...)
			cmd.Env = os.Environ()
			if test.marker != "" {
				cmd.Env = append(cmd.Env, Env+"="+test.marker)
			}
			if test.input == pipe {
				// The owner is gone: a supervisor would report that and exit.
				input, owner, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
				_ = owner.Close()
				cmd.Stdin = input
			}
			// What the process writes to descriptor 3.
			report := func() ([]byte, error) { return nil, nil }
			switch test.status {
			case pipe:
				reports, status, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer reports.Close()
				cmd.ExtraFiles = []*os.File{status}
				report = func() ([]byte, error) {
					_ = status.Close()
					return io.ReadAll(reports)
				}
			case regular:
				status, err := os.Create(filepath.Join(dir, "status"))
				if err != nil {
					t.Fatal(err)
				}
				defer status.Close()
				cmd.ExtraFiles = []*os.File{status}
				report = func() ([]byte, error) { return os.ReadFile(status.Name()) }
			}
			output, err := cmd.CombinedOutput()
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != test.exit {
				t.Fatalf("exit: %v, want status %d\n%s", err, test.exit, output)
			}
			if _, err := os.Stat(profile); err != nil {
				t.Fatalf("the directory named as the profile was removed: %v", err)
			}
			if written, err := report(); err != nil || len(written) != 0 {
				t.Fatalf("report on descriptor 3: %q, %v", written, err)
			}
		})
	}
}

// The browser's scratch directory is one that TemporaryDir creates. A candidate
// name that exists, even as a symlink, belongs to somebody else: it is neither
// used nor removed.
func TestTemporaryDirSkipsExistingNames(t *testing.T) {
	base := t.TempDir()
	victim := t.TempDir()
	taken := filepath.Join(base, "taken")
	if err := os.Mkdir(taken, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	kept := []string{filepath.Join(taken, "keep"), filepath.Join(victim, "keep"), filepath.Join(base, "link", "keep")}
	for _, path := range kept[:2] {
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unchanged := func() {
		t.Helper()
		for _, path := range kept {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("a directory that the guardian did not create changed: %v", err)
			}
		}
	}

	configuration := Command{Env: []string{"A=1", "TMPDIR=" + base}, Scratch: []string{"taken", "link", "free"}}
	cleanup, err := TemporaryDir(&configuration)
	if err != nil {
		t.Fatal(err)
	}
	free := filepath.Join(base, "free")
	if want := []string{"A=1", "TMPDIR=" + free}; !reflect.DeepEqual(configuration.Env, want) {
		t.Fatalf("browser environment = %q, want %q", configuration.Env, want)
	}
	if err := os.WriteFile(filepath.Join(free, "scratch"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(free); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scratch directory remains: %v", err)
	}
	unchanged()

	for _, names := range [][]string{{"taken", "link"}, nil} {
		configuration = Command{Env: []string{"TMPDIR=" + base}, Scratch: names}
		if _, err := TemporaryDir(&configuration); err == nil || errors.Is(err, os.ErrExist) != (names != nil) {
			t.Fatalf("names %q: %v", names, err)
		}
		unchanged()
	}
}
