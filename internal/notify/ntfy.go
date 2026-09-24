package notify

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ntfyTarget rewrites a watchglass ntfy URL into the HTTP endpoint ntfy's
// REST API expects. Returns "" for non-ntfy URLs.
//
//	ntfy://host/topic       -> https://host/topic
//	ntfys://host/topic      -> https://host/topic
//	ntfy+http://host/topic  -> http://host/topic  (local unencrypted server)
//	ntfy+https://host/topic -> https://host/topic
func ntfyTarget(url string) string {
	switch {
	case strings.HasPrefix(url, "ntfy+http://"), strings.HasPrefix(url, "ntfy+https://"):
		return strings.TrimPrefix(url, "ntfy+")
	case strings.HasPrefix(url, "ntfy://"):
		return "https://" + strings.TrimPrefix(url, "ntfy://")
	case strings.HasPrefix(url, "ntfys://"):
		return "https://" + strings.TrimPrefix(url, "ntfys://")
	}
	return ""
}

var ntfyHTTP = &http.Client{Timeout: 15 * time.Second}

// ntfySend delivers to one ntfy endpoint: a PUT with the png attachment as
// the body (title/message in headers), or a plain text POST when png is nil.
func ntfySend(ctx context.Context, target, title, body string, png []byte) error {
	var req *http.Request
	var err error
	if png != nil {
		req, err = http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(png))
		if err == nil {
			req.Header.Set("X-Message", body)
			req.Header.Set("X-Filename", "watch.png")
			req.Header.Set("Content-Type", "image/png")
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(body))
	}
	if err != nil {
		return err
	}
	req.Header.Set("X-Title", title)
	resp, err := ntfyHTTP.Do(req)
	if err != nil {
		// The *url.Error already quotes the endpoint; callers scrub it down
		// to scheme://host, since a topic on a public server is its password.
		return fmt.Errorf("ntfy: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("ntfy: status %d", resp.StatusCode)
	}
	return nil
}

// ntfyProblem says why an ntfy URL can't be sent to, or "" when it can (or
// isn't an ntfy URL). ntfy posts to the server's root when there is no
// topic, which never reaches anyone, so an empty topic is refused up front.
func ntfyProblem(raw string) string {
	t := ntfyTarget(raw)
	if t == "" {
		return ""
	}
	u, err := url.Parse(t)
	if err != nil {
		return "not a valid URL"
	}
	if u.Host == "" {
		return "no server: put the ntfy server after the scheme, for example ntfy://ntfy.sh/my-topic"
	}
	if strings.Trim(u.Path, "/") == "" {
		return "no topic: add a topic name after the slash, for example ntfy://ntfy.sh/my-topic"
	}
	return ""
}
