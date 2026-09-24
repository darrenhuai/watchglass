package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/darrenhuai/watchglass/internal/demo"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/web"
)

// First-run and start-up helpers: the version, the auto-created config,
// demo mode, the ready URL and the container health check.

// version is set at build time (-ldflags "-X main.version=0.1.8") by
// GoReleaser and the Dockerfile; see appVersion for other builds.
var version = "dev"

// options is everything run needs from the command line and environment.
type options struct {
	configPath, dbPath, listen, basePath, python string
	// tesseract is the -tesseract flag: the binary to read text with, when
	// it isn't on PATH or in the usual install places (ocr.FindTesseract).
	tesseract string
	// demo runs the built-in fake cameras from a throwaway config under
	// the temp dir (see prepareDemo); configPath and dbPath are ignored.
	demo bool
	// openBrowser opens the ready URL in the default browser: set when
	// watchglass.exe was double-clicked in Explorer.
	openBrowser bool
	// tempDir is where the demo lives; os.TempDir() unless a test says.
	tempDir string
}

// appVersion is the build's version: the one stamped in by the release
// build, else the module version `go install ...@v0.1.8` records, else
// "dev".
func appVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return version
}

// envTrue reads an on/off environment variable: 1, true, yes or on.
func envTrue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// emptyConfig is what a config file that doesn't exist yet starts as.
const emptyConfig = "watches: []\n"

// ensureConfig creates path, holding an empty watch list, when it doesn't
// exist yet, so a first run in an empty directory (or on a fresh container
// volume) comes up with the web UI instead of exiting. Anything else wrong
// with an existing file (it can't be read, it doesn't parse) is left for
// config.Load to report: a file that is there is never rewritten here.
func ensureConfig(path string) (created bool, err error) {
	_, err = os.Stat(path)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, permissionHint(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, permissionHint(err)
	}
	// O_EXCL: a file that appeared since the Stat is someone else's.
	// 0o600, like config.Save: the file can come to hold passwords.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, permissionHint(err)
	}
	if _, err := f.WriteString(emptyConfig); err != nil {
		f.Close()
		return false, err
	}
	return true, f.Close()
}

// permissionHint adds who watchglass runs as to a permission error (which
// already names the path): in a container the fix is nearly always the
// owner of a bind-mounted directory.
func permissionHint(err error) error {
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if uid := os.Getuid(); uid >= 0 {
		return fmt.Errorf("%w (watchglass runs as uid %d, gid %d, which needs write access there)", err, uid, os.Getgid())
	}
	return err
}

// demoDirName is the folder under the temp dir that -demo keeps its
// config and history in (demoDir adds the uid on Unix, where the temp dir
// is shared).
const demoDirName = "watchglass-demo"

// errDemoBusy is what lockDemo returns when another -demo holds the lock.
var errDemoBusy = errors.New("another watchglass -demo is running")

// demoFiles is where a -demo run keeps its files, and the lock it holds on
// them until unlock runs.
type demoFiles struct {
	dir, cfgPath, dbPath string
	unlock               func()
}

// prepareDemo writes a fresh demo config (demo.Config) into the demo dir
// (demoDir) and clears the history the last demo left, so every -demo
// start is the same. It never looks at -config. It takes the demo lock
// first, so a second -demo fails before it changes anything the first one
// is using; the caller holds the lock (runs unlock) until it exits.
func prepareDemo(root string, readText bool) (demoFiles, error) {
	dir, err := demoDir(root)
	if err != nil {
		return demoFiles{}, err
	}
	unlock, err := lockDemo(dir)
	if errors.Is(err, errDemoBusy) {
		return demoFiles{}, fmt.Errorf("%w with its files in %s; stop it first (Ctrl+C in its window)", err, dir)
	}
	if err != nil {
		return demoFiles{}, permissionHint(err)
	}
	d := demoFiles{dir: dir, cfgPath: filepath.Join(dir, "config.yaml"), dbPath: filepath.Join(dir, "watchglass.db"), unlock: unlock}
	fail := func(err error) (demoFiles, error) {
		unlock()
		return demoFiles{}, err
	}
	// The config is removed and created afresh (O_EXCL) rather than
	// overwritten, so whatever sits at that name, a link included, is
	// replaced and never written through.
	for _, p := range []string{d.dbPath, d.dbPath + "-wal", d.dbPath + "-shm", d.dbPath + "-journal", d.cfgPath + ".tmp", d.cfgPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fail(fmt.Errorf("couldn't reset the demo in %s: %w", dir, err))
		}
	}
	f, err := os.OpenFile(d.cfgPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail(permissionHint(err))
	}
	if _, err := f.Write(demo.Config(readText)); err != nil {
		f.Close()
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	return d, nil
}

