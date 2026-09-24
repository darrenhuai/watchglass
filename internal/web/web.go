// Package web serves watchglass's local UI: watch list, the draw-a-rectangle
// calibration loop, live readouts, and config editing. All mutations write
// back to config.yaml via config.Save — the file stays the source of truth.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"image/png"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/imgproc"
	"github.com/darrenhuai/watchglass/internal/notify"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/supervisor"
	"github.com/darrenhuai/watchglass/internal/trigger"
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
	// OnConfigChanged, when set, is called with the new watch list after
	// every successful config mutation (save, create, delete), outside s.mu.
	// NOTE: the calling handler still holds applyMu at that point (released
	// by its deferred Unlock on return), so the hook must not block — Task 7
	// wires it through a non-blocking ordered queue for exactly this reason.
	// The MQTT publisher uses it to resync discovery.
	OnConfigChanged func(watches []config.Watch)
	// BasePath, when non-empty, is prefixed to every link, form action, and
	// redirect the UI generates (e.g. "/watchglass" put in front of
	// "/watch/printer"). It exists for running behind a reverse proxy that
	// serves watchglass under a subpath: the proxy strips the prefix before
	// forwarding, so watchglass's own routes stay mounted at "/" exactly as
	// today (Handler below never sees or matches BasePath) — only what
	// watchglass writes back out needs the prefix reapplied. main sets this
	// once, before Handler is called, after validating it starts with "/"
	// and trimming any trailing slash; leaving it empty (the default) is
	// byte-identical to the pre-BasePath behavior. Templates read it via the
	// "u" FuncMap entry's closure over Server, so it must not change once
	// serving starts.
	BasePath string
	// DemoDir is set when watchglass runs with -demo: the folder its
	// throwaway config and history live in. Every page then carries a
	// banner saying so (layout.html, the "demoDir" func). Set once, before
	// Handler is called, like BasePath.
	DemoDir string

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
	engines ocr.Engines
	tmpl    *template.Template
	logf    func(string, ...any)
}

func New(cfgPath string, cfg *config.Config, sup *supervisor.Supervisor, reg *state.Registry, engines ocr.Engines, logf func(string, ...any)) (*Server, error) {
	s := &Server{
		NewSource: source.For,
		RunCtx:    context.Background(),
		cfgPath:   cfgPath,
		cfg:       cfg,
		sup:       sup,
		reg:       reg,
		engines:   engines,
		logf:      logf,
	}
	// "u" closes over s rather than capturing BasePath by value: New builds
	// and parses templates before main has set BasePath (it's assigned on
	// the returned *Server afterward), so the func must read it fresh on
	// each call. That's safe because BasePath is set once, before Handler
	// is ever called, and never mutated while serving.
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"b64png": func(b []byte) template.URL {
			return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(b))
		},
		"dur": func(d config.Duration) string { return d.String() },
		"u":   func(p string) string { return s.BasePath + p },
		// demoDir reads DemoDir fresh for the same reason "u" does.
		"demoDir": func() string { return s.DemoDir },
		// watchURL is the only way a watch name enters a URL: names may hold
		// '%', '\', spaces or non-ASCII, which a bare concatenation leaves
		// to be decoded (or path-normalized) into a different name.
		"watchURL":    s.watchURL,
		"pageTitle":   pageTitle,
		"shortErr":    summarizeErr,
		"errHint":     errHint,
		"reading":     viewReading,
		"pct":         confidencePct,
		"lowConf":     func(c float64) bool { return c < lowConfidence },
		"isoTime":     isoTime,
		"ppSet":       preprocessSet,
		"ppSummary":   preprocessSummary,
		"trigLabel":   triggerLabel,
		"confirmHelp": confirmHelp,
		"patternHelp": patternHelp,
		"redact":      redactSource,
		"engineNote": func(w config.Watch, tess, rapid bool) engineNote {
			return engineNoteFor(w, tess, rapid)
		},
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	s.tmpl = tmpl
	return s, nil
}

// watchURL is BasePath + "/watch/" + the path-escaped name + suffix (e.g.
// "/save"). ServeMux matches {name} against the escaped path and PathValue
// hands the handler the unescaped name back.
func (s *Server) watchURL(name, suffix string) string {
	return s.BasePath + "/watch/" + url.PathEscape(name) + suffix
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(assets))
	// Browsers still probe /favicon.ico whatever the page links; send them
	// to the SVG instead of answering every new tab with a 404.
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.BasePath+"/static/favicon.svg", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("POST /watch/new", s.create)
	mux.HandleFunc("GET /watch/{name}", s.detail)
	mux.HandleFunc("GET /watch/{name}/snapshot", s.snapshot)
	mux.HandleFunc("GET /watch/{name}/live", s.live)
	mux.HandleFunc("POST /watch/{name}/test", s.testRegion)
	mux.HandleFunc("POST /watch/{name}/test-notify", s.testNotify)
	mux.HandleFunc("POST /watch/{name}/save", s.save)
	mux.HandleFunc("POST /watch/{name}/delete", s.remove)
	// cop rejects cross-origin browser POSTs (via Sec-Fetch-Site, falling
	// back to Origin-vs-Host) to the POST routes above — CSRF protection
	// for /watch/new, /watch/{name}/test, /test-notify, /save and /delete
	// (a cross-site page must not make watchglass post to URLs of its
	// choosing any more than it may change the config).
	// Non-browser clients (curl, scripts) send neither header and are
	// unaffected; GET/HEAD/OPTIONS are always allowed regardless.
	//
	// Wrapped INSIDE withAuth (auth runs first): an unauthenticated request
	// gets a plain 401 without also probing whether it would have passed
	// the cross-origin check, and curl-with-Basic-Auth automation — which
	// carries no Sec-Fetch-Site/Origin headers either way — keeps working
	// unchanged on both layers.
	cop := http.NewCrossOriginProtection()
	return identify(s.withAuth(cop.Handler(mux)))
}

// IdentityHeader is set on every response, a 401 from withAuth included,
// so `watchglass -healthcheck` (and a double-clicked second copy looking
// for the first) can tell this server from whatever else answers on the
// port: Jenkins, Tomcat and dev servers all like 8080 too.
const IdentityHeader = "X-Watchglass"

func identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(IdentityHeader, "1")
		next.ServeHTTP(w, r)
	})
}

