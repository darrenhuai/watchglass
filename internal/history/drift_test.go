package history

import (
	"image"
	"reflect"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/drift"
)

func TestDriftRoundTripAcrossARestart(t *testing.T) {
	s, path := openTemp(t)
	if _, ok, err := s.LoadDrift("clock"); ok || err != nil {
		t.Fatalf("nothing saved yet: ok=%v err=%v", ok, err)
	}
	// Lost after a move: the move's offset and when it was seen are kept.
	want := drift.Saved{Key: "k1", BaseX: 0.5, BaseY: -1.25, MeanX: 0.75, MeanY: -1.5, State: drift.Lost, DX: 14.25, DY: 3,
		Since: time.Date(2026, 10, 28, 14, 2, 0, 0, time.UTC), Shake: 1.5,
		MovedSince: time.Date(2026, 10, 28, 13, 50, 0, 0, time.UTC),
		Ref: drift.Reference{FrameW: 640, FrameH: 480, Box: image.Rect(200, 180, 400, 250), Area: image.Rect(80, 60, 520, 370),
			Scale: 3, W: 4, H: 2, Gray: []uint8{1, 2, 3, 4, 5, 6, 7, 250}}}
	if err := s.SaveDrift("clock", want); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, ok, err := s2.LoadDrift("clock")
	if !ok || err != nil {
		t.Fatalf("after reopening: ok=%v err=%v", ok, err)
	}
	got.Since, got.MovedSince = got.Since.UTC(), got.MovedSince.UTC()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	// A deleted watch's row goes; the others stay.
	if err := s2.SaveDrift("other", want); err != nil {
		t.Fatal(err)
	}
	if err := s2.KeepDrift([]string{"other"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s2.LoadDrift("clock"); ok {
		t.Error("the deleted watch's position is still saved")
	}
	if _, ok, _ := s2.LoadDrift("other"); !ok {
		t.Error("the kept watch's position is gone")
	}
}
