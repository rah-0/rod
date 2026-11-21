//go:build linux

package launcher

import (
	"os"
	"path/filepath"
)

func (l *Launcher) guardianProfileFile() (*os.File, string, error) {
	if l.managedProfile == nil {
		if l.cleanupUserDataDir == "" {
			return nil, "", nil
		}
		if l.cleanupParent == nil {
			parent, err := os.OpenRoot(filepath.Dir(l.cleanupUserDataDir))
			if err != nil {
				return nil, "", err
			}
			l.cleanupParent, l.cleanupName = parent, filepath.Base(l.cleanupUserDataDir)
		}
		parent, err := l.cleanupParent.Open(".")
		return parent, l.cleanupName, err
	}
	parent, err := l.managedProfile.parent.Open(".")
	return parent, l.managedProfile.name, err
}
