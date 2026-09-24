package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/demo"
	"github.com/darrenhuai/watchglass/internal/web"
)

// A first run with no config file creates one with no watches (0600) and
// carries on: config.Load accepts what it wrote.
func TestEnsureConfigCreatesAMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	created, err := ensureConfig(p)
	if err != nil || !created {
		t.Fatalf("ensureConfig = %v, %v; want created", created, err)
	}
	raw, err := os.ReadFile(p)
	if err != nil || string(raw) != "watches: []\n" {
		t.Fatalf("created file = %q, %v", raw, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("created with mode %v, want 0600: it can come to hold passwords", fi.Mode().Perm())
		}
	}
	cfg, err := config.Load(p)
	if err != nil || len(cfg.Watches) != 0 {
		t.Fatalf("Load after create: %v, %+v", err, cfg)
	}
	// Second run: the file is there and is left alone.
	if created, err := ensureConfig(p); created || err != nil {
		t.Errorf("second ensureConfig = %v, %v; want untouched", created, err)
	}
}

// -config /config/sub/config.yaml on a fresh volume: the missing
// directories are made too.
func TestEnsureConfigCreatesMissingDirectories(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "config.yaml")
	if created, err := ensureConfig(p); err != nil || !created {
		t.Fatalf("ensureConfig = %v, %v", created, err)
	}
	if _, err := config.Load(p); err != nil {
		t.Fatal(err)
	}
}

// A file that is there but broken is the user's: run stops at Load with
// the parse error and never rewrites it.
func TestRunLeavesAMalformedConfigAlone(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	broken := "watches:\n  - name: printer\n    source: [unclosed\n"
	if err := os.WriteFile(p, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(options{configPath: p, dbPath: filepath.Join(dir, "w.db"), listen: "127.0.0.1:0", tempDir: dir})
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("run = %v, want a parse error", err)
	}
	if raw, _ := os.ReadFile(p); string(raw) != broken {
		t.Errorf("malformed config was modified: %q", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "w.db")); err == nil {
		t.Error("nothing past Load should have run (the history db was created)")
	}
}

