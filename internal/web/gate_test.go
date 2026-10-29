package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/runner"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
)

// The cycling-meter recipe's boxes on its sample frames (docs/recipes/
// meter-cycling-obis.md): the total, and the OBIS code beside it.
var (
	obisValue = config.Region{X: 0.31, Y: 0.35, W: 0.56, H: 0.22}
	obisGate  = config.Region{X: 0.15, Y: 0.35, W: 0.15, H: 0.12}
)

func regionForm(v url.Values, prefix string, r config.Region) {
	f := func(x float64) string { return fmt.Sprint(x) }
	names := []string{"x", "y", "w", "h"}
	if prefix != "" {
		names = []string{prefix + "x", prefix + "y", prefix + "w", prefix + "h"}
	}
	v.Set(names[0], f(r.X))
	v.Set(names[1], f(r.Y))
	v.Set(names[2], f(r.W))
	v.Set(names[3], f(r.H))
}

// gatedForm is the detail form of the meter watch with the gate ticked on
// the OBIS boxes, as the page posts it to Test or Save.
func gatedForm(pattern string) url.Values {
	v := url.Values{"engine": {"sevenseg"}, "ttype": {"value"}, "confirm": {"2"}, "cooldown": {"0s"}, "interval": {"2s"},
		"unit": {"kWh"}, "device_class": {"energy"}, "counter_shown": {"1"},
		"gate_shown": {"1"}, "gate_on": {"on"}, "gate_pattern": {pattern}, "gate_engine": {""}}
	regionForm(v, "", obisValue)
	regionForm(v, "g", obisGate)
	return v
}

// V2.3/V2.5: Test reads the gate from the same frame as the value, shows
// its reading next to the value's and says plainly whether it was open;
// on a closed gate the verdict is the skip the running watch makes. It
// honours the form as it is, unsaved: the watch on disk has no gate.
func TestTestRouteReportsGate(t *testing.T) {
	s, _ := newMeterServer(t)
	for _, c := range []struct {
		sample, pattern string
		open            bool
		gateRead        string
		verdict, title  string
	}{
		{"meter-obis-1-8-0.jpg", `^1\.8\.0$`, true, "1.8.0", "met", "Value read"},
		{"meter-obis-2-8-0.jpg", `^1\.8\.0$`, false, "2.8.0", "skipped", "Frame skipped"},
		// The decoder finds no digit on the blank frame: "?".
		{"meter-obis-blank.jpg", `^1\.8\.0$`, false, "?", "skipped", "Frame skipped"},
		// The pattern as typed, not saved: the same frame opens for 2.8.0.
		{"meter-obis-2-8-0.jpg", `^2\.8\.0$`, true, "2.8.0", "met", "Value read"},
	} {
		t.Run(c.sample+" "+c.pattern, func(t *testing.T) {
			img := recipeSample(t, c.sample)
			s.NewSource = func(config.Watch) (source.Source, error) { return &fakeSource{img: img}, nil }
			resp, body := postForm(t, s.Handler(), "/watch/meter/test", gatedForm(c.pattern))
			if resp.StatusCode != 200 {
				t.Fatalf("status %d: %s", resp.StatusCode, body)
			}
			state := "Gate closed."
			if c.open {
				state = "Gate open."
			}
			if !strings.Contains(body, `<strong class="gate-state">`+state+`</strong>`) {
				t.Errorf("Test doesn't say %q:\n%s", state, body)
			}
			read := "nothing"
			if c.gateRead != "" {
				read = `<span class="mono gate-read">` + c.gateRead + `</span>`
			}
			if !strings.Contains(body, "The gate box read "+read) {
				t.Errorf("Test doesn't show the gate read %s:\n%s", read, body)
			}
			if !strings.Contains(body, `<p class="verdict verdict-`+c.verdict+`">`) || !strings.Contains(body, `<strong class="verdict-title">`+c.title+`</strong>`) {
				t.Errorf("verdict should be %s %q:\n%s", c.verdict, c.title, body)
			}
			// The value is read either way, beside the gate, for reference;
			// on a skipped frame without advice to fix its box.
			if c.sample != "meter-obis-blank.jpg" && !strings.Contains(body, `<p class="crop-label">Value box</p>`) {
				t.Errorf("no value box beside the gate:\n%s", body)
			}
			if !c.open && strings.Contains(body, "Tighten the region") {
				t.Errorf("a skipped frame tells the user to fix the value box:\n%s", body)
			}
		})
	}
	// Without the gate ticked, Test is what it was: no gate line.
	img := recipeSample(t, "meter-obis-2-8-0.jpg")
	s.NewSource = func(config.Watch) (source.Source, error) { return &fakeSource{img: img}, nil }
	form := gatedForm(`^1\.8\.0$`)
	form.Del("gate_on")
	_, body := postForm(t, s.Handler(), "/watch/meter/test", form)
	if strings.Contains(body, "gate-test") || !strings.Contains(body, `<strong class="verdict-title">Value read</strong>`) {
		t.Errorf("an ungated Test shows a gate or no value:\n%s", body)
	}
	// A pattern that doesn't compile isn't read, and says why.
	_, body = postForm(t, s.Handler(), "/watch/meter/test", gatedForm(`(`))
	if !strings.Contains(body, `<strong class="gate-state">Gate not read.</strong>`) || !strings.Contains(body, "Gate pattern isn&#39;t a valid regular expression") {
		t.Errorf("a bad gate pattern:\n%s", body)
	}
}

