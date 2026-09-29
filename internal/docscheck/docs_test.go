package docscheck

import (
	"bufio"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/imgproc"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// root is the repository, two levels up from this package.
const root = "../.."

// mdFile is one Markdown file, split into what the checks need.
type mdFile struct {
	rel   string   // slash path from the repo root
	lines []string // the file, CRLF folded (Windows CI checks out CRLF)
	yaml  []block  // fenced yaml/yml blocks
	prose []int    // indexes of lines outside fenced code
}

type block struct {
	line int // 1-based line of the opening fence
	text string
}

// markdownFiles lists every doc a visitor can reach from the README.
func markdownFiles(t *testing.T) []mdFile {
	t.Helper()
	var rels []string
	for _, top := range []string{"README.md", "CONTRIBUTING.md"} {
		if _, err := os.Stat(filepath.Join(root, top)); err == nil {
			rels = append(rels, top)
		}
	}
	for _, dir := range []string{"docs", "examples", "addon", ".github"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(p, ".md") {
				rel, _ := filepath.Rel(root, p)
				rels = append(rels, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var out []mdFile
	for _, rel := range rels {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, parseMarkdown(rel, strings.ReplaceAll(string(raw), "\r\n", "\n")))
	}
	if len(out) < 10 {
		t.Fatalf("found only %d Markdown files; is root right?", len(out))
	}
	return out
}

var fenceRe = regexp.MustCompile("^\\s*(```+|~~~+)\\s*([A-Za-z0-9_+-]*)")

func parseMarkdown(rel, text string) mdFile {
	f := mdFile{rel: rel, lines: strings.Split(text, "\n")}
	fence, lang, start := "", "", 0
	var body []string
	for i, l := range f.lines {
		m := fenceRe.FindStringSubmatch(l)
		switch {
		case fence == "" && m != nil:
			fence, lang, start, body = m[1], strings.ToLower(m[2]), i+1, nil
		case fence != "" && m != nil && strings.HasPrefix(strings.TrimSpace(l), fence) && m[2] == "":
			if lang == "yaml" || lang == "yml" {
				f.yaml = append(f.yaml, block{line: start, text: strings.Join(body, "\n")})
			}
			fence = ""
		case fence != "":
			body = append(body, l)
		default:
			f.prose = append(f.prose, i)
		}
	}
	return f
}

// TestDocsHaveNoIndentedCodeBlocks: GitHub gives fenced blocks a copy
// button and indented ones none, and every command in the docs is meant to
// be copied.
func TestDocsHaveNoIndentedCodeBlocks(t *testing.T) {
	for _, f := range markdownFiles(t) {
		prev := ""
		inList := false
		for _, i := range f.prose {
			l := f.lines[i]
			trimmed := strings.TrimSpace(l)
			if trimmed == "" {
				prev = ""
				continue
			}
			if listItemRe.MatchString(l) {
				inList = true
			} else if !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
				inList = false
			}
			if prev == "" && !inList && (strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "\t")) && i > 0 && strings.TrimSpace(f.lines[i-1]) == "" {
				t.Errorf("%s:%d: indented code block; use a fenced one:\n%s", f.rel, i+1, l)
			}
			prev = l
		}
	}
}

var listItemRe = regexp.MustCompile(`^\s*([-*+]|\d+[.)])\s`)

// TestDocsYAMLLoads: a ```yaml block with a top-level watches: key is a
// whole config, and it must load the way `watchglass -config` loads it.
// Every other yaml block must at least parse.
func TestDocsYAMLLoads(t *testing.T) {
	whole := 0
	for _, f := range markdownFiles(t) {
		for _, b := range f.yaml {
			where := fmt.Sprintf("%s:%d", f.rel, b.line)
			if !regexp.MustCompile(`(?m)^watches:`).MatchString(b.text) {
				var v any
				if err := yaml.Unmarshal([]byte(b.text), &v); err != nil {
					t.Errorf("%s: yaml doesn't parse: %v", where, err)
				}
				continue
			}
			whole++
			if _, err := loadConfig(b.text); err != nil {
				t.Errorf("%s: config doesn't load: %v", where, err)
			}
		}
	}
	if whole < 8 {
		t.Errorf("only %d whole configs found in the docs; the recipes should have more", whole)
	}
	// The example configs next to the docs, loaded exactly as -config does.
	for _, p := range []string{"examples/config.yaml", "examples/demo/config.yaml", "examples/demo/config-sevenseg.yaml"} {
		if _, err := config.Load(filepath.Join(root, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func loadConfig(text string) (*config.Config, error) {
	var c config.Config
	if err := yaml.Unmarshal([]byte(text), &c); err != nil {
		return nil, err
	}
	return &c, c.Validate()
}

// Links: [text](target), ![alt](target), <img src="target">, <a href="target">.
var (
	inlineLinkRe = regexp.MustCompile(`!?\[(?:[^\[\]]|\[[^\]]*\])*\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	htmlLinkRe   = regexp.MustCompile(`(?:src|href)="([^"]+)"`)
	refLinkRe    = regexp.MustCompile(`^\s*\[[^\]]+\]:\s*(\S+)`)
	codeSpanRe   = regexp.MustCompile("`[^`]*`")
	headingRe    = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	htmlAnchorRe = regexp.MustCompile(`<a\s+(?:id|name)="([^"]+)"`)
)

// TestDocsRelativeLinksResolve: every relative link points at a file in
// the repo, and every #anchor at a heading GitHub generates that anchor for.
func TestDocsRelativeLinksResolve(t *testing.T) {
	files := markdownFiles(t)
	anchors := map[string]map[string]bool{}
	for _, f := range files {
		anchors[f.rel] = headingAnchors(f)
	}
	checked := 0
	for _, f := range files {
		for _, i := range f.prose {
			line := codeSpanRe.ReplaceAllString(f.lines[i], "")
			var targets []string
			for _, m := range inlineLinkRe.FindAllStringSubmatch(line, -1) {
				targets = append(targets, m[1])
			}
			for _, m := range htmlLinkRe.FindAllStringSubmatch(line, -1) {
				targets = append(targets, m[1])
			}
			if m := refLinkRe.FindStringSubmatch(line); m != nil {
				targets = append(targets, m[1])
			}
			for _, target := range targets {
				if err := checkTarget(f.rel, target, anchors); err != nil {
					t.Errorf("%s:%d: %s: %v", f.rel, i+1, target, err)
				}
				checked++
			}
		}
	}
	if checked < 50 {
		t.Errorf("only %d links checked; is the link pattern broken?", checked)
	}
}

func checkTarget(from, target string, anchors map[string]map[string]bool) error {
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	if u.Scheme != "" || strings.HasPrefix(target, "//") {
		return nil // external: checked by hand with a HEAD/GET, not in CI
	}
	file := from
	if u.Path != "" {
		p := path.Join(path.Dir(from), u.Path)
		if strings.HasPrefix(p, "../") {
			return fmt.Errorf("points outside the repository")
		}
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return fmt.Errorf("no such file (%s)", p)
		}
		if info.IsDir() || !strings.HasSuffix(p, ".md") {
			if u.Fragment != "" {
				return fmt.Errorf("an anchor on something that isn't a Markdown file")
			}
			return nil
		}
		file = p
	}
	if u.Fragment == "" {
		return nil
	}
	set, ok := anchors[file]
	if !ok {
		return fmt.Errorf("%s isn't one of the checked docs", file)
	}
	if !set[u.Fragment] {
		return fmt.Errorf("%s has no heading with the anchor #%s", file, u.Fragment)
	}
	return nil
}

// headingAnchors is GitHub's anchor for every heading: lower case, with
// punctuation dropped and spaces turned into hyphens, and -1, -2... on
// repeats. Explicit <a id="..."> anchors count too.
func headingAnchors(f mdFile) map[string]bool {
	set := map[string]bool{}
	seen := map[string]int{}
	for _, i := range f.prose {
		for _, m := range htmlAnchorRe.FindAllStringSubmatch(f.lines[i], -1) {
			set[m[1]] = true
		}
		m := headingRe.FindStringSubmatch(f.lines[i])
		if m == nil {
			continue
		}
		slug := slugify(m[2])
		if n := seen[slug]; n > 0 {
			set[fmt.Sprintf("%s-%d", slug, n)] = true
		} else {
			set[slug] = true
		}
		seen[slug]++
	}
	return set
}

var mdLinkTextRe = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

func slugify(heading string) string {
	s := mdLinkTextRe.ReplaceAllString(heading, "$1")
	s = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Your first watch":                   "your-first-watch",
		"`ffmpeg:` sources":                  "ffmpeg-sources",
		"Behind a reverse proxy":             "behind-a-reverse-proxy",
		"Reolink, Hikvision & Dahua":         "reolink-hikvision--dahua",
		"Tried seven_segments/ssocr?":        "tried-seven_segmentsssocr",
		"[Link](x.md) in a heading":          "link-in-a-heading",
		"Home Assistant: connected (or not)": "home-assistant-connected-or-not",
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestUILinksToDocsResolve: a link from the web UI into this repo's docs on
// GitHub must name a file that exists here, so renaming a doc can't leave
// the UI pointing at a 404.
func TestUILinksToDocsResolve(t *testing.T) {
	re := regexp.MustCompile(`https://github\.com/darrenhuai/watchglass/blob/master/([^"#\s]+)(?:#([^"\s]+))?`)
	files := markdownFiles(t)
	anchors := map[string]map[string]bool{}
	for _, f := range files {
		anchors[f.rel] = headingAnchors(f)
	}
	found := 0
	err := filepath.WalkDir(filepath.Join(root, "internal", "web"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, "_test.go") {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			found++
			target := m[1]
			if m[2] != "" {
				target += "#" + m[2]
			}
			if err := checkTarget("README.md", target, anchors); err != nil {
				t.Errorf("%s links to %s: %v", p, m[0], err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Error("no links from the web UI into the docs; the Source hint should link the camera URL cookbook")
	}
}

var sampleRe = regexp.MustCompile(`<!--\s*sample-check:\s*(\S+)\s+(\S+)\s+(met|not-met)(?:\s+reads=(\S+))?\s*-->`)

// TestRecipeSamples reads each recipe's committed sample frame with the
// recipe's own watch, as Test this region would.
func TestRecipeSamples(t *testing.T) {
	var tess ocr.Engine
	if bin, err := ocr.FindTesseract(""); err == nil {
		tess = ocr.NewTesseract(bin)
	}
	var rapid ocr.Engine
	rapidTried := false
	checks := 0
	for _, f := range markdownFiles(t) {
		if !strings.HasPrefix(f.rel, "docs/recipes/") {
			continue
		}
		watches := map[string]config.Watch{}
		for _, b := range f.yaml {
			if !regexp.MustCompile(`(?m)^watches:`).MatchString(b.text) {
				continue
			}
			c, err := loadConfig(b.text)
			if err != nil {
				continue // TestDocsYAMLLoads reports it
			}
			for _, w := range c.Watches {
				watches[w.Name] = w
			}
		}
		sc := bufio.NewScanner(strings.NewReader(strings.Join(f.lines, "\n")))
		for sc.Scan() {
			for _, m := range sampleRe.FindAllStringSubmatch(sc.Text(), -1) {
				checks++
				name, img, verdict, reads := m[1], m[2], m[3], m[4]
				t.Run(path.Base(f.rel)+"/"+name+"/"+path.Base(img), func(t *testing.T) {
					w, ok := watches[name]
					if !ok {
						t.Fatalf("no watch %q in a yaml block of %s", name, f.rel)
					}
					var eng ocr.Engine
					switch w.Engine {
					case "sevenseg":
						eng = ocr.NewSevenSeg()
					case "", "tesseract":
						if tess == nil {
							t.Skip("tesseract isn't installed")
						}
						eng = tess
					case "rapidocr":
						if !rapidTried {
							rapidTried = true
							ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
							if r, err := ocr.DetectRapidOCR(ctx, ""); err == nil {
								rapid = r
							}
							cancel()
						}
						if rapid == nil {
							t.Skip("no Python with rapidocr")
						}
						eng = rapid
					}
					fh, err := os.Open(filepath.Join(root, filepath.FromSlash(path.Join(path.Dir(f.rel), img))))
					if err != nil {
						t.Fatal(err)
					}
					frame, _, err := image.Decode(fh)
					fh.Close()
					if err != nil {
						t.Fatal(err)
					}
					crop := imgproc.Apply(imgproc.Crop(frame, w.Region), w.Preprocess)
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel()
					text, err := eng.Recognize(ctx, crop)
					if err != nil {
						t.Fatal(err)
					}
					cond, err := trigger.Check(w.Trigger, text)
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("read %q: %s", text, cond.Detail)
					if cond.Met != (verdict == "met") {
						t.Errorf("read %q, and %s; the recipe says %s", text, cond.Detail, verdict)
					}
					if reads != "" && text != reads {
						t.Errorf("read %q, the recipe says it reads %q", text, reads)
					}
				})
			}
		}
	}
	if checks < 6 {
		t.Errorf("only %d sample checks in docs/recipes", checks)
	}
}