// -demo writes its own config and db under the temp dir, fresh each start,
// and never creates or changes a config.yaml in the working directory.
func TestPrepareDemoLeavesTheWorkingConfigAlone(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	root := t.TempDir()

	d, err := prepareDemo(root, false)
	if err != nil {
		t.Fatal(err)
	}
	dir, cfgPath, dbPath := d.dir, d.cfgPath, d.dbPath
	if _, err := os.Stat(filepath.Join(cwd, "config.yaml")); !os.IsNotExist(err) {
		t.Errorf("prepareDemo created config.yaml in the working directory (%v)", err)
	}
	if filepath.Dir(dir) != root || !strings.HasPrefix(filepath.Base(dir), "watchglass-demo") ||
		filepath.Dir(cfgPath) != dir || filepath.Dir(dbPath) != dir {
		t.Errorf("demo paths %q %q %q should be in a watchglass-demo folder under %s", dir, cfgPath, dbPath, root)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Watches) != 2 || cfg.Watches[0].Source != "demo:printer" || cfg.Watches[1].Source != "demo:sevenseg" {
		t.Errorf("demo watches = %+v", cfg.Watches)
	}

	// An existing working-directory config is not touched either, and a
	// restart resets both the config and the history.
	mine := "watches: []\n# mine\n"
	if err := os.WriteFile(filepath.Join(cwd, "config.yaml"), []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("watches: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, []byte("old history"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.unlock()
	d2, err := prepareDemo(root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.unlock()
	if d2.dir != dir {
		t.Errorf("a restart moved the demo from %s to %s", dir, d2.dir)
	}
	if raw, _ := os.ReadFile(filepath.Join(cwd, "config.yaml")); string(raw) != mine {
		t.Errorf("working-directory config changed: %q", raw)
	}
	if raw, _ := os.ReadFile(cfgPath); string(raw) != string(demo.Config(true)) {
		t.Errorf("demo config not rewritten fresh: %q", raw)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("the last demo's history should be cleared (%v)", err)
	}
}

// A second -demo while one is running stops before it touches anything:
// on Unix deleting the first one's open history would succeed and quietly
// orphan it.
func TestPrepareDemoRefusesWhileAnotherDemoRuns(t *testing.T) {
	root := t.TempDir()
	d, err := prepareDemo(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.dbPath, []byte("first demo's history"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareDemo(root, false); !errors.Is(err, errDemoBusy) || !strings.Contains(err.Error(), d.dir) {
		t.Fatalf("second prepareDemo = %v, want errDemoBusy naming %s", err, d.dir)
	}
	if raw, _ := os.ReadFile(d.dbPath); string(raw) != "first demo's history" {
		t.Errorf("the running demo's history was touched: %q", raw)
	}
	d.unlock()
	d2, err := prepareDemo(root, false)
	if err != nil {
		t.Fatalf("after the first demo stopped: %v", err)
	}
	d2.unlock()
}

// A config.yaml someone planted as a link is replaced, never written
// through to what it points at.
func TestPrepareDemoNeverWritesThroughALink(t *testing.T) {
	root := t.TempDir()
	d, err := prepareDemo(root, false)
	if err != nil {
		t.Fatal(err)
	}
	d.unlock()
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(d.cfgPath)
	if err := os.Symlink(victim, d.cfgPath); err != nil {
		t.Skipf("can't make symlinks here: %v", err)
	}
	d2, err := prepareDemo(root, false)
	if err != nil {
		t.Fatal(err)
	}
	d2.unlock()
	if raw, _ := os.ReadFile(victim); string(raw) != "precious" {
		t.Errorf("the link's target was overwritten: %q", raw)
	}
	if fi, err := os.Lstat(d2.cfgPath); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("config.yaml should be a fresh regular file (%v, %v)", fi, err)
	}
}

// On Unix the temp dir is shared: the demo folder is per-uid and private,
// and one that is a link (to a folder somebody else controls) is refused.
func TestDemoDirIsPrivateOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows temp dir is already per-user")
	}
	root := t.TempDir()
	want := filepath.Join(root, fmt.Sprintf("watchglass-demo-%d", os.Getuid()))
	if err := os.Mkdir(want, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(want, 0o777) // past the umask: opened up, as an older watchglass or a stranger might leave it
	d, err := prepareDemo(root, false)
	if err != nil {
		t.Fatal(err)
	}
	d.unlock()
	if d.dir != want {
		t.Errorf("demo dir = %s, want %s", d.dir, want)
	}
	if fi, _ := os.Stat(d.dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("demo dir mode = %v, want 0700", fi.Mode().Perm())
	}

	elsewhere := t.TempDir()
	linked := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(linked, filepath.Base(want))); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareDemo(linked, false); err == nil || !strings.Contains(err.Error(), "not a directory owned by") {
		t.Errorf("a linked demo dir: prepareDemo = %v, want a refusal", err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("files were written through the link: %v", entries)
	}
}

// Running -demo on a port that is taken fails before the demo's files are
// reset, so the demo already running there keeps its history, and the
// error says what to do.
func TestRunOnATakenPortFailsBeforeTouchingTheDemo(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	root := t.TempDir()
	d, err := prepareDemo(root, false)
	if err != nil {
		t.Fatal(err)
	}
	d.unlock()
	if err := os.WriteFile(d.dbPath, []byte("running demo's history"), 0o600); err != nil {
		t.Fatal(err)
	}
	listen := taken.Addr().String()
	err = run(options{demo: true, listen: listen, tempDir: root})
	if err == nil || !strings.Contains(err.Error(), "Another program is already using "+listen) {
		t.Fatalf("run = %v, want the port-taken explanation", err)
	}
	if raw, _ := os.ReadFile(d.dbPath); string(raw) != "running demo's history" {
		t.Errorf("the demo was reset before the listen failed: %q", raw)
	}
}

func TestListenErrorSuggestsAFreePort(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	_, lerr := net.Listen("tcp", taken.Addr().String())
	if lerr == nil {
		t.Fatal("listening twice on one port succeeded")
	}
	port := taken.Addr().(*net.TCPAddr).Port
	got := listenError(taken.Addr().String(), lerr).Error()
	want := fmt.Sprintf("watchglass -listen 127.0.0.1:%d", port+1)
	if !strings.Contains(got, "Another program is already using") || !strings.Contains(got, want) {
		t.Errorf("listenError = %q, want the port explanation and %q", got, want)
	}
	// Anything else passes through unchanged.
	other := errors.New("listen tcp: lookup nowhere: no such host")
	if got := listenError("nowhere:80", other); got != other {
		t.Errorf("listenError(other) = %v", got)
	}
}

// Jenkins on 8080 is not watchglass: the double-click "already running"
// path must not send the browser there, and the healthcheck fails.
func TestProbeRejectsAnotherProgram(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<title>Jenkins</title>"))
	}))
	defer srv.Close()
	listen := strings.TrimPrefix(srv.URL, "http://")
	if err := probeWatchglass(listen); err == nil || !strings.Contains(err.Error(), "isn't watchglass") {
		t.Errorf("probeWatchglass = %v, want it to say this isn't watchglass", err)
	}
	if got := runHealthcheck(listen); got != 1 {
		t.Errorf("healthcheck against another program = %d, want 1", got)
	}
}

