//go:build !windows

package launcher

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rah-0/rod/lib/launcher/flags"
)

const (
	guardianArgument = "--rod-launcher-guardian"
	guardianEnv      = "_ROD_LAUNCHER_GUARDIAN"
)

type guardianCommand struct {
	Path        string
	Args        []string
	Env         []string
	Dir         string
	Preferences string
	Profile     string
}

type guardianStatus struct {
	PID   int
	Error string
	Errno syscall.Errno
	Op    string
	Path  string
	Ready bool
}

func (s *guardianStatus) setError(err error) {
	s.Error = err.Error()
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		s.Op = pathError.Op
		s.Path = pathError.Path
	}
	_ = errors.As(err, &s.Errno)
}

func (s guardianStatus) err() error {
	var err error = errors.New(s.Error)
	if s.Errno != 0 {
		err = s.Errno
		if s.Op != "" {
			err = &os.PathError{Op: s.Op, Path: s.Path, Err: err}
		}
	}
	return err
}

// A separate process must own cleanup: a goroutine cannot run after its owner
// panics, calls os.Exit, or is killed by a test timeout or SIGKILL. The private
// invocation also requires two inherited pipes, so an environment variable
// alone never changes the normal executable's behavior.
func init() {
	if os.Getenv(guardianEnv) != "1" || len(os.Args) != 4 || os.Args[1] != guardianArgument {
		return
	}
	status := os.NewFile(3, "rod-launcher-status")
	if !guardianPipe(os.Stdin) || !guardianPipe(status) {
		return
	}
	syscall.CloseOnExec(int(status.Fd()))
	os.Exit(runGuardian(status, os.Args[2], os.Args[3]))
}

func guardianPipe(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeNamedPipe != 0
}

func withoutGuardianEnv(env []string) []string {
	clean := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, guardianEnv+"=") {
			clean = append(clean, entry)
		}
	}
	return clean
}

// startProcess replaces cmd with the guardian command. Wait consequently waits
// for the browser, its remaining children, and temporary-profile cleanup.
func (l *Launcher) startProcess(cmd *exec.Cmd) (int, func(), error) {
	if cmd.Err != nil {
		return 0, nil, cmd.Err
	}
	executable, err := os.Executable()
	if err != nil {
		return 0, nil, err
	}
	livenessRead, livenessWrite, err := os.Pipe()
	if err != nil {
		return 0, nil, err
	}
	defer livenessRead.Close()
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		_ = livenessWrite.Close()
		return 0, nil, err
	}
	defer statusRead.Close()
	defer statusWrite.Close()
	profileFile, profileName, err := l.guardianProfileFile()
	if err != nil {
		_ = livenessWrite.Close()
		return 0, nil, err
	}
	if profileFile != nil {
		defer profileFile.Close()
	}

	configuration := guardianCommand{
		Path: cmd.Path,
		Args: cmd.Args,
		Env:  withoutGuardianEnv(cmd.Environ()),
		Dir:  cmd.Dir,
	}
	if l.cleanupUserDataDir != "" {
		configuration.Preferences = l.Get(flags.Preferences)
		configuration.Profile = l.Get(flags.ProfileDir)
	}
	guardian := exec.Command(executable, guardianArgument, l.cleanupUserDataDir, profileName)
	guardian.Env = append(withoutGuardianEnv(os.Environ()), guardianEnv+"=1")
	guardian.Stdin = livenessRead
	guardian.Stdout = cmd.Stdout
	guardian.Stderr = cmd.Stderr
	guardian.ExtraFiles = []*os.File{statusWrite}
	if profileFile != nil {
		guardian.ExtraFiles = append(guardian.ExtraFiles, profileFile)
	}
	guardian.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	guardian.WaitDelay = cmd.WaitDelay
	if err := guardian.Start(); err != nil {
		_ = livenessWrite.Close()
		return 0, nil, err
	}
	_ = livenessRead.Close()
	_ = statusWrite.Close()

	var once sync.Once
	stop := func() { once.Do(func() { _ = livenessWrite.Close() }) }
	fail := func(err error) (int, func(), error) {
		stop()
		// Launch's normal process reaper owns Wait even on partial startup.
		// Waiting here would bypass its bounded rollback budget.
		*cmd = *guardian
		return 0, stop, err
	}
	// The helper announces readiness before it can receive a command. If an
	// application's package initializer blocks during re-exec, no browser can
	// have started and terminating the helper is safe.
	decoder := json.NewDecoder(statusRead)
	results := make(chan guardianStatus, 1)
	readStatus := func() {
		var status guardianStatus
		if err := decoder.Decode(&status); err != nil {
			status.setError(err)
		}
		results <- status
	}
	go readStatus()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	var status guardianStatus
	select {
	case status = <-results:
	case <-l.ctx.Done():
		_ = guardian.Process.Kill()
		return fail(l.ctx.Err())
	case <-timer.C:
		_ = guardian.Process.Kill()
		return fail(fmt.Errorf("launcher guardian startup timed out"))
	}
	if !status.Ready {
		return fail(fmt.Errorf("launcher guardian startup: %w", status.err()))
	}
	go func() {
		if err := json.NewEncoder(livenessWrite).Encode(configuration); err != nil {
			var status guardianStatus
			status.setError(err)
			results <- status
			return
		}
		readStatus()
	}()
	select {
	case status = <-results:
	case <-l.ctx.Done():
		return fail(l.ctx.Err())
	case <-timer.C:
		return fail(fmt.Errorf("launcher browser startup timed out"))
	}
	if status.Error != "" {
		return fail(fmt.Errorf("launcher browser startup: %w", status.err()))
	}
	if status.PID <= 0 {
		return fail(fmt.Errorf("launcher guardian returned an invalid browser PID"))
	}
	*cmd = *guardian
	return status.PID, stop, nil
}

