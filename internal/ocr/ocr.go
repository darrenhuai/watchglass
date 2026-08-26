// Package ocr defines the text-recognition interface and the default
// Tesseract implementation. Tesseract runs as a SUBPROCESS, never linked:
// this keeps CGO_ENABLED=0 and keeps the MIT license tree clean.
package ocr

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os/exec"
	"strconv"
	"strings"
)

// Engine turns an image into text.
type Engine interface {
	Recognize(ctx context.Context, img image.Image) (string, error)
}

type runFunc func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error)

type Tesseract struct {
	Bin string
	PSM int // page segmentation mode; 7 = single text line
	run runFunc
}

func NewTesseract() *Tesseract {
	return &Tesseract{Bin: "tesseract", PSM: 7, run: execRun}
}

func (t *Tesseract) Recognize(ctx context.Context, img image.Image) (string, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	out, err := t.run(ctx, t.Bin, buf.Bytes(), "stdin", "stdout", "--psm", strconv.Itoa(t.PSM))
	if err != nil {
		return "", fmt.Errorf("tesseract: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func execRun(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok && stderr.Len() > 0 {
			return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}
	return out, err
}

// Word is one recognized token with tesseract's 0-100 confidence.
type Word struct {
	Text string
	Conf float64
}

// DetailedEngine is an Engine that can also report per-word confidences.
// The web UI's "test this region" uses it when available.
type DetailedEngine interface {
	Engine
	RecognizeWords(ctx context.Context, img image.Image) (string, []Word, error)
}

// RecognizeWords runs tesseract in TSV mode and returns the joined text plus
// per-word confidences.
func (t *Tesseract) RecognizeWords(ctx context.Context, img image.Image) (string, []Word, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", nil, fmt.Errorf("encode: %w", err)
	}
	out, err := t.run(ctx, t.Bin, buf.Bytes(), "stdin", "stdout", "--psm", strconv.Itoa(t.PSM), "tsv")
	if err != nil {
		return "", nil, fmt.Errorf("tesseract: %w", err)
	}
	words := parseTSV(string(out))
	parts := make([]string, 0, len(words))
	for _, w := range words {
		parts = append(parts, w.Text)
	}
	return strings.Join(parts, " "), words, nil
}

// parseTSV extracts level-5 (word) rows from tesseract's TSV output.
func parseTSV(s string) []Word {
	var words []Word
	for i, line := range strings.Split(s, "\n") {
		if i == 0 {
			continue // header
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 12 || cols[0] != "5" {
			continue
		}
		text := strings.TrimSpace(cols[11])
		if text == "" {
			continue
		}
		conf, err := strconv.ParseFloat(cols[10], 64)
		if err != nil {
			continue
		}
		words = append(words, Word{Text: text, Conf: conf})
	}
	return words
}
