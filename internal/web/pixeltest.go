package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html/template"
	"image"
	"image/png"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/imgproc"
	"github.com/darrenhuai/watchglass/internal/source"
)

// A pixel_change Test waits this long between its two frames: the watch's
// interval, kept between pixelTestMinGap and pixelTestMaxGap. Long enough
// for a flickering backlight or the camera's own noise to show, short
// enough to feel like a test and not a wait. app.js works out the same
// number for its "Comparing two frames N s apart…" line (pixelGapMs).
const (
	pixelTestMinGap = time.Second
	pixelTestMaxGap = 3 * time.Second
)

// pixelTestGap is the wait for a watch polled every interval.
func pixelTestGap(interval time.Duration) time.Duration {
	return min(max(interval, pixelTestMinGap), pixelTestMaxGap)
}

// pixelTestNow and pixelTestWait are the clock a pixel_change Test runs
// on; tests replace them so the gap is known without sleeping.
var (
	pixelTestNow  = time.Now
	pixelTestWait = realPixelTestWait
)

// realPixelTestWait sleeps d, or returns ctx's error if the page goes
// away first.
func realPixelTestWait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// testInterval is the interval a Test goes by: the form's when it holds a
// usable one (unsaved edits included), the saved one otherwise.
func testInterval(r *http.Request, saved config.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(r.FormValue("interval"))); err == nil && d >= time.Second {
		return d
	}
	return time.Duration(saved)
}

// testPixelChange answers a pixel_change Test. first is the frame testRegion
// has just grabbed from src; this waits pixelTestGap, grabs a second one
// and compares the region in both exactly as runner.Tick does (the crop as
// the camera sends it, no preprocessing or rotate, imgproc.PercentChanged
// with imgproc.NoiseTolerance). The verdict uses the form's Threshold.
//
// Each grab has its own grabTimeout. A device source (a webcam) serves one
// ffmpeg at a time (source.deviceSlots), so the second grab may queue
// behind the watch's own poll: that wait counts against its grabTimeout,
// not against the gap. Worst case the request takes two grab timeouts plus
// pixelTestMaxGap; app.js says "Still waiting for the camera" after 8 s.
func (s *Server) testPixelChange(w http.ResponseWriter, r *http.Request, wc config.Watch, src source.Source, first image.Image, region config.Region) {
	firstAt := pixelTestNow()
	gap := pixelTestGap(testInterval(r, wc.Interval))
	if err := pixelTestWait(r.Context(), gap); err != nil {
		// The page went away; nobody is reading the answer.
		http.Error(w, "The test was cancelled before the second frame.", http.StatusServiceUnavailable)
		return
	}
	// The same person is still asking, so the second grab is forced like
	// the first: a login the camera turned down between the two frames
	// is reported, not waited out (source.Forced).
	grabCtx, cancel := context.WithTimeout(r.Context(), grabTimeout)
	second, err := src.Grab(source.Forced(grabCtx))
	cancel()
	if err != nil {
		// text/plain like grabError: a summary for people, then the chain.
		http.Error(w, "The first frame arrived but the second didn't: "+
			lowerFirst(withHint(summarizeErr(err.Error()), err.Error()))+"\n"+err.Error(), http.StatusBadGateway)
		return
	}
	secondAt := pixelTestNow()

	a, b := imgproc.Crop(first, region), imgproc.Crop(second, region)
	pct := imgproc.PercentChanged(a, b, imgproc.NoiseTolerance)
	aURL, err := pngDataURL(a)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	bURL, err := pngDataURL(b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	trig, errs := triggerFromForm(r, wc.Trigger)
	px := &pixelTest{
		Second:    bURL,
		Gap:       gapText(secondAt.Sub(firstAt)),
		Changed:   changedText(pct, trig.Threshold),
		Threshold: strconv.FormatFloat(trig.Threshold, 'f', -1, 64) + "%",
	}
	res := testResult{Crop: aURL, At: firstAt, Pixel: px, Verdict: pixelVerdict(pct, trig.Threshold, px, errs)}
	if fa, fb := first.Bounds(), second.Bounds(); fa.Dx() != fb.Dx() || fa.Dy() != fb.Dy() {
		res.Note = fmt.Sprintf("The camera sent the two frames at different sizes (%d×%d, then %d×%d), so the whole region counts as changed. A watch sees the same when the camera changes resolution.",
			fa.Dx(), fa.Dy(), fb.Dx(), fb.Dy())
	}
	s.render(w, "testresult.html", res)
}

func pngDataURL(img image.Image) (template.URL, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())), nil
}

