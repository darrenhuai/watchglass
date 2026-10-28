package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"gopkg.in/yaml.v3"
)

// readmeWordLimit keeps the README a first screen and a quick start. The
// manual lives in docs/.
const readmeWordLimit = 950

var (
	commentRe   = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlTagRe   = regexp.MustCompile(`<[^>]+>`)
	mdImageRe   = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	linkTargRe  = regexp.MustCompile(`\]\([^)]*\)`)
	mdMarkupRe  = regexp.MustCompile("[\\[\\]|*#>`_-]+")
	fenceLineRe = regexp.MustCompile("^\\s*(```|~~~)")
)

// proseWords counts the words a reader reads: code blocks, HTML tags (an
// image's alt text included), images and link targets don't count, and
// neither do table pipes or Markdown markup.
func proseWords(md string) int {
	var kept []string
	fence := false
	for _, l := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		if fenceLineRe.MatchString(l) {
			fence = !fence
			continue
		}
		if !fence {
			kept = append(kept, l)
		}
	}
	t := strings.Join(kept, "\n")
	t = commentRe.ReplaceAllString(t, " ")
	t = htmlTagRe.ReplaceAllString(t, " ")
	t = mdImageRe.ReplaceAllString(t, " ")
	t = linkTargRe.ReplaceAllString(t, "]")
	t = mdMarkupRe.ReplaceAllString(t, " ")
	n := 0
	for _, w := range strings.Fields(t) {
		if strings.IndexFunc(w, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			n++
		}
	}
	return n
}

