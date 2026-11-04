package drift

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
)

// The photo is testdata/clock-sony-917.jpg (see testdata/README.md): a
// clock radio showing 9:17, 640x480. The tests' camera sees a 600x440
// window of it, so a camera that moved is the window moved the other
// way: real pixels on every side, nothing made up at the edge.
const (
	winX, winY = 20, 20
	winW, winH = 600, 440
)

// sonyBox is the box round the digits, in fractions of the window.
var sonyBox = config.Region{X: 210.0 / winW, Y: 177.0 / winH, W: 205.0 / winW, H: 67.0 / winH}

func loadPhoto(t testing.TB, path string) *image.RGBA {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	out := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out
}

func sony(t testing.TB) *image.RGBA {
	return loadPhoto(t, filepath.Join("testdata", "clock-sony-917.jpg"))
}

// view is what the camera sends when the picture has moved dx right and
// dy down: the window moved the other way. Its bounds don't start at 0,0,
// as a sub-image's don't.
func view(photo *image.RGBA, dx, dy int) image.Image {
	return photo.SubImage(image.Rect(winX-dx, winY-dy, winX-dx+winW, winY-dy+winH))
}

// camera is img as a camera would send it another time: each channel
// times gain (exposure and white balance), plus noise, in a fresh RGBA.
func camera(img image.Image, gain [3]float64, noise float64, seed int64) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	rng := rand.New(rand.NewSource(seed))
	src, ok := img.(*image.RGBA)
	if !ok {
		src = image.NewRGBA(b)
		draw.Draw(src, b, img, b.Min, draw.Src)
	}
	clamp := func(v float64) uint8 { return uint8(math.Max(0, math.Min(255, v+0.5))) }
	for y := 0; y < b.Dy(); y++ {
		row := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
		dst := out.Pix[out.PixOffset(0, y):]
		for x := 0; x < b.Dx(); x++ {
			n := rng.NormFloat64() * noise
			for i := 0; i < 3; i++ {
				dst[4*x+i] = clamp(float64(row[4*x+i])*gain[i] + n)
			}
			dst[4*x+3] = 255
		}
	}
	return out
}

var plain = [3]float64{1, 1, 1}

// pivotX, pivotY is the point a turned camera turns the picture about:
// a knock both shifts and turns a camera, which comes to a turn about
// some point other than the middle. Far from the box, so the box moves.
const pivotX, pivotY = 80.0, 400.0

// turned is the window turned deg degrees clockwise about the pivot,
// sampled bilinearly from the photo around it.
func turned(photo *image.RGBA, deg float64) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, winW, winH))
	s, c := math.Sincos(deg * math.Pi / 180)
	cx, cy := pivotX, pivotY
	for y := 0; y < winH; y++ {
		for x := 0; x < winW; x++ {
			// where this output pixel comes from: turned back
			px, py := float64(x)-cx, float64(y)-cy
			sx := c*px + s*py + cx + winX
			sy := -s*px + c*py + cy + winY
			x0, y0 := int(math.Floor(sx)), int(math.Floor(sy))
			fx, fy := sx-float64(x0), sy-float64(y0)
			var acc [3]float64
			for _, q := range [][3]float64{{0, 0, (1 - fx) * (1 - fy)}, {1, 0, fx * (1 - fy)}, {0, 1, (1 - fx) * fy}, {1, 1, fx * fy}} {
				p := photo.RGBAAt(x0+int(q[0]), y0+int(q[1]))
				acc[0] += float64(p.R) * q[2]
				acc[1] += float64(p.G) * q[2]
				acc[2] += float64(p.B) * q[2]
			}
			out.SetRGBA(x, y, color.RGBA{uint8(acc[0] + 0.5), uint8(acc[1] + 0.5), uint8(acc[2] + 0.5), 255})
		}
	}
	return out
}

func boxOf(img image.Image, r config.Region) image.Rectangle {
	return BoxRect(img.Bounds().Dx(), img.Bounds().Dy(), r.X, r.Y, r.W, r.H)
}

func TestMeasureFindsTheShift(t *testing.T) {
	photo := sony(t)
	ref := Take(camera(view(photo, 0, 0), plain, 2, 1), boxOf(view(photo, 0, 0), sonyBox))
	if ref == nil {
		t.Fatal("no reference")
	}
	for i, d := range [][2]int{{5, 0}, {0, -5}, {-4, 3}, {7, -7}, {-10, 3}, {12, 9}, {20, 0}, {-14, -14}} {
		got := Measure(ref, camera(view(photo, d[0], d[1]), plain, 2, int64(10+i)))
		if !got.Found || got.Local < trustMin {
			t.Errorf("shift %v: not found (%+v)", d, got)
			continue
		}
		if math.Hypot(got.DX-float64(d[0]), got.DY-float64(d[1])) > 1 {
			t.Errorf("shift %v: measured %.2f, %.2f", d, got.DX, got.DY)
		}
	}
}

func TestMeasureFindsTheBoxOfATurnedCamera(t *testing.T) {
	photo := sony(t)
	base := turned(photo, 0)
	box := boxOf(base, sonyBox)
	ref := Take(camera(base, plain, 2, 1), box)
	cx, cy := float64(box.Min.X+box.Max.X)/2-pivotX, float64(box.Min.Y+box.Max.Y)/2-pivotY
	for _, deg := range []float64{2, -2, 3} {
		s, c := math.Sincos(deg * math.Pi / 180)
		// where the box's centre went
		tx, ty := c*cx-s*cy-cx, s*cx+c*cy-cy
		got := Measure(ref, camera(turned(photo, deg), plain, 2, 7))
		t.Logf("turned %v°: box centre moved %.1f, %.1f, measured %.1f, %.1f", deg, tx, ty, got.DX, got.DY)
		if math.Hypot(got.DX-tx, got.DY-ty) > 2 {
			t.Errorf("turned %v°: box centre moved %.1f, %.1f, measured %.1f, %.1f (score %.2f, %.2f)", deg, tx, ty, got.DX, got.DY, got.Score, got.Local)
		}
	}
}

