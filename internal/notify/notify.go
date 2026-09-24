// Package notify fans out fire events to notification targets using
// shoutrrr URL strings (ntfy://, discord://, generic://, ...).
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/nicholas-fedor/shoutrrr"
	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
)

// Notifier sends a titled notification to whatever targets an
// implementation is configured with.
type Notifier interface {
	// Send delivers title/body. ctx is accepted for interface
	// compatibility but is not currently honored for cancellation.
	Send(ctx context.Context, title, body string) error
}

// ImageSender is a Notifier that can also deliver an image attachment.
// The runner type-asserts for it on fired events with a crop available.
type ImageSender interface {
	SendImage(ctx context.Context, title, body string, png []byte) error
}

// target is one line of a watch's notify list. ntfy URLs are delivered by
// direct HTTP (so they can carry the crop as an attachment); every other
// URL gets a shoutrrr router of its own, so each line's failure is its own
// and says which line it was (a router shared by several URLs reports its
// errors in completion order, not per URL).
type target struct {
	line   int    // 1-based position in the list
	label  string // scheme://host, safe to show (see Label)
	url    string // as configured, for scrubbing errors
	ntfy   string // the ntfy HTTP endpoint, or ""
	sender *router.ServiceRouter
}

// Shoutrrr is a Notifier backed by the shoutrrr library, fanning out a
// single Send call to every configured service URL. ntfy URLs are held out
// of the shoutrrr routers and delivered by direct HTTP instead, so Shoutrrr
// can also send image attachments (see SendImage) to those targets.
type Shoutrrr struct {
	targets []target
}

// NewShoutrrr builds a Shoutrrr sender from a list of shoutrrr service
// URLs (e.g. "ntfy://...", "discord://...", "generic+https://...").
// ntfy:// and ntfys:// URLs (and the local-server ntfy+http(s):// form) are
// held out for direct HTTP delivery; every other URL goes to a shoutrrr
// router. A URL Check refuses is refused here with the same words, so the
// form (which runs Check) and a watch's start never disagree. The error
// names the line that can't be used and its host, never the rest of the
// URL, which is often the credential.
func NewShoutrrr(urls []string) (*Shoutrrr, error) {
	s := &Shoutrrr{}
	for i, u := range urls {
		t := target{line: i + 1, label: Label(u), url: u}
		if p := Check(u); p != nil {
			return nil, fmt.Errorf("line %d (%s): %s", t.line, t.label, p.Reason)
		}
		if n := ntfyTarget(u); n != "" {
			t.ntfy = n
		} else {
			r, err := shoutrrr.CreateSender(u)
			if err != nil {
				return nil, fmt.Errorf("line %d (%s): %s", t.line, t.label, initProblem(err, u))
			}
			t.sender = r
		}
		s.targets = append(s.targets, t)
	}
	return s, nil
}

// initProblem is shoutrrr's reason a URL can't be used, without the
// wrappers that repeat the whole URL list ("creating sender for URLs
// [...]: error initializing router services: ").
func initProblem(err error, u string) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, "error initializing router services: "); i >= 0 {
		msg = msg[i+len("error initializing router services: "):]
	}
	return Scrub(msg, []string{u})
}

// Failure is one line of the notify list that didn't get the alert.
type Failure struct {
	Line  int
	Label string // scheme://host
	Msg   string // the cause, scrubbed of URLs and credentials
}

// SendError is a send that at least one line of the list didn't get.
// Its text is "line 2 of 3 (ntfy://ntfy.sh): ntfy: HTTP 404", one part
// per failed line joined by "; ", and never holds more of a URL than
// scheme://host. internal/web turns it into sentences.
type SendError struct {
	Total    int
	Failures []Failure
}

func (e *SendError) Error() string {
	parts := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		parts[i] = fmt.Sprintf("line %d of %d (%s): %s", f.Line, e.Total, f.Label, f.Msg)
	}
	return strings.Join(parts, "; ")
}

// Send delivers title/body to every configured service URL at once (both
// the shoutrrr-routed targets and the direct-HTTP ntfy targets) and
// returns a *SendError naming each line that failed. ctx is not honored
// for cancellation on the shoutrrr half: the underlying ServiceRouter has
// no context support, but each router gives up after its own 10 s. The
// ntfy half DOES honor ctx, since it issues its own http.Request.
func (s *Shoutrrr) Send(ctx context.Context, title, body string) error {
	return s.send(ctx, title, body, nil)
}

// SendImage delivers the png as an ntfy attachment where possible and plain
// text everywhere else. With no ntfy targets it degrades to Send.
func (s *Shoutrrr) SendImage(ctx context.Context, title, body string, png []byte) error {
	return s.send(ctx, title, body, png)
}

func (s *Shoutrrr) send(ctx context.Context, title, body string, png []byte) error {
	errs := make([]error, len(s.targets))
	var wg sync.WaitGroup
	for i := range s.targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			t := s.targets[i]
			if t.ntfy != "" {
				errs[i] = ntfySend(ctx, t.ntfy, title, body, png)
				return
			}
			errs[i] = errors.Join(t.sender.Send(body, &types.Params{"title": title})...)
		}(i)
	}
	wg.Wait()
	var fails []Failure
	for i, err := range errs {
		if err == nil {
			continue
		}
		t := s.targets[i]
		fails = append(fails, Failure{Line: t.line, Label: t.label, Msg: Scrub(err.Error(), []string{t.url})})
	}
	if len(fails) == 0 {
		return nil
	}
	return &SendError{Total: len(s.targets), Failures: fails}
}
