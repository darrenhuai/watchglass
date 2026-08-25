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

// Shoutrrr is a Notifier backed by the shoutrrr library, fanning out a
// single Send call to every configured service URL.
type Shoutrrr struct {
	sender *router.ServiceRouter
}

// NewShoutrrr builds a Shoutrrr sender from a list of shoutrrr service
// URLs (e.g. "ntfy://...", "discord://...", "generic+https://...").
func NewShoutrrr(urls []string) (*Shoutrrr, error) {
	sender, err := shoutrrr.CreateSender(urls...)
	if err != nil {
		return nil, err
	}
	return &Shoutrrr{sender: sender}, nil
}

// Send delivers title/body to every configured service URL, joining any
// per-target failures into a single error. ctx is not honored for
// cancellation: the underlying shoutrrr ServiceRouter has no context
// support, so a hanging webhook POST will not abort early.
func (s *Shoutrrr) Send(ctx context.Context, title, body string) error {
	params := &types.Params{"title": title}
	errs := s.sender.Send(body, params)
	return errors.Join(errs...)
}
