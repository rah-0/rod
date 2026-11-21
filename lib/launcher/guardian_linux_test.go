package launcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rah-0/rod/lib/launcher/internal/guardian"
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
	if err := guardian.Subreaper(); err != nil {
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
		guardian.ReapChildren(nil)
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
	if os.Getenv(guardian.Env) != "" {
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

// The guardian starts the browser with the configured arguments, environment,
// and working directory, in a process group of its own, without standard input,
// and with the launcher's output. The browser receives none of the pipes that
// the launch creates: a copy of the pipe that ties the guardian to its owner
// would hide the owner's exit from the guardian.
func TestGuardianBrowserProcessState(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(filepath.Join(dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("the sleep command is required")
	}
	descriptors := func(pid int) map[string]string {
		targets := map[string]string{}
		entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			// A descriptor can be closed before its target is read: the one
			// that lists this process's own descriptors always is.
			if target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, entry.Name())); err == nil {
				targets[entry.Name()] = target
			}
		}
		return targets
	}
	// A pipe that the test process inherited without the close-on-exec flag
	// reaches the browser under the same number. Any other pipe would have to
	// come from the launch, so list them before it creates its own.
	want := descriptors(os.Getpid())
	want["0"], want["1"], want["2"] = os.DevNull, output.Name(), output.Name()
	// The browser stand-in exits by itself after a minute.
	cmd := exec.Command(sleep, "60")
	cmd.Dir = dir
	// Bytes that are not valid UTF-8 reach the browser unchanged. The marker of
	// the guardian's own invocation never reaches it.
	const entry = "ROD_GUARDIAN_TEST=browser\xff"
	cmd.Env = []string{entry, guardian.Env + "=1", "TMPDIR=" + guardianTestTemporaryParent(t, dir)}
	cmd.Stdout = output
	cmd.Stderr = output
	pid, stop, err := New().startProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop()
		_ = cmd.Wait()
	})
	process := fmt.Sprintf("/proc/%d/", pid)

	got := descriptors(pid)
	for _, standard := range []string{"0", "1", "2"} {
		if got[standard] != want[standard] {
			t.Errorf("browser descriptor %s is %q, want %q", standard, got[standard], want[standard])
		}
	}
	// The stand-in opens files of its own while it starts, but no pipe.
	for descriptor, target := range got {
		if strings.HasPrefix(target, "pipe:") && want[descriptor] != target {
			t.Errorf("browser inherited descriptor %s, %q", descriptor, target)
		}
	}

	if cwd, err := os.Readlink(process + "cwd"); err != nil || cwd != dir {
		t.Errorf("browser working directory = %q, %v, want %q", cwd, err, dir)
	}
	if group, err := syscall.Getpgid(pid); err != nil || group != pid {
		t.Errorf("browser process group = %d, %v, want %d", group, err, pid)
	}
	data, err := os.ReadFile(process + "cmdline")
	if want := sleep + "\x0060\x00"; err != nil || string(data) != want {
		t.Errorf("browser arguments = %q, %v, want %q", data, err, want)
	}
	// The guardian replaces TMPDIR with a private child of that directory.
	data, err = os.ReadFile(process + "environ")
	env := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	scratch := "TMPDIR=" + filepath.Join(dir, "tmp-parent", "rod-")
	if err != nil || len(env) != 2 || env[0] != entry || !strings.HasPrefix(env[1], scratch) {
		t.Errorf("browser environment = %q, %v", env, err)
	}
}

