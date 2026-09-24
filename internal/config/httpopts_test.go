package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A20: tls_insecure and headers load, validate, and survive a UI save with
// the file's comments intact.
func TestTLSInsecureAndHeaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	src := `watches:
  - name: nvr
    source: https://user:pw@192.168.1.20/cgi-bin/snapshot.cgi
    # the camera ships a self-signed certificate
    tls_insecure: true
    headers:
      - "X-Api-Key: <KEY>"
    region: {x: 0, y: 0, w: 1, h: 1}
    trigger: {type: pixel_change, threshold: 10}
    notify: []
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	w := cfg.Watches[0]
	if !w.TLSInsecure || len(w.Headers) != 1 || w.Headers[0] != "X-Api-Key: <KEY>" {
		t.Fatalf("watch = %+v", w)
	}
	cfg.Watches[0].Trigger.Threshold = 20
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"# the camera ships a self-signed certificate", "tls_insecure: true", `X-Api-Key: <KEY>`, "threshold: 20"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("saved file lost %q:\n%s", want, raw)
		}
	}
	// Turned off, the key goes (omitempty), and nothing else changes.
	cfg.Watches[0].TLSInsecure = false
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "tls_insecure: true") {
		t.Errorf("tls_insecure should be off:\n%s", raw)
	}
}

func TestHeadersAndTLSInsecureAreChecked(t *testing.T) {
	base := func() Config {
		return Config{Watches: []Watch{{
			Name: "a", Source: "http://cam/snap", Region: Region{W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 5},
		}}}
	}
	for _, c := range []struct {
		name    string
		mod     func(*Watch)
		wantErr string
	}{
		{"good header", func(w *Watch) { w.Headers = []string{"Authorization: Bearer <TOKEN>"} }, ""},
		{"no colon", func(w *Watch) { w.Headers = []string{"X-Api-Key abc"} }, `must look like "Name: value"`},
		{"space in name", func(w *Watch) { w.Headers = []string{"X Api: abc"} }, "character HTTP doesn't allow"},
		{"line break in value", func(w *Watch) { w.Headers = []string{"X-A: b\r\nX-Evil: 1"} }, "line break"},
		{"tls_insecure on rtsp", func(w *Watch) { w.Source = "rtsp://cam/stream"; w.TLSInsecure = true }, "only apply to http:// and https://"},
		{"headers on the demo", func(w *Watch) { w.Source = "demo:printer"; w.Headers = []string{"A: b"} }, "only apply to http:// and https://"},
		{"tls_insecure on http is harmless", func(w *Watch) { w.TLSInsecure = true }, ""},
	} {
		cfg := base()
		c.mod(&cfg.Watches[0])
		err := cfg.Validate()
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}
}
