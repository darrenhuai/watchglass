package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"watchglass/internal/config"
	"watchglass/internal/history"
	"watchglass/internal/notify"
	"watchglass/internal/ocr"
	"watchglass/internal/runner"
	"watchglass/internal/source"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	dbPath := flag.String("db", "watchglass.db", "path to sqlite history database")
	flag.Parse()

	if err := run(*configPath, *dbPath); err != nil {
		fmt.Fprintln(os.Stderr, "watchglass:", err)
		os.Exit(1)
	}
}

func run(configPath, dbPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if len(cfg.Watches) == 0 {
		return fmt.Errorf("no watches configured in %s", configPath)
	}

	store, err := history.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open history db: %w", err)
	}
	defer store.Close()

	needsOCR := false
	for _, w := range cfg.Watches {
		if w.Trigger.Type != "pixel_change" {
			needsOCR = true
		}
	}
	var engine ocr.Engine
	if needsOCR {
		if _, err := exec.LookPath("tesseract"); err != nil {
			return fmt.Errorf("OCR watches configured but tesseract is not on PATH; " +
				"install it (e.g. apt install tesseract-ocr / choco install tesseract)")
		}
		engine = ocr.NewTesseract()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	for _, w := range cfg.Watches {
		var notifier notify.Notifier
		if len(w.Notify) > 0 {
			n, err := notify.NewShoutrrr(w.Notify)
			if err != nil {
				return fmt.Errorf("watch %q: notify: %w", w.Name, err)
			}
			notifier = n
		}
		r, err := runner.New(w, source.NewHTTPSnapshot(w.Source), engine, notifier, store, log.Printf)
		if err != nil {
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Run(ctx)
		}()
		log.Printf("watching %q (%s every %v)", w.Name, w.Trigger.Type, time.Duration(w.Interval))
	}
	wg.Wait()
	return nil
}