// The guardian writes the preferences into the profile that the launcher owns
// before it starts the browser, for the owner only.
func TestGuardianWritesPreferences(t *testing.T) {
	const preferences = `{"plugins":{"always_open_pdf_externally":true}}`
	// The browser stand-in exits by itself after a minute.
	const script = "#!/bin/sh\ncat \"$1/Work/Preferences\"\nexec sleep 60\n"
	dir := t.TempDir()
	browser := filepath.Join(dir, "browser")
	if err := os.WriteFile(browser, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(dir, "profile")
	if err := os.Mkdir(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(filepath.Join(dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	l := New().Preferences(preferences).ProfileDir("Work")
	l.cleanupUserDataDir = profile
	cmd := exec.Command(browser, profile)
	cmd.Stdout = output
	_, stop, err := l.startProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop()
		_ = cmd.Wait()
	})
	var read []byte
	for deadline := time.Now().Add(5 * time.Second); string(read) != preferences && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
		read, _ = os.ReadFile(output.Name())
	}
	if string(read) != preferences {
		t.Fatalf("the browser read the preferences %q when it started, want %q", read, preferences)
	}
	for path, permissions := range map[string]os.FileMode{"Work": 0o700, "Work/Preferences": 0o600} {
		info, err := os.Stat(filepath.Join(profile, path))
		if err != nil {
			t.Error(err)
		} else if info.Mode().Perm() != permissions {
			t.Errorf("%s has permissions %v, want %v", path, info.Mode().Perm(), permissions)
		}
	}
}

// A guardian that cannot start fails the launch at once, whether it reports the
// reason or exits without a report, and removes nothing.
func TestGuardianStartupFailure(t *testing.T) {
	for _, test := range []struct {
		name, want string
		setup      func(t *testing.T, l *Launcher, dir string)
	}{
		{"reported", "launcher browser startup: invalid managed profile name", func(t *testing.T, l *Launcher, dir string) {
			parent, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = parent.Close() })
			l.managedProfile = &managedProfile{parent: parent, name: ".."}
		}},
		{"silent exit", "launcher guardian startup: EOF", func(t *testing.T, _ *Launcher, _ string) {
			// The Go runtime rejects this setting and exits before any package
			// initializer runs.
			t.Setenv("GOMEMLIMIT", "invalid")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			profile := filepath.Join(dir, "profile")
			if err := os.Mkdir(profile, 0o700); err != nil {
				t.Fatal(err)
			}
			// The launcher itself gives up on a silent guardian after 30 seconds.
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			l := New().Context(ctx)
			test.setup(t, l, dir)
			cmd := guardianTestCommand(t, "browser", dir)
			pid, stop, err := l.startProcess(cmd)
			if stop != nil {
				defer stop()
			}
			if err == nil || err.Error() != test.want || pid != 0 {
				t.Fatalf("browser PID = %d, error = %v, want %q", pid, err, test.want)
			}
			// startProcess replaced cmd with the guardian's command.
			if err := cmd.Wait(); err == nil {
				t.Fatal("the guardian exited successfully")
			}
			if _, err := os.Stat(filepath.Join(dir, "pids")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the browser was started: %v", err)
			}
			if _, err := os.Stat(profile); err != nil {
				t.Fatalf("the guardian removed a directory that it does not own: %v", err)
			}
		})
	}
}

// The guardian takes over before Go initializes unicode, and so before every
// package that depends on it: strings, fmt, and most of what the launcher and
// an application import. Go's initialization trace lists the packages whose
// initializers ran in the guardian.
func TestGuardianSkipsLateInitializers(t *testing.T) {
	dir := t.TempDir()
	trace, err := os.Create(filepath.Join(dir, "trace"))
	if err != nil {
		t.Fatal(err)
	}
	defer trace.Close()
	debug := "inittrace=1"
	if inherited := os.Getenv("GODEBUG"); inherited != "" {
		debug = inherited + "," + debug
	}
	t.Setenv("GODEBUG", debug)
	// The guardian reports that this browser does not exist and exits.
	cmd := exec.Command(filepath.Join(dir, "missing-browser"))
	cmd.Stderr = trace
	if _, _, err := New().startProcess(cmd); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup error = %v, want os.ErrNotExist", err)
	}
	_ = cmd.Wait()
	data, err := os.ReadFile(trace.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "init os @") {
		t.Fatalf("the guardian printed no initialization trace:\n%s", data)
	}
	if strings.Contains(string(data), "init unicode @") {
		t.Fatalf("the guardian ran the initializers of unicode and of the packages that sort before it:\n%s", data)
	}
}

// The guardian adopts some descendants only while it is already cleaning up: a
// grandchild whose parent it has just killed. Others arrive earlier, such as a
// daemon whose parent exited at once. Both run in their own sessions, outside
// the browser's process group.
func TestGuardianReapsLateAdoptions(t *testing.T) {
	for _, graceful := range []bool{false, true} {
		t.Run(fmt.Sprintf("graceful=%t", graceful), func(t *testing.T) {
			dir := t.TempDir()
			cmd := guardianTestCommand(t, "tree", dir)
			cmd.Args[1] = "-test.run=^TestGuardianTreeProcess$"
			pid, stop, err := New().startProcess(cmd)
			if err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			t.Cleanup(stop)
			pids := guardianTestTreePIDs(t, dir, "browser", "middle", "late", "detached")
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
			select {
			case err := <-exited:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("guardian did not finish while a late descendant was alive")
			}
			guardianTestGone(t, append(pids, cmd.Process.Pid))
		})
	}
}

