//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// demoDir makes (or reuses) the folder -demo keeps its files in. The temp
// dir is shared by every user on the box, so the folder name carries the
// uid and the folder must be a real directory this user owns: one that is
// a symlink, or belongs to somebody else, could have files planted in it
// (a config.yaml linked to a file of ours, say) and is refused.
func demoDir(root string) (string, error) {
	uid := os.Getuid()
	dir := filepath.Join(root, fmt.Sprintf("%s-%d", demoDirName, uid))
	// A link planted in a sticky /tmp can make MkdirAll fail too (the
	// kernel won't follow it); the Lstat below explains that better.
	mkErr := os.MkdirAll(dir, 0o700)
	fi, err := os.Lstat(dir)
	if err != nil {
		if mkErr != nil {
			return "", permissionHint(mkErr)
		}
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() || !ok || int(st.Uid) != uid {
		return "", fmt.Errorf("%s is not a directory owned by uid %d, so the demo won't use it; remove it, or set TMPDIR to a directory of yours", dir, uid)
	}
	if mkErr != nil {
		return "", permissionHint(mkErr)
	}
	// Ours, but opened up (an older watchglass made it 0755): close it.
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// lockDemo holds an exclusive lock on dir until the returned func runs, so
// a second -demo can't delete the history a running one has open (on
// Unix the delete would succeed and quietly orphan it).
func lockDemo(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errDemoBusy
	}
	return func() { f.Close() }, nil
}
