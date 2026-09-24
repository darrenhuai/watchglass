// Package source provides frame sources. Tier A: plain HTTP snapshot URLs
// and MJPEG streams, read in pure Go.
package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register decoders: most cameras serve JPEG
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Source produces one frame per call. Implementations must respect ctx.
type Source interface {
	Grab(ctx context.Context) (image.Image, error)
}

const (
	// maxImageBytes caps the encoded image body read from any source
	// before decoding — a hard limit against a misbehaving/spoofed camera
	// streaming an unbounded response.
	maxImageBytes = 32 << 20 // 32 MiB

	// maxImagePixels caps width*height before the real pixel buffer is
	// allocated. image.Decode allocates its full pixel buffer up front
	// from the header-declared dimensions, so a tiny, well-formed file
	// that just declares an enormous width/height (a "decompression/decode
	// bomb") forces a multi-gigabyte allocation before any real pixel data
	// is read — fatal OOM on Pi-class targets, and recover() cannot catch
	// a runtime OOM. Checking image.DecodeConfig's cheap header-only parse
	// against this cap before calling image.Decode closes that hole.
	maxImagePixels = 16 << 20 // 16 megapixels
)

// decodeImage decodes r as an image, guarding against both a too-large body
// and a dimension bomb. Every Source that decodes attacker-reachable bytes
// (an HTTP snapshot response, ffmpeg's stdout) must go through this instead
// of calling image.Decode directly.
func decodeImage(r io.Reader) (image.Image, error) {
	limited := io.LimitReader(r, maxImageBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read image body: %w", err)
	}
	if len(buf) > maxImageBytes {
		return nil, fmt.Errorf("image body exceeds %d byte cap", maxImageBytes)
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("decode image header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("image has non-positive dimensions %dx%d", cfg.Width, cfg.Height)
	}
	// int64 multiplication: cfg.Width/Height come straight from an
	// attacker-controlled header, and a 32-bit int (Pi-class ARM builds)
	// would silently overflow computing width*height directly.
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, fmt.Errorf("image dimensions %dx%d exceed %d pixel cap", cfg.Width, cfg.Height, maxImagePixels)
	}

	img, _, err := image.Decode(bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return img, nil
}

// HTTPOptions are the per-watch settings of an http(s) source
// (config.Watch.TLSInsecure and Headers).
type HTTPOptions struct {
	// TLSInsecure accepts any certificate, for a camera with a
	// self-signed one. Only the sources built with it use the transport
	// that skips verification; every other source keeps the default.
	TLSInsecure bool
	// Headers go with every request, e.g. an API key a camera wants.
	Headers http.Header
}

// httpTimeout bounds one grab from request to decoded frame. It is a
// deadline on the request's context rather than http.Client.Timeout, so
// the same budget covers a snapshot and the first frame of an MJPEG
// stream, which never ends on its own.
const httpTimeout = 10 * time.Second

// HTTPSnapshot reads one frame from an http(s) URL: a still image (JPEG or
// PNG), or the first frame of an MJPEG stream (multipart/x-mixed-replace:
// ESP32-CAM's :81/stream, OctoPrint's ?action=stream, IP Webcam's /video).
// Credentials in the URL (http://user:pass@host/...) go out as Basic auth
// until the camera answers 401 asking for Digest. From then on every grab
// answers the remembered challenge up front, so the password never goes
// out as Basic again and a poll is one request, not two.
type HTTPSnapshot struct {
	URL     string
	Client  *http.Client
	Headers http.Header
	// Timeout overrides httpTimeout (tests).
	Timeout time.Duration
}

// digestState is what a camera's last Digest challenge taught us. It is
// shared by every source for the same scheme, host, user and password
// (digestFor), because the web UI builds a new source for each snapshot
// refresh and Test: a per-source memory would still send Basic on every
// one of them. The password is part of the key so a watch with a wrong
// one can't reset the nonce count a working watch on the same account is
// using (a camera that checks for replayed counts would refuse it).
type digestState struct {
	mu sync.Mutex
	ch *digestChallenge
	nc uint32 // requests made with ch's nonce
}

var digestStates sync.Map // "scheme://user@host#hash(password)" -> *digestState

// digestFor is the shared Digest memory for u's camera and user, or nil
// when u carries no credentials.
func digestFor(u *url.URL) *digestState {
	if u.User == nil {
		return nil
	}
	pass, _ := u.User.Password()
	sum := sha256.Sum256([]byte(pass))
	key := strings.ToLower(u.Scheme) + "://" + u.User.Username() + "@" + strings.ToLower(u.Host) + "#" + hex.EncodeToString(sum[:8])
	st, _ := digestStates.LoadOrStore(key, &digestState{})
	return st.(*digestState)
}

func NewHTTPSnapshot(url string) *HTTPSnapshot {
	return NewHTTPSource(url, HTTPOptions{})
}

// NewHTTPSource is NewHTTPSnapshot with the watch's options.
func NewHTTPSource(url string, opt HTTPOptions) *HTTPSnapshot {
	c := &http.Client{}
	if opt.TLSInsecure {
		c.Transport = insecureTransport()
	}
	return &HTTPSnapshot{URL: url, Client: c, Headers: opt.Headers}
}

var (
	insecureOnce sync.Once
	insecureRT   *http.Transport
)

// insecureTransport is shared by every tls_insecure source, so a snapshot
// polled every few seconds reuses its connections instead of leaving a new
// pool behind each time. It is never the default transport: a watch
// without tls_insecure can't end up on it.
func insecureTransport() *http.Transport {
	insecureOnce.Do(func() {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- opted in per watch: tls_insecure
		insecureRT = t
	})
	return insecureRT
}

// ErrCertNotTrusted marks a grab that failed because the camera's HTTPS
// certificate didn't verify.
var ErrCertNotTrusted = errors.New("certificate not trusted: set tls_insecure: true for this camera")

func (h *HTTPSnapshot) Grab(ctx context.Context) (image.Image, error) {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = httpTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	// Returning cancels the request, which also closes an MJPEG stream's
	// connection after its first frame.
	defer cancel()
	label := redactURL(h.URL)
	auth, sentNonce := h.cachedDigest()
	resp, err := h.get(ctx, auth)
	if err != nil {
		return nil, h.fail(label, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		if retry, ok := h.digestRetry(ctx, resp, sentNonce); ok {
			resp.Body.Close()
			if resp, err = retry(); err != nil {
				return nil, h.fail(label, err)
			}
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snapshot %s: status %d", label, resp.StatusCode)
	}
	var img image.Image
	if mt, params, perr := mime.ParseMediaType(resp.Header.Get("Content-Type")); perr == nil && strings.HasPrefix(mt, "multipart/") {
		img, err = firstPart(resp.Body, params["boundary"])
	} else {
		img, err = decodeImage(resp.Body)
	}
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: decode: %w", label, err)
	}
	return img, nil
}

func (h *HTTPSnapshot) get(ctx context.Context, authorization string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range h.Headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return h.Client.Do(req)
}

// cachedDigest answers the challenge the camera sent last time, with the
// next nonce count, and returns the nonce it answered. Both are "" before
// the camera has asked for Digest (the request then carries the URL's
// credentials as Basic, as Go sends them).
func (h *HTTPSnapshot) cachedDigest() (auth, nonce string) {
	u, err := url.Parse(h.URL)
	if err != nil {
		return "", ""
	}
	st := digestFor(u)
	if st == nil {
		return "", ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.ch == nil {
		return "", ""
	}
	st.nc++
	pass, _ := u.User.Password()
	return st.ch.authorization(u.User.Username(), pass, http.MethodGet, u.RequestURI(), "", st.nc), st.ch.nonce
}

// digestRetry is the second request for a 401 that asks for Digest auth,
// when the URL carries credentials to answer it with: a first contact, or
// a remembered nonce the camera has since retired. The challenge is kept
// for the next grabs. One retry only: a second 401 is the camera turning
// the login down. sentNonce is the nonce the refused request answered
// ("" for a Basic first contact). When the camera refuses an answer to
// the nonce it is still offering and doesn't call it stale, the password
// is wrong: no retry, so a wrong password costs the account one failed
// login per poll, not two (some cameras lock it after a few).
func (h *HTTPSnapshot) digestRetry(ctx context.Context, resp *http.Response, sentNonce string) (func() (*http.Response, error), bool) {
	u := resp.Request.URL
	st := digestFor(u)
	if st == nil {
		return nil, false
	}
	ch, ok := pickDigest(resp.Header.Values("WWW-Authenticate"))
	st.mu.Lock()
	defer st.mu.Unlock()
	if !ok {
		// The camera doesn't offer Digest (any more): back to Basic.
		st.ch, st.nc = nil, 0
		return nil, false
	}
	if sentNonce != "" && ch.nonce == sentNonce && !ch.stale {
		return nil, false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	st.ch, st.nc = &ch, 1
	pass, _ := u.User.Password()
	auth := ch.authorization(u.User.Username(), pass, http.MethodGet, u.RequestURI(), "", st.nc)
	return func() (*http.Response, error) { return h.get(ctx, auth) }, true
}

func (h *HTTPSnapshot) fail(label string, err error) error {
	var cv *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var he x509.HostnameError
	var ci x509.CertificateInvalidError
	if errors.As(err, &cv) || errors.As(err, &ua) || errors.As(err, &he) || errors.As(err, &ci) {
		return fmt.Errorf("snapshot %s: %w (%v)", label, ErrCertNotTrusted, err)
	}
	return fmt.Errorf("snapshot %s: %w", label, err)
}

// redactURL is the URL for an error message, with any password masked.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Redacted()
}

// firstPart decodes the first frame of a multipart stream. The declared
// boundary is taken with or without the leading "--" some cameras put in
// the header. A part that states its Content-Length is read to exactly
// that length, so the frame is decoded without waiting for the next one.
func firstPart(body io.Reader, boundary string) (image.Image, error) {
	boundary = strings.TrimPrefix(boundary, "--")
	if boundary == "" {
		return nil, errors.New("multipart stream without a boundary")
	}
	mr := multipart.NewReader(body, boundary)
	part, err := mr.NextPart()
	if err != nil {
		return nil, fmt.Errorf("read first frame of the stream: %w", err)
	}
	var r io.Reader = part
	if n, err := strconv.ParseInt(part.Header.Get("Content-Length"), 10, 64); err == nil && n > 0 && n <= maxImageBytes {
		buf := make([]byte, n)
		if _, err := io.ReadFull(part, buf); err != nil {
			return nil, fmt.Errorf("read first frame of the stream: %w", err)
		}
		r = bytes.NewReader(buf)
	}
	return decodeImage(r)
}
