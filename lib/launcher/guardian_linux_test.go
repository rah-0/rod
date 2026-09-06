package launcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const guardianTestRole = "ROD_GUARDIAN_TEST_ROLE"

func TestGuardianCleansDescendants(t *testing.T) {
	for _, graceful := range []bool{false, true} {
		t.Run(fmt.Sprintf("graceful=%t", graceful), func(t *testing.T) {
			dir := t.TempDir()
			profile := filepath.Join(dir, "profile")
			if err := os.Mkdir(profile, 0o700); err != nil {
				t.Fatal(err)
			}
			l := New()
			l.cleanupUserDataDir = profile
			cmd := guardianTestCommand(t, "browser", dir)
			pid, stop, err := l.startProcess(cmd)
			if err != nil {
				t.Fatal(err)
			}
			waited := false
			t.Cleanup(func() {
				stop()
				if !waited {
					_ = cmd.Wait()
				}
			})
			pids := guardianTestPIDs(t, dir)
			guardianTestTemporaryActive(t, dir)
			if pids[0] != pid {
				t.Fatalf("browser PID = %d, want %d", pids[0], pid)
			}
			if graceful {
				if err := os.WriteFile(filepath.Join(dir, "exit"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				stop()
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			waited = true
			guardianTestGone(t, pids)
			guardianTestTemporaryCleaned(t, dir)
			if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary profile remains: %v", err)
			}
		})
	}
}

func TestGuardianParentExit(t *testing.T) {
	if os.Getenv(guardianTestRole) == "harness" {
		guardianTestParentExit(t)
		return
	}
	for _, mode := range []string{"kill", "panic", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			for _, owned := range []bool{false, true} {
				t.Run(fmt.Sprintf("owned=%t", owned), func(t *testing.T) {
					dir := t.TempDir()
					cmd := guardianTestCommand(t, "harness", dir)
					cmd.Env = append(cmd.Env,
						"ROD_GUARDIAN_TEST_EXIT="+mode,
						"ROD_GUARDIAN_TEST_OWNED="+strconv.FormatBool(owned))
					if output, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("owner-exit harness: %v\n%s", err, output)
					}
				})
			}
		})
	}
}

func TestGuardianRealBrowserParentExit(t *testing.T) {
	bin, found := LookPath()
	if !found {
		t.Skip("a locally installed browser is required")
	}
	for _, phase := range []string{"startup", "active"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			cmd := guardianTestCommand(t, "harness", dir)
			cmd.Env = append(cmd.Env,
				"ROD_GUARDIAN_TEST_EXIT=kill",
				"ROD_GUARDIAN_TEST_OWNED=true",
				"ROD_GUARDIAN_TEST_BROWSER="+bin,
				"ROD_GUARDIAN_TEST_PHASE="+phase)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("browser owner-exit harness: %v\n%s", err, output)
			}
		})
	}
}

