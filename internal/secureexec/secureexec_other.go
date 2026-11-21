//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package secureexec

func platformSecure() bool { return false }