// memStore is a Store in memory.
type memStore struct{ m map[string]Saved }

func (s *memStore) LoadDrift(w string) (Saved, bool, error) { v, ok := s.m[w]; return v, ok, nil }
func (s *memStore) SaveDrift(w string, v Saved) error {
	if s.m == nil {
		s.m = map[string]Saved{}
	}
	v.Ref.Gray = append([]uint8(nil), v.Ref.Gray...)
	s.m[w] = v
	return nil
}

// run feeds a tracker one frame per poll, polls every pollEvery from
// *at, and returns the news it reported.
type feed struct {
	t     *testing.T
	tr    *Tracker
	at    time.Time
	news  []Change
	every time.Duration
}

func (f *feed) poll(img image.Image, rd Read) {
	f.at = f.at.Add(f.every)
	if ch, ok := f.tr.Observe(img, f.at, rd); ok {
		f.news = append(f.news, ch)
	}
}

// settled makes a tracker for w and feeds it until its reference is
// settled, with noisy frames of the unmoved picture.
func settled(t *testing.T, w config.Watch, store Store, frame func(seed int64) image.Image) (*feed, *Board) {
	t.Helper()
	b := NewBoard()
	tr := b.Tracker(w, store, t.Logf)
	f := &feed{t: t, tr: tr, at: time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC), every: checkEvery}
	for i := 0; i < 40 && tr.Status().State != Steady; i++ {
		f.poll(frame(int64(100+i)), ReadClean)
	}
	if tr.Status().State != Steady {
		t.Fatalf("not settled after 40 polls: %+v", tr.Status())
	}
	return f, b
}

func sonyWatch() config.Watch {
	return config.Watch{Name: "clock", Source: "http://cam.local/snap.jpg", Region: sonyBox}
}

func TestTrackerReportsAMoveOnceWithItsOffset(t *testing.T) {
	photo := sony(t)
	f, b := settled(t, sonyWatch(), nil, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) })
	for i := 0; i < 10; i++ {
		f.poll(camera(view(photo, 0, 0), plain, 3, int64(200+i)), ReadClean)
	}
	if len(f.news) != 0 {
		t.Fatalf("news before the move: %+v", f.news)
	}
	movedAt := f.at.Add(f.every)
	for i := 0; i < window+6; i++ {
		f.poll(camera(view(photo, 8, -3), plain, 3, int64(300+i)), ReadBad)
		if i == window-2 && len(f.news) != 0 {
			t.Fatalf("reported after %d checks, before %d agree", i+1, window)
		}
	}
	if len(f.news) != 1 {
		t.Fatalf("want one report of the move, got %+v", f.news)
	}
	got := f.news[0].Status
	if got.State != Moved || got.DX != 8 || got.DY != -3 || !got.Since.Equal(movedAt) {
		t.Errorf("report = %+v, want moved 8,-3 since %v", got, movedAt)
	}
	if st, _ := b.Status("clock"); st.State != Moved || Offset(st.DX, st.DY) != "8 px right and 3 px up" {
		t.Errorf("board = %+v", st)
	}
	// Moved back: steady again, with no second report.
	for i := 0; i < window+1; i++ {
		f.poll(camera(view(photo, 0, 0), plain, 3, int64(400+i)), ReadClean)
	}
	if st := f.tr.Status(); st.State != Steady || len(f.news) != 1 {
		t.Errorf("after moving back: %+v, news %+v", st, f.news)
	}
}

func TestTrackerReportsATurnedCamera(t *testing.T) {
	photo := sony(t)
	w := sonyWatch()
	f, _ := settled(t, w, nil, func(s int64) image.Image { return camera(turned(photo, 0), plain, 3, s) })
	frame := camera(turned(photo, 3), plain, 3, 9)
	for i := 0; i < window+2; i++ {
		f.poll(frame, ReadBad)
	}
	if len(f.news) != 1 || f.news[0].State != Moved {
		t.Fatalf("news = %+v", f.news)
	}
	box := boxOf(frame, w.Region)
	cx, cy := float64(box.Min.X+box.Max.X)/2-pivotX, float64(box.Min.Y+box.Max.Y)/2-pivotY
	s, c := math.Sincos(3 * math.Pi / 180)
	tx, ty := c*cx-s*cy-cx, s*cx+c*cy-cy
	if got := f.news[0].Status; math.Hypot(float64(got.DX)-tx, float64(got.DY)-ty) > 2 {
		t.Errorf("moved %d, %d; the box's centre moved %.1f, %.1f", got.DX, got.DY, tx, ty)
	}
}

