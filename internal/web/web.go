// Package web serves watchglass's local UI: watch list, the draw-a-rectangle
// calibration loop, live readouts, and config editing. All mutations write
// back to config.yaml via config.Save — the file stays the source of truth.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"watchglass/internal/config"
	"watchglass/internal/imgproc"
	"watchglass/internal/ocr"
	"watchglass/internal/source"
	"watchglass/internal/state"
	"watchglass/internal/supervisor"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Server struct {
	// NewSource builds the frame source for snapshot/test grabs. An
	// unsupported source scheme is a configuration error that must surface
	// at the HTTP request rather than silently failing. Tests inject fakes.
	NewSource func(w config.Watch) (source.Source, error)
	// RunCtx is the parent context for watches the UI starts or restarts.
	RunCtx context.Context

	cfgPath string
	mu      sync.Mutex // guards cfg
	cfg     *config.Config
	// applyMu serializes each watch mutation's persist-then-apply sequence
	// (mutateConfig followed by the matching sup.Start/Restart/Stop+Drop
	// call) across create, save, and remove. Without it, two concurrent
	// requests touching the same watch could persist in one order but apply
	// to the supervisor in the other, leaving it running a stale config
	// while the file shows the latest one.
	//
	// Lock ordering: applyMu is always acquired before mu, never the
	// reverse. mu is only ever held briefly inside mutateConfig/findWatch
	// and never across a blocking supervisor call (sup.Stop blocks until
	// the watch's goroutine exits), so this ordering cannot deadlock.
	applyMu sync.Mutex
	sup     *supervisor.Supervisor
	reg     *state.Registry
	engine  ocr.Engine
	tmpl    *template.Template
	logf    func(string, ...any)
}

func New(cfgPath string, cfg *config.Config, sup *supervisor.Supervisor, reg *state.Registry, engine ocr.Engine, logf func(string, ...any)) (*Server, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"b64png": func(b []byte) template.URL {
			return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(b))
		},
		"dur": func(d config.Duration) string { return time.Duration(d).String() },
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{
		NewSource: source.For,
		RunCtx:    context.Background(),
		cfgPath:   cfgPath,
		cfg:       cfg,
		sup:       sup,
		reg:       reg,
		engine:    engine,
		tmpl:      tmpl,
		logf:      logf,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(assets))
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("POST /watch/new", s.create)
	mux.HandleFunc("GET /watch/{name}", s.detail)
	mux.HandleFunc("GET /watch/{name}/snapshot", s.snapshot)
	mux.HandleFunc("GET /watch/{name}/live", s.live)
	mux.HandleFunc("POST /watch/{name}/test", s.testRegion)
	mux.HandleFunc("POST /watch/{name}/save", s.save)
	mux.HandleFunc("POST /watch/{name}/delete", s.remove)
	return mux
}

// findWatch returns a copy of the named watch under the config lock.
func (s *Server) findWatch(name string) (config.Watch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.cfg.Watches {
		if w.Name == name {
			return w, true
		}
	}
	return config.Watch{}, false
}

type indexRow struct {
	Watch  config.Watch
	Latest state.Sample
	Has    bool
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	watches := append([]config.Watch(nil), s.cfg.Watches...)
	s.mu.Unlock()
	rows := make([]indexRow, 0, len(watches))
	for _, wc := range watches {
		latest, has := s.reg.Latest(wc.Name)
		rows = append(rows, indexRow{Watch: wc, Latest: latest, Has: has})
	}
	s.render(w, "index.html", rows)
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	wc, ok := s.findWatch(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	src, err := s.NewSource(wc)
	if err != nil {
		http.Error(w, fmt.Sprintf("source: %v", err), http.StatusBadRequest)
		return
	}
	img, err := src.Grab(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("snapshot failed: %v", err), http.StatusBadGateway)
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.Bytes())
}

type liveData struct {
	Name   string
	Recent []state.Sample
}

