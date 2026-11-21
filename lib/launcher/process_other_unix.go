//go:build !linux && !windows

package launcher

import (
	"os/exec"
	"sync"
	"syscall"

	"github.com/rah-0/rod/internal/secureexec"
)

// Platforms without authenticated guardian channels use an ordinary process
// group. Explicit cleanup is supported; application-crash cleanup is not.
func (l *Launcher) startProcess(cmd *exec.Cmd) (int, func(), error) {
	if secureexec.Active() {
		return 0, nil, ErrSecureExecution
	}
	if l.cleanupUserDataDir != "" {
		if err := l.setupUserPreferences(); err != nil {
			return 0, nil, err
		}
	}
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
		})
	}
	return cmd.Process.Pid, stop, nil
}
