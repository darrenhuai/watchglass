package web

import (
	"context"
	"net/http"
	"time"
)

// frameSlots caps how many of the UI's own camera requests (the snapshot
// on a watch's page, Test this region) can hold a decoded frame at once.
// A frame from a 16 MP camera is 64 MiB as RGBA, and a pixel_change Test
// keeps its crop through a 1-3 s wait, a second grab and the PNG
// encoding, so without a cap a burst of Tests (a page left auto-retrying,
// a script, six browser tabs) could cost a Pi-class box all its memory.
// A request that finds every slot taken waits frameSlotWait for one and
// is then refused with a 503 that says so.
const frameSlots = 4

// frameSlotWait is how long a request waits for a slot before it is
// refused; tests shorten it.
var frameSlotWait = 2 * time.Second

// busyTestText and busySnapshotText are the 503 bodies (text/plain, like
// grabError's) for a Test and a snapshot that found no slot.
const (
	busyTestText     = "Another test is still running. Try again in a moment."
	busySnapshotText = "watchglass is busy with tests. The picture refreshes when they are done."
)

// takeFrameSlot takes one of the frameSlots for the request, waiting up to
// frameSlotWait, and answers w with a 503 when none came free (the caller
// returns then). release gives the slot back.
func (s *Server) takeFrameSlot(w http.ResponseWriter, r *http.Request, busy string) (release func(), ok bool) {
	ctx, cancel := context.WithTimeout(r.Context(), frameSlotWait)
	defer cancel()
	select {
	case s.frames <- struct{}{}:
		return func() { <-s.frames }, true
	case <-ctx.Done():
		http.Error(w, busy, http.StatusServiceUnavailable)
		return nil, false
	}
}
