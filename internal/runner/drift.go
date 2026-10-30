package runner

import (
	"context"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/drift"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// observeDrift hands the poll's whole frame to the watch's position check
// (Drift, set by the supervisor; nil when the watch has check_moves:
// false), with what the reading was like. A move, or a picture that can't
// be found any more, is logged by the check and shown on the page; with
// move_alert it is also an alert, sent like the others. A crash in the
// check is logged and costs nothing else: the reading is already made.
func (r *Runner) observeDrift(ctx context.Context, img image.Image, at time.Time, ev trigger.Event) {
	if r.Drift == nil {
		return
	}
	rd := driftRead(r.watch, ev)
	var (
		ch   drift.Change
		news bool
	)
	if err := Contain(func() error { ch, news = r.Drift.Observe(img, at, rd); return nil }); err != nil {
		r.logf("watch %s: checking whether the camera moved crashed; this is a bug, please report it: %v", r.watch.Name, err)
		return
	}
	r.isMoved = r.Drift.Status().State == drift.Moved || r.Drift.Status().State == drift.Lost
	if !news || !r.watch.MoveAlert {
		return
	}
	title := fmt.Sprintf("watchglass: %s (camera moved)", r.watch.Name)
	body := fmt.Sprintf("%s: the picture around the box moved about %s since %s. Readings may be wrong until the box is moved.",
		r.watch.Name, drift.Offset(ch.Status.DX, ch.Status.DY), ch.Status.Since.Format("15:04"))
	if ch.State == drift.Lost {
		title = fmt.Sprintf("watchglass: %s (box not in view)", r.watch.Name)
		body = fmt.Sprintf("%s: the picture around the box hasn't been in the camera's view since %s. Is the camera covered or pointed elsewhere?",
			r.watch.Name, ch.Status.Since.Format("15:04"))
	}
	r.raise(ctx, alert{ts: at, kind: "moved", title: title, body: body})
}

// driftRead is what the poll's reading was like, for the position check.
// A clean reading is text with no unsure digit. The seven-segment
// decoder's '?' is a digit it couldn't read, so a reading with one ("?"
// on a covered lens, "1?:?4" on a box half off the digits) is a bad
// reading, as the trigger and the page take it too: it neither teaches
// the check a reference nor tells it the box is still in view.
func driftRead(w config.Watch, ev trigger.Event) drift.Read {
	if w.Trigger.Type == "pixel_change" {
		return drift.ReadNone
	}
	text := strings.TrimSpace(ev.Reading)
	if text == "" || ev.Unsure || (w.Engine == "sevenseg" && strings.Contains(text, "?")) {
		return drift.ReadBad
	}
	return drift.ReadClean
}