// Cleanup neither depends on SIGCHLD nor changes how the process handles it. A
// package initializer that runs in the guardian before it takes over can
// ignore that signal, and the children of such a process leave no exit status:
// a wait reports only whether a child remains, including a descendant adopted
// in the meantime. The test reaps a tree with a late-adopted grandchild in a
// subprocess that ignores the signal.
func TestGuardianReapIgnoredChildSignal(t *testing.T) {
	if os.Getenv(guardianTestRole) != "reaper" {
		cmd := guardianTestCommand(t, "reaper", t.TempDir())
		cmd.Args[1] = "-test.run=^TestGuardianReapIgnoredChildSignal$"
		// The harness has nothing to report at exit.
		cmd.Env = guardianRaceEnv(cmd.Env)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("reaper harness: %v\n%s", err, output)
		}
		return
	}
	signal.Ignore(syscall.SIGCHLD)
	if err := guardian.Subreaper(); err != nil {
		t.Fatal(err)
	}
	guardianTestTreeStart(t, "middle", "", false)
	pids := guardianTestTreePIDs(t, os.Getenv("ROD_GUARDIAN_TEST_DIR"), "middle", "late")
	done := make(chan struct{})
	go func() {
		guardian.ReapChildren(nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cleanup did not finish while a late descendant was alive")
	}
	guardianTestGone(t, pids)
	if !signal.Ignored(syscall.SIGCHLD) {
		t.Fatal("cleanup changed how the process handles SIGCHLD")
	}
}

// The guardian ends when its owner's pipe closes or the browser exits, not on a
// signal that asks a program to exit, and it does not depend on the application
// handling that signal: the browser keeps running, and stopping it afterwards
// still removes its descendants and its scratch directory.
func TestGuardianOutlivesExitSignals(t *testing.T) {
	for _, sig := range guardianTestExitSignals {
		t.Run(sig.String(), func(t *testing.T) {
			dir := t.TempDir()
			temporary := guardianTestTemporaryParent(t, dir)
			cmd := guardianTestCommand(t, "tree", dir)
			cmd.Args[1] = "-test.run=^TestGuardianTreeProcess$"
			cmd.Env = append(cmd.Env, "TMPDIR="+temporary)
			pid, stop, err := New().startProcess(cmd)
			if err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			t.Cleanup(stop)
			pids := guardianTestTreePIDs(t, dir, "browser", "middle", "late", "detached")
			// startProcess replaced cmd with the guardian's command.
			guardianTestSignal(t, cmd.Process.Pid, sig)
			// A guardian that took the signal as a request to stop would end
			// the browser and itself within this time.
			time.Sleep(50 * time.Millisecond)
			select {
			case err := <-exited:
				t.Fatalf("the guardian ended on %v: %v", sig, err)
			default:
			}
			if err := syscall.Kill(pid, 0); err != nil {
				t.Fatalf("the browser is gone after %v reached its guardian: %v", sig, err)
			}
			stop()
			select {
			case err := <-exited:
				if err != nil {
					t.Fatalf("guardian exit after %v: %v", sig, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("guardian did not finish after %v", sig)
			}
			guardianTestGone(t, append(pids, cmd.Process.Pid))
			// The caller's file is all that the temporary directory still holds.
			if entries, err := os.ReadDir(temporary); err != nil || len(entries) != 1 {
				t.Fatalf("temporary directory contains unexpected residue: %v, %v", entries, err)
			}
		})
	}
}

// The guardian handles the exit signals before it reports that it is ready, so
// one that arrives while the launcher sends the browser's command does not end
// it. The test plays the launcher's part up to that report, sends the signal,
// and closes the guardian's input in place of a command: the guardian reports
// that failure and exits by itself.
func TestGuardianOutlivesExitSignalsOnceReady(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, sig := range guardianTestExitSignals {
		t.Run(sig.String(), func(t *testing.T) {
			input, owner, err := guardianChannel()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer owner.Close()
			reports, status, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reports.Close()
			defer status.Close()
			// A guardian that hangs is killed, which ends the reads below.
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, guardian.Argument, "", "")
			cmd.Env = guardianRaceEnv(append(withoutGuardianEnv(os.Environ()), guardian.Env+"=1"))
			cmd.Stdin = input
			cmd.ExtraFiles = []*os.File{status}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			_ = status.Close()
			if ready, err := guardian.ReadStatus(reports); err != nil || !ready.Ready {
				t.Fatalf("guardian startup: %+v, %v", ready, err)
			}
			if err := guardian.AnswerChallenge(reports, owner); err != nil {
				t.Fatal(err)
			}
			guardianTestSignal(t, cmd.Process.Pid, sig)
			_ = owner.Close()
			if failure, err := guardian.ReadStatus(reports); err != nil || failure.Error == "" {
				t.Fatalf("the guardian's report after %v: %+v, %v", sig, failure, err)
			}
			err = cmd.Wait()
			waited = true
			if cmd.ProcessState.ExitCode() != 1 {
				t.Fatalf("guardian exit after %v: %v", sig, err)
			}
		})
	}
}

// The browser keeps a SIGHUP or SIGINT that the launching program ignores, as
// SIGHUP is under nohup, and the guardian's own handling of the exit signals
// does not reach the browser. Each case launches from a subprocess, which
// confines the ignored signal to it.
func TestGuardianBrowserSignalDispositions(t *testing.T) {
	const ignoring = "ROD_GUARDIAN_TEST_IGNORE_SIGHUP"
	if os.Getenv(guardianTestRole) != "harness" {
		if _, err := exec.LookPath("sleep"); err != nil {
			t.Skip("the sleep command is required")
		}
		for _, ignore := range []bool{false, true} {
			t.Run(fmt.Sprintf("SIGHUP ignored=%t", ignore), func(t *testing.T) {
				cmd := guardianTestCommand(t, "harness", t.TempDir())
				cmd.Args[1] = "-test.run=^TestGuardianBrowserSignalDispositions$"
				// The harness has nothing to report at exit.
				cmd.Env = guardianRaceEnv(append(cmd.Env, ignoring+"="+strconv.FormatBool(ignore)))
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("launch harness: %v\n%s", err, output)
				}
			})
		}
		return
	}
	ignore := os.Getenv(ignoring) == "true"
	if ignore {
		signal.Ignore(syscall.SIGHUP)
	}
	// The browser stand-in changes no disposition and exits by itself after a
	// minute.
	cmd := exec.Command("sleep", "60")
	pid, stop, err := New().startProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop()
		_ = cmd.Wait()
	})
	var mask uint64
	for _, sig := range guardianTestExitSignals {
		mask |= guardianTestSignalBit(sig)
	}
	own := guardianTestSignalSet(os.Getpid(), "SigIgn") & mask
	if ignore && own&guardianTestSignalBit(syscall.SIGHUP) == 0 {
		t.Fatalf("this process does not ignore SIGHUP: it ignores the exit signals %#x", own)
	}
	if got := guardianTestSignalSet(pid, "SigIgn") & mask; got != own {
		t.Fatalf("the browser ignores the exit signals %#x, the launching process %#x", got, own)
	}
}

