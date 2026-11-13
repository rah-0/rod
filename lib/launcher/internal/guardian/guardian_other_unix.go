//go:build !linux && !windows

package guardian

import (
	"os"
	"syscall"
)

// Subreaper does nothing: only Linux lets a process adopt its orphaned
// descendants.
func Subreaper() error { return nil }

func kill(process *os.Process) { killGroup(process.Pid) }

// ReapChildren kills the browser's process group.
func ReapChildren(process *os.Process) { killGroup(process.Pid) }

func killGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
