//go:build !windows

package launcher

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rah-0/rod/lib/launcher/flags"
	"github.com/rah-0/rod/lib/launcher/internal/guardian"
)

func withoutGuardianEnv(env []string) []string {
	clean := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, guardian.Env+"=") {
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

	configuration := guardian.Command{
		Path: cmd.Path,
		Args: cmd.Args,
		Env:  withoutGuardianEnv(cmd.Environ()),
		Dir:  cmd.Dir,
	}
	if l.cleanupUserDataDir != "" {
		configuration.Preferences = l.Get(flags.Preferences)
		configuration.Profile = l.Get(flags.ProfileDir)
	}
	for range 10 {
		// Keep this short: Unix socket paths have a small fixed maximum length.
		configuration.Scratch = append(configuration.Scratch, "rod-"+rand.Text()[:8])
	}
	helper := exec.Command(executable, guardian.Argument, l.cleanupUserDataDir, profileName)
	helper.Env = guardianRaceEnv(append(withoutGuardianEnv(os.Environ()), guardian.Env+"=1"))
	helper.Stdin = livenessRead
	helper.Stdout = cmd.Stdout
	helper.Stderr = cmd.Stderr
	helper.ExtraFiles = []*os.File{statusWrite}
	if profileFile != nil {
		helper.ExtraFiles = append(helper.ExtraFiles, profileFile)
	}
	helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	helper.WaitDelay = cmd.WaitDelay
	if err := helper.Start(); err != nil {
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
		*cmd = *helper
		return 0, stop, err
	}
	// The helper announces readiness before it can receive a command. If an
	// application's package initializer blocks during re-exec, no browser can
	// have started and terminating the helper is safe.
	results := make(chan guardian.Status, 1)
	readStatus := func() {
		status, err := guardian.ReadStatus(statusRead)
		if err != nil {
			status.SetError(err)
		}
		results <- status
	}
	go readStatus()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	var status guardian.Status
	select {
	case status = <-results:
	case <-l.ctx.Done():
		_ = helper.Process.Kill()
		return fail(l.ctx.Err())
	case <-timer.C:
		_ = helper.Process.Kill()
		return fail(fmt.Errorf("launcher guardian startup timed out"))
	}
	if !status.Ready {
		return fail(fmt.Errorf("launcher guardian startup: %w", status.Err()))
	}
	go func() {
		if err := guardian.WriteCommand(livenessWrite, configuration); err != nil {
			var status guardian.Status
			status.SetError(err)
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
		return fail(fmt.Errorf("launcher browser startup: %w", status.Err()))
	}
	if status.PID <= 0 {
		return fail(fmt.Errorf("launcher guardian returned an invalid browser PID"))
	}
	*cmd = *helper
	return status.PID, stop, nil
}
