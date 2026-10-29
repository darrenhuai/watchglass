package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Every read runs the shim with --no-cls, and the shim reads that very
// flag: rapidocr's text-direction check turns seven-segment digits upside
// down ('12:05' read as '50:21' at 99%).
func TestRapidOCRRunsShimWithClsOff(t *testing.T) {
	var gotArgs []string
	r := NewRapidOCR("python")
	r.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"lines": [{"text": "12:05", "score": 0.99}], "cls": "off"}`), nil
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	if _, err := r.Recognize(context.Background(), img); err != nil {
		t.Fatal(err)
	}
	if want := []string{"-c", rapidShim, "--no-cls"}; !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("Recognize ran -c <shim> %q, want -c <shim> --no-cls", shimFlags(gotArgs))
	}
	gotArgs = nil
	if _, _, err := r.RecognizeWords(context.Background(), img); err != nil {
		t.Fatal(err)
	}
	if want := []string{"-c", rapidShim, "--no-cls"}; !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("RecognizeWords ran -c <shim> %q, want -c <shim> --no-cls", shimFlags(gotArgs))
	}
	if !strings.Contains(rapidShim, `"`+rapidNoCls+`"`) {
		t.Errorf("the shim doesn't look for %q in its arguments", rapidNoCls)
	}
}

// shimFlags is what follows "-c <shim>" in args, for a readable failure.
func shimFlags(args []string) []string {
	if len(args) < 2 || args[0] != "-c" || args[1] != rapidShim {
		return args
	}
	return args[2:]
}

// A rapidocr that can't turn the check off is read anyway, and the log
// says so once, not on every read.
func TestRapidOCRLogsOnceWhenClsStaysOn(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	cls := "default"
	r := NewRapidOCR("/opt/py/bin/python")
	r.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		return []byte(`{"lines": [{"text": "50:21", "score": 0.99}], "cls": "` + cls + `"}`), nil
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < 3; i++ {
		if got, err := r.Recognize(context.Background(), img); err != nil || got != "50:21" {
			t.Fatalf("Recognize = %q, %v; the read still counts", got, err)
		}
	}
	if n := strings.Count(logged.String(), "text-direction check"); n != 1 {
		t.Errorf("logged %d times, want once:\n%s", n, logged.String())
	}
	if !strings.Contains(logged.String(), "/opt/py/bin/python") || !strings.Contains(logged.String(), "pip install -U rapidocr") {
		t.Errorf("log line should name the interpreter and the fix:\n%s", logged.String())
	}

	logged.Reset()
	for _, cls = range []string{"off", ""} { // "" is an older shim's output
		r = NewRapidOCR("python")
		r.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
			return []byte(`{"lines": [], "cls": "` + cls + `"}`), nil
		}
		if _, err := r.Recognize(context.Background(), img); err != nil {
			t.Fatal(err)
		}
	}
	if logged.Len() != 0 {
		t.Errorf("logged with the check off:\n%s", logged.String())
	}
}

// testPython finds a working Python for the shim tests: these run the real
// shim against stand-in rapidocr packages, so they need Python but not
// rapidocr. On Windows python3 is often the Store stub, which fails.
func testPython(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if exec.Command(p, "-c", "import sys; sys.exit(0 if sys.version_info >= (3, 6) else 1)").Run() == nil {
			return p
		}
	}
	t.Skip("no Python 3 on PATH")
	return ""
}

// fakeRapidRun is execRun with PYTHONPATH pointed at one of the stand-in
// rapidocr packages under testdata/rapidfake, so the real shim imports it
// instead of any installed rapidocr. It also returns the shim's stderr.
func fakeRapidRun(t *testing.T, variant string, stderr *bytes.Buffer) runFunc {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", "rapidfake", variant))
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(os.Environ(), "PYTHONPATH="+dir, "PYTHONDONTWRITEBYTECODE=1", "PYTHONNOUSERSITE=1", "PYTHONIOENCODING=utf-8")
		cmd.Stdin = bytes.NewReader(stdin)
		cmd.Stderr = stderr
		out, err := cmd.Output()
		if err != nil {
			return out, &shimError{err: err, stderr: stderr.String()}
		}
		return out, nil
	}
}

type shimError struct {
	err    error
	stderr string
}

func (e *shimError) Error() string { return e.err.Error() + ": " + strings.TrimSpace(e.stderr) }

// The real shim, run the way Recognize runs it, against each shape of
// rapidocr: the check is off whichever way that version offers, and a
// version with no way at all still reads, says so on stderr and reports
// "default" so watchglass can log it.
func TestRapidShimTurnsClsOffOnEveryRapidOCR(t *testing.T) {
	py := testPython(t)
	log.SetOutput(io.Discard) // nooption's Recognize logs; TestRapidOCRLogsOnceWhenClsStaysOn checks that
	defer log.SetOutput(os.Stderr)
	for _, c := range []struct {
		variant, text, cls string
		warns              bool
	}{
		// Today's rapidocr: the call's keyword.
		{"current", "call use_cls=False config use_cls=None", "off", false},
		// No keyword on the call: the config key instead.
		{"nokeyword", "call config use_cls=False", "off", false},
		{"unknownparam", "call config use_cls=False", "off", false},
		// Neither: read with the check on, and say so. The engine built
		// for the keyword call does that read too: each real one loads
		// the models, which takes seconds.
		{"nooption", "call defaults, engines built 1", "default", true},
	} {
		t.Run(c.variant, func(t *testing.T) {
			var stderr bytes.Buffer
			r := NewRapidOCR(py)
			r.run = fakeRapidRun(t, c.variant, &stderr)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := r.run(ctx, r.Python, []byte("\x89PNG"), rapidArgs()...)
			if err != nil {
				t.Fatalf("shim: %v", err)
			}
			var doc struct {
				Lines []rapidLine `json:"lines"`
				Cls   string      `json:"cls"`
			}
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("shim output %q: %v", out, err)
			}
			if len(doc.Lines) != 1 || doc.Lines[0].Text != c.text {
				t.Errorf("lines = %+v, want the stand-in to report %q", doc.Lines, c.text)
			}
			if doc.Cls != c.cls {
				t.Errorf("cls = %q, want %q", doc.Cls, c.cls)
			}
			if warned := strings.Contains(stderr.String(), "can't turn its text-direction check off"); warned != c.warns {
				t.Errorf("stderr = %q, want a warning: %v", stderr.String(), c.warns)
			}
			// And through Recognize, the way a watch reads.
			got, err := r.Recognize(ctx, image.NewRGBA(image.Rect(0, 0, 8, 8)))
			if err != nil || got != c.text {
				t.Errorf("Recognize = %q, %v; want %q", got, err, c.text)
			}
		})
	}
}

// Without the flag the shim leaves rapidocr's own default alone, so it is
// the flag watchglass passes that turns the check off.
func TestRapidShimWithoutFlagKeepsDefault(t *testing.T) {
	py := testPython(t)
	var stderr bytes.Buffer
	run := fakeRapidRun(t, "current", &stderr)
	out, err := run(context.Background(), py, []byte("\x89PNG"), "-c", rapidShim)
	if err != nil {
		t.Fatalf("shim: %v", err)
	}
	lines, cls, err := parseRapidJSON(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Text != "call use_cls=None config use_cls=None" || cls != "default" {
		t.Errorf("lines = %+v, cls = %q; want rapidocr's defaults untouched", lines, cls)
	}
}

// An error from the read itself is reported, not taken for a missing
// keyword and retried with the check on.
func TestRapidShimReportsOtherErrors(t *testing.T) {
	py := testPython(t)
	var stderr bytes.Buffer
	r := NewRapidOCR(py)
	r.run = fakeRapidRun(t, "broken", &stderr)
	_, err := r.Recognize(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if err == nil {
		t.Fatal("Recognize succeeded, want the stand-in's error")
	}
	if !strings.Contains(err.Error(), "rapidocr: TypeError: cannot decode image of type bytes") {
		t.Errorf("error = %v, want the shim's one-line report of the TypeError", err)
	}
	if strings.Contains(stderr.String(), "can't turn its text-direction check off") {
		t.Errorf("stderr = %q: the shim retried a read that failed for another reason", stderr.String())
	}
}

// The same for the config attempt: an engine that fails to load with the
// check off in its config, for a reason other than the option, is a real
// failure. Taking it for an old rapidocr would read with the check on and
// log a misleading "update rapidocr" hint.
func TestRapidShimReportsConfigFailures(t *testing.T) {
	py := testPython(t)
	var stderr bytes.Buffer
	r := NewRapidOCR(py)
	r.run = fakeRapidRun(t, "brokenconfig", &stderr)
	got, err := r.Recognize(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if err == nil {
		t.Fatalf("Recognize = %q, want the stand-in's load error", got)
	}
	if !strings.Contains(err.Error(), "rapidocr: RuntimeError: onnxruntime: could not load model file rec.onnx") {
		t.Errorf("error = %v, want the shim's one-line report of the RuntimeError", err)
	}
	if strings.Contains(stderr.String(), "can't turn its text-direction check off") {
		t.Errorf("stderr = %q: the shim took a load failure for a missing option", stderr.String())
	}
}