// withAuth enforces HTTP Basic over the whole UI when an auth block is
// configured. Credentials are read under the config lock on every request
// so a future config reload picks them up without a restart.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		auth := s.cfg.Auth
		s.mu.Unlock()
		if auth == nil {
			next.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || !timingSafeEqual(user, auth.Username) || !timingSafeEqual(pass, auth.Password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="watchglass"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// timingSafeEqual compares via sha256 digests so length differences leak
// nothing and the comparison is constant-time.
func timingSafeEqual(a, b string) bool {
	da := sha256.Sum256([]byte(a))
	db := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(da[:], db[:]) == 1
}

// Watches returns a copy of the current watch list, safe to read without
// holding s.mu — used by the MQTT on-connect resync (cmd/watchglass/main.go)
// to read the live list at reconnect time instead of a stale boot snapshot.
func (s *Server) Watches() []config.Watch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]config.Watch(nil), s.cfg.Watches...)
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
	Status watchStatus
	// FiredAgo is "12 s ago" when the watch fired before its latest
	// reading (that one carries the fired tag itself). The fired sample
	// only stays latest for one interval, shorter than index.js's poll, so
	// without this a fire could come and go without the list showing it.
	FiredAgo string
	// AlertsFailing is set when the watch's last alert didn't reach its
	// notify URLs: the sentence the row's "alerts failing" marker carries
	// as its title (the Live panel shows the same one). A later send that
	// goes through clears it.
	AlertsFailing string
}

// agoText says how long ago something was, in the same words as app.js's
// stale badge: "just now", "40 s ago", "3 min ago", "5 h ago", "2 days ago".
// The list is re-rendered on every poll, so it stays current.
func agoText(d time.Duration) string {
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
}

// watchStatus is a watch's true health for display, derived from three
// independent signals that must_fix 1 found the UI conflating: whether the
// supervisor actually has the watch running (not just whether config.yaml
// lists it), and — if it is running — whether its source has been failing
// health.Tracker's consecutive-failure threshold. Rendered on the dashboard
// list, the detail page's status pill, and the Live panel's staleness
// badge, so a failed-restart or fetch-failing watch never looks like a
// genuinely healthy one in any of the three places.
type watchStatus struct {
	State   string // "running" | "stopped" | "error"
	Message string // set only for "error": health.Event.Message
	Since   time.Time
}

// isRunning reports whether name is currently running in the supervisor.
func (s *Server) isRunning(name string) bool {
	for _, n := range s.sup.Running() {
		if n == name {
			return true
		}
	}
	return false
}

// statusFor derives a watch's display status. A watch that isn't running at
// all (e.g. config.yaml still lists it, but a "Save & restart" failed to
// actually restart it — must_fix 1 case 1) always reads as "stopped",
// regardless of any stale health verdict left over from before. A running
// watch whose source has been failing reads as "error" (must_fix 1 case 2);
// everything else reads as "running".
func (s *Server) statusFor(name string, running bool) watchStatus {
	if !running {
		return watchStatus{State: "stopped"}
	}
	if h, ok := s.reg.GetHealth(name); ok && h.Down {
		return watchStatus{State: "error", Message: h.Message, Since: h.Since}
	}
	return watchStatus{State: "running"}
}

// indexData feeds index.html: the watch rows, a one-shot confirmation
// after a create/delete redirect, and — when a create was rejected — the
// add form's submitted values and field errors.
type indexData struct {
	Rows  []indexRow
	Flash *flash
	Form  formState
	// ConfigFile is the config file's base name, for the delete
	// confirmation's "is removed from …".
	ConfigFile string
}

// pollHeader marks index.js's background refresh of the list. Such a
// request must not consume the one-shot flash cookie: the confirmation
// belongs to the page load the user navigated to, not to a poll that
// happens to land between a redirect's Set-Cookie and its GET.
const pollHeader = "X-Watchglass-Poll"

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	s.renderIndex(w, r, http.StatusOK, formState{})
}

func (s *Server) renderIndex(w http.ResponseWriter, r *http.Request, status int, form formState) {
	s.mu.Lock()
	watches := append([]config.Watch(nil), s.cfg.Watches...)
	s.mu.Unlock()
	rows := make([]indexRow, 0, len(watches))
	now := time.Now()
	for _, wc := range watches {
		latest, has := s.reg.Latest(wc.Name)
		row := indexRow{
			Watch:  wc,
			Latest: latest,
			Has:    has,
			Status: s.statusFor(wc.Name, s.isRunning(wc.Name)),
		}
		if t, ok := s.reg.LastFired(wc.Name); ok && !(has && latest.Fired) {
			row.FiredAgo = agoText(now.Sub(t))
		}
		if dv := s.deliveryFor(wc, latest, has); dv.State == "failed" {
			row.AlertsFailing = dv.Sentence()
		}
		rows = append(rows, row)
	}
	data := indexData{Rows: rows, Form: form, ConfigFile: s.configFile()}
	if r.Method == http.MethodGet && r.Header.Get(pollHeader) == "" {
		data.Flash = s.takeFlash(w, r)
	}
	s.renderStatus(w, status, "index.html", data)
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	wc, ok := s.findWatch(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), grabTimeout)
	defer cancel()
	src, err := s.NewSource(wc)
	if err != nil {
		sourceError(w, err)
		return
	}
	img, err := src.Grab(ctx)
	if err != nil {
		grabError(w, err)
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
	Name string
	// Engine is the watch's configured engine: it decides whether a '?'
	// in a reading is the seven-segment decoder's "digit not read" or just
	// text that has a question mark in it.
	Engine string
	// Recent is newest first; the readout shows Recent[0]. Tiles is the
	// filmstrip: Recent with runs of equal readings collapsed.
	Recent []state.Sample
	Tiles  []stripTile
	Status watchStatus
	// Delivery is how the watch's alerts are getting out (the line under
	// the readout), and FiredTag what the newest reading's fired tag adds
	// when that reading is a fire: "sent", "not delivered", "sending" or "".
	Delivery deliveryView
	FiredTag string
	// Progress is the newest reading's step towards Confirm
	// (confirmProgress), nil when it isn't on its way to a fire.
	Progress *progressView
}

func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	wc, ok := s.findWatch(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	recent := s.reg.Recent(name)
	var latest state.Sample
	if len(recent) > 0 {
		latest = recent[0]
	}
	dv := s.deliveryFor(wc, latest, len(recent) > 0)
	s.renderCached(w, r, "live.html", liveData{
		Name:     name,
		Engine:   wc.Engine,
		Recent:   recent,
		Tiles:    collapseSamples(recent),
		Status:   s.statusFor(name, s.isRunning(name)),
		Delivery: dv,
		FiredTag: dv.firedTag,
		Progress: confirmProgress(wc.Trigger, latest),
	})
}

// deliveryView is what the page says about a watch's alerts. State is
// "failed" (the last alert didn't get through; it stays until one does),
// "sending", "sent", "none" (no notify URLs, so fires only show here) or
// "" (nothing sent yet).
type deliveryView struct {
	State string
	At    time.Time // when the alert it describes was raised
	Kind  string    // "fired", "down" or "recovered"
	Text  string    // "failed": what went wrong, in sentences (deliveryText)
	MQTT  bool      // "none": fires still go out over MQTT
	// firedTag is the suffix for the newest reading's fired tag.
	firedTag string
}

// KindNote names a non-fire alert: "" for a fire, " (stream down)",
// " (stream recovered)".
func (d deliveryView) KindNote() string {
	switch d.Kind {
	case "down":
		return " (stream down)"
	case "recovered":
		return " (stream recovered)"
	}
	return ""
}

