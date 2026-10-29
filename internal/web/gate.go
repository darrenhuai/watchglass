package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"image"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/runner"
)

// The gate on the pages (config.Gate, runner/gate.go): the form's Gate
// fieldset and the second box on the stage (gateFromForm), the gate's
// reading in Test (testGate), and what Live and the dashboard say about a
// gate that is shut (gateFor).

func init() {
	fieldAnchors["gate_region"] = "stage"
	fieldAnchors["gate_pattern"] = "f-gate-pattern"
	fieldAnchors["gate_engine"] = "f-gate-engine"
}

// gateFromForm reads the detail form's Gate fieldset: gate_on (the box),
// the gate's region (gx, gy, gw, gh), gate_pattern and gate_engine. A
// client that never showed the fieldset (gate_shown absent: curl, a page
// from before gates) keeps base's gate, and so does a pixel_change watch's
// form, which hides it. The gate's preprocess block has no fields: it is
// kept as the file has it. On a field that doesn't parse, the gate is
// still returned, with what was typed, for the form to show again.
func gateFromForm(r *http.Request, base config.Watch, ttype string) (*config.Gate, []fieldError) {
	if r.FormValue("gate_shown") == "" {
		return base.Gate, nil
	}
	if ttype == "pixel_change" || r.FormValue("gate_on") == "" {
		return nil, nil
	}
	var errs []fieldError
	g := &config.Gate{
		Pattern: r.FormValue("gate_pattern"),
		Engine:  r.FormValue("gate_engine"),
	}
	if base.Gate != nil {
		g.Preprocess = base.Gate.Preprocess
		g.Region = base.Gate.Region
	}
	if reg, err := parseGateRegion(r); err != nil {
		errs = append(errs, fieldError{"gate_region", "Gate box: " + err.Error() + " Draw it again on the snapshot."})
	} else {
		g.Region = reg
	}
	switch {
	case strings.TrimSpace(g.Pattern) == "":
		errs = append(errs, fieldError{"gate_pattern", "Gate pattern is empty. Enter what the code box shows when the value you want is on the display, for example ^1\\.8\\.0$."})
	default:
		if _, err := regexp.Compile(g.Pattern); err != nil {
			errs = append(errs, fieldError{"gate_pattern", "Gate pattern isn't a valid regular expression. " + regexpProblem(strings.TrimPrefix(err.Error(), "error parsing regexp: "))})
		}
	}
	switch g.Engine {
	case "", "tesseract", "sevenseg", "rapidocr":
	default:
		errs = append(errs, fieldError{"gate_engine", "Gate engine must be the value's, tesseract, sevenseg or rapidocr."})
		g.Engine = ""
	}
	return g, errs
}

// parseGateRegion is parseRegion for the gate's box (gx, gy, gw, gh).
func parseGateRegion(r *http.Request) (config.Region, error) {
	var v [4]float64
	for i, name := range []string{"gx", "gy", "gw", "gh"} {
		f, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue(name)), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return config.Region{}, errors.New("its position isn't a set of numbers between 0 and 1.")
		}
		v[i] = f
	}
	reg := config.Region{X: v[0], Y: v[1], W: v[2], H: v[3]}.Clamp()
	if reg.W <= 0 || reg.H <= 0 || reg.X < 0 || reg.Y < 0 || reg.X+reg.W > 1 || reg.Y+reg.H > 1 {
		return reg, errors.New("it must fit inside the frame.")
	}
	return reg, nil
}

// gateTest is the gate's half of a Test: what the gate box read on the
// same frame as the value, and whether that opens the gate.
type gateTest struct {
	Crop    template.URL
	Engine  string
	Pattern string
	// Read: the engine read the box (Text may be empty: nothing legible).
	Read bool
	Text string
	Open bool
	// Note says why there is no reading (the engine is missing or failed).
	Note string
}

