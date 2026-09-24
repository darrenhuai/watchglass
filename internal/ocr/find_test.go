package ocr

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func notOnPath(string) (string, error) { return "", errors.New("not found") }

// A07: tesseract installed the usual Windows way (UB-Mannheim / winget) is
// in Program Files and not on PATH; it is found there, in order, and the
// flag and PATH still come first.
func TestFindTesseractLooksWhereWindowsInstallsIt(t *testing.T) {
	env := map[string]string{
		"ProgramFiles":      `C:\Program Files`,
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
		"LOCALAPPDATA":      `C:\Users\me\AppData\Local`,
	}
	getenv := func(k string) string { return env[k] }
	places := tesseractPlaces("windows", getenv)
	want := []string{
		`C:\Program Files\Tesseract-OCR\tesseract.exe`,
		`C:\Program Files (x86)\Tesseract-OCR\tesseract.exe`,
		`C:\Users\me\AppData\Local\Programs\Tesseract-OCR\tesseract.exe`,
	}
	if strings.Join(places, "|") != strings.Join(want, "|") {
		t.Fatalf("places = %q, want %q", places, want)
	}
	only := func(p string) func(string) bool { return func(c string) bool { return c == p } }
	for _, w := range want {
		got, err := findTesseract("", "windows", notOnPath, getenv, only(w))
		if err != nil || got != w {
			t.Errorf("installed at %s: got %q, %v", w, got, err)
		}
	}
	if _, err := findTesseract("", "windows", notOnPath, getenv, func(string) bool { return false }); err == nil {
		t.Error("nothing installed anywhere, but FindTesseract found something")
	}
	onPath := func(n string) (string, error) {
		if n == "tesseract" {
			return `D:\tools\tesseract.exe`, nil
		}
		return "", errors.New("no")
	}
	if got, _ := findTesseract("", "windows", onPath, getenv, only(want[0])); got != `D:\tools\tesseract.exe` {
		t.Errorf("PATH should win over Program Files, got %q", got)
	}
	// The flag is the only place tried when it is set: a wrong path is an
	// error naming it, not a silent fall back to another install.
	flagLook := func(n string) (string, error) {
		if n == `E:\t\tesseract.exe` {
			return n, nil
		}
		return "", errors.New("no")
	}
	if got, err := findTesseract(`E:\t\tesseract.exe`, "windows", flagLook, getenv, only(want[0])); err != nil || got != `E:\t\tesseract.exe` {
		t.Errorf("flag: got %q, %v", got, err)
	}
	if _, err := findTesseract(`E:\nope.exe`, "windows", flagLook, getenv, only(want[0])); err == nil || !strings.Contains(err.Error(), `-tesseract E:\nope.exe`) {
		t.Errorf("a missing -tesseract should be an error naming it, got %v", err)
	}
	// An unset variable adds no place (not "\Tesseract-OCR\...").
	if p := tesseractPlaces("windows", func(string) string { return "" }); len(p) != 0 {
		t.Errorf("no env: places = %q", p)
	}
	if p := tesseractPlaces("linux", getenv); len(p) != 0 {
		t.Errorf("linux adds no places beyond PATH: %q", p)
	}
}

// The real lookup on Windows, against a fake Program Files: nothing on
// PATH, a tesseract.exe in <ProgramFiles>\Tesseract-OCR.
func TestFindTesseractInAFakeProgramFiles(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("reads the Windows environment variables")
	}
	pf := t.TempDir()
	exe := filepath.Join(pf, "Tesseract-OCR", "tesseract.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("not really"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ProgramFiles", pf)
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LOCALAPPDATA", "")
	got, err := FindTesseract("")
	if err != nil || !strings.EqualFold(got, exe) {
		t.Fatalf("FindTesseract = %q, %v; want %s", got, err, exe)
	}
}

func TestTesseractInstallPerOS(t *testing.T) {
	for goos, want := range map[string]string{
		"windows": "winget install UB-Mannheim.TesseractOCR",
		"darwin":  "brew install tesseract",
		"linux":   "apt install tesseract-ocr",
	} {
		if got := tesseractInstall(goos); got != want {
			t.Errorf("%s: %q, want %q", goos, got, want)
		}
	}
}