// Sentence is the failure as one line for a title attribute, the time in
// the server's zone.
func (d deliveryView) Sentence() string {
	return "Last alert" + d.KindNote() + " at " + d.At.Format("15:04:05") + " could not be delivered: " + d.Text
}

// deliveryFor works out deliveryView for a watch from the registry.
// latest is the newest reading (has: there is one), whose fired tag the
// view also decides.
func (s *Server) deliveryFor(wc config.Watch, latest state.Sample, has bool) deliveryView {
	if len(wc.Notify) == 0 {
		s.mu.Lock()
		mqtt := s.cfg.MQTT != nil
		s.mu.Unlock()
		return deliveryView{State: "none", MQTT: mqtt}
	}
	var v deliveryView
	d, done := s.reg.GetDelivery(wc.Name)
	sendingAt, sending := s.reg.Sending(wc.Name)
	switch {
	case done && !d.OK && !d.Skipped:
		v = deliveryView{State: "failed", At: d.TS, Kind: d.Kind, Text: deliveryText(d.Err)}
	case sending:
		v = deliveryView{State: "sending", At: sendingAt, Kind: "fired"}
	case done && d.OK:
		v = deliveryView{State: "sent", At: d.TS, Kind: d.Kind}
	}
	if has && latest.Fired {
		switch {
		case done && d.TS.Equal(latest.TS) && d.OK:
			v.firedTag = "sent"
		case done && d.TS.Equal(latest.TS) && !d.Skipped:
			v.firedTag = "not delivered"
		case sending && sendingAt.Equal(latest.TS):
			v.firedTag = "sending"
		}
	}
	return v
}

// sourceError and grabError answer /snapshot and /test when no frame could
// be had. The body is text/plain in two parts: a one-line summary for
// people (plus errHint's advice, when there is some), then the full error
// chain. app.js shows the first line and keeps
// the rest behind a "Technical detail" disclosure, always via textContent:
// the chain echoes the source URL, which is user input.
func sourceError(w http.ResponseWriter, err error) {
	http.Error(w, friendlyConfigError(err).Msg+"\n"+err.Error(), http.StatusBadRequest)
}