// TestTrackerIgnoresExposureAndWhiteBalance: T2's auto-exposure steps
// (0.65-0.8x and 1.25-1.45x), a white balance that drifts, and noise.
func TestTrackerIgnoresExposureAndWhiteBalance(t *testing.T) {
	photo := sony(t)
	f, _ := settled(t, sonyWatch(), nil, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) })
	gains := [][3]float64{
		{0.65, 0.65, 0.65}, {0.7, 0.7, 0.7}, {0.8, 0.8, 0.8}, {1.25, 1.25, 1.25}, {1.35, 1.35, 1.35}, {1.45, 1.45, 1.45},
		{1.1, 1.0, 0.88}, {0.9, 1.0, 1.12}, {0.8 * 0.92, 0.8, 0.8 * 1.1}, {1.4 * 1.06, 1.4, 1.4 * 0.94},
	}
	for round := 0; round < 4; round++ {
		for i, g := range gains {
			f.poll(camera(view(photo, 0, 0), g, 4, int64(1000+round*100+i)), ReadClean)
		}
	}
	if len(f.news) != 0 || f.tr.Status().State != Steady {
		t.Errorf("exposure and white balance alone: %+v, news %+v", f.tr.Status(), f.news)
	}
}

// TestTrackerIgnoresChangingDigits uses the washer photos of the
// appliance recipe (docs/recipes/samples): the same machine showing 1:23,
// 0:04, 0:00 and End.
func TestTrackerIgnoresChangingDigits(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "recipes", "samples")
	var shots []*image.RGBA
	for _, n := range []string{"washer-1-23.jpg", "washer-0-04.jpg", "washer-0-00.jpg", "washer-end.jpg"} {
		shots = append(shots, loadPhoto(t, filepath.Join(dir, n)))
	}
	// The appliance recipe's box round the time.
	w := config.Watch{Name: "washer", Source: "http://cam.local/snap.jpg", Region: config.Region{X: 0.44375, Y: 0.3222, W: 0.30625, H: 0.2822}}
	f, _ := settled(t, w, nil, func(s int64) image.Image { return camera(shots[0], plain, 3, s) })
	for i := 0; i < 60; i++ {
		f.poll(camera(shots[i%len(shots)], plain, 3, int64(2000+i)), ReadClean)
	}
	if len(f.news) != 0 || f.tr.Status().State != Steady {
		t.Errorf("changing digits alone: %+v, news %+v", f.tr.Status(), f.news)
	}
}

// TestMeasureLeavesTheBoxOut: what the box shows can move on its own (a
// line of text that scrolls, a bar that grows) while the display stays
// put. Here the box holds bold stripes that slide 4 px between the
// reference and the check, and the clock around it doesn't move: the
// check must find no shift.
func TestMeasureLeavesTheBoxOut(t *testing.T) {
	photo := sony(t)
	striped := func(phase int, seed int64) *image.RGBA {
		img := camera(view(photo, 0, 0), plain, 2, seed)
		box := boxOf(img, sonyBox)
		for y := box.Min.Y; y < box.Max.Y; y++ {
			for x := box.Min.X; x < box.Max.X; x++ {
				v := uint8(20)
				if (x+phase)%16 < 8 {
					v = 235
				}
				img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
			}
		}
		return img
	}
	ref := Take(striped(0, 1), boxOf(view(photo, 0, 0), sonyBox))
	if ref == nil {
		t.Fatal("no reference")
	}
	for i, phase := range []int{4, 12, 0} {
		got := Measure(ref, striped(phase, int64(20+i)))
		if !got.Found || got.Local < trustMin || got.Magnitude() > 1 {
			t.Errorf("stripes in the box slid by %d px, the clock didn't move: measured %+v, want no shift", phase, got)
		}
	}
}

// TestTrackerIgnoresShake replays checks measured on T2's emulated
// cameras (testdata/t2-shake.csv), which shake by 1-3 px every frame and
// go through exposure steps and IR night mode, through the move rule:
// no move is reported. On these stretches a fixed 4.5 px threshold did
// report one; a move has to stand out from the camera's own shake. The
// same stretches with the picture then moved 12 px right report it.
func TestTrackerIgnoresShake(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "t2-shake.csv"))
	if err != nil {
		t.Fatal(err)
	}
	stretches := map[string][]Result{}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		var name string
		var r Result
		f := strings.Split(line, ",")
		name = f[0]
		vals := make([]float64, 4)
		for i := range vals {
			if vals[i], err = strconv.ParseFloat(f[i+1], 64); err != nil {
				t.Fatal(err)
			}
		}
		r = Result{DX: vals[0], DY: vals[1], Score: vals[2], Local: vals[3], Found: true}
		if _, ok := stretches[name]; !ok {
			names = append(names, name)
		}
		stretches[name] = append(stretches[name], r)
	}
	run := func(rs []Result, moveFrom int) []Change {
		b := NewBoard()
		tr := b.Tracker(config.Watch{Name: "t2", Region: config.Region{X: 0.4, Y: 0.3, W: 0.2, H: 0.3}}, nil, t.Logf)
		tr.ref = &Reference{FrameW: 800, FrameH: 450, W: 10, H: 10, Scale: 1}
		at := time.Date(2026, 10, 21, 22, 36, 0, 0, time.UTC)
		var news []Change
		for i, r := range rs {
			if moveFrom >= 0 && i >= moveFrom {
				r.DX += 12
			}
			at = at.Add(6 * time.Second)
			if ch, ok := tr.judge(r, at); ok {
				news = append(news, ch)
			}
		}
		return news
	}
	for _, name := range names {
		if news := run(stretches[name], -1); len(news) != 0 {
			t.Errorf("%s: reported %+v", name, news)
		}
		news := run(stretches[name], 100)
		if len(news) != 1 || news[0].State != Moved || math.Abs(float64(news[0].Status.DX)-12) > 3 {
			t.Errorf("%s moved 12 px right: %+v", name, news)
		}
	}
}

func dark(seed int64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, winW, winH))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{6, 6, 7, 255}}, image.Point{}, draw.Src)
	return camera(img, plain, 2, seed)
}

