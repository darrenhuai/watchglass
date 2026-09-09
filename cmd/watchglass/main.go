package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
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
	configPath := flag.String("config", "config.yaml", "path to config file")
	dbPath := flag.String("db", "watchglass.db", "path to sqlite history database")
	listen := flag.String("listen", "127.0.0.1:8080", "web UI listen address (localhost-only by default; no auth yet)")
	basePath := flag.String("base-path", "", "URL path prefix for links/redirects when running behind a reverse proxy that strips it (e.g. /watchglass); empty (default) leaves the UI unprefixed")
	flag.Parse()

	bp, err := normalizeBasePath(*basePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "watchglass:", err)
		os.Exit(1)
	}

	if err := run(*configPath, *dbPath, *listen, bp); err != nil {
		fmt.Fprintln(os.Stderr, "watchglass:", err)
		os.Exit(1)
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

func run(configPath, dbPath, listen, basePath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	store, err := history.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open history db: %w", err)
	}
	defer store.Close()

	var pruneDone chan struct{}
	// Defer join before closing store: ctx.Done() → pruneDone → store.Close() (LIFO).
	defer func() {
		if pruneDone != nil {
			<-pruneDone
		}
	}()

	var engine ocr.Engine
	if _, err := exec.LookPath("tesseract"); err == nil {
		engine = ocr.NewTesseract()
	}
	for _, w := range cfg.Watches {
		if w.Trigger.Type != "pixel_change" && engine == nil {
			return fmt.Errorf("watch %q needs OCR but tesseract is not on PATH; "+
				"install it (e.g. apt install tesseract-ocr / choco install tesseract)", w.Name)
		}
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
	sup := supervisor.New(store, reg, engine, log.Printf)
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

	ws, err = web.New(configPath, cfg, sup, reg, engine, log.Printf)
	if err != nil {
		return err
	}
	ws.RunCtx = ctx
	ws.BasePath = basePath
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

	if cfg.Auth == nil && !strings.HasPrefix(listen, "127.0.0.1") && !strings.HasPrefix(listen, "localhost") &&
		!strings.HasPrefix(listen, "[::1]") {
		log.Printf("WARNING: web UI is listening on %s with NO authentication — anyone who can reach it "+
			"controls your watches and sees your streams; add an auth: block or bind to localhost", listen)
	}

	srv := &http.Server{Addr: listen, Handler: ws.Handler()}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shCtx)
	}()
	log.Printf("web UI on http://%s", listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
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
