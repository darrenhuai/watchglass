package web

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
)

// Home Assistant ingress. The Supervisor proxies
// /api/hassio_ingress/<token>/... to the add-on, strips that prefix, and
// says what it stripped in the X-Ingress-Path header, so every link, form
// action, redirect and cookie path watchglass writes back must start with
// it or the browser's next request lands outside the proxy. The prefix is
// per request (it carries the add-on's token), so it can't be a start-up
// setting like BasePath.
//
// Ingress mode is off unless main turns it on (-ingress or
// WATCHGLASS_INGRESS=1, which the add-on sets): a header any client can
// send must not rewrite links on an ordinary install. On, a request is an
// ingress request only when it comes from IngressFrom (the Supervisor's
// address, 172.30.32.2 in the add-on docs) and its header has the
// documented shape; anything else is a direct request and is answered
// exactly as with ingress off, basic auth included.

// IngressHeader is the header the Supervisor sets on every proxied request.
const IngressHeader = "X-Ingress-Path"

// ingressRoot is where every ingress prefix starts; what follows is the
// add-on's token.
const ingressRoot = "/api/hassio_ingress/"

// ingressTokenMax bounds the token: the Supervisor's is 64 hex characters.
const ingressTokenMax = 128

// ingressKey marks a request's context with its validated prefix.
type ingressKey struct{}

// validIngressPath reports whether p is "/api/hassio_ingress/" followed
// by one token of URL-safe characters (letters, digits, "-" and "_") and
// nothing else. The value ends up in href, src and action attributes, a
// cookie Path, Location headers and the data-base attribute app.js reads,
// so anything outside that shape (quotes, a second slash, "..", a scheme,
// CR or LF, a trailing slash) is refused as a whole rather than cleaned.
func validIngressPath(p string) bool {
	tok, ok := strings.CutPrefix(p, ingressRoot)
	if !ok || tok == "" || len(tok) > ingressTokenMax {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// ingressPath is the prefix r arrived under, or "" when r is not an
// ingress request: ingress mode off, a peer other than IngressFrom, no
// header, more than one, or a value outside the documented shape.
func (s *Server) ingressPath(r *http.Request) string {
	if !s.Ingress || !s.fromIngressProxy(r) {
		return ""
	}
	values := r.Header.Values(IngressHeader)
	switch {
	case len(values) == 0:
		return ""
	case len(values) > 1:
		// A real Supervisor whose header doesn't match the docs would show
		// up here or below; say so once rather than silently serving bare
		// links. Both cases share the once: one line per start is enough
		// to point at the header.
		s.ingressWarn.Do(func() {
			s.logf("ingress: ignoring %s from %s: %d values, want one; links on that page stay unprefixed", IngressHeader, r.RemoteAddr, len(values))
		})
		return ""
	case !validIngressPath(values[0]):
		s.ingressWarn.Do(func() {
			s.logf("ingress: ignoring %s %q from %s: not %s<token>; links on that page stay unprefixed", IngressHeader, values[0], r.RemoteAddr, ingressRoot)
		})
		return ""
	}
	return values[0]
}

// fromIngressProxy reports whether r's connection comes from IngressFrom.
// RemoteAddr is what the listener saw, never a forwarded-for header. A
// zero IngressFrom matches nothing: a parsed address is never the zero
// netip.Addr, so no guard is needed for it.
func (s *Server) fromIngressProxy(r *http.Request) bool {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	return ap.Addr().Unmap() == s.IngressFrom.Unmap()
}

// withIngress decides once per request whether it is an ingress request
// and, if so, records its prefix for base, isIngress and withAuth.
func (s *Server) withIngress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := s.ingressPath(r); p != "" {
			r = r.WithContext(context.WithValue(r.Context(), ingressKey{}, p))
		}
		next.ServeHTTP(w, r)
	})
}

// isIngress reports whether r came through the Supervisor's ingress proxy
// (and so has already passed Home Assistant's login).
func isIngress(r *http.Request) bool {
	_, ok := r.Context().Value(ingressKey{}).(string)
	return ok
}

// base is the prefix every link, form action, redirect and cookie path in
// r's answer starts with: the ingress prefix for an ingress request,
// BasePath otherwise. Templates get it as the page's Base field (the "u"
// and "watchURL" funcs take it first).
func (s *Server) base(r *http.Request) string {
	if p, ok := r.Context().Value(ingressKey{}).(string); ok {
		return p
	}
	return s.BasePath
}
