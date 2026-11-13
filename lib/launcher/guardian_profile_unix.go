//go:build !windows

package launcher

import "os"

func (l *Launcher) guardianProfileFile() (*os.File, string, error) {
	if l.managedProfile == nil {
		return nil, "", nil
	}
	parent, err := l.managedProfile.parent.Open(".")
	return parent, l.managedProfile.name, err
}
