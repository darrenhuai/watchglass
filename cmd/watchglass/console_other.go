//go:build !windows

package main

// Only Windows users start watchglass by double-clicking it; everywhere
// else it runs from a terminal, a service manager or a container, which
// show its output and must never have a browser opened for them.

func launchedFromExplorer() bool { return false }

func openBrowser(string) error { return nil }

func waitForEnter() {}
