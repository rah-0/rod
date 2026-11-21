package utils

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// OutputFileExclusive writes a private new file. Existing destinations, including
// symlinks, are never opened or overwritten. Missing directories use mode 0700
// and the file uses 0600 on Unix; Windows inherits parent ACLs.
// The caller must trust the parent path and any existing ancestor directories.
func OutputFileExclusive(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, writeErr := file.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	return errors.Join(writeErr, file.Close())
}
