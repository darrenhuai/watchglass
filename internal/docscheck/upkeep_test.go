package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readDoc(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// section returns the text under a "## heading" up to the next heading of
// the same level, or "" if there is no such heading.
func section(doc, heading string) string {
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		return ""
	}
	rest := doc[i+len(heading)+2:]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

var demoRunRe = regexp.MustCompile(`docker run --rm\b.*ghcr\.io/darrenhuai/watchglass`)

// TestDemoLinesPullTheNewestImage: Docker keeps the image it pulled first,
// so a demo line without --pull always shows whatever release was out the
// first time someone ran it (a cached 0.1.10 while 0.1.11 was out).
func TestDemoLinesPullTheNewestImage(t *testing.T) {
	n := 0
	for _, rel := range []string{"README.md", "docs/install.md"} {
		for i, l := range strings.Split(readDoc(t, rel), "\n") {
			if !demoRunRe.MatchString(l) {
				continue
			}
			n++
			if !strings.Contains(l, "--pull always") {
				t.Errorf("%s:%d: the demo line should pull the newest image with --pull always:\n%s", rel, i+1, l)
			}
		}
	}
	if n < 2 {
		t.Errorf("found %d demo lines in README.md and docs/install.md, want one in each", n)
	}
}

// TestDocsSayHowToUpdate: install.md and the add-on's docs each say how to
// update, the README points there, and install.md's docker run update
// removes the container its docker run sample creates.
func TestDocsSayHowToUpdate(t *testing.T) {
	install := readDoc(t, "docs/install.md")
	up := section(install, "## Updating")
	if up == "" {
		t.Fatal("docs/install.md has no ## Updating section")
	}
	for _, want := range []string{
		"docker compose pull && docker compose up -d",
		"docker pull ghcr.io/darrenhuai/watchglass:latest",
		"**Update**",
		"install -m 0755 watchglass /usr/local/bin/watchglass", // the systemd section's own line
	} {
		if !strings.Contains(up, want) {
			t.Errorf("docs/install.md's Updating section should say %q", want)
		}
	}
	m := regexp.MustCompile(`docker run -d --name (\S+)`).FindStringSubmatch(install)
	if m == nil {
		t.Fatal("docs/install.md has no docker run -d --name sample")
	}
	if !strings.Contains(up, "docker rm -f "+m[1]+"\n") {
		t.Errorf("the docker run sample names its container %q; Updating should remove that one (docker rm -f %s)", m[1], m[1])
	}
	if !strings.Contains(section(install, "## Run it as a service (systemd)"), "install -m 0755 watchglass /usr/local/bin/watchglass") {
		t.Error("Updating copies the binary to /usr/local/bin/watchglass, but the systemd section no longer installs it there")
	}
	if !strings.Contains(install, "- [Updating](#updating)") {
		t.Error("docs/install.md's contents list should link to Updating")
	}
	if !strings.Contains(readDoc(t, "README.md"), "(docs/install.md#updating)") {
		t.Error("README.md should link to docs/install.md#updating")
	}
	addon := section(readDoc(t, "addon/DOCS.md"), "## Updating")
	if !strings.Contains(addon, "**Update**") || !strings.Contains(addon, "Check for updates") {
		t.Errorf("addon/DOCS.md should have an Updating section naming the Update button and Check for updates; it has:\n%s", addon)
	}
}

// TestPixelChangeDocsQuoteTheLiveStrip: the pixel_change procedure tells
// you to read your camera's noise off the Live panel and says how many
// readings it keeps; that has to be the number main.go gives the registry.
func TestPixelChangeDocsQuoteTheLiveStrip(t *testing.T) {
	m := regexp.MustCompile(`state\.New\(([0-9]+)\)`).FindStringSubmatch(readDoc(t, "cmd/watchglass/main.go"))
	if m == nil {
		t.Fatal("cmd/watchglass/main.go no longer calls state.New(<n>); update this test")
	}
	doc := section(readDoc(t, "docs/configuration.md"), "## Triggers")
	got := regexp.MustCompile(`keeps the last ([0-9]+) readings`).FindStringSubmatch(doc)
	if got == nil {
		t.Fatal("docs/configuration.md's pixel_change procedure doesn't say how many readings the Live panel keeps")
	}
	if got[1] != m[1] {
		t.Errorf("docs/configuration.md says the Live panel keeps the last %s readings; main.go keeps %s (state.New)", got[1], m[1])
	}
	if !strings.Contains(doc, "Threshold `100`") {
		t.Error("the pixel_change procedure should say to measure with Threshold 100, so the watch doesn't fire on noise")
	}
	// 100 doesn't make a watch quiet: trigger.ObservePixel fires at
	// pct >= Threshold, and imgproc.PercentChanged gives 100 when the
	// picture size changes or every pixel in the box does.
	if strings.Contains(doc, "stays quiet") {
		t.Error("the pixel_change procedure promises a watch that stays quiet; at Threshold 100 it can still fire")
	}
	if !strings.Contains(doc, "notify list empty") {
		t.Error("the pixel_change procedure should say to leave the notify list empty while measuring")
	}
}

// TestRapidOCRDocsSayHowToReadAnUpsideDownDisplay: watchglass runs rapidocr
// with its text-direction check off, so rapidocr no longer turns a display
// that is upside down in the picture. The docs must tell people to use
// rotate: 180 for that, and link to the section that explains it.
func TestRapidOCRDocsSayHowToReadAnUpsideDownDisplay(t *testing.T) {
	doc := readDoc(t, "docs/configuration.md")
	if !strings.Contains(doc, "\n### A display that is sideways\n") {
		t.Fatal("docs/configuration.md has no ### A display that is sideways section; update this test")
	}
	i := strings.Index(doc, "\n### rapidocr\n")
	if i < 0 {
		t.Fatal("docs/configuration.md has no ### rapidocr section")
	}
	rapid := doc[i:]
	if j := strings.Index(rapid, "\n#### "); j >= 0 {
		rapid = rapid[:j]
	}
	for _, want := range []string{"text-direction check off", "`rotate: 180`", "(#a-display-that-is-sideways)"} {
		if !strings.Contains(rapid, want) {
			t.Errorf("docs/configuration.md's rapidocr section should say %q", want)
		}
	}
	if strings.Contains(rapid, "never is") {
		t.Error("docs/configuration.md's rapidocr section says a box is never upside down; a camera mounted the wrong way up gives one")
	}
}
