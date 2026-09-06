//go:build !linux && !windows

package launcher

import "os/exec"

func guardianSubreaper() error { return nil }

func guardianKill(cmd *exec.Cmd) { killGroup(cmd.Process.Pid) }

func guardianReapChildren(cmd *exec.Cmd) { killGroup(cmd.Process.Pid) }