func TestTrackerSaysACoveredLensCantBeFound(t *testing.T) {
	photo := sony(t)
	f, b := settled(t, sonyWatch(), nil, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) })
	coveredAt := f.at.Add(f.every)
	n := int(lostAfter/f.every) + 2
	for i := 0; i < n; i++ {
		f.poll(dark(int64(i)), ReadBad)
		if f.at.Sub(coveredAt) < lostAfter && len(f.news) != 0 {
			t.Fatalf("reported %v after the lens was covered, before %v", f.at.Sub(coveredAt), lostAfter)
		}
	}
	if len(f.news) != 1 || f.news[0].State != Lost || !f.news[0].Status.Since.Equal(coveredAt) {
		t.Fatalf("news = %+v, want lost since %v", f.news, coveredAt)
	}
	if st, _ := b.Status("clock"); st.State != Lost {
		t.Errorf("board = %+v", st)
	}
	// Uncovered: steady again, nothing more to report.
	f.poll(camera(view(photo, 0, 0), plain, 3, 77), ReadClean)
	if f.tr.Status().State != Steady || len(f.news) != 1 {
		t.Errorf("uncovered: %+v, news %+v", f.tr.Status(), f.news)
	}
}

// A display that shows something else for a while (a menu, a boot screen)
// while its box still reads cleanly isn't "can't find".
func TestTrackerDoesntCallAChangedScreenLostWhileItReads(t *testing.T) {
	photo := sony(t)
	f, _ := settled(t, sonyWatch(), nil, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) })
	for i := 0; i < int(2*lostAfter/f.every); i++ {
		f.poll(dark(int64(i)), ReadClean)
	}
	if len(f.news) != 0 {
		t.Errorf("news = %+v", f.news)
	}
}

func TestTrackerKeepsItsReferenceAcrossARestart(t *testing.T) {
	photo := sony(t)
	store := &memStore{}
	w := sonyWatch()
	f, _ := settled(t, w, store, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) })
	if _, ok := store.m["clock"]; !ok {
		t.Fatal("nothing saved once settled")
	}
	// Bumped while watchglass was off: the restarted tracker still knows
	// where the picture was, and says it moved.
	b2 := NewBoard()
	tr2 := b2.Tracker(w, store, t.Logf)
	if st := tr2.Status(); st.State != Steady {
		t.Fatalf("restored state = %+v, want steady", st)
	}
	f2 := &feed{t: t, tr: tr2, at: f.at.Add(time.Hour), every: checkEvery}
	for i := 0; i < window+1; i++ {
		f2.poll(camera(view(photo, -6, 5), plain, 3, int64(500+i)), ReadBad)
	}
	if len(f2.news) != 1 || f2.news[0].Status.DX != -6 || f2.news[0].Status.DY != 5 {
		t.Fatalf("after the restart: news %+v", f2.news)
	}
	// Restarted again while moved: it still says so, and doesn't report
	// the same move a second time.
	b3 := NewBoard()
	tr3 := b3.Tracker(w, store, t.Logf)
	if st, _ := b3.Status("clock"); st.State != Moved || st.DX != -6 || st.DY != 5 {
		t.Fatalf("second restart: board %+v", st)
	}
	f3 := &feed{t: t, tr: tr3, at: f2.at.Add(time.Minute), every: checkEvery}
	for i := 0; i < window+3; i++ {
		f3.poll(camera(view(photo, -6, 5), plain, 3, int64(600+i)), ReadBad)
	}
	if len(f3.news) != 0 || tr3.Status().State != Moved {
		t.Errorf("second restart: %+v, news %+v", tr3.Status(), f3.news)
	}
	// A new box (a save that moved it) is a new reference: learning.
	w.Region.X += 0.01
	if st := NewBoard().Tracker(w, store, t.Logf).Status(); st.State != Learning {
		t.Errorf("new box: %+v, want learning", st)
	}
	// So is another camera with the same box; the login alone isn't.
	w = sonyWatch()
	w.Source = "http://user:secret@cam.local/snap.jpg"
	if st := NewBoard().Tracker(w, store, t.Logf).Status(); st.State == Learning {
		t.Errorf("same camera with a login: %+v, want the saved reference", st)
	}
	w.Source = "http://cam2.local/snap.jpg"
	if st := NewBoard().Tracker(w, store, t.Logf).Status(); st.State != Learning {
		t.Errorf("new source: %+v, want learning", st)
	}
}

func TestRetakeTakesTheNextFrame(t *testing.T) {
	photo := sony(t)
	f, b := settled(t, sonyWatch(), nil, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) })
	for i := 0; i < window+1; i++ {
		f.poll(camera(view(photo, 9, 0), plain, 3, int64(700+i)), ReadBad)
	}
	if f.tr.Status().State != Moved {
		t.Fatalf("not moved: %+v", f.tr.Status())
	}
	// "The new position is fine": the next frame is the reference, even
	// with a bad reading, and the picture there is steady.
	b.Retake("clock")
	for i := 0; i < settleChecks+window+2; i++ {
		f.poll(camera(view(photo, 9, 0), plain, 3, int64(800+i)), ReadBad)
	}
	if st := f.tr.Status(); st.State != Steady || len(f.news) != 1 {
		t.Errorf("after retake: %+v, news %+v", st, f.news)
	}
}

