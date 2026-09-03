// Package notify fans out fire events to notification targets using
// shoutrrr URL strings (ntfy://, discord://, generic://, ...).
package notify

import (
	"context"
	"errors"

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

// Shoutrrr is a Notifier backed by the shoutrrr library, fanning out a
// single Send call to every configured service URL. ntfy URLs are held out
// of the shoutrrr router and delivered by direct HTTP instead, so Shoutrrr
// can also send image attachments (see SendImage) to those targets.
type Shoutrrr struct {
	sender      *router.ServiceRouter
	ntfyTargets []string
}

// NewShoutrrr builds a Shoutrrr sender from a list of shoutrrr service
// URLs (e.g. "ntfy://...", "discord://...", "generic+https://...").
// ntfy:// and ntfys:// URLs (and the local-server ntfy+http(s):// form) are
// held out for direct HTTP delivery; every other URL goes to the shoutrrr
// router as before.
func NewShoutrrr(urls []string) (*Shoutrrr, error) {
	var ntfyTargets []string
	var rest []string
	for _, u := range urls {
		if t := ntfyTarget(u); t != "" {
			ntfyTargets = append(ntfyTargets, t)
			continue
		}
		rest = append(rest, u)
	}
	var sender *router.ServiceRouter
	if len(rest) > 0 {
		s, err := shoutrrr.CreateSender(rest...)
		if err != nil {
			return nil, err
		}
		sender = s
	}
	return &Shoutrrr{sender: sender, ntfyTargets: ntfyTargets}, nil
}

// Send delivers title/body to every configured service URL (both the
// shoutrrr-routed targets and the direct-HTTP ntfy targets), joining any
// per-target failures into a single error. ctx is not honored for
// cancellation on the shoutrrr half: the underlying shoutrrr ServiceRouter
// has no context support, so a hanging webhook POST will not abort early.
// The ntfy half DOES honor ctx, since it issues its own http.Request.
func (s *Shoutrrr) Send(ctx context.Context, title, body string) error {
	var errs []error
	if s.sender != nil {
		params := &types.Params{"title": title}
		errs = append(errs, s.sender.Send(body, params)...)
	}
	for _, target := range s.ntfyTargets {
		errs = append(errs, ntfySend(ctx, target, title, body, nil))
	}
	return errors.Join(errs...)
}

// SendImage delivers the png as an ntfy attachment where possible and plain
// text everywhere else. With no ntfy targets it degrades to Send.
func (s *Shoutrrr) SendImage(ctx context.Context, title, body string, png []byte) error {
	var errs []error
	if s.sender != nil {
		params := &types.Params{"title": title}
		errs = append(errs, s.sender.Send(body, params)...)
	}
	for _, target := range s.ntfyTargets {
		errs = append(errs, ntfySend(ctx, target, title, body, png))
	}
	return errors.Join(errs...)
}
