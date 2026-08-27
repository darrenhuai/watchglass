// Package web serves watchglass's local UI: watch list, the draw-a-rectangle
// calibration loop, live readouts, and config editing. All mutations write
// back to config.yaml via config.Save — the file stays the source of truth.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"image/png"
	"net/http"
	"sync"
	"time"

	"watchglass/internal/config"
	"watchglass/internal/ocr"
	"watchglass/internal/source"
	"watchglass/internal/state"
	"watchglass/internal/supervisor"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Server struct {
	// NewSource builds the frame source for snapshot/test grabs; tests inject.
	NewSource func(w config.Watch) source.Source
	// RunCtx is the parent context for watches the UI starts or restarts.
	RunCtx context.Context

	cfgPath string
	mu      sync.Mutex // guards cfg
	cfg     *config.Config
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
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{
		NewSource: func(w config.Watch) source.Source { return source.NewHTTPSnapshot(w.Source) },
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
	img, err := s.NewSource(wc).Grab(ctx)
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
	http.Error(w, "not implemented", 501)
}
func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", 501)
}
func (s *Server) testRegion(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", 501)
}
func (s *Server) save(w http.ResponseWriter, r *http.Request) { http.Error(w, "not implemented", 501) }
func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", 501)
}
