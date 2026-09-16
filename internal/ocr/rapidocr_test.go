package ocr

import (
	"context"
	"errors"
	"image"
	"reflect"
	"strings"
	"testing"
	"time"
)

const fixtureRapidJSON = `{"lines": [{"text": "PRINTER-01", "score": 0.99994}, {"text": "PRINTING", "score": 0.99995}, {"text": "79%", "score": 0.99676}]}`

func TestRapidOCRRecognizeRunsShimOnPNG(t *testing.T) {
	var gotBin string
	var gotArgs []string
	r := NewRapidOCR("/usr/bin/python3")
	r.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		gotBin = bin
		gotArgs = args
		if !strings.HasPrefix(string(stdin), "\x89PNG") {
			t.Error("expected PNG bytes on stdin")
		}
		return []byte(fixtureRapidJSON), nil
	}
	got, err := r.Recognize(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if got != "PRINTER-01 PRINTING 79%" {
		t.Errorf("got %q", got)
	}
	if gotBin != "/usr/bin/python3" {
		t.Errorf("bin = %q", gotBin)
	}
	want := []string{"-c", rapidShim}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %v, want [-c <shim>]", gotArgs)
	}
	if !strings.Contains(rapidShim, "from rapidocr import RapidOCR") {
		t.Errorf("embedded shim does not look like the rapidocr shim:\n%s", rapidShim)
	}
}

func TestRapidOCRRecognizeWordsOneWordPerLine(t *testing.T) {
	var gotArgs []string
	r := NewRapidOCR("python")
	r.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(fixtureRapidJSON), nil
	}
	text, words, err := r.RecognizeWords(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if err != nil {
		t.Fatalf("RecognizeWords: %v", err)
	}
	if text != "PRINTER-01 PRINTING 79%" {
		t.Errorf("text = %q", text)
	}
	if len(words) != 3 {
		t.Fatalf("words = %d, want 3", len(words))
	}
	wantWords := []Word{{Text: "PRINTER-01", Conf: 99.994}, {Text: "PRINTING", Conf: 99.995}, {Text: "79%", Conf: 99.676}}
	for i, w := range wantWords {
		if words[i].Text != w.Text || abs(words[i].Conf-w.Conf) > 1e-9 {
			t.Errorf("word %d = %+v, want %+v", i, words[i], w)
		}
	}
	if !reflect.DeepEqual(gotArgs, []string{"-c", rapidShim}) {
		t.Errorf("args = %v, want [-c <shim>]", gotArgs)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestRapidOCRRecognizeWordsSkipsEmptyText(t *testing.T) {
	r := NewRapidOCR("python")
	r.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		return []byte(`{"lines": [{"text": "", "score": 0.5}, {"text": " 42 ", "score": 0.9}]}`), nil
	}
	text, words, err := r.RecognizeWords(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if err != nil {
		t.Fatalf("RecognizeWords: %v", err)
	}
	if len(words) != 1 || words[0].Text != " 42 " {
		t.Errorf("words = %+v, want just the non-empty line", words)
	}
	if text != "42" {
		t.Errorf("text = %q, want trimmed join", text)
	}
}

func TestRapidOCREmptyResult(t *testing.T) {
	for _, out := range []string{`{"lines": []}`, `{"lines": null}`, `{"lines": []}` + "\n"} {
		r := NewRapidOCR("python")
		r.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
			return []byte(out), nil
		}
		text, err := r.Recognize(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
		if err != nil {
			t.Fatalf("Recognize(%s): %v", out, err)
		}
		if text != "" {
			t.Errorf("Recognize(%s) = %q, want empty", out, text)
		}
		text, words, err := r.RecognizeWords(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
		if err != nil {
			t.Fatalf("RecognizeWords(%s): %v", out, err)
		}
		if text != "" || len(words) != 0 {
			t.Errorf("RecognizeWords(%s) = %q, %v; want nothing", out, text, words)
		}
	}
}

func TestRapidOCRMalformedOutput(t *testing.T) {
	r := NewRapidOCR("python")
	r.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		return []byte("Python was not found; run without arguments to install"), nil
	}
	_, err := r.Recognize(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if err == nil {
		t.Fatal("expected an error for non-JSON output")
	}
	if !strings.HasPrefix(err.Error(), "rapidocr:") || !strings.Contains(err.Error(), "unexpected output") || !strings.Contains(err.Error(), "Python was not found") {
		t.Errorf("error = %q, want rapidocr: unexpected output: <bytes>", err)
	}
	if _, _, err := r.RecognizeWords(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8))); err == nil {
		t.Fatal("RecognizeWords: expected an error for non-JSON output")
	}
}

func TestParseRapidJSONTruncatesLongGarbage(t *testing.T) {
	long := strings.Repeat("x", 500)
	_, err := parseRapidJSON([]byte(long))
	if err == nil {
		t.Fatal("expected error")
	}
	if len(err.Error()) > 300 {
		t.Errorf("error should quote at most the first 200 bytes, got %d chars", len(err.Error()))
	}
}

func TestRapidOCRPropagatesRunError(t *testing.T) {
	boom := errors.New("exit status 3: rapidocr: No module named 'rapidocr'")
	r := NewRapidOCR("python")
	r.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		return nil, boom
	}
	_, err := r.Recognize(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if err == nil || !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "rapidocr:") {
		t.Errorf("Recognize error = %v, want rapidocr: wrapping the run error", err)
	}
	_, _, err = r.RecognizeWords(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if err == nil || !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "rapidocr:") {
		t.Errorf("RecognizeWords error = %v, want rapidocr: wrapping the run error", err)
	}
}

