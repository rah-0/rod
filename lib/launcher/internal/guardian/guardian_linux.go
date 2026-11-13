package guardian

import (
	"errors"
	"os"
	"strconv"
	"syscall"
	"time"
)

// wrappedError is fmt.Errorf("%s: %w") without importing fmt.
type wrappedError struct {
	message string
	err     error
}

func (e *wrappedError) Error() string { return e.message + ": " + e.err.Error() }

func (e *wrappedError) Unwrap() error { return e.err }

// Subreaper makes this process adopt its orphaned descendants.
func Subreaper() error {
	if _, err := os.ReadDir("/proc/self/task"); err != nil {
		return &wrappedError{"launcher guardian requires procfs", err}
	}
	children := "/proc/self/task/" + strconv.Itoa(os.Getpid()) + "/children"
	if _, err := os.ReadFile(children); err != nil {
		return &wrappedError{"launcher guardian requires process children in procfs", err}
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

func kill(process *os.Process) {
	// Go uses a pidfd when available, preserving process identity if the
	// browser exits concurrently. Remaining descendants are handled below.
	_ = process.Kill()
}

// rescanInterval is how long ReapChildren waits between two scans.
const rescanInterval = 10 * time.Millisecond

// ReapChildren kills and reaps every remaining child, including adopted
// descendants.
func ReapChildren(_ *os.Process) {
	for {
		// A child may have been created by any Go runtime thread. Each thread
		// exposes its children separately; adopted grandchildren are included.
		tasks, _ := os.ReadDir("/proc/self/task")
		for _, task := range tasks {
			data, _ := os.ReadFile("/proc/self/task/" + task.Name() + "/children")
			eachPID(data, func(pid int) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			})
		}
		// Reap every child that has exited before scanning again.
		for {
			pid, err := syscall.Wait4(-1, nil, syscall.WNOHANG, nil)
			if errors.Is(err, syscall.ECHILD) {
				return
			}
			if pid <= 0 {
				break
			}
		}
		// A killed child needs a moment to exit, and its own children are
		// adopted only then, so scan again after a pause. The pause is a plain
		// sleep and not a wait for SIGCHLD through os/signal: receiving a
		// signal that way can make the Go runtime start OS threads, and a
		// supervisor that cannot create a thread during cleanup aborts and
		// leaves descendants and directories behind.
		time.Sleep(rescanInterval)
	}
}

// eachPID calls visit for every positive process ID in a procfs "children"
// file, a list of decimal numbers separated by spaces.
func eachPID(data []byte, visit func(pid int)) {
	for start := 0; start < len(data); {
		if isSpace(data[start]) {
			start++
			continue
		}
		end := start
		for end < len(data) && !isSpace(data[end]) {
			end++
		}
		if pid, err := strconv.Atoi(string(data[start:end])); err == nil && pid > 0 {
			visit(pid)
		}
		start = end
	}
}

func isSpace(c byte) bool {
	return c == ' ' || (c >= '\t' && c <= '\r')
}
