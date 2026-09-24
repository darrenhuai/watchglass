package ocr

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// TesseractInstall is how to install tesseract on this OS, for the boot
// log and the web UI's engine note.
func TesseractInstall() string { return tesseractInstall(runtime.GOOS) }

func tesseractInstall(goos string) string {
	switch goos {
	case "windows":
		return "winget install UB-Mannheim.TesseractOCR"
	case "darwin":
		return "brew install tesseract"
	}
	return "apt install tesseract-ocr"
}

// FindTesseract is the tesseract binary watchglass reads text with. With
// flagPath (the -tesseract flag) only that is tried. Otherwise PATH, and
// then the places the usual installers put it without touching PATH: on
// Windows the UB-Mannheim installer (also what winget installs) goes to
// %ProgramFiles%\Tesseract-OCR, or %LOCALAPPDATA%\Programs\Tesseract-OCR
// for a per-user install, and on macOS Homebrew's bin directories, which a
// service started by launchd doesn't have on PATH.
func FindTesseract(flagPath string) (string, error) {
	return findTesseract(flagPath, runtime.GOOS, exec.LookPath, os.Getenv, isFile)
}

func findTesseract(flagPath, goos string, look func(string) (string, error), getenv func(string) string, exists func(string) bool) (string, error) {
	if flagPath != "" {
		p, err := look(flagPath)
		if err != nil {
			return "", fmt.Errorf("-tesseract %s: not found", flagPath)
		}
		return p, nil
	}
	if p, err := look("tesseract"); err == nil {
		return p, nil
	}
	for _, c := range tesseractPlaces(goos, getenv) {
		if exists(c) {
			return c, nil
		}
	}
	return "", errors.New("tesseract not found")
}

// tesseractPlaces are the install locations FindTesseract checks after
// PATH, in order.
func tesseractPlaces(goos string, getenv func(string) string) []string {
	var out []string
	switch goos {
	case "windows":
		for _, env := range []struct{ name, sub string }{
			{"ProgramFiles", ""},
			{"ProgramFiles(x86)", ""},
			{"LOCALAPPDATA", "Programs"},
		} {
			dir := getenv(env.name)
			if dir == "" {
				continue
			}
			// Joined with a backslash whatever the host, so the list is
			// the same when a test on Linux asks for Windows's.
			parts := []string{strings.TrimRight(dir, `\/`)}
			if env.sub != "" {
				parts = append(parts, env.sub)
			}
			parts = append(parts, "Tesseract-OCR", "tesseract.exe")
			out = append(out, strings.Join(parts, `\`))
		}
	case "darwin":
		out = append(out, "/opt/homebrew/bin/tesseract", "/usr/local/bin/tesseract")
	}
	return out
}

func isFile(p string) bool {
	st, err := os.Stat(filepath.FromSlash(p))
	return err == nil && st.Mode().IsRegular()
}