// A real browser that is launched through the public API stays supervised
// while its guardian receives the exit signals, none of which the application
// handles: the browser still answers after each one, and Kill and Cleanup then
// leave no process and no directory behind.
func TestGuardianRealBrowserOutlivesExitSignals(t *testing.T) {
	if os.Getenv(guardianTestRole) != "harness" {
		bin, found := LookPath()
		if !found {
			t.Skip("a locally installed browser is required")
		}
		// The profile and the browser's scratch directory are the only entries
		// of this directory. Its path is short: Chrome creates a socket below
		// it, and socket paths have a small fixed maximum length.
		temporary, err := os.MkdirTemp("", "rod-test-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(temporary) })
		cmd := guardianTestCommand(t, "harness", temporary)
		cmd.Args[1] = "-test.run=^TestGuardianRealBrowserOutlivesExitSignals$"
		cmd.Env = guardianRaceEnv(append(cmd.Env, "ROD_GUARDIAN_TEST_BROWSER="+bin))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("browser signal harness: %v\n%s", err, output)
		}
		return
	}
	// As a subreaper, this subprocess adopts every process that outlives the
	// guardian, so a process that the launch leaves behind is a child of it.
	if err := guardian.Subreaper(); err != nil {
		t.Fatal(err)
	}
	temporary := os.Getenv("ROD_GUARDIAN_TEST_DIR")
	t.Setenv("TMPDIR", temporary)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	l := New().Bin(os.Getenv("ROD_GUARDIAN_TEST_BROWSER")).Context(ctx)
	t.Cleanup(func() {
		l.Kill()
		guardian.ReapChildren(nil)
	})
	u, err := l.Launch()
	if err != nil {
		t.Fatal(err)
	}
	// The guardian is the parent of the browser.
	supervisor, err := strconv.Atoi(guardianTestStatus(l.PID(), "PPid"))
	if err != nil {
		t.Fatal(err)
	}
	for _, sig := range guardianTestExitSignals {
		guardianTestSignal(t, supervisor, sig)
		if _, err := ResolveURL(ctx, u); err != nil {
			t.Fatalf("the browser does not answer after %v reached its guardian: %v", sig, err)
		}
		if parent := guardianTestStatus(l.PID(), "PPid"); parent != strconv.Itoa(supervisor) {
			t.Fatalf("the guardian ended on %v: the browser's parent is process %s", sig, parent)
		}
	}
	l.Kill()
	if err := l.CleanupContext(ctx); err != nil {
		t.Fatal(err)
	}
	if pid, err := syscall.Wait4(-1, nil, syscall.WNOHANG, nil); !errors.Is(err, syscall.ECHILD) {
		t.Fatalf("a process remains after Kill and Cleanup: %d, %v", pid, err)
	}
	if entries, err := os.ReadDir(temporary); err != nil || len(entries) != 0 {
		t.Fatalf("temporary directory contains unexpected residue: %v, %v", entries, err)
	}
}

