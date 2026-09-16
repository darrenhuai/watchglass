package ocr

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os/exec"
	"strings"
)

// rapidShim is the Python program watchglass runs for every rapidocr read:
// PNG bytes on stdin, {"lines": [{"text", "score"}, ...]} on stdout. The
// rapidocr CLI prints a Python repr with a numpy dump, so the interpreter is
// run with this shim via `python -c` instead. Embedded so the static binary
// stays the only file to ship.
//
//go:embed rapid_shim.py
var rapidShim string

// ErrNoRapidOCR is returned by Engines.For when a watch wants rapidocr and
// no Python that imports it was found at boot.
var ErrNoRapidOCR = errors.New("needs the rapidocr engine but no Python with the rapidocr package was found on PATH; " +
	"pip install rapidocr onnxruntime (looked for python3, python; or pass -python /path/to/python)")

// RapidOCR reads printed text with the PaddleOCR PP-OCR mobile models via
// the RapidOCR Python package. Like tesseract it is a one-shot SUBPROCESS,
// never linked: each read spawns Python with rapidShim, which keeps
// CGO_ENABLED=0 and the static binary. Python is the interpreter path or
// name (DetectRapidOCR fills in the resolved path).
type RapidOCR struct {
	Python string
	run    runFunc
}

var _ DetailedEngine = (*RapidOCR)(nil)

func NewRapidOCR(python string) *RapidOCR {
	return &RapidOCR{Python: python, run: execRun}
}

// rapidLine is one text line the shim reports, score in 0-1.
type rapidLine struct {
	Text  string  `json:"text"`
	Score float64 `json:"score"`
}

func (r *RapidOCR) Recognize(ctx context.Context, img image.Image) (string, error) {
	lines, err := r.read(ctx, img)
	if err != nil {
		return "", err
	}
	return joinRapidLines(lines), nil
}

// RecognizeWords returns one Word per recognized line (rapidocr scores
// lines, not words) with its confidence scaled to tesseract's 0-100.
func (r *RapidOCR) RecognizeWords(ctx context.Context, img image.Image) (string, []Word, error) {
	lines, err := r.read(ctx, img)
	if err != nil {
		return "", nil, err
	}
	var words []Word
	for _, l := range lines {
		if l.Text == "" {
			continue
		}
		words = append(words, Word{Text: l.Text, Conf: l.Score * 100})
	}
	return joinRapidLines(lines), words, nil
}

func (r *RapidOCR) read(ctx context.Context, img image.Image) ([]rapidLine, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("rapidocr: encode: %w", err)
	}
	out, err := r.run(ctx, r.Python, buf.Bytes(), "-c", rapidShim)
	if err != nil {
		if ctx.Err() != nil {
			// The context killed the interpreter; "exit status 1" alone
			// would read as a crash.
			return nil, fmt.Errorf("rapidocr: %w (%v)", ctx.Err(), err)
		}
		return nil, fmt.Errorf("rapidocr: %w", err)
	}
	return parseRapidJSON(out)
}

func joinRapidLines(lines []rapidLine) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, l.Text)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// parseRapidJSON decodes the shim's {"lines": [...]} document. A missing or
// null list is an empty read; anything that is not that document (a
// traceback, the Store stub's "Python was not found") is an error quoting
// the first 200 bytes.
func parseRapidJSON(b []byte) ([]rapidLine, error) {
	var doc struct {
		Lines []rapidLine `json:"lines"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), &doc); err != nil {
		head := b
		if len(head) > 200 {
			head = head[:200]
		}
		return nil, fmt.Errorf("rapidocr: unexpected output: %s", head)
	}
	return doc.Lines, nil
}

// rapidProbe is what a candidate interpreter must run cleanly to count: the
// package alone is not enough, since pip install rapidocr does not pull in
// the inference runtime.
const rapidProbe = "import rapidocr, onnxruntime"

// DetectRapidOCR finds a Python that can run the rapidocr engine. With
// python set only that interpreter is tried; otherwise python3 then python
// (on Windows python3 is often the Microsoft Store stub, which is on PATH
// but exits non-zero, so being on PATH is not enough — each candidate has
// to prove the import). The caller applies the timeout.
func DetectRapidOCR(ctx context.Context, python string) (*RapidOCR, error) {
	candidates := []string{"python3", "python"}
	if python != "" {
		candidates = []string{python}
	}
	return detectRapidOCR(ctx, candidates, exec.LookPath, execRun)
}

func detectRapidOCR(ctx context.Context, candidates []string, look func(string) (string, error), run runFunc) (*RapidOCR, error) {
	var reasons []string
	for _, cand := range candidates {
		path, err := look(cand)
		if err != nil {
			// A bare name is looked up on PATH; an explicit path (the
			// -python flag) simply isn't there.
			if strings.ContainsAny(cand, `/\`) {
				reasons = append(reasons, fmt.Sprintf("%s: not found", cand))
			} else {
				reasons = append(reasons, fmt.Sprintf("%s: not on PATH", cand))
			}
			continue
		}
		if _, err := run(ctx, path, nil, "-c", rapidProbe); err != nil {
			if ctx.Err() != nil {
				// The caller's deadline killed the probe; the remaining
				// candidates would only be killed the same way.
				reasons = append(reasons, fmt.Sprintf("%s: probe timed out (%v)", cand, ctx.Err()))
				break
			}
			reasons = append(reasons, fmt.Sprintf("%s: probe failed: %s", cand, probeReason(err)))
			continue
		}
		r := NewRapidOCR(path)
		r.run = run
		return r, nil
	}
	return nil, fmt.Errorf("no Python with the rapidocr package (%s)", strings.Join(reasons, "; "))
}

// probeReason condenses a failed probe's error ("exit status N: <stderr>",
// see execRun) to one line for the boot log. A Python traceback opens with
// its header and ends with the exception, so for those the last non-empty
// line is quoted (ModuleNotFoundError: No module named 'rapidocr'); for
// anything else (the Store stub's one-liner) the first line is.
func probeReason(err error) string {
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	first := strings.TrimSpace(lines[0])
	i := strings.Index(first, "Traceback (most recent call last)")
	if i < 0 {
		return first
	}
	for j := len(lines) - 1; j > 0; j-- {
		if last := strings.TrimSpace(lines[j]); last != "" {
			return first[:i] + last
		}
	}
	return first
}
