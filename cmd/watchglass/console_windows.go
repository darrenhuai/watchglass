//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// launchedFromExplorer reports whether watchglass.exe was started by a
// double-click rather than from a terminal. Explorer gives a console
// program a console of its own, so this process is the only one attached
// to it; a terminal's console also has the shell attached. A process with
// no console at all (a service, a detached start) gets 0 and is not
// treated as a double-click either, and neither is one whose output is
// redirected to a file or a pipe: a script is reading that, not a person
// looking at a window.
func launchedFromExplorer() bool {
	if t, err := syscall.GetFileType(syscall.Stdout); err != nil || t != syscall.FILE_TYPE_CHAR {
		return false
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	if proc.Find() != nil {
		return false
	}
	var pids [2]uint32
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}

// openBrowser opens url in the default browser.
func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// waitForEnter keeps a double-clicked console window open until the
// error above it has been read.
func waitForEnter() {
	fmt.Fprint(os.Stderr, "\nPress Enter to exit.")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
