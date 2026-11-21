// Package secureexec identifies processes whose ambient arguments and
// environment must not select programs or private launcher entry points.
package secureexec

import "os"

// Active reports a credential transition or an OS secure-execution marker.
// A failure to read the OS marker is treated as secure execution.
func Active() bool {
	return credentialsDiffer(os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid()) || platformSecure()
}

func credentialsDiffer(uid, euid, gid, egid int) bool {
	return uid != euid || gid != egid
}