func TestRapidOCRIsDetailedEngine(t *testing.T) {
	var _ DetailedEngine = NewRapidOCR("python")
}

// fakeDetect builds look/run fakes: onPath maps a candidate to its resolved
// path (absent = not on PATH); probeErr maps a resolved path to the error
// its "import rapidocr, onnxruntime" probe returns (absent = probe OK).
func fakeDetect(onPath map[string]string, probeErr map[string]error) (func(string) (string, error), runFunc, *[]string) {
	var probed []string
	look := func(name string) (string, error) {
		if p, ok := onPath[name]; ok {
			return p, nil
		}
		return "", errors.New("executable file not found in %PATH%")
	}
	run := func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		probed = append(probed, bin)
		if len(stdin) != 0 {
			return nil, errors.New("probe must run with empty stdin")
		}
		if !reflect.DeepEqual(args, []string{"-c", "import rapidocr, onnxruntime"}) {
			return nil, errors.New("probe args wrong: " + strings.Join(args, " "))
		}
		if err, ok := probeErr[bin]; ok {
			return nil, err
		}
		return nil, nil
	}
	return look, run, &probed
}

func TestDetectRapidOCRFallsBackToPython(t *testing.T) {
	look, run, probed := fakeDetect(map[string]string{"python": `C:\Python\python.exe`}, nil)
	r, err := detectRapidOCR(context.Background(), []string{"python3", "python"}, look, run)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if r.Python != `C:\Python\python.exe` {
		t.Errorf("Python = %q, want the resolved python path", r.Python)
	}
	if !reflect.DeepEqual(*probed, []string{`C:\Python\python.exe`}) {
		t.Errorf("probed %v; python3 is not on PATH and must not be run", *probed)
	}
	if r.run == nil {
		t.Error("detected engine has no run func")
	}
}

func TestDetectRapidOCRSkipsStoreStub(t *testing.T) {
	look, run, probed := fakeDetect(
		map[string]string{"python3": `C:\WindowsApps\python3.exe`, "python": `C:\Python\python.exe`},
		map[string]error{`C:\WindowsApps\python3.exe`: errors.New("exit status 9009: Python was not found; run without arguments to install from the Microsoft Store")},
	)
	r, err := detectRapidOCR(context.Background(), []string{"python3", "python"}, look, run)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if r.Python != `C:\Python\python.exe` {
		t.Errorf("Python = %q, want python after the python3 stub failed its probe", r.Python)
	}
	if !reflect.DeepEqual(*probed, []string{`C:\WindowsApps\python3.exe`, `C:\Python\python.exe`}) {
		t.Errorf("probed %v; want python3 then python", *probed)
	}
}

