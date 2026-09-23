package config

import (
	"strings"
	"testing"
)

// A region dragged to the frame's edge arrives with each edge rounded to
// four decimals on its own, so x+w can read 1.0001; that is the edge, not a
// region outside the frame. Clamp absorbs less than two grid steps of
// overshoot and nothing more, and Validate keeps the clamped region.
func TestRegionClampAbsorbsEdgeRounding(t *testing.T) {
	cases := []struct {
		in   Region
		want Region
	}{
		{Region{X: 0.0063, Y: 0, W: 0.9938, H: 1}, Region{X: 0.0063, Y: 0, W: 1 - 0.0063, H: 1}},
		{Region{X: 0, Y: 0.0556, W: 1, H: 0.9445}, Region{X: 0, Y: 0.0556, W: 1, H: 1 - 0.0556}},
		{Region{X: 0.5, Y: 0.5, W: 0.5 + 1e-9, H: 0.5 + 1e-9}, Region{X: 0.5, Y: 0.5, W: 0.5, H: 0.5}},
		{Region{X: 0.5, Y: 0.5, W: 0.5002, H: 0.5}, Region{X: 0.5, Y: 0.5, W: 0.5002, H: 0.5}}, // two steps over: left alone
		{Region{X: 0.5, Y: 0.5, W: 0.6, H: 0.5}, Region{X: 0.5, Y: 0.5, W: 0.6, H: 0.5}},
		{Region{X: 0.25, Y: 0.25, W: 0.5, H: 0.5}, Region{X: 0.25, Y: 0.25, W: 0.5, H: 0.5}},
	}
	for _, c := range cases {
		if got := c.in.Clamp(); got != c.want {
			t.Errorf("Clamp(%+v) = %+v, want %+v", c.in, got, c.want)
		}
	}
	mk := func(r Region) *Config {
		return &Config{Watches: []Watch{{
			Name: "a", Source: "http://x/s.jpg", Region: r,
			Trigger: Trigger{Type: "pixel_change", Threshold: 10},
		}}}
	}
	edge := mk(Region{X: 0.0063, Y: 0.0556, W: 0.9938, H: 0.9445})
	if err := edge.Validate(); err != nil {
		t.Fatalf("an edge region a rounding step past 1 must validate: %v", err)
	}
	if r := edge.Watches[0].Region; r.X+r.W > 1 || r.Y+r.H > 1 || r.W < 0.99 || r.H < 0.94 {
		t.Errorf("Validate should keep the clamped region, got %+v", r)
	}
	for _, bad := range []Region{{X: 0.5, Y: 0, W: 0.5002, H: 1}, {X: 0.5, Y: 0.5, W: 0.6, H: 0.5}, {X: 0, Y: 0, W: 1.001, H: 1}} {
		if err := mk(bad).Validate(); err == nil || !strings.Contains(err.Error(), "region must be normalized") {
			t.Errorf("region %+v should still be rejected, got %v", bad, err)
		}
	}
}