// gapText words the time between the two frames: "3.0 s".
func gapText(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + " s"
}

// changedText words a measured percentage the way the Live panel does
// ("4.2%", trigger.ObservePixel's %.1f), with more decimals only where one
// would contradict the verdict: 19.96% against a Threshold of 20 is "19.96%",
// not "20.0%", and a few changed pixels are never "0.0%".
func changedText(pct, threshold float64) string {
	for d := 1; d <= 4; d++ {
		s := strconv.FormatFloat(pct, 'f', d, 64)
		v, _ := strconv.ParseFloat(s, 64)
		if (v >= threshold) == (pct >= threshold) && (v > 0) == (pct > 0) {
			return s + "%"
		}
	}
	return strconv.FormatFloat(pct, 'f', -1, 64) + "%"
}

// noiseRoom is "a few times" the measured noise, as a whole percent: the
// Threshold the result suggests when nothing moved on the screen.
func noiseRoom(pct float64) float64 {
	return math.Max(1, math.Ceil(3*pct))
}

// pixelVerdict answers a pixel_change Test: the measured change against
// the form's Threshold, firing at pct >= Threshold as trigger.ObservePixel
// does. watchglass can't tell a change on the screen from camera noise, so
// the second sentence says what the number means if nothing moved, which
// the person pressing Test can see: the camera's noise floor, or a
// Threshold inside that noise.
func pixelVerdict(pct, threshold float64, px *pixelTest, errs []fieldError) *testVerdict {
	measured := px.Changed + " of the region changed between two frames " + px.Gap + " apart"
	if pct == 0 {
		measured = "Nothing changed between two frames " + px.Gap + " apart (" + px.Changed + " of the region)"
	}
	for _, e := range errs {
		if e.Field == "tthreshold" {
			return &testVerdict{State: "invalid", Title: "Can't check the trigger", Detail: e.Msg + " " + measured + "."}
		}
	}
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		return &testVerdict{State: "invalid", Title: "Can't check the trigger", Detail: "Threshold must be a number. " + measured + "."}
	}
	if threshold <= 0 {
		// config.Validate's rule, in errtext.go's words.
		return &testVerdict{State: "invalid", Title: "Can't check the trigger", Detail: "Threshold must be above 0: the percent of the region that has to change. " + measured + "."}
	}
	num := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) + "%" }
	room := noiseRoom(pct)
	// Noise with no room for a Threshold a few times above it (a backlight
	// flickering over the whole region) can't be tuned away.
	const tooNoisy = " leaves little room for a Threshold: try a smaller region on a steadier part of the screen."
	if pct >= threshold {
		// The docs' own step 2 is to press Test while the screen does what
		// the watch is for, so a met verdict leads with that reading; the
		// noise reading comes second, as the alternative.
		advice := ": raise it to about " + num(room) + "."
		if room > 100 {
			advice = ". Noise that large" + tooNoisy
		}
		return &testVerdict{State: "met", Title: "Would fire",
			Detail: measured + ", at or above the Threshold of " + px.Threshold + ". If that was the change you want to catch, " + px.Threshold + " catches it. If the screen didn't change, this is camera noise and the Threshold is inside it" + advice}
	}
	detail := measured + ", under the Threshold of " + px.Threshold + "."
	switch {
	case pct == 0:
		detail += " If the screen was still, the camera adds no noise here, so any Threshold stays quiet until something moves."
	case threshold >= room:
		detail += " If the screen didn't change, " + px.Changed + " is the camera's noise floor and " + px.Threshold + " is well above it."
	case room > 100:
		detail += " If the screen didn't change, " + px.Changed + " is camera noise, and noise that large" + tooNoisy
	default:
		detail += " If the screen didn't change, " + px.Changed + " is the camera's noise floor and " + px.Threshold + " is close to it: about " + num(room) + " is safer."
	}
	return &testVerdict{State: "unmet", Title: "Would not fire", Detail: detail}
}
