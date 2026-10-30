package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/drift"
)

// driftView is what the page says about a watch's position
// (internal/drift): the picture around its box moved, or can't be found,
// or the area is too plain to tell.
type driftView struct {
	State  drift.State
	Offset string // Moved: "14 px right and 3 px down"
	DX, DY int
	Since  time.Time
	// Name and Base build the action URLs in live.html.
	Name, Base string
}

// Sentence is the warning as one line, for the dashboard's title and
// the Live panel; times in the server's zone.
func (d driftView) Sentence() string {
	switch d.State {
	case drift.Moved:
		return "The picture around this box moved about " + d.Offset + " since " + d.Since.Format("15:04") + ". Readings may be wrong until the box is moved."
	case drift.Lost:
		return "The picture around this box hasn't been in view since " + d.Since.Format("15:04") + ". Is the camera covered or pointed elsewhere? Readings may be wrong until it is back."
	}
	return ""
}

// Warn says whether the view is a warning (Moved or Lost).
func (d driftView) Warn() bool { return d.State == drift.Moved || d.State == drift.Lost }

// driftFor is the watch's position view, or nil when there is nothing
// to say: the check is off, still learning, or the picture is where it
// was.
func (s *Server) driftFor(r *http.Request, name string) *driftView {
	st, ok := s.sup.Drift.Status(name)
	if !ok {
		return nil
	}
	switch st.State {
	case drift.Moved, drift.Lost, drift.Unusable:
		return &driftView{State: st.State, Offset: drift.Offset(st.DX, st.DY), DX: st.DX, DY: st.DY, Since: st.Since,
			Name: name, Base: s.base(r)}
	}
	return nil
}

// driftWarning is the dashboard row's marker title, or "".
func (s *Server) driftWarning(r *http.Request, name string) string {
	if d := s.driftFor(r, name); d != nil && d.Warn() {
		return d.Sentence()
	}
	return ""
}

// moveBox moves the watch's box by the offset its position check
// measured, saves the config (comments kept, as every save), restarts the
// watch and has it take a new reference from its first frame. The form
// carries the offset the page showed: if the check has measured another
// one since, nothing is moved and the page says so.
func (s *Server) moveBox(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.findWatch(name); !ok {
		http.NotFound(w, r)
		return
	}
	st, ok := s.sup.Drift.Status(name)
	if !ok || st.State != drift.Moved {
		s.setFlash(w, r, "drift-gone", name, "")
		http.Redirect(w, r, watchURL(s.base(r), name, ""), http.StatusSeeOther)
		return
	}
	if r.FormValue("dx") != strconv.Itoa(st.DX) || r.FormValue("dy") != strconv.Itoa(st.DY) {
		s.setFlash(w, r, "drift-changed", name, "")
		http.Redirect(w, r, watchURL(s.base(r), name, ""), http.StatusSeeOther)
		return
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	var moved config.Region
	err := s.mutateConfig(func(c *config.Config) error {
		for i := range c.Watches {
			if c.Watches[i].Name == name {
				moved = drift.Shifted(c.Watches[i].Region, st)
				c.Watches[i].Region = moved
				return nil
			}
		}
		return errWatchVanished
	})
	if err != nil {
		msg := "watchglass couldn't write " + s.configFile() + ", so the box wasn't moved and the watch keeps running as it was."
		switch {
		case errors.Is(err, errWatchVanished):
			msg = "This watch was deleted, so there was no box to move."
		case !errors.Is(err, errSaveFailed):
			msg = "The rest of " + s.configFile() + " didn't validate, so the box wasn't moved and the watch keeps running as it was."
		}
		s.renderError(w, r, statusFor(err), errorPageData{
			Title: "Couldn't move the box of " + name, Message: msg, Detail: err.Error(),
			BackURL: "/watch/" + url.PathEscape(name), BackLabel: "Back to " + name,
		})
		return
	}
	canonical, _ := s.findWatch(name)
	s.sup.Stop(name)
	s.reg.DropSamples(name)
	s.sup.Drift.Retake(name)
	if err := s.sup.Start(s.RunCtx, canonical); err != nil {
		s.logf("ERROR: move box %s: saved, but restart failed: %v — the watch is now stopped", name, err)
		s.renderError(w, r, http.StatusInternalServerError, errorPageData{
			Title:   "Moved the box, but the watch didn't restart",
			Message: "The new box is in " + s.configFile() + ", but " + name + " is stopped until you fix this and save again.",
			Reason:  friendlyStartError(err), Detail: err.Error(),
			BackURL: "/watch/" + url.PathEscape(name), BackLabel: "Back to " + name,
		})
		return
	}
	s.notifyConfigChanged()
	s.logf("watch %s: moved its box %s (now x=%g y=%g) and saved it", name, drift.Offset(st.DX, st.DY), moved.X, moved.Y)
	s.setFlash(w, r, "drift-moved", drift.Offset(st.DX, st.DY), "")
	http.Redirect(w, r, watchURL(s.base(r), name, ""), http.StatusSeeOther)
}

// keepBox leaves the box where it is and has the watch take a new
// reference from its next frame: the new picture is the right one.
func (s *Server) keepBox(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.findWatch(name); !ok {
		http.NotFound(w, r)
		return
	}
	st, ok := s.sup.Drift.Status(name)
	if !ok || (st.State != drift.Moved && st.State != drift.Lost) {
		s.setFlash(w, r, "drift-gone", name, "")
		http.Redirect(w, r, watchURL(s.base(r), name, ""), http.StatusSeeOther)
		return
	}
	s.sup.Drift.Retake(name)
	s.logf("watch %s: its box stays where it is; it takes a new picture of the area around it", name)
	s.setFlash(w, r, "drift-kept", name, "")
	http.Redirect(w, r, watchURL(s.base(r), name, ""), http.StatusSeeOther)
}