func TestDetectRapidOCRFirstWorkingCandidateWins(t *testing.T) {
	look, run, probed := fakeDetect(map[string]string{"python3": "/usr/bin/python3", "python": "/usr/bin/python"}, nil)
	r, err := detectRapidOCR(context.Background(), []string{"python3", "python"}, look, run)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if r.Python != "/usr/bin/python3" {
		t.Errorf("Python = %q, want python3 (first working candidate)", r.Python)
	}
	if len(*probed) != 1 {
		t.Errorf("probed %v; must stop at the first working candidate", *probed)
	}
}

func TestDetectRapidOCROverrideOnly(t *testing.T) {
	look, run, probed := fakeDetect(map[string]string{"/opt/venv/bin/python": "/opt/venv/bin/python", "python3": "/usr/bin/python3"}, nil)
	r, err := detectRapidOCR(context.Background(), []string{"/opt/venv/bin/python"}, look, run)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if r.Python != "/opt/venv/bin/python" {
		t.Errorf("Python = %q, want the override", r.Python)
	}
	if !reflect.DeepEqual(*probed, []string{"/opt/venv/bin/python"}) {
		t.Errorf("probed %v; only the override may be tried", *probed)
	}
	// An override that fails is an error even when python3 would have worked.
	look, run, _ = fakeDetect(map[string]string{"python3": "/usr/bin/python3"}, nil)
	if r, err := detectRapidOCR(context.Background(), []string{"/nope/python"}, look, run); err == nil {
		t.Errorf("detect with a bad override = %+v, want an error", r)
	} else if !strings.Contains(err.Error(), "/nope/python") {
		t.Errorf("error %q should name the override", err)
	}
}

func TestDetectRapidOCRNothingWorks(t *testing.T) {
	look, run, _ := fakeDetect(
		map[string]string{"python": "/usr/bin/python"},
		map[string]error{"/usr/bin/python": errors.New("exit status 1: Traceback (most recent call last):\n  File \"<string>\", line 1, in <module>\nModuleNotFoundError: No module named 'onnxruntime'")},
	)
	r, err := detectRapidOCR(context.Background(), []string{"python3", "python"}, look, run)
	if err == nil {
		t.Fatalf("detect = %+v, want an error", r)
	}
	msg := err.Error()
	// The boot log gets the exception line of the traceback, not its header:
	// "Traceback (most recent call last):" says nothing about what is missing.
	for _, want := range []string{"python3", "python", "not on PATH", "probe failed: exit status 1: ModuleNotFoundError: No module named 'onnxruntime'"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should mention %q", msg, want)
		}
	}
	for _, junk := range []string{"Traceback", "File \"<string>\"", "\n"} {
		if strings.Contains(msg, junk) {
			t.Errorf("error %q should condense the traceback to its last line, found %q", msg, junk)
		}
	}
}