func TestProseWords(t *testing.T) {
	for in, want := range map[string]int{
		"Read the digits.":                                             3,
		"See [the docs](docs/install.md) first.":                       4,
		"```bash\ndocker run --rm image\n```\nOpen it.":                2,
		"| You have | Use |\n|---|---|\n| A meter | An IR read head |": 9,
		"<img src=\"x.gif\" alt=\"a long description\">\nShort.":       1,
		"![alt words here](docs/img/x.png) and `code`":                 2,
		"<!-- sample-check: a b met -->\nWords — and more":             3,
	} {
		if got := proseWords(in); got != want {
			t.Errorf("proseWords(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestReadmeWordBudget: the README stays under readmeWordLimit words.
func TestReadmeWordBudget(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	n := proseWords(string(raw))
	t.Logf("README.md: %d words", n)
	if n >= readmeWordLimit {
		t.Errorf("README.md has %d words, the limit is under %d; move the detail into docs/", n, readmeWordLimit)
	}
}

// A watchglass version such as 0.1.10, not part of a longer dotted number
// (an IP address) and not a four-digit year (Home Assistant 2026.10.0). A
// full stop that ends the sentence still counts as the end of the version.
var pinnedVersionRe = regexp.MustCompile(`(?:^|[^0-9.])v?[0-9]{1,2}\.[0-9]+\.[0-9]+(?:[^0-9.]|\.(?:[^0-9]|$)|$)`)

// TestInstallDocsNameNoVersion: a version number in the install steps goes
// stale with the next release (install.md said 0.1.10 after 0.1.11 was
// out). Say how to find the version instead.
func TestInstallDocsNameNoVersion(t *testing.T) {
	for _, rel := range []string{"README.md", "docs/install.md"} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
			if m := pinnedVersionRe.FindString(l); m != "" {
				t.Errorf("%s:%d: names version %q, which goes stale; use <version> or point at the releases page:\n%s", rel, i+1, strings.TrimSpace(m), l)
			}
		}
	}
}

func TestPinnedVersionRe(t *testing.T) {
	for in, want := range map[string]bool{
		"a line like `watchglass 0.1.10 ready`":    true,
		"its own tag (`:0.1.10`)":                  true,
		"tagged v0.2.0":                            true,
		"released as 0.1.11.":                      true,
		"open http://192.168.1.50.":                false,
		"checked on Home Assistant 2026.10.0":      false,
		"http://192.168.1.50/snapshot.jpg":         false,
		"`-ingress-from` | `172.30.32.2`":          false,
		"With Go 1.27 or newer":                    false,
		"a line like `watchglass <version> ready`": false,
	} {
		if got := pinnedVersionRe.MatchString(in); got != want {
			t.Errorf("pinnedVersionRe.MatchString(%q) = %v, want %v", in, got, want)
		}
	}
}

// pitchPhrase is the promise the README's first line makes. The add-on's
// pitch (addon/DOCS.md) and its store card (the description in
// addon/config.yaml) repeat it, so a Home Assistant user reads the same
// sentence in all three places. Change it everywhere when it changes.
const pitchPhrase = "when they show what you're waiting for"

// TestAddonPitchMatchesReadme: the add-on's pitch line and its store
// description say what the README's first line says, rather than drifting
// into a second wording.
func TestAddonPitchMatchesReadme(t *testing.T) {
	for _, rel := range []string{"README.md", "addon/DOCS.md"} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		if len(lines) < 3 || !strings.Contains(lines[2], pitchPhrase) {
			t.Errorf("%s: line 3 is the pitch and should contain %q; it is:\n%s", rel, pitchPhrase, strings.Join(lines[:min(3, len(lines))], "\n"))
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "addon", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var addon struct {
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(raw, &addon); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(addon.Description, pitchPhrase) {
		t.Errorf("addon/config.yaml: description is the add-on's line in the Home Assistant store and should contain %q; it is:\n%s", pitchPhrase, addon.Description)
	}
}

// TestPitchHasNoEmDash: the README, the install guide and the add-on's
// pitch are written the way the owner writes, which is without em dashes.
func TestPitchHasNoEmDash(t *testing.T) {
	for _, rel := range []string{"README.md", "docs/install.md", "addon/DOCS.md", "addon/config.yaml"} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range strings.Split(string(raw), "\n") {
			if strings.ContainsRune(l, '—') {
				t.Errorf("%s:%d: em dash; use a colon, a comma or two sentences:\n%s", rel, i+1, l)
			}
		}
	}
}

// TestReleaseArchivesOwnedByRoot: every entry in a release archive is owned
// by root. Without that, goreleaser records whoever ran the build (uid
// 1001 on GitHub's runners), and an archive unpacked as root hands the
// binary to whatever account has uid 1001 on that machine, which could swap
// it before `install` copies it into /usr/local/bin.
func TestReleaseArchivesOwnedByRoot(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Archives []struct {
			ID         string      `yaml:"id"`
			BuildsInfo fileInfo    `yaml:"builds_info"`
			Files      []yaml.Node `yaml:"files"`
		} `yaml:"archives"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Archives) == 0 {
		t.Fatal("no archives in .goreleaser.yaml")
	}
	for _, a := range cfg.Archives {
		if !a.BuildsInfo.root() {
			t.Errorf("archive %q: builds_info owner/group = %q/%q, want root/root, so the binary isn't owned by the build user", a.ID, a.BuildsInfo.Owner, a.BuildsInfo.Group)
		}
		if len(a.Files) == 0 {
			t.Errorf("archive %q lists no files; goreleaser then adds LICENSE and README with the build user as owner", a.ID)
		}
		for _, n := range a.Files {
			var f struct {
				Src  string   `yaml:"src"`
				Info fileInfo `yaml:"info"`
			}
			if n.Kind != yaml.MappingNode {
				t.Errorf("archive %q: file %q is a bare path, which keeps the build user as owner; give it src: and info: owner: root, group: root", a.ID, n.Value)
				continue
			}
			if err := n.Decode(&f); err != nil {
				t.Fatal(err)
			}
			if !f.Info.root() {
				t.Errorf("archive %q: file %q has owner/group %q/%q, want root/root", a.ID, f.Src, f.Info.Owner, f.Info.Group)
			}
		}
	}
}

type fileInfo struct {
	Owner string `yaml:"owner"`
	Group string `yaml:"group"`
}

func (f fileInfo) root() bool { return f.Owner == "root" && f.Group == "root" }