func runGuardian(status *os.File, profile, managedProfile string) int {
	defer status.Close()
	if profile != "" {
		defer os.RemoveAll(profile)
	}
	report := func(pid int, err error) bool {
		result := guardianStatus{PID: pid}
		if err != nil {
			result.setError(err)
		}
		return json.NewEncoder(status).Encode(result) == nil
	}
	cleanupManaged, err := guardianManagedCleanup(managedProfile)
	if err != nil {
		report(0, err)
		return 1
	}
	defer cleanupManaged()
	if err := guardianSubreaper(); err != nil {
		report(0, err)
		return 1
	}
	if err := json.NewEncoder(status).Encode(guardianStatus{Ready: true}); err != nil {
		return 1
	}
	decoder := json.NewDecoder(os.Stdin)
	var configuration guardianCommand
	if err := decoder.Decode(&configuration); err != nil {
		report(0, err)
		return 1
	}
	cleanupTemporary, err := guardianTemporaryDir(&configuration)
	if err != nil {
		report(0, err)
		return 1
	}
	defer cleanupTemporary()
	if profile != "" && configuration.Preferences != "" {
		if err := guardianPreferences(profile, configuration); err != nil {
			report(0, err)
			return 1
		}
	}
	cmd := &exec.Cmd{
		Path:        configuration.Path,
		Args:        configuration.Args,
		Env:         configuration.Env,
		Dir:         configuration.Dir,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
	}
	if err := cmd.Start(); err != nil {
		report(0, err)
		return 1
	}
	// The liveness descriptor belongs only to the parent and this guardian. The
	// browser inherits neither end, so owner death reliably produces EOF.
	var lifetime sync.Mutex
	finished := false
	kill := func() {
		lifetime.Lock()
		defer lifetime.Unlock()
		if !finished {
			guardianKill(cmd)
		}
	}
	go func() {
		_, _ = io.Copy(io.Discard, io.MultiReader(decoder.Buffered(), os.Stdin))
		kill()
	}()
	if !report(cmd.Process.Pid, nil) {
		kill()
	}
	_ = status.Close()
	_ = cmd.Wait()
	lifetime.Lock()
	// Chrome helpers can outlive the browser's main process. The guardian owns
	// their cleanup even after graceful Browser.close or a startup failure.
	guardianReapChildren(cmd)
	finished = true
	lifetime.Unlock()
	return 0
}

// Chrome creates singleton sockets and other scratch files outside its user
// profile. Give it a private child of its requested temporary directory so an
// abrupt exit cannot leave these files behind in a shared directory.
func guardianTemporaryDir(configuration *guardianCommand) (func(), error) {
	base := "/tmp"
	env := make([]string, 0, len(configuration.Env)+1)
	for _, entry := range configuration.Env {
		name, value, _ := strings.Cut(entry, "=")
		if name == "TMPDIR" {
			base = value
			continue
		}
		env = append(env, entry)
	}
	if base == "" {
		base = "/tmp"
	}
	if !filepath.IsAbs(base) {
		// Preserve filesystem traversal: cleaning "symlink/../scratch" would
		// select a different directory before the kernel follows the symlink.
		if configuration.Dir != "" {
			base = configuration.Dir + "/" + base
		}
		if !filepath.IsAbs(base) {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			base = cwd + "/" + base
		}
	}
	parent, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	for range 10 {
		// Keep this short: Unix socket paths have a small fixed maximum length.
		name := "rod-" + rand.Text()[:8]
		err = parent.Mkdir(name, 0o700)
		if err == nil {
			configuration.Env = append(env, "TMPDIR="+base+"/"+name)
			return func() {
				// Retain the directory handle so a renamed or repointed parent
				// path cannot redirect cleanup into another user's files.
				_ = parent.RemoveAll(name)
				_ = parent.Close()
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			break
		}
	}
	_ = parent.Close()
	return nil, err
}

func guardianPreferences(profile string, configuration guardianCommand) error {
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(profile)
	if err != nil {
		return err
	}
	defer root.Close()
	name := configuration.Profile
	if name == "" {
		name = "Default"
	}
	if err := root.MkdirAll(name, 0o700); err != nil {
		return err
	}
	return root.WriteFile(filepath.Join(name, "Preferences"), []byte(configuration.Preferences), 0o600)
}
