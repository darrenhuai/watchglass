package config

import (
	"strings"
	"testing"
	"time"
)

// lettersYAML is a boiler watch as a person would write it, with comments
// around the lines a letters: key lands between.
const lettersYAML = `# The boiler in the cellar.
watches:
  - name: boiler
    source: http://192.168.1.71/snapshot.jpg
    interval: 30s
    region: {x: 0.3875, y: 0.3111, w: 0.225, h: 0.3111}
    engine: sevenseg # two blue digits
    trigger:
      type: ocr_match
      pattern: "^E[0-9]+$" # a fault code
      confirm: 3
    notify: []
`

func TestLettersLoadsAndValidates(t *testing.T) {
	cfg := loadOrFatal(t, writeTemp(t, lettersYAML))
	if cfg.Watches[0].Letters {
		t.Fatal("letters should be off unless the file says so")
	}
	cfg = loadOrFatal(t, writeTemp(t, strings.Replace(lettersYAML, "    engine: sevenseg", "    engine: sevenseg\n    letters: true", 1)))
	if !cfg.Watches[0].Letters {
		t.Fatal("letters: true not loaded")
	}
	// Only a sevenseg watch reads letters; set elsewhere it would be
	// silently ignored, so it is refused with the watch named.
	for _, engine := range []string{"", "tesseract", "rapidocr"} {
		c := &Config{Watches: []Watch{{
			Name: "a", Source: "http://x", Region: Region{W: 1, H: 1}, Engine: engine, Letters: true,
			Trigger: Trigger{Type: "ocr_changed"},
		}}}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), `watch "a": letters only applies to engine sevenseg`) {
			t.Errorf("engine %q with letters: err = %v", engine, err)
		}
	}
}

// Setting Letters writes one letters: line into the file and nothing else
// moves, comments included; clearing it deletes the line again; a watch
// that never had it never gains "letters: false".
func TestSaveLettersKeepsCommentsAndOmitsFalse(t *testing.T) {
	p := writeTemp(t, lettersYAML)
	cfg := loadOrFatal(t, p)
	cfg.Watches[0].Letters = true
	saveOrFatal(t, p, cfg)
	after := readFile(t, p)
	if !strings.Contains(after, "\n    letters: true\n") {
		t.Fatalf("letters: true not written:\n%s", after)
	}
	sb, sa := squash(lettersYAML), squash(after)
	if len(sa) != len(sb)+1 {
		t.Fatalf("expected exactly one new line, got %d -> %d:\n%s", len(sb), len(sa), after)
	}
	i := 0
	for _, line := range sa {
		if i < len(sb) && line == sb[i] {
			i++
		}
	}
	if i != len(sb) {
		t.Errorf("a line of the original was changed or lost (matched %d of %d):\n%s", i, len(sb), after)
	}
	for _, want := range []string{"# The boiler in the cellar.", "# two blue digits", "# a fault code"} {
		if !strings.Contains(after, want) {
			t.Errorf("lost comment %q:\n%s", want, after)
		}
	}
	got := loadOrFatal(t, p)
	if !got.Watches[0].Letters {
		t.Fatal("letters not read back")
	}
	got.Watches[0].Letters = false
	saveOrFatal(t, p, got)
	back := readFile(t, p)
	if strings.Contains(back, "letters") {
		t.Errorf("letters back at false must delete the key:\n%s", back)
	}
	if strings.Join(squash(back), "\n") != strings.Join(sb, "\n") {
		t.Errorf("file after setting and clearing letters differs from the original:\n%s", back)
	}
	// An unrelated save never adds letters: false.
	p2 := writeTemp(t, lettersYAML)
	cfg2 := loadOrFatal(t, p2)
	cfg2.Watches[0].Interval = Duration(45 * time.Second)
	saveOrFatal(t, p2, cfg2)
	if after := readFile(t, p2); strings.Contains(after, "letters") || !strings.Contains(after, "interval: 45s") {
		t.Errorf("an unrelated save wrote letters, or lost the change:\n%s", after)
	}
}