// TestGuardianTreeProcess is one process of the tree that the tests above
// build. Each process ends by itself, so a failed cleanup leaks nothing for
// long. syscall.Exit leaves without the race detector's exit delay.
func TestGuardianTreeProcess(t *testing.T) {
	dir := os.Getenv("ROD_GUARDIAN_TEST_DIR")
	record := func(name string) {
		// Rename so a reader never sees a partly written PID.
		path := filepath.Join(dir, "pid-"+name)
		if err := os.WriteFile(path+".tmp", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Minute)
	switch os.Getenv(guardianTestRole) {
	case "tree":
		record("browser")
		guardianTestTreeStart(t, "middle", "", false)
		if err := guardianTestTreeStart(t, "detach", "", true).Wait(); err != nil {
			t.Fatal(err)
		}
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "exit")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		syscall.Exit(0)
	case "middle":
		record("middle")
		guardianTestTreeStart(t, "leaf", "late", true)
		time.Sleep(time.Until(deadline))
		syscall.Exit(0)
	case "detach":
		guardianTestTreeStart(t, "leaf", "detached", true)
		syscall.Exit(0)
	case "leaf":
		record(os.Getenv("ROD_GUARDIAN_TEST_NAME"))
		time.Sleep(time.Until(deadline))
		syscall.Exit(0)
	}
}

// guardianTestTreeStart runs TestGuardianTreeProcess in a child process, in its
// own session when detached.
func guardianTestTreeStart(t *testing.T, role, name string, detached bool) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestGuardianTreeProcess$")
	cmd.Env = append(os.Environ(), guardianTestRole+"="+role, "ROD_GUARDIAN_TEST_NAME="+name)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: detached}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// guardianTestTreePIDs waits until the named tree processes have recorded
// their PIDs.
func guardianTestTreePIDs(t *testing.T, dir string, names ...string) []int {
	t.Helper()
	var pids []int
	guardianTestEventually(t, func() bool {
		pids = pids[:0]
		for _, name := range names {
			data, err := os.ReadFile(filepath.Join(dir, "pid-"+name))
			pid, parseErr := strconv.Atoi(string(data))
			if err != nil || parseErr != nil || pid <= 0 {
				return false
			}
			pids = append(pids, pid)
		}
		return true
	})
	return pids
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
	configuration := guardian.Command{Env: []string{"TMPDIR=" + alias}, Scratch: []string{"rod-scratch"}}
	cleanup, err := guardian.TemporaryDir(&configuration)
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
			configuration := guardian.Command{Dir: tc.work, Env: []string{"TMPDIR=" + tc.temporary}, Scratch: []string{"rod-scratch"}}
			cleanup, err := guardian.TemporaryDir(&configuration)
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

// guardianTestExitSignals are the signals that ask a program to exit.
var guardianTestExitSignals = []syscall.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM}

// guardianTestSignal sends sig to the process and waits until the process has
// taken it: until then the kernel lists the signal as pending.
func guardianTestSignal(t *testing.T, pid int, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(pid, sig); err != nil {
		t.Fatalf("send %v to process %d: %v", sig, pid, err)
	}
	guardianTestEventually(t, func() bool {
		return guardianTestSignalSet(pid, "ShdPnd")&guardianTestSignalBit(sig) == 0
	})
}

// guardianTestSignalSet returns a signal set of the process as the kernel
// reports it under name in the process status, such as SigIgn for the ignored
// signals. A process that is gone has an empty set.
func guardianTestSignalSet(pid int, name string) uint64 {
	set, _ := strconv.ParseUint(guardianTestStatus(pid, name), 16, 64)
	return set
}

func guardianTestSignalBit(sig syscall.Signal) uint64 { return 1 << (sig - 1) }

// guardianTestStatus returns the value of one field of the process status, or
// "" when the process is gone.
func guardianTestStatus(pid int, name string) string {
	data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, found := strings.CutPrefix(line, name+":"); found {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
