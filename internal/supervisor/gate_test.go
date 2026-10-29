package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// A gated watch whose gate never opens (here: a pattern the display never
// shows) still tells OnEvent it is polling, with the zero Event and no
// crop, so the Home Assistant publisher seeds its health topic "online"
// as it does from a first reading. The registry, which Live and the
// dashboard read samples from, gets nothing.
func TestGateThatNeverOpensStillReachesOnEvent(t *testing.T) {
	reg := state.New(5)
	s := New(nil, reg, ocr.Engines{}, func(string, ...any) {})
	s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: fixtureImage(t)}, nil }
	type got struct {
		ev  trigger.Event
		png int
	}
	ch := make(chan got, 64)
	s.OnEvent = func(watch string, ev trigger.Event, png []byte) {
		if watch == "meter" {
			select {
			case ch <- got{ev, len(png)}:
			default:
			}
		}
	}
	w := testWatch("meter")
	w.Interval = config.Duration(30 * time.Millisecond)
	w.Engine = "sevenseg"
	w.Trigger = config.Trigger{Type: "value", Confirm: 1}
	w.Gate = &config.Gate{Region: config.Region{X: 0, Y: 0, W: 1, H: 1}, Pattern: `^1\.8\.0$`}
	if err := s.Start(context.Background(), w); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()
	for i := 0; i < 2; i++ {
		select {
		case g := <-ch:
			if g.ev != (trigger.Event{}) || g.png != 0 {
				t.Fatalf("a closed poll reached OnEvent as %+v with %d bytes of crop", g.ev, g.png)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("OnEvent never heard the closed polls")
		}
	}
	if st, ok := s.Gate("meter"); !ok || !st.HasRead || st.Open {
		t.Errorf("Gate = %+v, %v; want read and closed", st, ok)
	}
	if _, ok := reg.Latest("meter"); ok {
		t.Error("a closed poll left a sample in the registry")
	}
}
