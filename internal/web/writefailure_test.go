package web

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/source"
)

// An unwritable config file on save brings the form back exactly as
// submitted, with the failure above it. The old error page relied on
// history.back() to restore typed text, which browsers don't do for every
// field (or at all without the back/forward cache).
func TestSaveWriteFailureRerendersFormWithSubmittedValues(t *testing.T) {
	s, _ := newTestServer(t)
	s.cfgPath = filepath.Join(t.TempDir(), "missing", "config.yaml")
	// printer isn't started here, so the copy must not claim it keeps running.
	form := url.Values{
		"x": {"0.25"}, "y": {"0.25"}, "w": {"0.5"}, "h": {"0.25"},
		"ttype": {"pixel_change"}, "tthreshold": {"44"}, "confirm": {"2"},
		"cooldown": {"33s"}, "interval": {"7s"}, "health_after": {"7"},
		"notify": {"ntfy://x/y\n"},
	}
	resp, body := postForm(t, s.Handler(), "/watch/printer/save", form)
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500; body: %s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`<div id="form-errors" class="form-alert" role="alert" tabindex="-1" autofocus>`,
		`<p class="form-alert-title">Couldn&#39;t save printer</p>`,
		"couldn&#39;t write config.yaml, so nothing was saved and the watch keeps its previous settings.",
		"Technical detail",
		`id="f-cooldown" name="cooldown" value="33s"`,
		`id="f-interval" name="interval" value="7s"`,
		`id="watchform"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("re-rendered form missing %q; body:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Back to your edits") || strings.Contains(body, "history.back") {
		t.Error("the form is re-rendered in place; there must be no history.back() button")
	}
	if got, _ := s.findWatch("printer"); got.Trigger.Cooldown != 0 {
		t.Errorf("nothing should have been saved, cooldown = %v", got.Trigger.Cooldown)
	}
}

func TestCreateWriteFailureRerendersAddFormWithValues(t *testing.T) {
	s, _ := newTestServer(t)
	s.cfgPath = filepath.Join(t.TempDir(), "missing", "config.yaml")
	resp, body := postForm(t, s.Handler(), "/watch/new", url.Values{"name": {"cam-d"}, "source": {"http://x/y.jpg"}})
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500; body: %s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`<div class="form-alert form-alert-inline" role="alert" tabindex="-1" autofocus>`,
		`<p class="form-alert-title">Couldn&#39;t create the watch</p>`,
		"couldn&#39;t write config.yaml, so nothing was created",
		`value="cam-d"`, `value="http://x/y.jpg"`,
		"Technical detail",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("re-rendered index missing %q; body:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Back to your edits") {
		t.Error("no history.back() button on a re-rendered form")
	}
}

// Free-text fields keep the browser's own form-state restoration:
// autocomplete="off" opts a field out of it on back navigation.
func TestFormFieldsDoNotOptOutOfFormRestore(t *testing.T) {
	s, _ := newTestServer(t)
	_, index := get(t, s.Handler(), "/")
	_, detail := get(t, s.Handler(), "/watch/printer")
	for name, body := range map[string]string{"index": index, "detail": detail} {
		if strings.Contains(body, `autocomplete="off"`) {
			t.Errorf("%s: a field still carries autocomplete=\"off\"", name)
		}
		if !strings.Contains(body, `spellcheck="false"`) {
			t.Errorf("%s: machine-value fields should still turn spellcheck off", name)
		}
	}
}

// The copy names the config file actually in use (-config can point
// anywhere), not a hard-coded config.yaml.
func TestCopyNamesTheConfigFileInUse(t *testing.T) {
	s, cfgPath := newTestServer(t)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "cams.yml")
	if err := os.WriteFile(other, data, 0o644); err != nil {
		t.Fatal(err)
	}
	s.cfgPath = other
	h := s.Handler()
	form := url.Values{"x": {"0"}, "y": {"0"}, "w": {"1"}, "h": {"1"}, "ttype": {"pixel_change"},
		"tthreshold": {"10"}, "confirm": {"1"}, "cooldown": {"0s"}, "interval": {"5s"}}
	resp, _ := postForm(t, h, "/watch/printer/save", form)
	if resp.StatusCode != 303 {
		t.Fatalf("save status = %d", resp.StatusCode)
	}
	_, body := getWithCookies(t, h, "/watch/printer", []*http.Cookie{flashCookieFrom(t, resp)})
	if !strings.Contains(body, "Saved to cams.yml and restarted the watch") {
		t.Errorf("flash should name cams.yml; body:\n%s", body)
	}
	if !strings.Contains(body, "Writes to <code>cams.yml</code>") || !strings.Contains(body, `title="Merged into cams.yml:`) {
		t.Errorf("save note should name cams.yml; body:\n%s", body)
	}
	if strings.Contains(body, "config.yaml") {
		t.Errorf("no copy should say config.yaml when the file is cams.yml; body:\n%s", body)
	}
}

// A create that was written but whose watch didn't start must not read as
// a plain success: the flash becomes a warning with the reason.
func TestCreateStartFailureFlashesAWarning(t *testing.T) {
	s, _ := newTestServer(t)
	s.sup.NewSource = func(w config.Watch) (source.Source, error) {
		return nil, errors.New("unsupported source scheme")
	}
	h := s.Handler()
	resp, _ := postForm(t, h, "/watch/new", url.Values{"name": {"oven"}, "source": {"http://cam2/snap.jpg"}})
	if resp.StatusCode != 303 {
		t.Fatalf("create status = %d, want 303 (the watch is written)", resp.StatusCode)
	}
	_, body := getWithCookies(t, h, "/watch/oven", []*http.Cookie{flashCookieFrom(t, resp)})
	if !strings.Contains(body, `<p class="flash flash-warn" role="alert">`) ||
		!strings.Contains(body, `Created <strong class="mono">oven</strong> in config.yaml, but it didn't start. The source can&#39;t be used: unsupported source scheme. It stays stopped`) ||
		!strings.Contains(body, "unsupported source scheme") {
		t.Errorf("detail after a failed start should warn with the reason; body:\n%s", body)
	}
	if strings.Contains(body, "Drag on the snapshot to choose the region") || strings.Contains(body, `class="flash" role="status"`) {
		t.Errorf("a failed start must not show the success flash; body:\n%s", body)
	}
	// A forged reason on a non-create kind is ignored.
	_, body = getWithCookies(t, h, "/", []*http.Cookie{{Name: flashCookie, Value: url.QueryEscape("deleted|oven|boom")}})
	if strings.Contains(body, "flash-warn") || strings.Contains(body, "boom") {
		t.Errorf("only a create carries a reason; body:\n%s", body)
	}
}
