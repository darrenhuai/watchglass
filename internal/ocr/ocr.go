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
	return cmd.Output()
}
