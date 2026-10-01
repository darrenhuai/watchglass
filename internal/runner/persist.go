package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// A watch's trigger state (when it last fired, what it had settled on) is
// kept in the history database, so a restart doesn't re-send an alert that
// already went out. All of it happens on two goroutines that never overlap:
// New reads it on the caller's, before Run exists, and Tick writes it on
// the poll goroutine. The supervisor waits for a watch's poll goroutine to
// exit before it starts the next runner for the same name.

// restoreFloor is the shortest time a settled state stays good for after
// the watch last read its region: long enough for a Save, an update or a
// reboot.
const restoreFloor = 15 * time.Minute

// restoreWindow is how long after its last reading a watch's settled state
// is still taken to be about what is on the display now. A restart inside
// it carries on; after it the watch starts fresh, so a condition that is
// met when watchglass comes back from a day off is reported, as news. The
// cooldown extends it: the user has said that long is too soon for a
// repeat. So does a slow poll, where one gap can outlast the floor.
func restoreWindow(w config.Watch) time.Duration {
	d := restoreFloor
	if c := time.Duration(w.Trigger.Cooldown); c > d {
		d = c
	}
	gap := time.Duration(w.Interval)
	if m := time.Duration(w.MaxInterval); m > gap {
		gap = m
	}
	if 2*gap > d {
		d = 2 * gap
	}
	return d
}

// Fingerprint identifies the question a watch asks: what it looks at
// (source, region, engine, preprocess) and what it looks for (trigger type,
// pattern, op, threshold). Saved trigger state is only restored under the
// same fingerprint, so editing any of those starts the watch fresh, and
// editing anything else (notify URLs, interval, confirm, cooldown, headers,
// the Home Assistant unit, the camera's password, a field the trigger type
// doesn't read, another spelling of the same preprocess) does not.
//
// It hashes the whole watch with the fields that don't matter cleared,
// rather than listing the ones that do, so a field added to config.Watch
// later counts as part of the question until someone decides otherwise:
// forgetting to exclude one costs a repeat alert when it is edited,
// forgetting to include one would cost a missed alert. The hash is of the
// YAML form, where an omitempty field left at its zero value doesn't
// appear, so adding such a field changes no existing watch's fingerprint.
func Fingerprint(w config.Watch) string {
	w.Name = ""
	w.Interval, w.MaxInterval, w.HealthAfter = 0, 0, 0
	w.Notify, w.Headers, w.TLSInsecure = nil, nil, false
	w.Unit, w.DeviceClass = "", ""
	w.Trigger.Confirm, w.Trigger.Cooldown = 0, 0
	if w.Engine == "" {
		w.Engine = "tesseract" // the default, spelled either way
	}
	// A new camera password is the same camera.
	if u, err := url.Parse(w.Source); err == nil && u.User != nil {
		u.User = nil
		w.Source = u.String()
	}
	// Preprocess as imgproc.Apply reads it, not as it is spelled: upscale 0
	// and 1 are both off (the web form writes 1 back as 0), and a binarize
	// level grays the image whether Grayscale is ticked or not.
	if w.Preprocess.Upscale < 2 {
		w.Preprocess.Upscale = 0
	}
	if w.Preprocess.Threshold > 0 {
		w.Preprocess.Grayscale = false
	}
	// Fields the trigger type never reads, which the web UI may still carry
	// over from another type.
	switch w.Trigger.Type {
	case "pixel_change":
		w.Engine, w.Preprocess = "", config.Preprocess{}
		w.Trigger.Pattern, w.Trigger.Op = "", ""
	case "ocr_match":
		w.Trigger.Op, w.Trigger.Threshold = "", 0
	case "ocr_changed":
		w.Trigger.Pattern, w.Trigger.Op, w.Trigger.Threshold = "", "", 0
	}
	b, err := yaml.Marshal(w)
	if err != nil {
		// Nothing in a Watch fails to marshal; if that ever changes, a
		// fingerprint that matches nothing only means starting fresh.
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// restoreState loads the watch's saved trigger state into the evaluator if
// it still applies. The fire time is restored whenever the fingerprint
// matches, so a running cooldown ends when it was going to. The settled
// state is restored only inside restoreWindow of the watch's last reading.
// Whatever the outcome, the saved row is left agreeing with the evaluator:
// state that was not restored is forgotten for good, not picked up by the
// restart after this one.
func (r *Runner) restoreState(now time.Time) {
	if r.store == nil {
		return
	}
	name := r.watch.Name
	r.fingerprint = Fingerprint(r.watch)
	row, ok, err := r.store.LoadTriggerState(name)
	if err != nil {
		r.logf("watch %s: couldn't read its saved trigger state, so it starts fresh: %v", name, err)
		return
	}
	if !ok {
		return
	}
	rowState := trigger.State{LastFired: row.LastFired, Stable: row.Stable, HasStable: row.HasStable}
	if row.Fingerprint != r.fingerprint || r.fingerprint == "" {
		r.logf("watch %s: what it looks at or looks for has changed, so it starts fresh", name)
		r.persist(trigger.State{})
		return
	}
	st := trigger.State{LastFired: row.LastFired}
	if row.HasStable {
		polled, ok, err := r.store.LastPolled(name)
		switch {
		case err != nil:
			r.logf("watch %s: couldn't read when it last polled, so it starts fresh: %v", name, err)
		case !ok || now.Sub(polled) > restoreWindow(r.watch):
			ago := "an unknown time"
			if ok {
				ago = now.Sub(polled).Round(time.Second).String()
			}
			r.logf("watch %s: it last read the screen %s ago, too long to assume the display is unchanged, so a condition that holds now counts as new", name, ago)
		default:
			st.Stable, st.HasStable = row.Stable, true
		}
	}
	r.eval.Restore(st)
	st = r.eval.State()
	r.saved = rowState
	if !st.LastFired.IsZero() {
		r.restoredFire = st.LastFired
	}
	if st.HasStable {
		if st.LastFired.IsZero() {
			r.logf("watch %s: carrying on from before the restart", name)
		} else {
			r.logf("watch %s: carrying on from before the restart (last fired %s)", name, st.LastFired.Format("2006-01-02 15:04:05"))
		}
	}
	if !st.Equal(rowState) {
		r.persist(st)
	}
}

// saveState writes the evaluator's state if it changed since the last
// write: after a fire, or when a new settled state commits. Most polls
// change neither and write nothing.
func (r *Runner) saveState() {
	if r.store == nil {
		return
	}
	if st := r.eval.State(); !st.Equal(r.saved) {
		r.persist(st)
	}
}

// persist writes st as the watch's saved state. A failed write is logged
// and tried again on the next poll (r.saved still differs).
func (r *Runner) persist(st trigger.State) {
	err := r.store.SaveTriggerState(r.watch.Name, history.TriggerState{
		Fingerprint: r.fingerprint, LastFired: st.LastFired, Stable: st.Stable, HasStable: st.HasStable})
	if err != nil {
		r.logf("watch %s: save trigger state: %v", r.watch.Name, err)
		return
	}
	r.saved = st
}

// RestoredFire is when the watch last fired according to the state New
// restored, or false if it restored none. The supervisor uses it to tell
// the registry, which is in memory and would otherwise say the watch has
// never fired until it fires again.
func (r *Runner) RestoredFire() (time.Time, bool) {
	return r.restoredFire, !r.restoredFire.IsZero()
}
