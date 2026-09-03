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
	flag.Parse()

	if err := run(*configPath, *dbPath, *listen); err != nil {
		fmt.Fprintln(os.Stderr, "watchglass:", err)
		os.Exit(1)
	}
}

func run(configPath, dbPath, listen string) error {
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

	if cfg.MQTT != nil {
		mc, err := hass.Connect(*cfg.MQTT, log.Printf)
		if err != nil {
			return fmt.Errorf("mqtt: %w", err)
		}
		pub = hass.NewPublisher(mc, *cfg.MQTT, log.Printf)
		pub.SyncAsync(cfg.Watches)
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

	for _, w := range cfg.Watches {
		if err := sup.Start(ctx, w); err != nil {
			return err
		}
		log.Printf("watching %q (%s every %v)", w.Name, w.Trigger.Type, time.Duration(w.Interval))
	}

	ws, err := web.New(configPath, cfg, sup, reg, engine, log.Printf)
	if err != nil {
		return err
	}
	ws.RunCtx = ctx
	if pub != nil {
		ws.OnConfigChanged = pub.SyncAsync
	}

	if cfg.Auth == nil && !strings.HasPrefix(listen, "127.0.0.1") && !strings.HasPrefix(listen, "localhost") {
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