// testGate reads the form's gate (gateFromForm) from the frame the value
// was cut from, with the gate's engine (the form's, or the value's) and
// preprocessing. nil when the form has no gate. A gate whose fields don't
// parse isn't read: Note says so.
func (s *Server) testGate(ctx context.Context, r *http.Request, wc config.Watch, frame image.Image, valueEngine string, prep config.Preprocess) *gateTest {
	ttype := wc.Trigger.Type
	if _, ok := r.Form["ttype"]; ok {
		ttype = r.FormValue("ttype")
	}
	g, errs := gateFromForm(r, wc, ttype)
	if g == nil {
		return nil
	}
	gt := &gateTest{Pattern: g.Pattern}
	if len(errs) > 0 {
		gt.Note = errs[0].Msg
		return gt
	}
	tw := wc
	tw.Engine, tw.Preprocess, tw.Gate = valueEngine, prep, g
	gt.Engine = tw.GateEngine()
	if gt.Engine == "" {
		gt.Engine = "tesseract"
	}
	eng, err := s.engines.For(tw.GateEngine())
	if err != nil {
		gt.Note = "The gate box wasn't read: " + gt.Engine + " isn't available on this box."
		return gt
	}
	budget := testReadBudget(gt.Engine)
	rctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	re := regexp.MustCompile(g.Pattern) // checked by gateFromForm
	gr, err := runner.ReadGate(rctx, frame, tw, eng, re)
	if err != nil {
		timedOut := errors.Is(err, context.DeadlineExceeded) || errors.Is(rctx.Err(), context.DeadlineExceeded)
		gt.Note = "The gate box wasn't read: " + strings.TrimSuffix(readFailureNote(gt.Engine, budget, timedOut), ".") + "."
		return gt
	}
	if u, perr := pngDataURL(gr.Crop); perr == nil {
		gt.Crop = u
	}
	gt.Read, gt.Text, gt.Open = true, gr.Text, gr.Open
	return gt
}

// closedVerdict is the verdict on a frame whose gate is closed: the running
// watch skips it, whatever the value box says. The gate line above it
// says what the gate read.
func closedVerdict(gt *gateTest) *testVerdict {
	return &testVerdict{State: "skipped", Title: "Frame skipped",
		Detail: "The gate is closed on this frame, so the watch skips it: nothing is published or counted. The value box below is only for reference."}
}

// gateView is what Live and the dashboard say about a gated watch's gate.
// nil for a watch without a gate, or before the gate was first read.
type gateView struct {
	Open bool
	// Long: shut for health_after polls in a row and GateLongAfter (the
	// dashboard tags only that). Minutes is how long, in whole minutes.
	Long    bool
	Minutes int
	// Text is the sentence Live shows.
	Text string
}

// gateFor is the watch's gateView, from its running runner.
func (s *Server) gateFor(wc config.Watch) *gateView {
	if wc.Gate == nil {
		return nil
	}
	get := s.gateOf
	if get == nil {
		if s.sup == nil {
			return nil
		}
		get = s.sup.Gate
	}
	st, ok := get(wc.Name)
	if !ok {
		return nil
	}
	return newGateView(st, wc.Gate.Pattern, time.Now())
}

func newGateView(st runner.GateStatus, pattern string, now time.Time) *gateView {
	if !st.Gated || !st.HasRead {
		return nil
	}
	if st.Open {
		return &gateView{Open: true, Text: "Gate open on the last frame: the code box matches " + pattern + ", so the value was read."}
	}
	saw := "nothing"
	if st.Saw != "" {
		saw = "'" + st.Saw + "'"
	}
	v := &gateView{Long: st.Long, Minutes: int(now.Sub(st.ClosedSince) / time.Minute)}
	if v.Long {
		v.Text = fmt.Sprintf("Gate closed for %d min (last saw %s). The value isn't read until the code box matches %s again; if the display should be showing it, check the gate box.", v.Minutes, saw, pattern)
		return v
	}
	polls := "frame"
	if st.ClosedPolls > 1 {
		polls = fmt.Sprintf("%d frames", st.ClosedPolls)
	}
	v.Text = fmt.Sprintf("Gate closed on the last %s (last saw %s), so the value was skipped.", polls, saw)
	return v
}
