//go:build !windows

package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

func (l *Launcher) guardianProfileFile() (*os.File, string, error) {
	if l.managedProfile == nil {
		return nil, "", nil
	}
	parent, err := l.managedProfile.parent.Open(".")
	return parent, l.managedProfile.name, err
}

func guardianManagedCleanup(name string) (func(), error) {
	if name == "" {
		return func() {}, nil
	}
	if name == "." || name == ".." || filepath.Base(name) != name {
		return nil, fmt.Errorf("invalid managed profile name")
	}
	parent := os.NewFile(4, "rod-manager-profile-parent")
	defer parent.Close()
	syscall.CloseOnExec(4)
	info, err := parent.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("managed profile parent is not a directory")
	}
	path := "/dev/fd/4"
	if runtime.GOOS == "linux" {
		path = "/proc/self/fd/4"
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("managed profile parent descriptor could not be reopened")
	}
	return func() {
		defer root.Close()
		// Keep deletion anchored to the inherited directory even when its
		// pathname has been renamed or a parent symlink has been replaced.
		_ = root.RemoveAll(name)
	}, nil
}