func grabError(w http.ResponseWriter, err error) {
	http.Error(w, withHint(summarizeErr(err.Error()), err.Error())+"\n"+err.Error(), http.StatusBadGateway)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	s.renderStatus(w, http.StatusOK, name, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.logf("render %s: %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// pageTitle names each page in the tab, history and bookmarks. A watch's
// state leads the detail title so a failing watch is visible in a tab
// strip; app.js keeps that prefix current as the watch changes state.
func pageTitle(data any) string {
	const app = "watchglass"
	switch d := data.(type) {
	case indexData:
		return "Watches · " + app
	case detailData:
		prefix := ""
		if d.Status.State == "error" || d.Status.State == "stopped" {
			prefix = "[" + d.Status.State + "] "
		}
		return prefix + d.Watch.Name + " · " + app
	case errorPageData:
		return d.Title + " · " + app
	}
	return app
}

// flash is a one-shot confirmation carried across the redirect that ends a
// successful save, create or delete. Kind is "saved", "created" or
// "deleted"; Subject is the watch name, or the time for "saved". Reason is
// set only on a create that was written but whose watch didn't start, which
// turns the confirmation into a warning. File is the config file's base
// name (the -config flag can point anywhere), filled in when it is read.
type flash struct {
	Kind    string
	Subject string
	Reason  string
	File    string
	At      time.Time // "saved" only: when
}

// flashReasonMax keeps the cookie well under browser size limits; a start
// failure's summary is one sentence, so this only trims pathological ones.
const flashReasonMax = 300

const flashCookie = "wg_flash"

// setFlash stores the confirmation in a short-lived cookie rather than a
// query parameter, so the redirect Location stays the plain page URL and a
// reload or a shared link never repeats it.
// Each part is escaped on its own before they are joined, so a watch name
// (or a reason) holding '|' can't spill into the next field.
func (s *Server) setFlash(w http.ResponseWriter, kind, subject, reason string) {
	v := kind + "|" + url.QueryEscape(subject)
	if reason != "" {
		if r := []rune(reason); len(r) > flashReasonMax {
			reason = string(r[:flashReasonMax-1]) + "…"
		}
		v += "|" + url.QueryEscape(reason)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    url.QueryEscape(v),
		Path:     s.BasePath + "/",
		MaxAge:   60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// takeFlash reads and clears the confirmation. It must run before the
// response headers are written.
func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) *flash {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: s.BasePath + "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	v, err := url.QueryUnescape(c.Value)
	if err != nil {
		return nil
	}
	parts := strings.Split(v, "|")
	if len(parts) < 2 || len(parts) > 3 {
		return nil
	}
	for i := 1; i < len(parts); i++ {
		if parts[i], err = url.QueryUnescape(parts[i]); err != nil {
			return nil
		}
	}
	f := &flash{Kind: parts[0], Subject: parts[1], File: s.configFile()}
	if len(parts) == 3 {
		f.Reason = parts[2]
	}
	switch f.Kind {
	case "saved":
		// Subject is the save time (RFC 3339, UTC); the page shows it in the
		// viewer's zone once app.js has run.
		if f.At, err = time.Parse(time.RFC3339, f.Subject); err != nil {
			return nil
		}
		f.Reason = ""
		return f
	case "deleted":
		f.Reason = ""
		return f
	case "created":
		return f
	}
	return nil
}

// configFile is the config file's base name for user-facing copy.
func (s *Server) configFile() string {
	if s.cfgPath == "" {
		return "the config file"
	}
	return filepath.Base(s.cfgPath)
}

// formState carries a rejected form back into its page, so a typo costs
// one correction instead of every edit: Values holds what was typed into
// the free-text fields (an invalid duration can't live in a config.Watch),
// Errors the problems in form order.
type formState struct {
	Values map[string]string
	Errors []fieldError
	// Failure is set when the input was fine but the config file couldn't
	// be written: the form comes back with everything still typed in, so
	// retrying is one click once the file is writable again.
	Failure *writeFailure
	// Notify lists the notify lines a save refused, each with its reason
	// and the URL it probably meant, shown under the Notify box.
	Notify []notifyProblem
}

// notifyProblem is one notify line that can't be used (notify.Check).
// Fix is the full rewrite a "Use this" button puts in the line; FixShown
// is the same with its credentials masked, for the page to show.
type notifyProblem struct {
	Line     int
	Label    string
	Reason   string
	Fix      string
	FixShown string
}

// notifyLines splits the Notify box into its URLs, one per non-blank line,
// the same way for Save and for Send test notification.
func notifyLines(v string) []string {
	var out []string
	for _, line := range strings.Split(v, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// checkNotify runs notify.Check on each line: the same test a watch's start
// runs, so a list the form accepts is one the watch starts with.
func checkNotify(urls []string) []notifyProblem {
	var out []notifyProblem
	for i, u := range urls {
		p := notify.Check(u)
		if p == nil {
			continue
		}
		np := notifyProblem{Line: i + 1, Label: notify.Label(u), Reason: p.Reason, Fix: p.Fix}
		if p.Fix != "" {
			np.FixShown = notify.Redact(p.Fix)
		}
		out = append(out, np)
	}
	return out
}

// writeFailure explains a save or create that couldn't be written.
type writeFailure struct {
	Title   string // what didn't happen, e.g. "Couldn't save printer"
	Message string // the state things are in now
	Detail  string // the raw error chain, behind a disclosure
}

// Val is the submitted text for field name, or fallback on a fresh page.
func (f formState) Val(name, fallback string) string {
	if v, ok := f.Values[name]; ok {
		return v
	}
	return fallback
}

// Rejected is true when the page is a save that was refused or couldn't be
// written: the form shows the submitted values, which differ from the file,
// so it is dirty from the start (the Save bar says so and app.js asks
// before the page is left).
func (f formState) Rejected() bool { return len(f.Errors) > 0 || f.Failure != nil }

// Err is the error for field name, or "".
func (f formState) Err(name string) string {
	for _, e := range f.Errors {
		if e.Field == name {
			return e.Msg
		}
	}
	return ""
}

// fieldAnchors maps a form field to the element the error summary's link
// for it jumps to.
var fieldAnchors = map[string]string{
	"name":         "new-name",
	"source":       "new-source",
	"region":       "stage",
	"ttype":        "f-ttype",
	"engine":       "f-engine",
	"pattern":      "f-pattern",
	"op":           "f-op",
	"tthreshold":   "f-threshold",
	"confirm":      "f-confirm",
	"cooldown":     "f-cooldown",
	"interval":     "f-interval",
	"max_interval": "f-maxinterval",
	"health_after": "f-health",
	"pp_threshold": "f-binarize",
	"pp_upscale":   "f-upscale",
	"notify":       "f-notify",
}

// Anchor is "" for an error that belongs to no single field.
func (e fieldError) Anchor() string { return fieldAnchors[e.Field] }

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	src := strings.TrimSpace(r.FormValue("source"))
	// A rejected create comes back to the list with the add form still
	// filled in and the problem next to its field, not to a separate page.
	form := formState{Values: map[string]string{"name": r.FormValue("name"), "source": r.FormValue("source")}}
	// Each field is checked on its own so one round trip reports both a bad
	// name and a bad source; config.Validate (inside mutateConfig below)
	// stops at the first problem and stays the check that counts.
	if name == "" {
		form.Errors = append(form.Errors, fieldError{"name", "Enter a name for the watch."})
	} else if err := config.ValidWatchName(name); err != nil {
		form.Errors = append(form.Errors, friendlyConfigError(err))
	} else if _, taken := s.findWatch(name); taken {
		form.Errors = append(form.Errors, friendlyConfigError(fmt.Errorf("duplicate watch name %q", name)))
	}
	if src == "" {
		form.Errors = append(form.Errors, fieldError{"source", "Enter the camera's source URL."})
	} else if _, err := config.SourceKind(src); err != nil {
		form.Errors = append(form.Errors, friendlyConfigError(err))
	}
	if len(form.Errors) > 0 {
		s.renderIndex(w, r, http.StatusBadRequest, form)
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
		if errors.Is(err, errSaveFailed) {
			form.Failure = &writeFailure{
				Title:   "Couldn't create the watch",
				Message: "watchglass couldn't write " + s.configFile() + ", so nothing was created. Make the file writable, then press Create again.",
				Detail:  err.Error(),
			}
			s.renderIndex(w, r, http.StatusInternalServerError, form)
			return
		}
		form.Errors = append(form.Errors, friendlyConfigError(err))
		s.renderIndex(w, r, http.StatusBadRequest, form)
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
	startReason := ""
	if err := s.sup.Start(s.RunCtx, canonical); err != nil {
		// The watch is already persisted to config.yaml (the redirect below
		// reflects that), but it did not start. The detail page's stopped
		// pill shows the state, and the flash turns into a warning with the
		// reason rather than a plain "Created"; log loudly here too so it
		// doesn't slip by unnoticed in server logs.
		s.logf("ERROR: create %s: watch saved to config.yaml but failed to start: %v", name, err)
		startReason = friendlyStartError(err)
		if !strings.HasSuffix(startReason, ".") {
			startReason += "."
		}
	}
	s.notifyConfigChanged()
	s.setFlash(w, "created", name, startReason)
	http.Redirect(w, r, s.watchURL(name, ""), http.StatusSeeOther)
}

type detailData struct {
	Watch  config.Watch
	Status watchStatus
	// TesseractAvailable is false when the server started with no tesseract on
	// PATH — should_fix 2: the detail page uses it to disable/annotate the
	// OCR-only trigger types in the Type dropdown instead of only failing
	// after Save writes a config whose restart is already known to fail.
	// The built-in seven-segment decoder is always present, so the template
	// only locks those types while the watch's engine is tesseract.
	TesseractAvailable bool
	// RapidOCRAvailable is false when boot found no Python with the rapidocr
	// package — the same lock/annotation as TesseractAvailable, applied
	// while the watch's engine is rapidocr.
	RapidOCRAvailable bool
	// TesseractInstall is the command that installs tesseract on this OS,
	// for the engine note (ocr.TesseractInstall).
	TesseractInstall string
	// ShowTLS puts the Certificate checkbox (tls_insecure) on the page:
	// an https:// source, or a watch that already has it on.
	ShowTLS bool
	// Fresh is set while the watch's trigger is still Create's default
	// (isFresh): the page offers the "What are you watching?" presets.
	Fresh bool
	// Base carries BasePath into the page so app.js can prefix the fetch
	// URLs it builds client-side (the "u" FuncMap func only covers
	// server-rendered links) — see the #stage data-base attribute in
	// detail.html.
	Base string
	// ConfigFile is the config file's base name, for copy that names it.
	ConfigFile string
	// Flash is the confirmation after a save or create redirect.
	Flash *flash
	// Form is set when a save was rejected: the page is re-rendered with
	// what was submitted (Watch holds every field that parsed) and the
	// field errors, and nothing was written.
	Form formState
}

func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	wc, ok := s.findWatch(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := s.detailFor(wc)
	data.Flash = s.takeFlash(w, r)
	s.render(w, "detail.html", data)
}

func (s *Server) detailFor(wc config.Watch) detailData {
	return detailData{
		Watch:              wc,
		Status:             s.statusFor(wc.Name, s.isRunning(wc.Name)),
		TesseractAvailable: s.engines.Tesseract != nil,
		RapidOCRAvailable:  s.engines.RapidOCR != nil,
		TesseractInstall:   ocr.TesseractInstall(),
		Fresh:              isFresh(wc),
		ShowTLS:            strings.HasPrefix(wc.Source, "https://") || wc.TLSInsecure,
		Base:               s.BasePath,
		ConfigFile:         s.configFile(),
	}
}

// errorPageData feeds error.html, which is only for failures a form can't
// fix in place: a saved watch didn't restart, a delete failed, a watch
// vanished mid-save. (An unwritable config file on save or create re-renders
// the form instead, with everything still typed in.) (must_fix 3: these used to be bare http.Error
// text bodies with no way back into the app.)
type errorPageData struct {
	Title   string // the h1: what didn't happen, e.g. "Couldn't save printer"
	Message string // one or two sentences: what state things are in now
	Reason  string // the cause in plain words, when there is one
	Detail  string // the raw error chain, behind a disclosure
	// BackURL is relative to "/"; renderError prefixes BasePath.
	BackURL   string
	BackLabel string
}

// renderError writes a styled, on-brand error page. Falls back to
// http.Error if the template itself fails to render, matching s.render.
func (s *Server) renderError(w http.ResponseWriter, status int, data errorPageData) {
	var buf bytes.Buffer
	data.BackURL = s.BasePath + data.BackURL
	if err := s.tmpl.ExecuteTemplate(&buf, "error.html", data); err != nil {
		s.logf("render error.html: %v", err)
		http.Error(w, data.Title+": "+data.Message, status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// parseRegion reads and validates a normalized region (0.0-1.0) from form
// fields x, y, w, h.
func parseRegion(r *http.Request) (config.Region, error) {
	f := func(name string) (float64, error) {
		v, err := strconv.ParseFloat(r.FormValue(name), 64)
		if err != nil {
			return 0, fmt.Errorf("region %s must be a number between 0 and 1", strings.ToUpper(name))
		}
		// strconv.ParseFloat happily accepts "NaN"/"Inf"/"-Inf" as valid
		// floats, and NaN in particular sails through every plain
		// comparison the bounds check below performs, so it must be
		// rejected explicitly here.
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, fmt.Errorf("region %s must be a number between 0 and 1", strings.ToUpper(name))
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
	// A region dragged to the frame's edge can arrive with x+w a rounding
	// step past 1 (each edge rounded to four decimals on its own); that is
	// the edge, not a region outside the frame.
	reg = reg.Clamp()
	if reg.W <= 0 || reg.H <= 0 || reg.X < 0 || reg.Y < 0 || reg.X+reg.W > 1 || reg.Y+reg.H > 1 {
		return reg, errors.New("region must fit inside the frame: X and Y at least 0, W and H above 0, X+W and Y+H at most 1")
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
			return p, fmt.Errorf("binarize must be a whole number from 0 to 255")
		}
		p.Threshold = n
	}
	if v := r.FormValue("pp_upscale"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 4 {
			return p, fmt.Errorf("upscale must be off, 2x, 3x or 4x")
		}
		p.Upscale = n
	}
	return p, nil
}

type testResult struct {
	Crop template.URL
	// At is when the frame was tested; Engine names the reader, "" when
	// the trigger type reads no text (pixel_change).
	At     time.Time
	Engine string
	// Read is true when an engine actually read the crop (Text may still
	// be empty: nothing legible). Reading is Text split for display.
	Read    bool
	Text    string
	Reading readingView
	Words   []ocr.Word
	Note    string
	// NoteDetail is the raw error behind a failed read, shown under Note
	// behind "Technical detail".
	NoteDetail string
	// Verdict is the trigger's condition checked against this one reading.
	Verdict *testVerdict
}

// testVerdict answers "would this fire?" for one test. State is "met",
// "unmet", "info" (the type can't be judged from one frame) or "invalid"
// (the trigger settings themselves don't work).
type testVerdict struct {
	State  string
	Title  string
	Detail string
}

// verdictFor checks trig against the test's reading. It says "condition
// met", never "will notify": edges, Confirm and Cooldown depend on the
// readings around this one. view says whether the seven-segment decoder
// left digits unread ("?4?", "?"): the check still runs, since the watch
// judges the same text, but a met/unmet answer about "4" (or "no number")
// when the display shows 24.5 would be false confidence, so it comes back
// as info.
func verdictFor(trig config.Trigger, reading string, view readingView, errs []fieldError) *testVerdict {
	// Only a threshold the numeric check needs can stop it; a bad Confirm
	// or Cooldown is Save's to report (their base values stand in).
	for _, e := range errs {
		if e.Field == "tthreshold" && trig.Type == "numeric" {
			return &testVerdict{State: "invalid", Title: "Can't check the trigger", Detail: e.Msg}
		}
	}
	cond, err := trigger.Check(trig, reading)
	if err != nil {
		return &testVerdict{State: "invalid", Title: "Can't check the trigger", Detail: friendlyConfigError(fmt.Errorf("trigger: %w", err)).Msg}
	}
	switch {
	case !cond.Evaluable && trig.Type == "pixel_change":
		return &testVerdict{State: "info", Title: "Nothing to compare yet", Detail: cond.Detail + "."}
	case !cond.Evaluable:
		return &testVerdict{State: "info", Title: "No verdict from one reading", Detail: cond.Detail + "."}
	case view.Unreadable:
		return &testVerdict{State: "info", Title: "No digits read", Detail: "The decoder couldn't read any digit, so there's nothing to check the trigger against yet."}
	case view.Partial:
		return &testVerdict{State: "info", Title: "Partial reading", Detail: "Some digits weren't read, so this only checked part of the number: " +
			lowerFirst(cond.Detail) + "."}
	case !cond.Met:
		return &testVerdict{State: "unmet", Title: "Condition not met", Detail: cond.Detail + "."}
	}
	confirm := trig.Confirm
	if confirm <= 0 {
		confirm = 3 // config.Validate's default
	}
	when := "on the first reading like this"
	if confirm > 1 {
		// Confirm counts readings that meet the condition (trigger.go
		// condKey), not identical texts.
		when = fmt.Sprintf("after %d readings in a row meet it", confirm)
	}
	return &testVerdict{State: "met", Title: "Condition met", Detail: cond.Detail + ". The watch fires " + when + ", unless it was already met or Cooldown is running."}
}

func (s *Server) testRegion(w http.ResponseWriter, r *http.Request) {
	wc, ok := s.findWatch(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Error bodies here are text/plain (see grabError); app.js renders a
	// non-OK answer as text, never as markup.
	region, err := parseRegion(r)
	if err != nil {
		http.Error(w, upperFirst(err.Error()), http.StatusBadRequest)
		return
	}
	prep, err := parsePreprocess(r)
	if err != nil {
		http.Error(w, upperFirst(err.Error()), http.StatusBadRequest)
		return
	}
	grabCtx, cancelGrab := context.WithTimeout(r.Context(), grabTimeout)
	defer cancelGrab()
	// The Certificate box as the form has it: ticking it and pressing Test
	// tries the camera that way before anything is saved.
	testWatch := wc
	testWatch.TLSInsecure = formTLSInsecure(r, wc.TLSInsecure)
	src, err := s.NewSource(testWatch)
	if err != nil {
		sourceError(w, err)
		return
	}
	img, err := src.Grab(grabCtx)
	cancelGrab()
	if err != nil {
		grabError(w, err)
		return
	}
	prepped := imgproc.Apply(imgproc.Crop(img, region), prep)
	var buf bytes.Buffer
	if err := png.Encode(&buf, prepped); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	res := testResult{
		Crop: template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())),
		At:   time.Now(),
	}
	// The trigger as the form has it now (unsaved edits included), so the
	// verdict answers for what the user is looking at. A request without
	// ttype (curl, an older client) is judged by the saved trigger.
	trig, trigErrs := wc.Trigger, []fieldError(nil)
	if _, ok := r.Form["ttype"]; ok {
		trig, trigErrs = triggerFromForm(r, wc.Trigger)
	}
	// pixel_change reads no text, so a test of it doesn't run an engine:
	// its answer is the crop and the note that one frame has nothing to
	// compare with. (Only when the form says so; see above.)
	if r.FormValue("ttype") == "pixel_change" {
		res.Verdict = verdictFor(trig, "", readingView{}, trigErrs)
		s.render(w, "testresult.html", res)
		return
	}
	// The form's engine wins over the saved one so the decoder can be tried
	// before saving; a request without the field (curl, an older client)
	// reads with whatever the watch is configured for.
	engName := r.FormValue("engine")
	if engName == "" {
		engName = wc.Engine
	}
	res.Engine = engName
	if res.Engine == "" {
		res.Engine = "tesseract"
	}
	engine, err := s.engines.For(engName)
	switch {
	case errors.Is(err, ocr.ErrNoTesseract):
		res.Note = "tesseract isn't installed, so this shows the crop only. Install it (" + ocr.TesseractInstall() + ") and restart watchglass to read text, or for a digit display try Engine: sevenseg."
	case errors.Is(err, ocr.ErrNoRapidOCR):
		res.Note = "rapidocr isn't installed, so this shows the crop only. Install it with pip install rapidocr onnxruntime and restart watchglass."
	case err != nil:
		res.Note = upperFirst(fmt.Sprintf("engine unavailable: %v", err)) + "."
	default:
		// The read has its own budget, sized for the engine (see
		// testReadBudgets), after the grab's.
		budget := testReadBudget(res.Engine)
		ctx, cancel := context.WithTimeout(r.Context(), budget)
		defer cancel()
		if e, ok := engine.(ocr.DetailedEngine); ok {
			res.Text, res.Words, err = e.RecognizeWords(ctx, prepped)
		} else {
			res.Text, err = engine.Recognize(ctx, prepped)
		}
		if err != nil {
			timedOut := errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)
			res.Note = readFailureNote(res.Engine, budget, timedOut)
			res.NoteDetail = err.Error()
			break
		}
		res.Read = true
		res.Reading = viewReading(engName, res.Text)
		res.Verdict = verdictFor(trig, res.Text, res.Reading, trigErrs)
	}
	s.render(w, "testresult.html", res)
}

// testNotifyTimeout bounds one test send. shoutrrr's routers give up after
// their own 10 s whatever the context says; the ntfy client honours it.
const testNotifyTimeout = 10 * time.Second

// testNotifyMax is the most lines one test sends to.
const testNotifyMax = 10

// notifyTestRow is one line of the Notify box after a test send.
type notifyTestRow struct {
	Line  int
	Label string // scheme://host, never the credential
	OK    bool
	// Cause is what went wrong sending (a phrase after Label, see
	// deliveryCause); Problem is why the URL couldn't be used at all, with
	// Fix/FixShown when there is an obvious rewrite.
	Cause   string
	Problem *notifyProblem
	// Fix is the rewrite for a URL that was sent to but failed in a way
	// with one obvious remedy (plain http behind an https URL).
	Fix *notifyProblem
}

type notifyTestData struct {
	Name string
	At   time.Time
	Rows []notifyTestRow
	Sent int
}

// testNotify sends a test notification to each URL in the form's Notify
// box as it is now, saved or not, and says line by line whether it got
// there. Nothing is written and the running watch is untouched. Error
// bodies are text/plain, like /test's.
func (s *Server) testNotify(w http.ResponseWriter, r *http.Request) {
	wc, ok := s.findWatch(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	lines := notifyLines(r.FormValue("notify"))
	switch {
	case len(lines) == 0:
		http.Error(w, "There's nothing to send to yet: add a notification URL first.", http.StatusBadRequest)
		return
	case len(lines) > testNotifyMax:
		http.Error(w, fmt.Sprintf("Test at most %d URLs at a time.", testNotifyMax), http.StatusBadRequest)
		return
	}
	title := "watchglass: " + wc.Name + " (test)"
	body := "Test from watchglass: " + wc.Name + " can reach you."
	rows := make([]notifyTestRow, len(lines))
	var wg sync.WaitGroup
	for i, u := range lines {
		rows[i] = notifyTestRow{Line: i + 1, Label: notify.Label(u)}
		if ps := checkNotify([]string{u}); len(ps) > 0 {
			ps[0].Line = i + 1
			rows[i].Problem = &ps[0]
			continue
		}
		wg.Add(1)
		go func(row *notifyTestRow, u string) {
			defer wg.Done()
			n, err := notify.NewShoutrrr([]string{u})
			if err == nil {
				ctx, cancel := context.WithTimeout(r.Context(), testNotifyTimeout)
				err = n.Send(ctx, title, body)
				cancel()
			}
			var se *notify.SendError
			switch {
			case err == nil:
				row.OK = true
			case errors.As(err, &se) && len(se.Failures) > 0:
				row.Cause = upperFirst(deliveryCause(se.Failures[0].Msg, row.Label))
				// A server that answered https with plain http has one obvious
				// fix, the same URL on +http://: offer it like a refused URL's.
				if low := strings.ToLower(se.Failures[0].Msg); strings.Contains(low, "gave http response to https client") ||
					strings.Contains(low, "first record does not look like a tls handshake") {
					if fix := plainHTTPFix(u); fix != "" {
						row.Fix = &notifyProblem{Line: row.Line, Fix: fix, FixShown: notify.Redact(fix)}
					}
				}
			default:
				row.Cause = upperFirst(deliveryCause(notify.Scrub(err.Error(), []string{u}), row.Label))
			}
		}(&rows[i], u)
	}
	wg.Wait()
	data := notifyTestData{Name: wc.Name, At: time.Now(), Rows: rows}
	for _, row := range rows {
		if row.OK {
			data.Sent++
		}
	}
	s.render(w, "notifytest.html", data)
}

// triggerFromForm reads the trigger fields of the detail form, the same way
// for Save and for Test. A field that doesn't parse keeps base's value and
// is reported.
func triggerFromForm(r *http.Request, base config.Trigger) (config.Trigger, []fieldError) {
	var errs []fieldError
	trig := config.Trigger{
		Type:    r.FormValue("ttype"),
		Pattern: r.FormValue("pattern"),
		Op:      r.FormValue("op"),
	}
	if v := strings.TrimSpace(r.FormValue("tthreshold")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err != nil {
			errs = append(errs, fieldError{"tthreshold", "Threshold must be a number."})
			trig.Threshold = base.Threshold
		} else {
			trig.Threshold = f
		}
	}
	if v := strings.TrimSpace(r.FormValue("confirm")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			errs = append(errs, fieldError{"confirm", "Confirm must be a whole number, 1 or more."})
			trig.Confirm = base.Confirm
		} else {
			trig.Confirm = n
		}
	}
	if v := strings.TrimSpace(r.FormValue("cooldown")); v != "" {
		if d, err := time.ParseDuration(v); err != nil {
			errs = append(errs, fieldError{"cooldown", fmt.Sprintf("Cooldown %q isn't a duration. %s", v, durationPhrase["cooldown"])})
			trig.Cooldown = base.Cooldown
		} else {
			trig.Cooldown = config.Duration(d)
		}
	}
	return trig, errs
}

// durationPhrase is the hint each duration field's parse error ends with,
// in the same words as the field's title attribute in detail.html.
var durationPhrase = map[string]string{
	"interval":     "Use a number with a unit, for example 5s, 1m30s or 2h.",
	"cooldown":     "Use for example 30s or 5m, or 0 for none.",
	"max_interval": "Use for example 10m, or leave it empty to turn it off.",
}

// formTLSInsecure is the Certificate checkbox's answer when the page had
// it (tls_shown), else the saved value: an unchecked box sends nothing,
// so its absence only means "off" on a page that showed it.
func formTLSInsecure(r *http.Request, saved bool) bool {
	if r.FormValue("tls_shown") == "" {
		return saved
	}
	return r.FormValue("tls_insecure") != ""
}

// parseWatchForm builds an updated copy of base from the detail form. It
// reads every field and reports every problem, in form order, rather than
// stopping at the first: the save handler re-renders the form with all of
// them marked. Fields that failed keep base's value in the returned watch.
func parseWatchForm(base config.Watch, r *http.Request) (config.Watch, []fieldError) {
	var errs []fieldError
	fail := func(field, msg string) { errs = append(errs, fieldError{field, msg}) }
	w := base

	if region, err := parseRegion(r); err != nil {
		fail("region", upperFirst(err.Error())+". Drag on the snapshot to draw it again.")
	} else {
		w.Region = region
	}
	// A client that omits the field (curl, a script, a stale page) keeps the
	// saved engine; the detail form always sends it. Resetting a sevenseg
	// watch to tesseract on a box without it would stop the watch and drop
	// the engine line from the file in one go.
	if _, ok := r.Form["engine"]; ok {
		w.Engine = r.FormValue("engine")
	} else {
		w.Engine = base.Engine
	}
	// The Engine select always submits a value, so a watch that never said
	// which engine would gain engine: tesseract in the file on its first
	// save. "" is how the default is spelled; "tesseract" is kept only
	// where the file already spelled it out.
	if w.Engine == "tesseract" && base.Engine != "tesseract" {
		w.Engine = ""
	}
	w.TLSInsecure = formTLSInsecure(r, base.TLSInsecure)
	// An empty numeric field means 0 (Validate applies the defaults), as
	// before; an unparseable one keeps base's value alongside its error.
	trig, trigErrs := triggerFromForm(r, base.Trigger)
	errs = append(errs, trigErrs...)
	if v := strings.TrimSpace(r.FormValue("interval")); v == "" {
		fail("interval", "Enter an interval. "+durationPhrase["interval"])
	} else if d, err := time.ParseDuration(v); err != nil {
		fail("interval", fmt.Sprintf("Interval %q isn't a duration. %s", v, durationPhrase["interval"]))
	} else {
		w.Interval = config.Duration(d)
	}
	w.MaxInterval = 0
	if v := strings.TrimSpace(r.FormValue("max_interval")); v != "" {
		if d, err := time.ParseDuration(v); err != nil {
			fail("max_interval", fmt.Sprintf("Max interval %q isn't a duration. %s", v, durationPhrase["max_interval"]))
			w.MaxInterval = base.MaxInterval
		} else {
			w.MaxInterval = config.Duration(d)
		}
	}
	w.HealthAfter = 0
	if v := strings.TrimSpace(r.FormValue("health_after")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			fail("health_after", "Down after must be a whole number of failed grabs.")
			w.HealthAfter = base.HealthAfter
		} else {
			w.HealthAfter = n
		}
	}
	if prep, err := parsePreprocess(r); err != nil {
		field := "pp_threshold"
		if strings.HasPrefix(err.Error(), "upscale") {
			field = "pp_upscale"
		}
		fail(field, upperFirst(err.Error())+".")
	} else {
		w.Preprocess = prep
	}
	w.Notify = notifyLines(r.FormValue("notify"))
	// Every line is checked before anything is written: a URL shoutrrr can't
	// use would be saved and then stop the watch at its restart.
	for _, p := range checkNotify(w.Notify) {
		fail("notify", fmt.Sprintf("Notify line %d: %s", p.Line, p.Reason))
	}
	w.Trigger = trig
	// The detail form hides Pattern/Compare/Threshold for the types that
	// never read them (app.js's updateTriggerFields mirrors trigger.New's
	// switch), but a hidden control still submits, so whatever was left in
	// it would be persisted unvalidated — config.Validate only compiles
	// Pattern for ocr_match/numeric — and, invisible in the UI, could never
	// be cleared, only to break a later hand edit of the type. Drop what
	// the type ignores.
	switch w.Trigger.Type {
	case "pixel_change":
		w.Trigger.Pattern, w.Trigger.Op = "", ""
	case "ocr_changed":
		w.Trigger.Pattern, w.Trigger.Op, w.Trigger.Threshold = "", "", 0
	case "ocr_match":
		w.Trigger.Op, w.Trigger.Threshold = "", 0
	}
	return w, errs
}

// watchFormValues is what the detail form's free-text fields held when it
// was submitted, shown back verbatim on a rejected save.
func watchFormValues(r *http.Request) map[string]string {
	v := map[string]string{}
	for _, k := range []string{"tthreshold", "confirm", "cooldown", "interval", "max_interval", "health_after"} {
		v[k] = r.FormValue(k)
	}
	return v
}

// statusFor maps a mutateConfig error to its HTTP status: config.Save I/O
// failures are server errors, everything else is caller error.
func statusFor(err error) int {
	if errors.Is(err, errSaveFailed) {
		return http.StatusInternalServerError
	}
	return http.StatusBadRequest
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
	// Shallow-copy the whole config so top-level fields (MQTT, and anything
	// added later) survive UI saves, then give Watches its own slice.
	nextVal := *s.cfg
	nextVal.Watches = append([]config.Watch(nil), s.cfg.Watches...)
	// MQTT is a pointer: without its own copy, next.Validate()'s in-place
	// defaulting (ClientID, BaseTopic, ...) would mutate the live config's
	// MQTT struct even when fn or Validate later rejects the mutation.
	if s.cfg.MQTT != nil {
		m := *s.cfg.MQTT
		nextVal.MQTT = &m
	}
	if s.cfg.Auth != nil {
		a := *s.cfg.Auth
		nextVal.Auth = &a
	}
	next := &nextVal
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

// notifyConfigChanged hands the current watch list to the OnConfigChanged
// hook, if set. The hook itself runs outside s.mu (released below before
// calling it), but NOT outside applyMu — the calling handler still holds
// it until its deferred Unlock fires on return — so the hook must not
// block; see the OnConfigChanged field doc.
func (s *Server) notifyConfigChanged() {
	if s.OnConfigChanged == nil {
		return
	}
	s.mu.Lock()
	watches := append([]config.Watch(nil), s.cfg.Watches...)
	s.mu.Unlock()
	s.OnConfigChanged(watches)
}

// errWatchVanished is a save racing a delete of the same watch.
var errWatchVanished = errors.New("watch vanished")

func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	base, ok := s.findWatch(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// renderError prefixes BasePath itself.
	backURL, backLabel := "/watch/"+url.PathEscape(name), "Back to "+name
	// A rejected save re-renders the form with what was submitted and every
	// problem marked next to its field (status 400); nothing is written.
	rejected := func(updated config.Watch, errs []fieldError) {
		data := s.detailFor(updated)
		data.Form = formState{Values: watchFormValues(r), Errors: errs, Notify: checkNotify(updated.Notify)}
		s.renderStatus(w, http.StatusBadRequest, "detail.html", data)
	}
	updated, ferrs := parseWatchForm(base, r)
	if len(ferrs) > 0 {
		rejected(updated, ferrs)
		return
	}
	if msg := s.blockedTrigger(base, updated); msg != "" {
		rejected(updated, []fieldError{{"ttype", msg}})
		return
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	err := s.mutateConfig(func(c *config.Config) error {
		for i := range c.Watches {
			if c.Watches[i].Name == name {
				c.Watches[i] = updated
				return nil
			}
		}
		return errWatchVanished
	})
	switch {
	case errors.Is(err, errSaveFailed):
		// Nothing is wrong with the input, so the form comes back exactly as
		// submitted with the failure above it (never history.back(), which
		// can't be trusted to restore typed text).
		data := s.detailFor(updated)
		kept := "the watch keeps running with its previous settings"
		if !s.isRunning(name) {
			kept = "the watch keeps its previous settings"
		}
		data.Form = formState{Values: watchFormValues(r), Failure: &writeFailure{
			Title:   "Couldn't save " + name,
			Message: "watchglass couldn't write " + s.configFile() + ", so nothing was saved and " + kept + ". Your changes are still below; make the file writable, then save again.",
			Detail:  err.Error(),
		}}
		s.renderStatus(w, http.StatusInternalServerError, "detail.html", data)
		return
	case errors.Is(err, errWatchVanished):
		s.renderError(w, http.StatusNotFound, errorPageData{
			Title:     "Couldn't save " + name,
			Message:   "This watch was deleted while you were editing it, so there was nothing to save.",
			BackURL:   "/",
			BackLabel: "All watches",
		})
		return
	case err != nil:
		rejected(updated, []fieldError{friendlyConfigError(err)})
		return
	}
	canonical, ok := s.findWatch(name)
	if !ok {
		s.renderError(w, http.StatusInternalServerError, errorPageData{
			Title:     "Couldn't save " + name,
			Message:   "The watch disappeared right after it was saved.",
			BackURL:   "/",
			BackLabel: "All watches",
		})
		return
	}
	restartErr := s.sup.Restart(s.RunCtx, canonical)
	// A delivery failure was about the URLs the watch had; new ones haven't
	// been tried, so "alerts failing" would now be a claim about the wrong
	// list. (Send test notification tries them without waiting for a fire.)
	// Cleared after Restart's Stop, so no report from the old run can land
	// after it, and before the failure return, since a stopped watch with a
	// new list must not keep the old list's claim either.
	if !slices.Equal(base.Notify, canonical.Notify) {
		s.reg.ClearDelivery(name)
	}
	if err := restartErr; err != nil {
		// must_fix 1 case 1: Restart calls Stop then Start, so a Start
		// failure here (e.g. the trigger type was switched to ocr_match/
		// numeric with no tesseract on PATH) leaves the watch fully stopped
		// even though config.yaml now holds the new config — s.isRunning
		// will correctly read false and the dashboard/detail page will show
		// it stopped, not a stale healthy LED. Say so explicitly here too.
		s.logf("ERROR: save %s: saved, but restart failed: %v — the watch is now stopped", name, err)
		s.renderError(w, http.StatusInternalServerError, errorPageData{
			Title:     "Saved, but the watch didn't restart",
			Message:   "The new settings are in " + s.configFile() + ", but " + name + " is stopped until you fix this and save again.",
			Reason:    friendlyStartError(err),
			Detail:    err.Error(),
			BackURL:   backURL,
			BackLabel: backLabel,
		})
		return
	}
	s.notifyConfigChanged()
	s.setFlash(w, "saved", time.Now().UTC().Format(time.RFC3339), "")
	http.Redirect(w, r, s.watchURL(name, ""), http.StatusSeeOther)
}

// blockedTrigger says why the submitted trigger can't run on this box: a
// text trigger type on an engine whose reader isn't installed. Writing
// that config would only stop the watch (Restart fails right after the
// save), so a CHANGE of type or engine into that state is refused before
// anything is written. A watch already saved in that state (a hand-edited
// file, or the engine gone since) still saves its other fields: its type
// is never disabled in the form, and the restart failure page says why.
func (s *Server) blockedTrigger(base, updated config.Watch) string {
	t := updated.Trigger.Type
	if t == "pixel_change" || (t == base.Trigger.Type && engineName(updated.Engine) == engineName(base.Engine)) {
		return ""
	}
	switch missingEngine(updated.Engine, s.engines.Tesseract != nil, s.engines.RapidOCR != nil) {
	case "tesseract":
		alt := "sevenseg"
		if s.engines.RapidOCR != nil {
			alt = "sevenseg or rapidocr"
		}
		return t + " reads text with tesseract, and tesseract isn't installed on this box, so saving this would stop the watch. Nothing was saved: switch Engine to " + alt + ", or install tesseract (" + ocr.TesseractInstall() + ") and restart watchglass."
	case "rapidocr":
		return t + " reads text with rapidocr, and no Python with the rapidocr package was found, so saving this would stop the watch. Nothing was saved: run pip install rapidocr onnxruntime and restart watchglass, or switch Engine."
	}
	return ""
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
		msg := "watchglass couldn't write " + s.configFile() + ", so " + name + " wasn't deleted and keeps running."
		if !errors.Is(err, errSaveFailed) {
			msg = "The rest of " + s.configFile() + " didn't validate, so " + name + " wasn't deleted and keeps running."
		}
		s.renderError(w, statusFor(err), errorPageData{
			Title:     "Couldn't delete " + name,
			Message:   msg,
			Detail:    err.Error(),
			BackURL:   "/",
			BackLabel: "All watches",
		})
		return
	}
	s.sup.Stop(name)
	s.reg.Drop(name)
	s.notifyConfigChanged()
	s.setFlash(w, "deleted", name, "")
	http.Redirect(w, r, s.BasePath+"/", http.StatusSeeOther)
}
