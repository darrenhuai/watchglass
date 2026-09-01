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
	"syscall"
	"time"

	"watchglass/internal/config"
	"watchglass/internal/history"
	"watchglass/internal/ocr"
	"watchglass/internal/state"
	"watchglass/internal/supervisor"
	"watchglass/internal/web"
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

	needsFFmpeg := false
	for _, w := range cfg.Watches {
		kind, err := config.SourceKind(w.Source)
		if err != nil {
			return err
		}
		if kind == "ffmpeg" {
			needsFFmpeg = true
		}
	}
	if needsFFmpeg {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			return fmt.Errorf("a watch uses an rtsp/device source but ffmpeg is not on PATH; " +
				"install it (e.g. apt install ffmpeg / choco install ffmpeg)")
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := state.New(10)
	sup := supervisor.New(store, reg, engine, log.Printf)
	defer sup.StopAll()
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
