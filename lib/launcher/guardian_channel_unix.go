//go:build linux

package launcher

import (
	"os"
	"syscall"
)

// A socketpair records its creator's credentials in the kernel. Linux verifies
// those credentials and the executable of the creating parent before private
// initialization can run.
func guardianChannel() (*os.File, *os.File, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	return os.NewFile(uintptr(fds[0]), "guardian-command"), os.NewFile(uintptr(fds[1]), "guardian-owner"), nil
}