// detectEngines finds the OCR engines this box has and says what it found
// in one log line (engineReport).
func detectEngines(python, tesseract string) ocr.Engines {
	engines, line := findEngines(python, tesseract, ocr.FindTesseract, probeRapidOCR)
	log.Print(line)
	return engines
}

// probeRapidOCR is ocr.DetectRapidOCR with a deadline. rapidocr is a Python
// package, so being on PATH proves nothing (on Windows python3 is usually
// the Store stub): the probe imports it, bounded so a wedged interpreter
// can't hold up boot.
func probeRapidOCR(python string) (*ocr.RapidOCR, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return ocr.DetectRapidOCR(ctx, python)
}

// findEngines is detectEngines with the lookups passed in, for tests.
//
// rapidocr is always probed, not only when a watch uses it: the web UI
// offers it for new watches only when it is there. Its absence is the
// normal case, so it is one word in the line; the probe's reasons are only
// spelled out when -python named an interpreter, which says the user
// expects it to work.
func findEngines(python, tesseract string, findTess func(string) (string, error), probeRapid func(string) (*ocr.RapidOCR, error)) (ocr.Engines, string) {
	engines := ocr.Engines{SevenSeg: ocr.NewSevenSeg()}
	var tessNote, rapidNote string
	if bin, err := findTess(tesseract); err == nil {
		engines.Tesseract = ocr.NewTesseract(bin)
		tessNote = bin
	} else if tesseract != "" {
		tessNote = fmt.Sprintf("missing (%v)", err)
	} else {
		tessNote = "missing (" + ocr.TesseractInstall() + ", then restart watchglass; or pass -tesseract <path>)"
	}
	if rapid, err := probeRapid(python); err == nil {
		engines.RapidOCR = rapid
		rapidNote = rapid.Python
	} else if python != "" {
		rapidNote = fmt.Sprintf("unavailable (%v)", err)
	} else {
		rapidNote = "not installed"
	}
	return engines, "engines: tesseract=" + tessNote + ", sevenseg=built-in, rapidocr=" + rapidNote
}

// dialAddr is a host:port that reaches a server listening on listen: a
// wildcard host (":8080", "0.0.0.0:8080", "[::]:8080") becomes 127.0.0.1.
// A non-empty port replaces listen's (the port actually bound, for ":0").
func dialAddr(listen, port string) string {
	host, p, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if port != "" {
		p = port
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, p)
}

// readyURL is the address the start-up log tells people to open.
func readyURL(listen string, bound net.Addr) string {
	port := ""
	if bound != nil {
		_, port, _ = net.SplitHostPort(bound.String())
	}
	return "http://" + dialAddr(listen, port) + "/"
}

// runHealthcheck is -healthcheck: exit status 0 when watchglass answers
// on listen (probeWatchglass), 1 when it doesn't.
func runHealthcheck(listen string) int {
	if err := probeWatchglass(listen); err != nil {
		fmt.Fprintln(os.Stderr, "watchglass: healthcheck:", err)
		return 1
	}
	return 0
}

// probeWatchglass GETs / on listen and reports whether a working watchglass
// answered: one that sets web.IdentityHeader, with any status below 500,
// the 401 a server with an auth: block gives an anonymous request
// included. No answer, a 5xx, or another program on the port is an error.
func probeWatchglass(listen string) error {
	client := &http.Client{
		Timeout:       3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	addr := dialAddr(listen, "")
	resp, err := client.Get("http://" + addr + "/")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.Header.Get(web.IdentityHeader) == "" {
		return fmt.Errorf("something answers on %s, but it isn't watchglass", addr)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// listenError explains a failed listen. The common one is a port another
// program (or another watchglass) already has, and the fix is a free one.
func listenError(listen string, err error) error {
	var errno syscall.Errno
	// 10048 is Windows' WSAEADDRINUSE, which syscall.EADDRINUSE isn't.
	if !errors.As(err, &errno) || (errno != syscall.EADDRINUSE && errno != 10048) {
		return err
	}
	msg := fmt.Sprintf("%v\nAnother program is already using %s. Close it, or start watchglass on a free port", err, listen)
	if host, port, perr := net.SplitHostPort(listen); perr == nil {
		if n, aerr := strconv.Atoi(port); aerr == nil && n > 0 && n < 65535 {
			msg += fmt.Sprintf(" from a terminal: watchglass -listen %s", net.JoinHostPort(host, strconv.Itoa(n+1)))
		}
	}
	return errors.New(msg)
}

// isLoopback reports whether listen only accepts connections from this
// machine.
func isLoopback(listen string) bool {
	return strings.HasPrefix(listen, "127.0.0.1") || strings.HasPrefix(listen, "localhost") ||
		strings.HasPrefix(listen, "[::1]")
}
