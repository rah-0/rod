//go:build !linux && !windows

package guardian

import (
	"errors"
	"os"
)

// Private re-execution is disabled where authenticated parent credentials are
// unavailable. The launcher uses an ordinary child process on these systems.
func parentChannel(*os.File) bool { return false }

func authenticateParent(*os.File) error {
	return errors.New("guardian parent authentication is unavailable")
}