func TestProbeReason(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// The Windows Store stub: one line, quoted as is.
		{"exit status 9009: Python was not found; run without arguments to install from the Microsoft Store", "exit status 9009: Python was not found; run without arguments to install from the Microsoft Store"},
		// A non-traceback multi-line stderr keeps its first line.
		{"exit status 1: first line\nsecond line", "exit status 1: first line"},
		// A traceback is condensed to the exception line, exit status kept.
		{"exit status 1: Traceback (most recent call last):\n  File \"<string>\", line 1, in <module>\n    import rapidocr, onnxruntime\nModuleNotFoundError: No module named 'rapidocr'\n", "exit status 1: ModuleNotFoundError: No module named 'rapidocr'"},
		// A header with nothing after it falls back to the header.
		{"exit status 1: Traceback (most recent call last):", "exit status 1: Traceback (most recent call last):"},
		{"exit status 1", "exit status 1"},
	} {
		if got := probeReason(errors.New(c.in)); got != c.want {
			t.Errorf("probeReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// An explicit interpreter path (-python) that isn't there is "not found",
// not "not on PATH": nothing was looked up on PATH.
func TestDetectRapidOCRExplicitPathNotFound(t *testing.T) {
	look, run, probed := fakeDetect(map[string]string{"python3": "/usr/bin/python3"}, nil)
	for _, cand := range []string{"/opt/venv/bin/python", `C:\venv\Scripts\python.exe`} {
		_, err := detectRapidOCR(context.Background(), []string{cand}, look, run)
		if err == nil {
			t.Fatalf("detect(%q) succeeded, want an error", cand)
		}
		if !strings.Contains(err.Error(), cand+": not found") || strings.Contains(err.Error(), "not on PATH") {
			t.Errorf("detect(%q) = %q, want %q", cand, err, cand+": not found")
		}
	}
	if len(*probed) != 0 {
		t.Errorf("probed %v; a missing path must not be run", *probed)
	}
	// A bare name keeps the PATH wording.
	if _, err := detectRapidOCR(context.Background(), []string{"pypy3"}, look, run); err == nil || !strings.Contains(err.Error(), "pypy3: not on PATH") {
		t.Errorf("detect(pypy3) = %v, want 'pypy3: not on PATH'", err)
	}
}

// A probe killed by the caller's deadline is reported as a timeout, not as
// a crash, and the remaining candidates are not tried against a dead
// context.
func TestDetectRapidOCRProbeTimeout(t *testing.T) {
	look, _, _ := fakeDetect(map[string]string{"python3": "/usr/bin/python3", "python": "/usr/bin/python"}, nil)
	var probed []string
	run := func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		probed = append(probed, bin)
		if ctx.Err() != nil {
			return nil, errors.New("exit status 1") // what exec reports for a killed process on Windows
		}
		return nil, nil
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	r, err := detectRapidOCR(ctx, []string{"python3", "python"}, look, run)
	if err == nil {
		t.Fatalf("detect = %+v, want an error", r)
	}
	msg := err.Error()
	if !strings.Contains(msg, "python3: probe timed out (context deadline exceeded)") {
		t.Errorf("error %q should say the probe timed out", msg)
	}
	if !reflect.DeepEqual(probed, []string{"/usr/bin/python3"}) {
		t.Errorf("probed %v; must stop at the first timed-out candidate", probed)
	}
	if strings.Contains(msg, "probe failed") {
		t.Errorf("error %q must not read as a crash", msg)
	}
}

// A read killed by its context says so; "exit status 1" alone would read
// as an interpreter crash.
func TestRapidOCRRecognizeTimeout(t *testing.T) {
	r := NewRapidOCR("python")
	r.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	_, err := r.Recognize(ctx, img)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "rapidocr: context deadline exceeded") || !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("Recognize error = %v, want rapidocr: context deadline exceeded (exit status 1)", err)
	}
	_, _, err = r.RecognizeWords(ctx, img)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "rapidocr: context deadline exceeded") {
		t.Errorf("RecognizeWords error = %v, want the same wrapping", err)
	}
	// A run error under a live context is still just the run error.
	_, err = r.Recognize(context.Background(), img)
	if err == nil || err.Error() != "rapidocr: exit status 1" {
		t.Errorf("Recognize error = %v, want rapidocr: exit status 1", err)
	}
}

// Integration test: only runs when a Python with rapidocr + onnxruntime is
// installed. A blank image must read as empty text without erroring.
func TestRapidOCRRealBinary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r, err := DetectRapidOCR(ctx, "")
	if err != nil {
		t.Skipf("rapidocr not installed: %v", err)
	}
	t.Logf("using %s", r.Python)
	got, err := r.Recognize(ctx, image.NewRGBA(image.Rect(0, 0, 100, 40)))
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if got != "" {
		t.Errorf("blank image read as %q, want empty", got)
	}
}
