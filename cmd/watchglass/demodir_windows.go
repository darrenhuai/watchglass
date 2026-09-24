//go:build windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// demoDir makes (or reuses) the folder -demo keeps its files in. On
// Windows the temp dir is already the user's own (under their profile),
// so the name needs no user id; a junction or link in its place is still
// refused rather than followed.
func demoDir(root string) (string, error) {
	dir := filepath.Join(root, demoDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", permissionHint(err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || !fi.IsDir() {
		return "", fmt.Errorf("%s is a link, not a directory, so the demo won't use it; remove it and start again", dir)
	}
	return dir, nil
}

const errorSharingViolation = syscall.Errno(32)

// lockDemo holds dir's lock file open with no sharing until the returned
// func runs, so a second -demo stops before it touches anything.
func lockDemo(dir string) (func(), error) {
	name, err := syscall.UTF16PtrFromString(filepath.Join(dir, "lock"))
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, errorSharingViolation) {
		return nil, errDemoBusy
	}
	if err != nil {
		return nil, err
	}
	return func() { syscall.CloseHandle(h) }, nil
}
