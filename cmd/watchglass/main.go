package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/hass"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/supervisor"
	"github.com/darrenhuai/watchglass/internal/web"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file (created, with no watches, if it doesn't exist)")
	dbPath := flag.String("db", "watchglass.db", "path to sqlite history database")
	listen := flag.String("listen", "127.0.0.1:8080", "web UI listen address (localhost-only by default; add an auth: block to config.yaml before exposing it)")
	basePath := flag.String("base-path", "", "URL path prefix for links/redirects when running behind a reverse proxy that strips it (e.g. /watchglass); empty (default) leaves the UI unprefixed")
	python := flag.String("python", "", "Python interpreter for engine: rapidocr (default: first of python3, python on PATH that imports rapidocr)")
	demoMode := flag.Bool("demo", false, "try watchglass on two built-in fake cameras, with its own config in the temp dir that is reset on every start; -config and -db are not touched (also WATCHGLASS_DEMO=1)")
	healthcheck := flag.Bool("healthcheck", false, "check that the watchglass on -listen answers: exit 0 if it does, 1 if not (for container health checks)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("watchglass", appVersion())
		return
	}
	if *healthcheck {
		os.Exit(runHealthcheck(*listen))
	}

	// Double-clicked in Explorer: the console window is watchglass's own
	// and closes the moment it exits, so an error waits for Enter or
	// nobody would ever read it.
	explorer := launchedFromExplorer()
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "watchglass:", err)
		if explorer {
			waitForEnter()
		}
		os.Exit(1)
	}

	bp, err := normalizeBasePath(*basePath)
	if err != nil {
		fail(err)
	}
	o := options{
		configPath:  *configPath,
		dbPath:      *dbPath,
		listen:      *listen,
		basePath:    bp,
		python:      *python,
		demo:        *demoMode || envTrue("WATCHGLASS_DEMO"),
		openBrowser: explorer,
		tempDir:     os.TempDir(),
	}
	if o.demo {
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "config" || f.Name == "db" {
				log.Printf("demo: ignoring -%s %s; the demo keeps its own config and history", f.Name, f.Value)
			}
		})
	}
	if err := run(o); err != nil {
		fail(err)
	}
}

// normalizeBasePath validates and trims -base-path. Empty stays empty — the
// default, byte-identical to pre-BasePath behavior. A non-empty value must
// start with "/" (the actual mistake this guards against: a bare
// "watchglass" would silently produce relative links); one trailing slash
// is trimmed for convenience so "-base-path /watchglass/" and
// "-base-path /watchglass" behave the same.
func normalizeBasePath(bp string) (string, error) {
	if bp == "" {
		return "", nil
	}
	if !strings.HasPrefix(bp, "/") {
		return "", fmt.Errorf("-base-path %q must start with \"/\" (e.g. -base-path /watchglass)", bp)
	}
	return strings.TrimSuffix(bp, "/"), nil
}