// V2.3/V2.5: Save writes the gate into the file with its comments kept,
// the page shows it again with the box switch, unticking the gate takes
// it out, pixel_change drops it, a client that never showed the fieldset
// keeps it, and a bad gate pattern is refused at its field.
func TestSaveWritesTheGate(t *testing.T) {
	s, _ := newMeterServer(t)
	head := "# my house meter\n"
	raw, err := os.ReadFile(s.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.cfgPath, append([]byte(head), raw...), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, body := postForm(t, s.Handler(), "/watch/meter/save", gatedForm(`^1\.8\.0$`))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d\n%s", resp.StatusCode, body)
	}
	file := readCfg(t, s.cfgPath)
	for _, want := range []string{head, "    gate:\n      region: {x: 0.15, y: 0.35, w: 0.15, h: 0.12}\n      pattern: ^1\\.8\\.0$\n"} {
		if !strings.Contains(file, want) {
			t.Errorf("file lacks %q:\n%s", want, file)
		}
	}
	wc, _ := s.findWatch("meter")
	if wc.Gate == nil || wc.Gate.Pattern != `^1\.8\.0$` || wc.Gate.Region != obisGate {
		t.Fatalf("saved gate = %+v", wc.Gate)
	}
	_, page := get(t, s.Handler(), "/watch/meter")
	for _, want := range []string{
		`<input type="checkbox" id="f-gate" name="gate_on" checked`,
		`name="gate_pattern" class="mono" value="^1\.8\.0$"`,
		`<input type="text" name="gx" value="0.15"`,
		`<div class="box-switch" id="box-switch" role="group" aria-label="Box to edit">`,
		`aria-label="Region editor, value box.`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("detail page lacks %q", want)
		}
	}

	// A hand-written comment inside the gate survives the next save.
	file = strings.Replace(file, "      pattern: ^1\\.8\\.0$\n", "      pattern: ^1\\.8\\.0$ # import\n", 1)
	if err := os.WriteFile(s.cfgPath, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	form := gatedForm(`^1\.8\.0$`)
	form.Set("confirm", "3")
	if resp, body := postForm(t, s.Handler(), "/watch/meter/save", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d\n%s", resp.StatusCode, body)
	}
	if file = readCfg(t, s.cfgPath); !strings.Contains(file, "pattern: ^1\\.8\\.0$ # import") || !strings.Contains(file, head) {
		t.Errorf("comment lost:\n%s", file)
	}

	// A client without the fieldset (curl) keeps the gate.
	form = gatedForm("")
	for _, k := range []string{"gate_shown", "gate_on", "gate_pattern", "gate_engine", "gx", "gy", "gw", "gh"} {
		form.Del(k)
	}
	if resp, body := postForm(t, s.Handler(), "/watch/meter/save", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d\n%s", resp.StatusCode, body)
	}
	if wc, _ := s.findWatch("meter"); wc.Gate == nil {
		t.Error("a save without the gate fields dropped the gate")
	}

	// A bad pattern is refused at its field, the gate shown as typed.
	resp, body = postForm(t, s.Handler(), "/watch/meter/save", gatedForm(`(1.8.0`))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, `id="err-gate-pattern"`) || !strings.Contains(body, `value="(1.8.0"`) || !strings.Contains(body, `<a href="#f-gate-pattern">`) {
		t.Errorf("bad gate pattern: %d\n%s", resp.StatusCode, body)
	}
	resp, body = postForm(t, s.Handler(), "/watch/meter/save", gatedForm(""))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "Gate pattern is empty") {
		t.Errorf("empty gate pattern: %d", resp.StatusCode)
	}

	// Unticked, the gate goes from the file.
	form = gatedForm(`^1\.8\.0$`)
	form.Del("gate_on")
	if resp, body := postForm(t, s.Handler(), "/watch/meter/save", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d\n%s", resp.StatusCode, body)
	}
	if file = readCfg(t, s.cfgPath); strings.Contains(file, "gate") {
		t.Errorf("unticked gate still in the file:\n%s", file)
	}
	// pixel_change has no gate, whatever the hidden fields say.
	form = gatedForm(`^1\.8\.0$`)
	form.Set("ttype", "pixel_change")
	form.Set("tthreshold", "5")
	if resp, body := postForm(t, s.Handler(), "/watch/meter/save", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d\n%s", resp.StatusCode, body)
	}
	if wc, _ := s.findWatch("meter"); wc.Gate != nil {
		t.Errorf("pixel_change kept a gate: %+v", wc.Gate)
	}
}

