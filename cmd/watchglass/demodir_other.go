//go:build !windows && !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package main

import "os"

// demoDir on the platforms watchglass isn't released for: a new private
// directory each start (os.MkdirTemp makes it 0700 with a random name),
// which is safe in a shared temp dir without any ownership checks.
func demoDir(root string) (string, error) {
	dir, err := os.MkdirTemp(root, demoDirName+"-")
	return dir, permissionHint(err)
}

// lockDemo has nothing to do: every start has a directory of its own.
func lockDemo(string) (func(), error) { return func() {}, nil }