func TestOffsetAndShifted(t *testing.T) {
	for _, c := range []struct {
		dx, dy int
		want   string
	}{{14, 3, "14 px right and 3 px down"}, {-5, 0, "5 px left"}, {0, -7, "7 px up"}, {0, 0, "less than a pixel"}} {
		if got := Offset(c.dx, c.dy); got != c.want {
			t.Errorf("Offset(%d, %d) = %q, want %q", c.dx, c.dy, got, c.want)
		}
	}
	r := config.Region{X: 0.5, Y: 0.25, W: 0.25, H: 0.25}
	got := Shifted(r, Status{State: Moved, DX: 16, DY: -8, FrameW: 640, FrameH: 480})
	if got != (config.Region{X: 0.525, Y: 0.2333, W: 0.25, H: 0.25}) {
		t.Errorf("Shifted = %+v", got)
	}
	// kept inside the frame
	got = Shifted(config.Region{X: 0.7, Y: 0.01, W: 0.29, H: 0.2}, Status{DX: 64, DY: -64, FrameW: 640, FrameH: 480})
	if got.X != 0.71 || got.Y != 0 {
		t.Errorf("Shifted at the edge = %+v", got)
	}
}

func TestCheckMovesFalseTurnsItOff(t *testing.T) {
	off := false
	w := sonyWatch()
	w.CheckMoves = &off
	b := NewBoard()
	if tr := b.Tracker(w, nil, t.Logf); tr != nil {
		t.Error("a tracker for a watch with check_moves: false")
	}
	if st, _ := b.Status("clock"); st.State != Off {
		t.Errorf("board = %+v", st)
	}
}

// BenchmarkCheck is one check of an unmoved picture and of a moved one
// (which also runs the turn search).
func BenchmarkCheck(b *testing.B) {
	photo := sony(b)
	ref := Take(camera(view(photo, 0, 0), plain, 2, 1), boxOf(view(photo, 0, 0), sonyBox))
	for _, d := range [][2]int{{0, 0}, {8, -3}} {
		img := camera(view(photo, d[0], d[1]), plain, 2, 3)
		b.Run(fmt.Sprintf("shift%d,%d", d[0], d[1]), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				Measure(ref, img)
			}
		})
	}
}

// The demo's cameras (watchglass -demo) show the same scene with the
// display changing: their watches never report a move.
func TestDemoFramesDontMove(t *testing.T) {
	for _, c := range []struct {
		set    string
		region config.Region
	}{
		{"printer", config.Region{X: 0.0312, Y: 0.4167, W: 0.9375, H: 0.2222}},
		{"sevenseg", config.Region{X: 0.14, Y: 0.14, W: 0.72, H: 0.72}},
	} {
		var frames []*image.RGBA
		for i := 0; i < 6; i++ {
			frames = append(frames, loadPhoto(t, filepath.Join("..", "demo", "frames", c.set, fmt.Sprintf("frame_%d.png", i))))
		}
		b := NewBoard()
		tr := b.Tracker(config.Watch{Name: c.set, Source: "demo:" + c.set, Region: c.region}, nil, t.Logf)
		f := &feed{t: t, tr: tr, at: time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC), every: checkEvery}
		for i := 0; i < 60; i++ {
			f.poll(frames[i%len(frames)], ReadClean)
		}
		if len(f.news) != 0 {
			t.Errorf("%s: %+v", c.set, f.news)
		}
		t.Logf("%s: %s", c.set, tr.Status().State)
	}
}

// A box with too little around it (one that fills the frame), or one on
// a plain wall, isn't checked: the page notes it once, the log says so
// once, and it stays that way however many clean readings follow, until
// the watch is saved or restarted or a new picture is asked for. It used
// to learn again every few polls, logging the same line each time and
// making the page's note blink.
func TestTooPlainStaysUnusableAndSaysSoOnce(t *testing.T) {
	photo := sony(t)
	wall := func(seed int64) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, winW, winH))
		draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{180, 176, 170, 255}}, image.Point{}, draw.Src)
		return camera(img, plain, 2, seed)
	}
	for _, tc := range []struct {
		name   string
		region config.Region
		frame  func(int64) image.Image
		says   string
	}{
		{"whole frame", config.Region{X: 0, Y: 0, W: 1, H: 1}, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) }, "leaves too little of the picture around it"},
		{"plain wall", sonyBox, wall, "too plain, or changes too much"},
	} {
		var lines []string
		logf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
		b := NewBoard()
		w := sonyWatch()
		w.Region = tc.region
		tr := b.Tracker(w, nil, logf)
		f := &feed{t: t, tr: tr, at: time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC), every: checkEvery}
		unusableFrom := -1
		for i := 0; i < 120; i++ {
			f.poll(tc.frame(int64(i)), ReadClean)
			st, _ := b.Status(w.Name)
			if st.State == Unusable && unusableFrom < 0 {
				unusableFrom = i
			}
			if unusableFrom >= 0 && st.State != Unusable {
				t.Fatalf("%s: unusable at poll %d, then %s at poll %d", tc.name, unusableFrom, st.State, i)
			}
		}
		if unusableFrom < 0 {
			t.Fatalf("%s: never unusable; log %q", tc.name, lines)
		}
		said := 0
		for _, l := range lines {
			if strings.Contains(l, tc.says) {
				said++
			}
		}
		if said != 1 || len(lines) != 1 {
			t.Errorf("%s: logged %q, want the %q line once", tc.name, lines, tc.says)
		}
		// Asking for a new picture still wakes it up.
		b.Retake(w.Name)
		f.poll(tc.frame(500), ReadClean)
		if tr.ref == nil && tc.name == "plain wall" {
			t.Errorf("%s: a retake took no new picture", tc.name)
		}
	}
}

