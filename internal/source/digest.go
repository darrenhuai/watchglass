package source

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

// HTTP Digest access authentication (RFC 7616), the client half, for the
// cameras that answer a snapshot request with 401 and a Digest challenge
// (Dahua, Amcrest, many Hikvision). Only qop=auth (or no qop, RFC 2069),
// with MD5 or SHA-256 and their -sess variants: auth-int would mean
// hashing a request body a GET doesn't have, and it is never the only
// qop a camera offers.

type digestChallenge struct {
	realm, nonce, opaque, algorithm string
	qopAuth                         bool // the server offers qop=auth
	userhash                        bool
	stale                           bool // the nonce expired; the credentials were fine
}

// pickDigest returns the challenge to answer from a 401's WWW-Authenticate
// values: SHA-256 when the server offers it alongside MD5 (RFC 7616 asks
// clients to prefer the strongest), else the first usable one.
func pickDigest(headers []string) (digestChallenge, bool) {
	var best digestChallenge
	found := false
	for _, h := range headers {
		scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
		if !strings.EqualFold(scheme, "Digest") {
			continue
		}
		p := parseAuthParams(rest)
		c := digestChallenge{realm: p["realm"], nonce: p["nonce"], opaque: p["opaque"], algorithm: p["algorithm"]}
		if c.algorithm == "" {
			c.algorithm = "MD5"
		}
		if newHash(c.algorithm) == nil || c.nonce == "" {
			continue
		}
		if q, ok := p["qop"]; ok {
			for _, v := range strings.Split(q, ",") {
				if strings.EqualFold(strings.TrimSpace(v), "auth") {
					c.qopAuth = true
				}
			}
			if !c.qopAuth {
				continue // auth-int only
			}
		}
		c.userhash = strings.EqualFold(p["userhash"], "true")
		c.stale = strings.EqualFold(p["stale"], "true")
		if !found || (strings.HasPrefix(strings.ToUpper(c.algorithm), "SHA-256") && !strings.HasPrefix(strings.ToUpper(best.algorithm), "SHA-256")) {
			best, found = c, true
		}
	}
	return best, found
}

// parseAuthParams reads the comma-separated name=value pairs of a
// challenge, where a value may be a quoted string with commas and
// backslash escapes in it.
func parseAuthParams(s string) map[string]string {
	out := map[string]string{}
	for {
		s = strings.TrimLeft(s, " \t,")
		if s == "" {
			return out
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return out
		}
		name := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = strings.TrimLeft(s[eq+1:], " \t")
		var val string
		if strings.HasPrefix(s, `"`) {
			var b strings.Builder
			i := 1
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
			}
			val = b.String()
			if i < len(s) {
				i++
			}
			s = s[i:]
		} else {
			end := strings.IndexByte(s, ',')
			if end < 0 {
				end = len(s)
			}
			val = strings.TrimSpace(s[:end])
			s = s[end:]
		}
		out[name] = val
	}
}

func newHash(algorithm string) func() hash.Hash {
	switch strings.ToUpper(algorithm) {
	case "MD5", "MD5-SESS":
		return md5.New
	case "SHA-256", "SHA-256-SESS":
		return sha256.New
	}
	return nil
}

// authorization is the Authorization header answering c for a GET of uri
// (the request target: path and query), as the nc-th request made with
// c's nonce. cnonce is random unless a test sets it.
func (c digestChallenge) authorization(user, pass, method, uri, cnonce string, count uint32) string {
	hf := newHash(c.algorithm)
	h := func(s string) string {
		x := hf()
		x.Write([]byte(s))
		return hex.EncodeToString(x.Sum(nil))
	}
	if cnonce == "" {
		var b [12]byte
		_, _ = rand.Read(b[:])
		cnonce = hex.EncodeToString(b[:])
	}
	nc := fmt.Sprintf("%08x", count)
	ha1 := h(user + ":" + c.realm + ":" + pass)
	if strings.HasSuffix(strings.ToUpper(c.algorithm), "-SESS") {
		ha1 = h(ha1 + ":" + c.nonce + ":" + cnonce)
	}
	ha2 := h(method + ":" + uri)
	var response string
	if c.qopAuth {
		response = h(ha1 + ":" + c.nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	} else {
		response = h(ha1 + ":" + c.nonce + ":" + ha2)
	}
	username := user
	if c.userhash {
		username = h(user + ":" + c.realm)
	}
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	parts := []string{
		"username=" + q(username),
		"realm=" + q(c.realm),
		"nonce=" + q(c.nonce),
		"uri=" + q(uri),
		"algorithm=" + c.algorithm,
		"response=" + q(response),
	}
	if c.opaque != "" {
		parts = append(parts, "opaque="+q(c.opaque))
	}
	if c.qopAuth {
		parts = append(parts, "qop=auth", "nc="+nc, "cnonce="+q(cnonce))
	}
	if c.userhash {
		parts = append(parts, "userhash=true")
	}
	return fmt.Sprintf("Digest %s", strings.Join(parts, ", "))
}
