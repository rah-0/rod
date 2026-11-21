//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package secureexec

import "syscall"

func platformSecure() bool { return syscall.Issetugid() }