func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.findWatch(name); !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "live.html", liveData{Name: name, Recent: s.reg.Recent(name)})
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.logf("render %s: %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	src := strings.TrimSpace(r.FormValue("source"))
	if name == "" || src == "" {
		http.Error(w, "name and source are required", http.StatusBadRequest)
		return
	}
	nw := config.Watch{
		Name:     name,
		Source:   src,
		Interval: config.Duration(5 * time.Second),
		Region:   config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger:  config.Trigger{Type: "pixel_change", Threshold: 25, Cooldown: config.Duration(5 * time.Minute)},
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	err := s.mutateConfig(func(c *config.Config) error {
		c.Watches = append(c.Watches, nw)
		return nil
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errSaveFailed) {
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}
	// Start from the canonical, post-Validate watch rather than nw:
	// Validate defaults Trigger.Confirm (0 -> 3) on the copy mutateConfig
	// persisted, but nw itself is untouched by that — starting the
	// supervisor with nw would run trigger.New's own default (1) while
	// config.yaml (and the UI) say 3.
	canonical, ok := s.findWatch(name)
	if !ok {
		// Should be unreachable: mutateConfig just appended and persisted
		// this watch under applyMu, and nothing else can remove it while
		// applyMu is held. Fall back to nw so the watch still starts.
		s.logf("ERROR: create %s: watch vanished immediately after persisting; starting with pre-validation config", name)
		canonical = nw
	}
	if err := s.sup.Start(s.RunCtx, canonical); err != nil {
		// The watch is already persisted to config.yaml (the redirect below
		// reflects that), but it did not start. The detail page's
		// running/stopped badge will show the true state to the user; log
		// loudly here too so it doesn't slip by unnoticed in server logs.
		s.logf("ERROR: create %s: watch saved to config.yaml but failed to start: %v", name, err)
	}
	http.Redirect(w, r, "/watch/"+name, http.StatusSeeOther)
}

type detailData struct {
	Watch   config.Watch
	Running bool
}

func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	wc, ok := s.findWatch(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	running := false
	for _, n := range s.sup.Running() {
		if n == wc.Name {
			running = true
		}
	}
	s.render(w, "detail.html", detailData{Watch: wc, Running: running})
}

// parseRegion reads and validates a normalized region (0.0-1.0) from form
// fields x, y, w, h.
func parseRegion(r *http.Request) (config.Region, error) {
	f := func(name string) (float64, error) {
		v, err := strconv.ParseFloat(r.FormValue(name), 64)
		if err != nil {
			return 0, fmt.Errorf("region %s: %w", name, err)
		}
		return v, nil
	}
	var reg config.Region
	var err error
	if reg.X, err = f("x"); err != nil {
		return reg, err
	}
	if reg.Y, err = f("y"); err != nil {
		return reg, err
	}
	if reg.W, err = f("w"); err != nil {
		return reg, err
	}
	if reg.H, err = f("h"); err != nil {
		return reg, err
	}
	if reg.W <= 0 || reg.H <= 0 || reg.X < 0 || reg.Y < 0 || reg.X+reg.W > 1 || reg.Y+reg.H > 1 {
		return reg, fmt.Errorf("region out of bounds: %+v", reg)
	}
	return reg, nil
}

// parsePreprocess reads preprocess options from form fields pp_grayscale,
// pp_invert, pp_threshold, pp_upscale. All fields are optional.
func parsePreprocess(r *http.Request) (config.Preprocess, error) {
	var p config.Preprocess
	p.Grayscale = r.FormValue("pp_grayscale") == "on"
	p.Invert = r.FormValue("pp_invert") == "on"
	if v := r.FormValue("pp_threshold"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 255 {
			return p, fmt.Errorf("preprocess threshold must be 0-255")
		}
		p.Threshold = n
	}
	if v := r.FormValue("pp_upscale"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 4 {
			return p, fmt.Errorf("preprocess upscale must be 0-4")
		}
		p.Upscale = n
	}
	return p, nil
}

type testResult struct {
	Crop  template.URL
	Text  string
	Words []ocr.Word
	Note  string
}

func (s *Server) testRegion(w http.ResponseWriter, r *http.Request) {
	wc, ok := s.findWatch(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	region, err := parseRegion(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	prep, err := parsePreprocess(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	src, err := s.NewSource(wc)
	if err != nil {
		http.Error(w, fmt.Sprintf("source: %v", err), http.StatusBadRequest)
		return
	}
	img, err := src.Grab(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("snapshot failed: %v", err), http.StatusBadGateway)
		return
	}
	prepped := imgproc.Apply(imgproc.Crop(img, region), prep)
	var buf bytes.Buffer
	if err := png.Encode(&buf, prepped); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	res := testResult{Crop: template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()))}
	switch e := s.engine.(type) {
	case nil:
		res.Note = "No OCR engine available (tesseract not on PATH) — showing the preprocessed crop only."
	case ocr.DetailedEngine:
		res.Text, res.Words, err = e.RecognizeWords(ctx, prepped)
	default:
		res.Text, err = e.Recognize(ctx, prepped)
	}
	if err != nil {
		res.Note = fmt.Sprintf("OCR failed: %v", err)
	}
	s.render(w, "testresult.html", res)
}

// parseWatchForm builds an updated copy of base from the detail form.
func parseWatchForm(base config.Watch, r *http.Request) (config.Watch, error) {
	region, err := parseRegion(r)
	if err != nil {
		return base, err
	}
	prep, err := parsePreprocess(r)
	if err != nil {
		return base, err
	}
	interval, err := time.ParseDuration(r.FormValue("interval"))
	if err != nil {
		return base, fmt.Errorf("interval: %w", err)
	}
	cooldown := time.Duration(0)
	if v := r.FormValue("cooldown"); v != "" {
		if cooldown, err = time.ParseDuration(v); err != nil {
			return base, fmt.Errorf("cooldown: %w", err)
		}
	}
	confirm := 0
	if v := r.FormValue("confirm"); v != "" {
		if confirm, err = strconv.Atoi(v); err != nil {
			return base, fmt.Errorf("confirm: %w", err)
		}
	}
	threshold := 0.0
	if v := r.FormValue("tthreshold"); v != "" {
		if threshold, err = strconv.ParseFloat(v, 64); err != nil {
			return base, fmt.Errorf("threshold: %w", err)
		}
	}
	var notifyURLs []string
	for _, line := range strings.Split(r.FormValue("notify"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			notifyURLs = append(notifyURLs, line)
		}
	}
	w := base
	w.Region = region
	w.Preprocess = prep
	w.Interval = config.Duration(interval)
	w.Notify = notifyURLs
	w.Trigger = config.Trigger{
		Type:      r.FormValue("ttype"),
		Pattern:   r.FormValue("pattern"),
		Op:        r.FormValue("op"),
		Threshold: threshold,
		Confirm:   confirm,
		Cooldown:  config.Duration(cooldown),
	}
	return w, nil
}

// errSaveFailed marks a mutateConfig failure that happened at the
// config.Save (disk I/O) step rather than in fn or Validate — callers use
// errors.Is to tell a server-side failure (retrying won't help the client)
// apart from a client-shaped validation failure (it will).
var errSaveFailed = errors.New("config save failed")

// mutateConfig applies fn to a shallow copy of the config — only the
// Watches slice header is copied, so each config.Watch's inner slices
// (e.g. Notify) still share their backing arrays with the current config.
// fn must replace whole Watch structs wholesale rather than editing their
// inner slices in place. mutateConfig then validates the result, saves it
// to disk, and installs it as current — all under the config lock. An
// error from fn or Validate leaves both the in-memory config and the file
// untouched; an error from config.Save is wrapped in errSaveFailed.
func (s *Server) mutateConfig(fn func(*config.Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := &config.Config{Watches: append([]config.Watch(nil), s.cfg.Watches...)}
	if err := fn(next); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := config.Save(s.cfgPath, next); err != nil {
		return fmt.Errorf("%w: %v", errSaveFailed, err)
	}
	s.cfg = next
	return nil
}

func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	base, ok := s.findWatch(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	updated, err := parseWatchForm(base, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	err = s.mutateConfig(func(c *config.Config) error {
		for i := range c.Watches {
			if c.Watches[i].Name == name {
				c.Watches[i] = updated
				return nil
			}
		}
		return fmt.Errorf("watch %q vanished", name)
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errSaveFailed) {
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}
	if err := s.sup.Restart(s.RunCtx, updated); err != nil {
		http.Error(w, fmt.Sprintf("saved, but restart failed: %v", err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/watch/"+name, http.StatusSeeOther)
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.findWatch(name); !ok {
		http.NotFound(w, r)
		return
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	// Persist first, then act on the supervisor/registry: if config.Save
	// fails (e.g. disk I/O error), the watch must still be running with its
	// history intact, matching config.yaml which still lists it. Stopping
	// and dropping it first would orphan a "removed" watch that the file
	// still says is present.
	err := s.mutateConfig(func(c *config.Config) error {
		out := c.Watches[:0]
		for _, wc := range c.Watches {
			if wc.Name != name {
				out = append(out, wc)
			}
		}
		c.Watches = out
		return nil
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errSaveFailed) {
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.sup.Stop(name)
	s.reg.Drop(name)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