// Small knocks that add up: the picture moves 3 px, five minutes later 3
// px more, and so on. Each knock alone is under the threshold, but once
// the picture is 6 px from where it was the move is reported, and later
// knocks update the offset on the page. The threshold used to grow with
// every steady check's distance from where the picture was, so it rose
// with the knocks (to 37.5 px at 15 px off) and nothing was ever said.
func TestSmallKnocksAddUpToAMove(t *testing.T) {
	photo := sony(t)
	f, _ := settled(t, sonyWatch(), nil, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) })
	reportedAt := 0
	for k := 1; k <= 5; k++ {
		for i := 0; i < 60; i++ {
			f.poll(camera(view(photo, 3*k, 0), plain, 3, int64(k*1000+i)), ReadClean)
		}
		if reportedAt == 0 && len(f.news) > 0 {
			reportedAt = 3 * k
		}
		if th := f.tr.threshold(); th > moveMin {
			t.Errorf("after %d knocks the threshold is %.2f px: a still camera's is %v", k, th, moveMin)
		}
	}
	if len(f.news) != 1 || reportedAt != 6 || math.Abs(float64(f.news[0].Status.DX)-6) > 1 || f.news[0].Status.DY != 0 {
		t.Fatalf("five 3 px knocks: reported at %d px, news %+v; want one report, about 6 px right", reportedAt, f.news)
	}
	if st := f.tr.Status(); st.State != Moved || math.Abs(float64(st.DX)-15) > 1 || st.DY != 0 {
		t.Errorf("after 15 px of knocks the page says %+v", st)
	}
}

// A camera that sags slowly, or a screen that creeps on its stand: 1 px
// every 30 seconds. It is reported once it has gone about 5 px, and the
// page follows it from there.
func TestSlowCreepIsReported(t *testing.T) {
	photo := sony(t)
	f, _ := settled(t, sonyWatch(), nil, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) })
	reportedAt := 0
	for step := 1; step <= 20; step++ {
		for i := 0; i < 6; i++ {
			f.poll(camera(view(photo, step, 0), plain, 3, int64(step*100+i)), ReadClean)
		}
		if reportedAt == 0 && len(f.news) > 0 {
			reportedAt = step
		}
	}
	if len(f.news) != 1 || reportedAt < 5 || reportedAt > 7 {
		t.Fatalf("crept 20 px: reported at %d px, news %+v; want one report at 5 to 7 px", reportedAt, f.news)
	}
	if st := f.tr.Status(); st.State != Moved || st.DX < 18 || st.DX > 20 {
		t.Errorf("after creeping 20 px the page says %+v", st)
	}
}

// The same at the check's level, slower and longer: 1 px a minute, with
// a little measuring noise, is reported before it has gone 6 px.
func TestCreepOfAPixelAMinuteIsReported(t *testing.T) {
	b := NewBoard()
	tr := b.Tracker(config.Watch{Name: "c", Region: config.Region{X: 0.4, Y: 0.3, W: 0.2, H: 0.3}}, nil, t.Logf)
	tr.ref = &Reference{FrameW: 800, FrameH: 450, W: 10, H: 10, Scale: 1}
	at := time.Date(2026, 10, 21, 22, 36, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 12*60; i++ {
		at = at.Add(checkEvery)
		dx := float64(i) / 12
		r := Result{DX: dx + rng.NormFloat64()*0.3, DY: rng.NormFloat64() * 0.3, Score: 0.9, Local: 0.9, Found: true}
		if ch, ok := tr.judge(r, at); ok {
			if ch.State != Moved || dx > 6 {
				t.Fatalf("reported %+v at %.1f px", ch, dx)
			}
			return
		}
	}
	t.Fatalf("crept 60 px at 1 px a minute: never reported; threshold %.2f", tr.threshold())
}

// The picture moves right after the reference was taken, before "not
// moved" has settled. The reference's own frame read well, so the move
// is measured from it and reported, rather than taken as where the
// picture always was.
func TestMoveWhileSettlingIsReported(t *testing.T) {
	photo := sony(t)
	b := NewBoard()
	tr := b.Tracker(sonyWatch(), nil, t.Logf)
	f := &feed{t: t, tr: tr, at: time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC), every: checkEvery}
	for i := 0; i < learnReadings; i++ {
		f.poll(camera(view(photo, 0, 0), plain, 3, int64(i)), ReadClean)
	}
	if tr.ref == nil {
		t.Fatal("no reference after three clean readings")
	}
	for i := 0; i < settleChecks+window+2; i++ {
		f.poll(camera(view(photo, 12, 0), plain, 3, int64(50+i)), ReadBad)
	}
	if len(f.news) != 1 || f.news[0].Status.DX != 12 || f.news[0].Status.DY != 0 {
		t.Errorf("moved 12 px right while settling: news %+v, status %+v", f.news, tr.Status())
	}
}

// "The new position is fine" on a moved watch, then a restart before
// the new picture has settled: the warning that was dealt with doesn't
// come back from the database; the watch learns where things are again.
func TestKeptBoxDoesntComeBackMovedAfterARestart(t *testing.T) {
	photo := sony(t)
	store := &memStore{}
	w := sonyWatch()
	f, b := settled(t, w, store, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) })
	for i := 0; i < window+1; i++ {
		f.poll(camera(view(photo, 9, 0), plain, 3, int64(700+i)), ReadBad)
	}
	if store.m["clock"].State != Moved {
		t.Fatalf("saved %+v, want moved", store.m["clock"].State)
	}
	b.Retake("clock")
	f.poll(camera(view(photo, 9, 0), plain, 3, 800), ReadBad)
	if st := NewBoard().Tracker(w, store, t.Logf).Status(); st.State != Learning {
		t.Errorf("restarted right after the box was kept: %+v, want learning", st)
	}
	// Once the new picture settles, a restart keeps it.
	for i := 0; i < settleChecks+1; i++ {
		f.poll(camera(view(photo, 9, 0), plain, 3, int64(900+i)), ReadBad)
	}
	if st := NewBoard().Tracker(w, store, t.Logf).Status(); st.State != Steady {
		t.Errorf("restarted after the kept box settled: %+v, want steady", st)
	}
}

