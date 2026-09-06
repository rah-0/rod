package launcher

import (
	"os/exec"
	"sync"
)

func (l *Launcher) startProcess(cmd *exec.Cmd) (int, func(), error) {
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
			_ = cmd.Process.Kill()
		})
	}
	return cmd.Process.Pid, stop, nil
}
