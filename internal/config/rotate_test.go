package config

import (
	"strings"
	"testing"
	"time"
)

// rotateYAML is a small file with comments around the block a rotate: key
// lands in, written the way a person would annotate it.
const rotateYAML = `# The meter in the cellar. The camera is screwed in on its side.
watches:
  - name: meter
    source: http://192.168.1.50/snapshot.jpg
    interval: 30s
    # The box is drawn on the picture as the camera sends it.
    region: {x: 0.4, y: 0.3, w: 0.2, h: 0.6}
    preprocess:
      grayscale: true # the LCD has a green tint
    engine: sevenseg
    trigger:
      type: ocr_changed # tell me about every new reading
    notify: []
`

func rotateConfig(deg int) *Config {
	return &Config{Watches: []Watch{{
		Name: "a", Source: "http://x",
		Region:     Region{X: 0, Y: 0, W: 1, H: 1},
		Preprocess: Preprocess{Rotate: deg},
		Trigger:    Trigger{Type: "ocr_changed"},
	}}}
}

func TestValidateRotate(t *testing.T) {
	for _, deg := range []int{0, 90, 180, 270} {
		if err := rotateConfig(deg).Validate(); err != nil {
			t.Errorf("rotate %d: rejected: %v", deg, err)
		}
		if !ValidRotate(deg) {
			t.Errorf("ValidRotate(%d) = false", deg)
		}
	}
	for _, deg := range []int{1, 45, 89, 91, -90, 360, 450} {
		err := rotateConfig(deg).Validate()
		if err == nil {
			t.Errorf("rotate %d: expected an error", deg)
			continue
		}
		// The message names the watch and all four values it takes.
		for _, want := range []string{`watch "a"`, "preprocess rotate must be 0, 90, 180 or 270"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("rotate %d: error %q lacks %q", deg, err, want)
			}
		}
		if ValidRotate(deg) {
			t.Errorf("ValidRotate(%d) = true", deg)
		}
	}
}

func TestRotateLoadsFromYAML(t *testing.T) {
	file := func(line string) string {
		return "watches:\n  - name: a\n    source: http://x\n    region: {x: 0, y: 0, w: 1, h: 1}\n" + line + "    trigger: {type: ocr_changed}\n"
	}
	for line, want := range map[string]int{
		"":                               0,
		"    preprocess: {rotate: 0}\n":  0,
		"    preprocess: {rotate: 90}\n": 90,
		"    preprocess:\n      rotate: 180\n      grayscale: true\n": 180,
		"    preprocess: {rotate: 270}\n":                             270,
	} {
		cfg, err := Load(writeTemp(t, file(line)))
		if err != nil {
			t.Errorf("%q: %v", line, err)
			continue
		}
		if got := cfg.Watches[0].Preprocess.Rotate; got != want {
			t.Errorf("%q: rotate = %d, want %d", line, got, want)
		}
	}
	for _, bad := range []string{"45", "-90", "360", "91"} {
		_, err := Load(writeTemp(t, file("    preprocess: {rotate: "+bad+"}\n")))
		if err == nil || !strings.Contains(err.Error(), "preprocess rotate must be 0, 90, 180 or 270") {
			t.Errorf("rotate: %s: err = %v, want the message that names the four values", bad, err)
		}
	}
}