// A covered lens whose box now and then reads a lone digit in the noise
// (the seven-segment decoder does) is still "can't find": one stray
// clean reading isn't the box reading well.
func TestCoveredLensWithAStrayReadingIsStillLost(t *testing.T) {
	photo := sony(t)
	f, _ := settled(t, sonyWatch(), nil, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) })
	coveredAt := f.at.Add(f.every)
	for i := 0; i < int(lostAfter/f.every)+2; i++ {
		rd := ReadBad
		if i%7 == 6 {
			rd = ReadClean
		}
		f.poll(dark(int64(i)), rd)
	}
	if len(f.news) != 1 || f.news[0].State != Lost || !f.news[0].Status.Since.Equal(coveredAt) {
		t.Fatalf("news = %+v, want lost since %v", f.news, coveredAt)
	}
}

// A camera that shakes by a pixel each way between frames (the H2
// verifier's live camera): the move is reported close to the truth, and
// "Move the box with it" a minute later moves the box to within a pixel
// and a half of it. The offset used to be the median of the four checks
// that reported it, less the median of five settle checks, rounded: 12 to
// 15% of reports were more than 2 px off, the box was moved by that, and
// the new reference made the error the new rest position.
func TestShakingCameraOffsetIsAccurate(t *testing.T) {
	region := config.Region{X: 0.4, Y: 0.3, W: 0.2, H: 0.3}
	moves := [][2]float64{{10, 3}, {-7, -19}, {-7, 7}, {20, -5}}
	over2, n, sum := 0, 0, 0.0
	for seed := int64(1); seed <= 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		tr := NewBoard().Tracker(config.Watch{Name: "c", Region: region}, nil, func(string, ...any) {})
		tr.ref = &Reference{FrameW: 800, FrameH: 450, W: 10, H: 10, Scale: 1}
		at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
		var news []Change
		check := func(x, y float64) {
			at = at.Add(checkEvery)
			r := Result{DX: x + float64(rng.Intn(3)-1) + rng.NormFloat64()*0.2, DY: y + float64(rng.Intn(3)-1) + rng.NormFloat64()*0.2,
				Score: 0.9, Local: 0.9, Found: true}
			if ch, ok := tr.judge(r, at); ok {
				news = append(news, ch)
			}
		}
		for i := 0; i < 40; i++ {
			check(0, 0)
		}
		mv := moves[seed%int64(len(moves))]
		for i := 0; i < window+12; i++ {
			check(mv[0], mv[1])
		}
		if len(news) != 1 || news[0].State != Moved {
			t.Fatalf("seed %d, moved %v: news %+v", seed, mv, news)
		}
		n++
		if e := math.Hypot(float64(news[0].Status.DX)-mv[0], float64(news[0].Status.DY)-mv[1]); e > 2 {
			over2++
		}
		box := Shifted(region, tr.Status())
		ax, ay := (box.X-region.X)*800, (box.Y-region.Y)*450
		e := math.Hypot(ax-mv[0], ay-mv[1])
		sum += e
		if e > 1.5 {
			t.Errorf("seed %d, moved %v: the box would move %.2f, %.2f, %.2f px off", seed, mv, ax, ay, e)
		}
	}
	if over2 > n/50 {
		t.Errorf("%d of %d reports more than 2 px off", over2, n)
	}
	if m := sum / float64(n); m > 0.6 {
		t.Errorf("the box would move %.2f px off on average", m)
	}
}

// Ten checks measured on one of T2's shaking emulated cameras (heat-pump
// fault, checks 65 to 74 of calib/t2_v4.csv), which never moved. Whether
// the picture moved is decided from the median of the five settle
// checks, the rule T2's 316,272 checks were run through with no false
// move. Deciding it from the finer mean instead, the window here came
// out at 4.50 px with a 4.5 px threshold: a false move.
func TestShakyStartIsNotAMove(t *testing.T) {
	checks := [][2]float64{{-1.13, 1.56}, {-3.10, 2.19}, {-1.57, 2.40}, {0.46, -1.47}, {-4.55, 0.15},
		{-1.96, -2.84}, {-1.43, -3.42}, {-1.07, -1.91}, {-0.74, -2.72}, {-2.16, -2.08},
		{0.78, 0.03}, {0.09, -0.88}, {0.04, 0.77}, {-3.56, 1.43}, {-0.52, 0.18}}
	tr := NewBoard().Tracker(config.Watch{Name: "t2", Region: config.Region{X: 0.4, Y: 0.3, W: 0.2, H: 0.3}}, nil, func(string, ...any) {})
	tr.ref = &Reference{FrameW: 800, FrameH: 450, W: 10, H: 10, Scale: 1}
	at := time.Date(2026, 10, 21, 22, 36, 0, 0, time.UTC)
	for i, c := range checks {
		at = at.Add(6 * time.Second)
		if ch, ok := tr.judge(Result{DX: c[0], DY: c[1], Score: 0.95, Local: 0.95, Found: true}, at); ok {
			t.Fatalf("check %d: %+v", i, ch)
		}
	}
	if st := tr.Status(); st.State != Steady {
		t.Errorf("status %+v, want steady", st)
	}
}