func guardianTestParentExit(t *testing.T) {
	// Confine subreaper behavior to this subprocess. It also reaps the guardian
	// after owner death on CI hosts whose PID 1 does not reap orphan processes.
	if err := guardianSubreaper(); err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("ROD_GUARDIAN_TEST_DIR")
	profile := filepath.Join(dir, "profile")
	if err := os.Mkdir(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := guardianTestCommand(t, "owner", dir)
	if os.Getenv("ROD_GUARDIAN_TEST_EXIT") == "timeout" {
		cmd.Args = append(cmd.Args, "-test.timeout=2s")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if !waited {
			_ = cmd.Wait()
		}
		guardianReapChildren(nil)
	})
	pids := guardianTestPIDs(t, dir)
	if os.Getenv("ROD_GUARDIAN_TEST_BROWSER") == "" {
		guardianTestTemporaryActive(t, dir)
	}
	if os.Getenv("ROD_GUARDIAN_TEST_EXIT") == "kill" {
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
	} else if os.Getenv("ROD_GUARDIAN_TEST_EXIT") == "panic" {
		if err := os.WriteFile(filepath.Join(dir, "panic"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("owner unexpectedly exited successfully")
	}
	waited = true
	guardianTestEventually(t, func() bool {
		for _, pid := range pids[:len(pids)-1] {
			if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				return false
			}
		}
		return true
	})
	if os.Getenv("ROD_GUARDIAN_TEST_OWNED") == "true" {
		guardianTestEventually(t, func() bool {
			_, err := os.Stat(profile)
			return errors.Is(err, os.ErrNotExist)
		})
	} else if _, err := os.Stat(profile); err != nil {
		t.Fatalf("caller-owned profile was removed: %v", err)
	}
	if os.Getenv("ROD_GUARDIAN_TEST_BROWSER") == "" {
		guardianTestTemporaryCleaned(t, dir)
	}
}

func TestGuardianOwnerProcess(t *testing.T) {
	if os.Getenv(guardianTestRole) != "owner" {
		return
	}
	dir := os.Getenv("ROD_GUARDIAN_TEST_DIR")
	l := New()
	if bin := os.Getenv("ROD_GUARDIAN_TEST_BROWSER"); bin != "" {
		guardianTestRealBrowser(t, l, bin, dir)
		return
	}
	if os.Getenv("ROD_GUARDIAN_TEST_OWNED") == "true" {
		l.cleanupUserDataDir = filepath.Join(dir, "profile")
	}
	cmd := guardianTestCommand(t, "browser", dir)
	if _, _, err := l.startProcess(cmd); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "panic")); err == nil {
			panic("intentional owner panic")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func guardianTestRealBrowser(t *testing.T, l *Launcher, bin, dir string) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	l.Context(ctx)
	profile := filepath.Join(dir, "profile")
	l.UserDataDir(profile)
	l.cleanupUserDataDir = profile
	cmd := exec.Command(bin, l.FormatArgs()...)
	l.setupCmd(cmd)
	pid, _, err := l.startProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ROD_GUARDIAN_TEST_PHASE") == "active" {
		u, err := l.getURL()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveURL(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	var pids []string
	pids = append(pids, strconv.Itoa(pid))
	var descendants func(int)
	descendants = func(parent int) {
		paths, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", parent))
		for _, path := range paths {
			data, _ := os.ReadFile(path)
			for _, value := range strings.Fields(string(data)) {
				child, err := strconv.Atoi(value)
				if err == nil {
					if child != pid {
						pids = append(pids, value)
					}
					descendants(child)
				}
			}
		}
	}
	descendants(cmd.Process.Pid)
	pids = append(pids, strconv.Itoa(cmd.Process.Pid))
	if err := os.WriteFile(filepath.Join(dir, "pids"), []byte(strings.Join(pids, " ")), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestGuardianBrowserProcess(t *testing.T) {
	role := os.Getenv(guardianTestRole)
	if role != "browser" && role != "child" {
		return
	}
	if os.Getenv(guardianEnv) != "" {
		t.Fatal("guardian marker leaked into browser environment")
	}
	if role == "child" {
		for {
			time.Sleep(time.Hour)
		}
	}
	dir := os.Getenv("ROD_GUARDIAN_TEST_DIR")
	// Stand in for Chrome's socket directory, which is outside its profile and
	// cannot run its own cleanup when the process is abruptly terminated.
	scratch := filepath.Join(os.TempDir(), "socket-dir")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "SingletonSocket"), []byte("socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "browser-tmpdir"), []byte(os.TempDir()), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := guardianTestCommand(t, "child", dir)
	// Crash reporters may detach from Chrome's process group. The Linux
	// subreaper must clean them up as well as ordinary browser children.
	// Disable the test helper's cancellation so graceful helper exit cannot
	// clean up this child before the guardian has to adopt it.
	cmd.Cancel = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("%d %d %d", os.Getpid(), cmd.Process.Pid, os.Getppid())
	if err := os.WriteFile(filepath.Join(dir, "pids"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "exit")); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func guardianTestCommand(t *testing.T, role, dir string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	test := "TestGuardianBrowserProcess"
	if role == "owner" {
		test = "TestGuardianOwnerProcess"
	} else if role == "harness" {
		test = "TestGuardianParentExit"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^"+test+"$")
	cmd.Env = append(os.Environ(), guardianTestRole+"="+role, "ROD_GUARDIAN_TEST_DIR="+dir)
	if role == "browser" {
		cmd.Env = append(cmd.Env, "TMPDIR="+guardianTestTemporaryParent(t, dir))
	}
	return cmd
}

func guardianTestTemporaryParent(t *testing.T, dir string) string {
	t.Helper()
	parent := filepath.Join(dir, "tmp-parent")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "keep"), []byte("caller-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	return parent
}

func guardianTestTemporaryPath(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "browser-tmpdir"))
	if err != nil {
		t.Fatal(err)
	}
	path := string(data)
	if filepath.Dir(path) != filepath.Join(dir, "tmp-parent") {
		t.Fatalf("browser scratch %q is not a private child of the caller's temporary directory", path)
	}
	return path
}

func guardianTestTemporaryActive(t *testing.T, dir string) {
	t.Helper()
	path := guardianTestTemporaryPath(t, dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("browser temporary directory permissions = %v", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(path, "socket-dir", "SingletonSocket")); err != nil {
		t.Fatal(err)
	}
}

func guardianTestTemporaryCleaned(t *testing.T, dir string) {
	t.Helper()
	path := guardianTestTemporaryPath(t, dir)
	guardianTestEventually(t, func() bool {
		_, err := os.Stat(path)
		return errors.Is(err, os.ErrNotExist)
	})
	parent := filepath.Join(dir, "tmp-parent")
	data, err := os.ReadFile(filepath.Join(parent, "keep"))
	if err != nil || string(data) != "caller-owned" {
		t.Fatalf("caller-owned temporary data changed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary directory contains unexpected residue: %v, %v", entries, err)
	}
}

func TestGuardianScratchCleanupOnStartupFailure(t *testing.T) {
	const source = `#!/bin/sh
mkdir "$TMPDIR/socket-dir"
printf socket > "$TMPDIR/socket-dir/SingletonSocket"
printf '%s' "$TMPDIR" > "$ROD_GUARDIAN_TEST_DIR/browser-tmpdir"
printf 'intentional startup failure\n' >&2
exit 1
`
	for _, relative := range []bool{false, true} {
		t.Run(fmt.Sprintf("relative=%t", relative), func(t *testing.T) {
			dir := t.TempDir()
			parent := guardianTestTemporaryParent(t, dir)
			script := filepath.Join(dir, "browser")
			if err := os.WriteFile(script, []byte(source), 0o700); err != nil {
				t.Fatal(err)
			}
			if relative {
				parent = "tmp-parent"
			}
			originalTemporary := os.Getenv("TMPDIR")
			l := New().Context(t.Context()).Bin(script).WorkingDir(dir).Preferences(`{}`).
				Env(append(os.Environ(), "TMPDIR="+parent, "ROD_GUARDIAN_TEST_DIR="+dir)...)
			if _, err := l.Launch(); err == nil || !strings.Contains(err.Error(), "intentional startup failure") {
				t.Fatalf("startup error = %v", err)
			}
			l.Cleanup()
			guardianTestTemporaryCleaned(t, dir)
			if os.Getenv("TMPDIR") != originalTemporary {
				t.Fatal("launcher changed the parent process's temporary directory")
			}
		})
	}
}

func TestGuardianTemporaryCleanupConfined(t *testing.T) {
	dir := t.TempDir()
	actual := guardianTestTemporaryParent(t, dir)
	victim := filepath.Join(dir, "victim")
	alias := filepath.Join(dir, "alias")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	configuration := guardianCommand{Env: []string{"TMPDIR=" + alias}}
	cleanup, err := guardianTemporaryDir(&configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	path := strings.TrimPrefix(configuration.Env[0], "TMPDIR=")
	name := filepath.Base(path)
	if filepath.Dir(path) != alias {
		t.Fatal("temporary path expanded the caller's short symlink spelling")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(victim, name), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(victim, name, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(filepath.Join(actual, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original temporary directory remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(actual, "keep")); err != nil {
		t.Fatalf("caller temporary sibling was removed: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("cleanup escaped through a repointed temporary-directory symlink: %v", err)
	}
}

func TestGuardianTemporaryDirectoryResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "actual", "work")
	actual := filepath.Join(dir, "actual", "scratch")
	wrong := filepath.Join(dir, "scratch")
	link := filepath.Join(dir, "work-link")
	for _, path := range []string{work, actual, wrong} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "keep"), []byte("caller-owned"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(work, link); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeLink, err := filepath.Rel(cwd, link)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, work, temporary string }{
		{"absolute working directory", link, "../scratch"},
		{"relative working directory", relativeLink, "../scratch"},
		{"absolute temporary directory", "", link + "/../scratch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configuration := guardianCommand{Dir: tc.work, Env: []string{"TMPDIR=" + tc.temporary}}
			cleanup, err := guardianTemporaryDir(&configuration)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			path := strings.TrimPrefix(configuration.Env[0], "TMPDIR=")
			name := filepath.Base(path)
			if err := os.WriteFile(path+"/created", []byte("scratch"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(actual, name, "created")); err != nil {
				t.Fatalf("browser temporary path did not follow the working-directory symlink: %v", err)
			}
			if _, err := os.Stat(filepath.Join(wrong, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary directory was created beside the symlink instead of its target: %v", err)
			}
			cleanup()
			if _, err := os.Stat(filepath.Join(actual, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary directory remains: %v", err)
			}
			for _, parent := range []string{actual, wrong} {
				if _, err := os.Stat(filepath.Join(parent, "keep")); err != nil {
					t.Fatalf("caller-owned temporary data was removed: %v", err)
				}
			}
		})
	}
}

func guardianTestPIDs(t *testing.T, dir string) []int {
	t.Helper()
	var pids []int
	guardianTestEventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(dir, "pids"))
		if err != nil {
			return false
		}
		var parsed []int
		for _, value := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(value)
			if err != nil || pid <= 0 {
				return false
			}
			parsed = append(parsed, pid)
		}
		pids = parsed
		return len(pids) >= 2
	})
	return pids
}

func guardianTestGone(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("process %d remains after guardian exit: %v", pid, err)
		}
	}
}

func guardianTestEventually(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("guardian cleanup did not finish within five seconds")
}
