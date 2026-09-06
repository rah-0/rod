package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func guardianSubreaper() error {
	if _, err := os.ReadDir("/proc/self/task"); err != nil {
		return fmt.Errorf("launcher guardian requires procfs: %w", err)
	}
	children := "/proc/self/task/" + strconv.Itoa(os.Getpid()) + "/children"
	if _, err := os.ReadFile(children); err != nil {
		return fmt.Errorf("launcher guardian requires process children in procfs: %w", err)
	}
	// PR_SET_CHILD_SUBREAPER adopts orphaned Chrome descendants, including
	// crash reporters which create their own session/process group.
	const prSetChildSubreaper = 36
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func guardianKill(cmd *exec.Cmd) {
	// Go uses a pidfd when available, preserving process identity if the
	// browser exits concurrently. Remaining descendants are handled below.
	_ = cmd.Process.Kill()
}

func guardianReapChildren(_ *exec.Cmd) {
	for {
		// A child may have been created by any Go runtime thread. Each thread
		// exposes its children separately; adopted grandchildren are included.
		paths, _ := filepath.Glob("/proc/self/task/*/children")
		for _, path := range paths {
			data, _ := os.ReadFile(path)
			for _, child := range strings.Fields(string(data)) {
				pid, err := strconv.Atoi(child)
				if err == nil && pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
		pid, err := syscall.Wait4(-1, nil, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.ECHILD) {
			return
		}
		if pid == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
}