// A small move on a camera that shakes: the check just before the move
// caught the picture at the edge of its shake, toward where it then went,
// so it is within agree pixels of the first checks after the move, and
// the window that reports the move takes it in. It used to count in the
// move's offset and its time: a report a check early and short of the
// move (the re-verifier's 5 px moves on the clock photo: 2.24 px off in
// 2 runs of 97, and that number is the one the alert keeps).
func TestFirstReportLeavesOutTheCheckBeforeTheMove(t *testing.T) {
	tr := NewBoard().Tracker(config.Watch{Name: "c", Region: config.Region{X: 0.4, Y: 0.3, W: 0.2, H: 0.3}}, nil, func(string, ...any) {})
	tr.ref = &Reference{FrameW: 800, FrameH: 450, W: 10, H: 10, Scale: 1}
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	var news []Change
	check := func(x, y float64) time.Time {
		at = at.Add(checkEvery)
		if ch, ok := tr.judge(Result{DX: x, DY: y, Score: 0.9, Local: 0.9, Found: true}, at); ok {
			news = append(news, ch)
		}
		return at
	}
	// Settling with the shake to the left: the median is -1, 0. Then
	// steady checks at 0, 0 bring "not moved" for the offset to -1/3, 0.
	for _, c := range [][2]float64{{-1, 0}, {-1, 1}, {-1, -1}, {-1, 0}, {0, 0}, {0, 0}, {0, 0}, {0, 0}, {0, 0}, {0, 0}, {0, 0}, {0, 0}} {
		check(c[0], c[1])
	}
	if st := tr.Status(); st.State != Steady || math.Abs(tr.baseX+1.0/3) > 1e-9 || tr.baseY != 0 {
		t.Fatalf("settled: %+v, base %.3f,%.3f; want steady, base -0.333,0", st, tr.baseX, tr.baseY)
	}
	check(1.1, 1) // before the move, shaken right and down
	movedAt := check(4, 1)
	check(4, 0)
	if len(news) != 0 {
		t.Fatalf("reported before four checks: %+v", news)
	}
	check(4, 1)
	if len(news) != 1 || news[0].State != Moved {
		t.Fatalf("want the move reported on its third check, with the one before it in the window: %+v", news)
	}
	st := news[0].Status
	wantX, wantY := 4+1.0/3, 2.0/3 // the mean of the three checks after the move, from -1/3, 0
	if !st.Since.Equal(movedAt) || math.Abs(st.FX-wantX) > 0.01 || math.Abs(st.FY-wantY) > 0.01 || st.DX != 4 || st.DY != 1 {
		t.Errorf("report %+v; want moved %.2f,%.2f (4,1) since %v", st, wantX, wantY, movedAt.Format("15:04:05"))
	}
}

// Moved, then the lens covered until the picture is lost, then uncovered
// with the picture still moved: that is the move already reported, not a
// new one, with or without a restart while it was covered. The warning
// comes back with its first time at once. It used to go steady on the
// first check back in view, log "is back", and report the move again
// four checks later: a second move alert.
func TestMovedCoveredUncoveredIsOneMove(t *testing.T) {
	photo := sony(t)
	for _, restart := range []bool{false, true} {
		store := &memStore{}
		f, _ := settled(t, sonyWatch(), store, func(s int64) image.Image { return camera(view(photo, 0, 0), plain, 3, s) })
		for i := 0; i < window+4; i++ {
			f.poll(camera(view(photo, 8, -3), plain, 3, int64(300+i)), ReadBad)
		}
		if len(f.news) != 1 || f.news[0].State != Moved {
			t.Fatalf("moved: news %+v", f.news)
		}
		since := f.news[0].Status.Since
		for i := 0; i < int(lostAfter/f.every)+2; i++ {
			f.poll(dark(int64(i)), ReadBad)
		}
		if len(f.news) != 2 || f.news[1].State != Lost {
			t.Fatalf("covered: news %+v", f.news)
		}
		if restart {
			tr := NewBoard().Tracker(sonyWatch(), store, t.Logf)
			if st := tr.Status(); st.State != Lost {
				t.Fatalf("restarted while covered: %+v, want lost", st)
			}
			f = &feed{t: t, tr: tr, at: f.at.Add(time.Minute), every: checkEvery}
		}
		f.news = nil
		f.poll(camera(view(photo, 8, -3), plain, 3, 500), ReadBad)
		if st := f.tr.Status(); st.State != Moved || st.DX != 8 || st.DY != -3 || !st.Since.Equal(since) {
			t.Errorf("restart %v: back in view, still moved: %+v, want moved 8,-3 since %v", restart, st, since)
		}
		for i := 1; i < window+6; i++ {
			f.poll(camera(view(photo, 8, -3), plain, 3, int64(500+i)), ReadBad)
		}
		if st := f.tr.Status(); len(f.news) != 0 || st.State != Moved || !st.Since.Equal(since) {
			t.Errorf("restart %v: uncovered: %+v, news %+v; want the same move and no news", restart, st, f.news)
		}
		// And when it is back where it was, the warning goes.
		for i := 0; i < window+1; i++ {
			f.poll(camera(view(photo, 0, 0), plain, 3, int64(600+i)), ReadClean)
		}
		if st := f.tr.Status(); st.State != Steady || len(f.news) != 0 {
			t.Errorf("restart %v: moved back: %+v, news %+v", restart, st, f.news)
		}
	}
}