func TestReadyURLAndDialAddr(t *testing.T) {
	bound := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 51234}
	cases := []struct{ listen, want string }{
		{"127.0.0.1:8080", "http://127.0.0.1:8080/"},
		{"0.0.0.0:8080", "http://127.0.0.1:8080/"},
		{":8080", "http://127.0.0.1:8080/"},
		{"[::]:8080", "http://127.0.0.1:8080/"},
		{"localhost:9000", "http://localhost:9000/"},
		{"[::1]:8080", "http://[::1]:8080/"},
		{"192.168.1.5:8080", "http://192.168.1.5:8080/"},
	}
	for _, c := range cases {
		if got := readyURL(c.listen, nil); got != c.want {
			t.Errorf("readyURL(%q) = %q, want %q", c.listen, got, c.want)
		}
	}
	// :0 logs the port actually bound.
	if got := readyURL("127.0.0.1:0", bound); got != "http://127.0.0.1:51234/" {
		t.Errorf("readyURL with a bound port = %q", got)
	}
}

// -healthcheck: any answer below 500 is healthy (a 401 from a server with
// auth included); a 5xx or no answer is not.
func TestHealthcheck(t *testing.T) {
	for status, want := range map[int]int{200: 0, 401: 0, 303: 0, 500: 1, 503: 1} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				t.Errorf("healthcheck asked for %q, want /", r.URL.Path)
			}
			w.Header().Set(web.IdentityHeader, "1")
			if status == 303 {
				http.Redirect(w, r, "/elsewhere", status)
				return
			}
			w.WriteHeader(status)
		}))
		listen := strings.TrimPrefix(srv.URL, "http://")
		if got := runHealthcheck(listen); got != want {
			t.Errorf("status %d: healthcheck = %d, want %d", status, got, want)
		}
		srv.Close()
		if status == 200 {
			if got := runHealthcheck(listen); got != 1 {
				t.Errorf("nothing listening: healthcheck = %d, want 1", got)
			}
		}
	}
}

func TestEnvTrue(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, "YES": true, " on ": true, "": false, "0": false, "false": false, "demo": false} {
		t.Setenv("WATCHGLASS_TEST_FLAG", v)
		if got := envTrue("WATCHGLASS_TEST_FLAG"); got != want {
			t.Errorf("envTrue(%q) = %v, want %v", v, got, want)
		}
	}
}