// TestSaveRotateKeepsCommentsAndOmitsZero: setting Rotate writes one
// rotate: line into the file and nothing else moves; setting it back to 0
// deletes the line again; a watch that never had it never gains
// "rotate: 0".
func TestSaveRotateKeepsCommentsAndOmitsZero(t *testing.T) {
	p := writeTemp(t, rotateYAML)
	cfg := loadOrFatal(t, p)
	cfg.Watches[0].Preprocess.Rotate = 90
	saveOrFatal(t, p, cfg)
	after := readFile(t, p)

	if !strings.Contains(after, "\n      rotate: 90\n") {
		t.Fatalf("rotate: 90 not written under preprocess:\n%s", after)
	}
	sb, sa := squash(rotateYAML), squash(after)
	if len(sa) != len(sb)+1 {
		t.Fatalf("expected exactly one new line, got %d -> %d:\n%s", len(sb), len(sa), after)
	}
	// Every line the file had is still there, comments included, in order.
	i := 0
	for _, line := range sa {
		if i < len(sb) && line == sb[i] {
			i++
		}
	}
	if i != len(sb) {
		t.Errorf("a line of the original was changed or lost (matched %d of %d):\n%s", i, len(sb), after)
	}
	if nb, na := len(commentLine.FindAllString(rotateYAML, -1)), len(commentLine.FindAllString(after, -1)); na != nb {
		t.Errorf("comment lines: %d before, %d after\n%s", nb, na, after)
	}
	for _, want := range []string{"# the LCD has a green tint", "# tell me about every new reading", "# The box is drawn on the picture as the camera sends it."} {
		if !strings.Contains(after, want) {
			t.Errorf("lost comment %q:\n%s", want, after)
		}
	}
	got := loadOrFatal(t, p)
	if got.Watches[0].Preprocess != (Preprocess{Rotate: 90, Grayscale: true}) {
		t.Errorf("preprocess after save = %+v", got.Watches[0].Preprocess)
	}

	// Back to none: the key goes, the rest of the block and its comment stay.
	got.Watches[0].Preprocess.Rotate = 0
	saveOrFatal(t, p, got)
	back := readFile(t, p)
	if strings.Contains(back, "rotate") {
		t.Errorf("rotate back at 0 must delete the key:\n%s", back)
	}
	if strings.Join(squash(back), "\n") != strings.Join(sb, "\n") {
		t.Errorf("file after setting and clearing rotate differs from the original:\n%s", back)
	}

	// Rotate as the only preprocess setting: the block is written with it
	// and goes away with it.
	bare := strings.Replace(rotateYAML, "    preprocess:\n      grayscale: true # the LCD has a green tint\n", "", 1)
	p2 := writeTemp(t, bare)
	cfg2 := loadOrFatal(t, p2)
	cfg2.Watches[0].Preprocess.Rotate = 270
	saveOrFatal(t, p2, cfg2)
	if after := readFile(t, p2); !strings.Contains(after, "rotate: 270") || strings.Count(after, "#") != strings.Count(bare, "#") {
		t.Errorf("rotate: 270 not written, or a comment lost:\n%s", after)
	}
	cfg2 = loadOrFatal(t, p2)
	if cfg2.Watches[0].Preprocess.Rotate != 270 {
		t.Fatalf("rotate = %d, want 270", cfg2.Watches[0].Preprocess.Rotate)
	}
	cfg2.Watches[0].Preprocess.Rotate = 0
	saveOrFatal(t, p2, cfg2)
	if after := readFile(t, p2); strings.Contains(after, "rotate") || strings.Contains(after, "preprocess") {
		t.Errorf("cleared rotate left the key or an empty preprocess block:\n%s", after)
	}

	// A save that changes something else in a watch with a preprocess block
	// but no turn doesn't add "rotate: 0" to that block.
	p4 := writeTemp(t, rotateYAML)
	cfg4 := loadOrFatal(t, p4)
	cfg4.Watches[0].Interval = Duration(45 * time.Second)
	saveOrFatal(t, p4, cfg4)
	if after := readFile(t, p4); strings.Contains(after, "rotate") || !strings.Contains(after, "interval: 45s") {
		t.Errorf("an unrelated save wrote rotate, or lost the change:\n%s", after)
	}

	// A fresh file: a watch without a turn never gets "rotate: 0".
	p3 := writeTemp(t, "watches: []\n")
	saveOrFatal(t, p3, rotateConfig(0))
	if after := readFile(t, p3); strings.Contains(after, "rotate") {
		t.Errorf("rotate: 0 was written:\n%s", after)
	}
}