func readCfg(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// V2.2: Live says what the gate did on the last poll, and plainly when it
// has been shut a long time ("gate closed for N min (last saw '2.8.0')");
// the list tags only the long case.
func TestLiveAndListSayGateClosed(t *testing.T) {
	s, _ := newMeterServer(t)
	wc, _ := s.findWatch("meter")
	wc.Gate = &config.Gate{Region: obisGate, Pattern: `^1\.8\.0$`}
	s.cfg.Watches[0] = wc
	now := time.Now()
	st := runner.GateStatus{Gated: true, HasRead: true, Saw: "2.8.0", ClosedSince: now.Add(-6*time.Minute - 10*time.Second), ClosedPolls: 180, Long: true}
	s.gateOf = func(string) (runner.GateStatus, bool) { return st, true }
	_, body := get(t, s.Handler(), "/watch/meter/live")
	want := `<p class="gate-status gate-status-long"><span class="gate-swatch" aria-hidden="true"></span><span>Gate closed for 6 min (last saw &#39;2.8.0&#39;). The value isn&#39;t read until the code box matches ^1\.8\.0$ again; if the display should be showing it, check the gate box.</span></p>`
	if !strings.Contains(body, want) {
		t.Errorf("live lacks the long-closed line:\n%s", body)
	}
	if err := s.sup.Start(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	// A gate that has been shut since the watch started (a box off the
	// code, a pattern that never matches) leaves no reading: the row
	// still says what the gate is doing, with a half-lit LED.
	_, index := get(t, s.Handler(), "/")
	if !strings.Contains(index, `<span class="led led-unsure" aria-hidden="true"></span>`) ||
		!strings.Contains(index, `No readings yet</span> <span class="tag tag-gate" title="Gate closed for 6 min`) || !strings.Contains(index, ">gate closed 6 min</span>") {
		t.Errorf("the list doesn't tag a gate shut since the start:\n%s", index)
	}
	short := runner.GateStatus{Gated: true, HasRead: true, Saw: "2.8.0", ClosedSince: now.Add(-3 * time.Second), ClosedPolls: 2}
	long := st
	st = short
	_, index = get(t, s.Handler(), "/")
	if !strings.Contains(index, `No readings yet</span> <span class="reading-sub" title="Gate closed on the last 2 frames (last saw &#39;2.8.0&#39;), so the value was skipped.">gate closed</span>`) || strings.Contains(index, "tag-gate") {
		t.Errorf("the list doesn't say a gate shut since the start is closed, or tags it too soon:\n%s", index)
	}
	st = long
	s.reg.Add("meter", state.Sample{TS: now, Reading: "004521.7", PNG: []byte("png")})
	_, index = get(t, s.Handler(), "/")
	if !strings.Contains(index, `<span class="tag tag-gate" title="Gate closed for 6 min`) || !strings.Contains(index, ">gate closed 6 min</span>") {
		t.Errorf("the list doesn't tag the long-closed gate:\n%s", index)
	}

	st = runner.GateStatus{Gated: true, HasRead: true, Saw: "", ClosedSince: now.Add(-4 * time.Second), ClosedPolls: 2}
	_, body = get(t, s.Handler(), "/watch/meter/live")
	if !strings.Contains(body, `<p class="gate-status gate-status-closed">`) || !strings.Contains(body, "Gate closed on the last 2 frames (last saw nothing), so the value was skipped. The latest reading above is from the last open frame.") {
		t.Errorf("live lacks the short-closed line:\n%s", body)
	}
	_, index = get(t, s.Handler(), "/")
	if strings.Contains(index, "tag-gate") {
		t.Error("the list tags a gate shut for two frames")
	}
	st = runner.GateStatus{Gated: true, HasRead: true, Open: true, LastOpen: now}
	if _, body = get(t, s.Handler(), "/watch/meter/live"); !strings.Contains(body, `<p class="gate-status">`) || !strings.Contains(body, "Gate open on the last frame") {
		t.Errorf("live lacks the open line:\n%s", body)
	}
	// A watch without a gate says nothing about one.
	wc.Gate = nil
	s.cfg.Watches[0] = wc
	if _, body = get(t, s.Handler(), "/watch/meter/live"); strings.Contains(body, "gate-status") {
		t.Error("an ungated watch shows a gate line")
	}
}

// A new or moved gate drops the old crops: the strip may hold other
// phases' readings.
func TestGateChangeReadsDifferently(t *testing.T) {
	a := config.Watch{Region: obisValue, Trigger: config.Trigger{Type: "value"}}
	b := a
	b.Gate = &config.Gate{Region: obisGate, Pattern: "^1$"}
	c := a
	c.Gate = &config.Gate{Region: obisGate, Pattern: "^2$"}
	d := a
	d.Gate = &config.Gate{Region: obisGate, Pattern: "^1$", Engine: "tesseract"}
	if !readsDifferently(a, b) || !readsDifferently(b, c) || readsDifferently(b, d) || readsDifferently(b, b) {
		t.Error("readsDifferently disagrees about the gate")
	}
}