func run(o options) error {
	// Bound first, before any watch starts and before -demo resets its
	// files: a port that is taken fails the start-up cleanly instead of
	// after a second copy of every watch has begun polling (and
	// notifying), or after a second -demo has wiped the first one's
	// history.
	ln, err := net.Listen("tcp", o.listen)
	if err != nil {
		// Double-clicked while the first copy is still running: send the
		// browser to that one rather than failing. Only if it really is
		// watchglass answering; 8080 is a popular port.
		if o.openBrowser && probeWatchglass(o.listen) == nil {
			url := readyURL(o.listen, nil)
			fmt.Printf("watchglass is already running at %s; opening it.\n", url)
			return openBrowser(url)
		}
		return listenError(o.listen, err)
	}
	defer ln.Close()

	var engines ocr.Engines
	demoDir := ""
	if o.demo {
		// demo-printer reads the text when tesseract is here, so the
		// engines are found before its config is written.
		engines = detectEngines(o.python)
		d, err := prepareDemo(o.tempDir, engines.Tesseract != nil)
		if err != nil {
			return err
		}
		defer d.unlock()
		demoDir, o.configPath, o.dbPath = d.dir, d.cfgPath, d.dbPath
		log.Printf("demo: two fake cameras; config and history in %s, reset on every start", d.dir)
		if engines.Tesseract == nil {
			log.Printf("demo: tesseract isn't installed, so demo-printer watches for the pixels changing instead of reading PRINT COMPLETE")
		}
	} else {
		created, err := ensureConfig(o.configPath)
		if err != nil {
			return err
		}
		if created {
			abs, _ := filepath.Abs(o.configPath)
			log.Printf("created %s (empty; add watches in the web UI)", abs)
		}
	}

	cfg, err := config.Load(o.configPath)
	if err != nil {
		return permissionHint(err)
	}

	store, err := history.Open(o.dbPath)
	if err != nil {
		return fmt.Errorf("open history db: %w", permissionHint(err))
	}
	defer store.Close()

	var pruneDone chan struct{}
	// Defer join before closing store: ctx.Done() → pruneDone → store.Close() (LIFO).
	defer func() {
		if pruneDone != nil {
			<-pruneDone
		}
	}()

	if !o.demo {
		engines = detectEngines(o.python)
	}
	if err := checkEngines(cfg.Watches, engines); err != nil {
		return err
	}

	needsFFmpegWatch := ""
	for _, w := range cfg.Watches {
		kind, err := config.SourceKind(w.Source)
		if err != nil {
			return err
		}
		if kind == "ffmpeg" && needsFFmpegWatch == "" {
			needsFFmpegWatch = w.Name
		}
	}
	if needsFFmpegWatch != "" {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			return fmt.Errorf("watch %q uses an rtsp/device source but ffmpeg is not on PATH; "+
				"install it (e.g. apt install ffmpeg / choco install ffmpeg)", needsFFmpegWatch)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := state.New(10)
	var pub *hass.Publisher
	// Registered before StopAll's defer so LIFO runs StopAll first, then
	// this: watches drain their last publishes, then MQTT says offline.
	defer func() {
		if pub != nil {
			pub.Close()
		}
	}()
	sup := supervisor.New(store, reg, engines, log.Printf)
	defer sup.StopAll()

	// ws is declared here, before Connect, and assigned only after web.New
	// runs below — the onReady closure captures it by reference (not by
	// value) so it always reads whatever ws currently holds when it fires,
	// never a value frozen at closure-creation time.
	var ws *web.Server
	if cfg.MQTT != nil {
		// Bug 4: the initial discovery sync used to fire right after
		// Connect() returned, which races paho's async handshake — with
		// SetConnectRetry(true), IsConnected() can read true while still
		// mid-CONNECT, and publishes made in that window get destroyed by
		// paho's own CleanSession reset once the real connect lands, so
		// the first sync's discovery configs silently vanish. Fixed by
		// running the sync from the onReady callback instead, which
		// BuildOptions invokes only on a real established connection —
		// see internal/hass/client.go for the full mechanism.
		//
		// Bug (c3ad308 regression): onReady fires on every reconnect, not
		// just the first connect — that part is correct and intentional
		// (see BuildOptions's doc comment). What was wrong is that it used
		// to resync a `watches := cfg.Watches` slice captured ONCE here at
		// boot. hass.Sync diffs against its own previous in-memory state,
		// so a reconnect resync replaying that stale boot list would
		// resurrect deleted watches' retained HA discovery configs and wipe
		// the discovery/state topics of any watch added or renamed after
		// boot — indistinguishable from actually deleting it. Fixed by
		// reading the live list via ws.Watches() at fire time instead of
		// closing over a snapshot.
		//
		// ws is assigned after Connect (web.New needs sup/reg, built below),
		// so the nil check on ws guards a real gap this time, not just a
		// theoretical one — but the connect handshake still takes at least
		// milliseconds, so in practice onReady fires well after ws is set;
		// the guard just makes that ordering safe instead of assumed, and
		// turns the theoretical early-fire case into a logged no-op rather
		// than a nil-pointer panic.
		mc, err := hass.Connect(*cfg.MQTT, log.Printf, func() {
			if pub != nil && ws != nil {
				pub.SyncAsync(ws.Watches())
			}
		})
		if err != nil {
			return fmt.Errorf("mqtt: %w", err)
		}
		pub = hass.NewPublisher(mc, *cfg.MQTT, log.Printf)
		sup.OnEvent = pub.OnEvent
		sup.OnHealth = pub.OnHealth
		log.Printf("mqtt: publishing to %s (discovery prefix %s)", cfg.MQTT.Broker, cfg.MQTT.DiscoveryPrefix)
	}

	if cfg.HistoryDays > 0 {
		retention := time.Duration(cfg.HistoryDays) * 24 * time.Hour
		prune := func() {
			n, err := store.Prune(time.Now().Add(-retention))
			if err != nil {
				log.Printf("history: prune: %v", err)
				return
			}
			if n > 0 {
				log.Printf("history: pruned %d readings older than %d days", n, cfg.HistoryDays)
			}
		}
		prune()
		pruneDone = make(chan struct{})
		go func() {
			defer close(pruneDone)
			t := time.NewTicker(24 * time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					prune()
				}
			}
		}()
	}

	startWatches(ctx, sup, cfg.Watches, log.Printf)

	ws, err = web.New(o.configPath, cfg, sup, reg, engines, log.Printf)
	if err != nil {
		return err
	}
	ws.RunCtx = ctx
	ws.BasePath = o.basePath
	ws.DemoDir = demoDir
	if pub != nil {
		// UI-triggered syncs and on-connect resyncs both now read the live
		// watch list (UI saves via the config lock directly; on-connect via
		// ws.Watches(), see the onReady wiring above) and funnel through the
		// same SyncAsync queue, so a UI save racing a reconnect can no longer
		// desync one from the other — whichever lands second just republishes
		// the same current state the first one did. A sync that happens to
		// land mid-reconnect is therefore genuinely harmless, not just
		// assumed so: it heals, it never regresses.
		ws.OnConfigChanged = pub.SyncAsync
	}

	if cfg.Auth == nil && !isLoopback(o.listen) {
		// SUPERVISOR_TOKEN: the Home Assistant add-on runs this same image
		// but publishes 8080 on every interface of the host, so there the
		// WARNING is true and stays.
		if envTrue("WATCHGLASS_IN_CONTAINER") && os.Getenv("SUPERVISOR_TOKEN") == "" {
			// The image listens on 0.0.0.0 on purpose: inside a container
			// that is the only way to be reachable at all, and the port
			// mapping (127.0.0.1:8080:8080 in the compose file) decides who
			// can. Every start said WARNING about it, and it isn't one.
			log.Printf("listening on %s inside the container; the port mapping decides who can reach it (add an auth: block to config.yaml before publishing it beyond localhost)", o.listen)
		} else {
			log.Printf("WARNING: web UI is listening on %s with NO authentication — anyone who can reach it "+
				"controls your watches and sees your streams; add an auth: block or bind to localhost", o.listen)
		}
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shCtx)
	}()
	url := readyURL(o.listen, ln.Addr())
	if o.basePath == "" {
		log.Printf("watchglass %s ready: open %s", appVersion(), url)
	} else {
		// Behind a proxy that strips the prefix, every link on the page
		// starts with basePath, which only resolves through the proxy.
		log.Printf("watchglass %s ready on %s (links start with %s, so open it through your reverse proxy)", appVersion(), url, o.basePath)
	}
	if o.openBrowser {
		if err := openBrowser(url); err != nil {
			log.Printf("couldn't open a browser (%v); open %s yourself", err, url)
		}
		fmt.Println("watchglass is running. Close this window to stop it.")
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// checkEngines is the boot-time refusal for a config that can't run: every
// OCR watch must resolve its engine — tesseract on PATH, a Python with
// rapidocr, or the built-in seven-segment decoder — before anything
// starts, so a missing binary is
// one clear error at launch rather than N watches failing in the log.
// pixel_change watches never read an engine and are skipped.
func checkEngines(watches []config.Watch, engines ocr.Engines) error {
	for _, w := range watches {
		if w.Trigger.Type == "pixel_change" {
			continue
		}
		if _, err := engines.For(w.Engine); err != nil {
			return fmt.Errorf("watch %q %w", w.Name, err)
		}
	}
	return nil
}

// startWatches starts every configured watch, logging and continuing past
// any that fail to start rather than aborting: config.Validate (called by
// Load, above) rejects everything it knows how to check, but a Start
// failure is still possible for anything Validate can't see from the
// static config alone. Bug 1: this loop used to return the first Start
// error, which made run() (and so main) exit non-zero — fine on a normal
// launch, but fatal at boot under a restart-always supervisor
// (docker restart: unless-stopped): the very next start hits the exact
// same failing watch and exits again, crash-looping the whole daemon
// (every OTHER watch included) until someone hand-edits config.yaml. Since
// the web UI is what lets a user fix a bad watch's config in the first
// place, it must come up regardless of any individual watch's fate.
func startWatches(ctx context.Context, sup *supervisor.Supervisor, watches []config.Watch, logf func(string, ...any)) {
	for _, w := range watches {
		if err := sup.Start(ctx, w); err != nil {
			logf("ERROR: watch %q failed to start: %v (fix its config in the web UI or config.yaml)", w.Name, err)
			continue
		}
		logf("watching %q (%s every %v)", w.Name, w.Trigger.Type, time.Duration(w.Interval))
	}
}
