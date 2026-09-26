package rice

import (
	"bytes"

	"github.com/valyala/fasthttp"
)

// Accepts returns the offer the request's Accept header prefers, or "" when it
// accepts none of them. An offer is a media type, "type/subtype", optionally
// with parameters: "text/plain; charset=utf-8". Only type/subtype is matched,
// and the offer is returned exactly as passed, so it can go straight to
// SetContentType.
//
// It adds Vary: Accept to the response, once, whatever it returns and whether
// or not the request sent Accept: the response depends on the header either
// way, and a cache that is not told so serves one client's format to another.
// This is a write from something that reads like an accessor, and it is
// deliberate; see ADR-0018.
//
// Matching follows RFC 9110 §12.5.1. A request with no Accept header accepts
// anything and gets the first offer. Otherwise each offer takes the quality of
// the most specific range that matches it — text/html over text/* over */* —
// q=0 means not acceptable, the highest quality wins, and a tie goes to the
// earlier offer. Every Accept line counts. A malformed element is skipped.
//
// An offer that is not type/subtype, or whose type or subtype is *, panics: the
// server must name what it produces.
//
// It allocates nothing. It does not write a status; a handler with nothing to
// offer returns ErrNotAcceptable.
func (c *Ctx) Accepts(offers ...string) string {
	c.poison.check()
	best := negotiate(c.fctx.Request.Header.PeekAll(fasthttp.HeaderAccept), offers)
	c.addVaryAccept()
	return best
}

// negotiate is Accepts' matching, over the Accept header's lines. Every offer is
// validated, even when there is no Accept header, so a bad offer panics on the
// first request rather than on the first request that sends one.
func negotiate(accepts [][]byte, offers []string) string {
	present := false
	for _, line := range accepts {
		if len(trimOWS(line)) > 0 {
			present = true
			break
		}
	}

	best, bestQ := "", 0
	for i, offer := range offers {
		typ, sub := splitOffer(offer)
		if !present {
			if i == 0 {
				best = offer
			}
			continue
		}
		// Strictly greater: an offer with the same quality as an earlier one
		// never displaces it, which is the server's tie-break (ADR-0018).
		if q := offerQuality(accepts, typ, sub); q > bestQ {
			best, bestQ = offer, q
		}
	}
	return best
}

// splitOffer returns an offer's type and subtype, panicking on an offer that is
// not a concrete media type.
func splitOffer(offer string) (typ, sub string) {
	mt := offer
	if i := indexByteString(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	mt = trimOWSString(mt)
	i := indexByteString(mt, '/')
	if i <= 0 || i == len(mt)-1 {
		panic("rice: Accepts offer " + `"` + offer + `"` + " is not a media type of the form type/subtype")
	}
	typ, sub = mt[:i], mt[i+1:]
	if typ == "*" || sub == "*" {
		panic("rice: Accepts offer " + `"` + offer + `"` + " is a wildcard; offer the media types the handler produces")
	}
	return typ, sub
}

// offerQuality returns the quality, in thousandths, that the Accept lines give
// typ/sub: the q of the most specific range that matches it, the first such
// range among equally specific ones, or 0 when none matches.
func offerQuality(accepts [][]byte, typ, sub string) int {
	q, spec := 0, 0
	for _, line := range accepts {
		for rest := line; len(rest) > 0; {
			var elem []byte
			elem, rest = nextItem(rest, ',')
			if eq, es, ok := matchRange(elem, typ, sub); ok && es > spec {
				q, spec = eq, es
			}
		}
	}
	return q
}

// matchRange reports whether one Accept element matches typ/sub, how specific
// the match is (3 type/subtype, 2 type/*, 1 */*), and its quality. An element
// whose range is malformed or whose q is not a qvalue matches nothing.
func matchRange(elem []byte, typ, sub string) (q, spec int, ok bool) {
	r, params := nextItem(elem, ';')
	r = trimOWS(r)
	i := bytes.IndexByte(r, '/')
	if i <= 0 || i == len(r)-1 {
		return 0, 0, false
	}
	rt, rs := r[:i], r[i+1:]
	switch {
	case isStar(rt) && isStar(rs):
		spec = 1
	case isStar(rt):
		return 0, 0, false // */html is not a media range
	case !equalFoldASCII(rt, typ):
		return 0, 0, false
	case isStar(rs):
		spec = 2
	case equalFoldASCII(rs, sub):
		spec = 3
	default:
		return 0, 0, false
	}

	q = 1000
	for len(params) > 0 {
		var p []byte
		p, params = nextItem(params, ';')
		eq := bytes.IndexByte(p, '=')
		if eq < 0 {
			continue
		}
		if name := trimOWS(p[:eq]); len(name) == 1 && (name[0] == 'q' || name[0] == 'Q') {
			v, valid := parseQ(trimOWS(p[eq+1:]))
			if !valid {
				return 0, 0, false
			}
			q = v
		}
	}
	return q, spec, true
}

// parseQ parses an RFC 9110 qvalue into thousandths: "0" or "0." followed by up
// to three digits, or "1" or "1." followed by up to three zeros.
func parseQ(b []byte) (int, bool) {
	if len(b) == 0 || len(b) > 5 {
		return 0, false
	}
	if len(b) > 1 && b[1] != '.' {
		return 0, false
	}
	switch b[0] {
	case '0':
		q, mul := 0, 100
		for _, c := range b[min(2, len(b)):] {
			if c < '0' || c > '9' {
				return 0, false
			}
			q += int(c-'0') * mul
			mul /= 10
		}
		return q, true
	case '1':
		for _, c := range b[min(2, len(b)):] {
			if c != '0' {
				return 0, false
			}
		}
		return 1000, true
	}
	return 0, false
}

// nextItem splits s at the first sep outside a quoted string, honouring
// backslash escapes inside quotes, and returns the part before it and the rest.
func nextItem(s []byte, sep byte) (item, rest []byte) {
	quoted := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case !quoted && c == sep:
			return s[:i], s[i+1:]
		}
	}
	return s, nil
}

func isStar(b []byte) bool { return len(b) == 1 && b[0] == '*' }

func isOWS(c byte) bool { return c == ' ' || c == '\t' }

// trimOWS trims HTTP optional whitespace, space and tab, from both ends.
func trimOWS(b []byte) []byte {
	for len(b) > 0 && isOWS(b[0]) {
		b = b[1:]
	}
	for len(b) > 0 && isOWS(b[len(b)-1]) {
		b = b[:len(b)-1]
	}
	return b
}

func trimOWSString(s string) string {
	for len(s) > 0 && isOWS(s[0]) {
		s = s[1:]
	}
	for len(s) > 0 && isOWS(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

func indexByteString(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// equalFoldASCII compares b and s, folding ASCII letters only. Media types are
// ASCII tokens, so Unicode folding would be both slower and wrong.
func equalFoldASCII(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := 0; i < len(b); i++ {
		x, y := b[i], s[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// addVaryAccept adds Vary: Accept unless a Vary line already lists Accept, in
// any case, or *. It adds rather than sets, so a Vary: Origin written by CORS or
// the handler survives.
func (c *Ctx) addVaryAccept() {
	h := &c.fctx.Response.Header
	for _, line := range h.PeekAll(fasthttp.HeaderVary) {
		for rest := line; len(rest) > 0; {
			var tok []byte
			tok, rest = nextItem(rest, ',')
			tok = trimOWS(tok)
			if isStar(tok) || equalFoldASCII(tok, fasthttp.HeaderAccept) {
				return
			}
		}
	}
	h.Add(fasthttp.HeaderVary, fasthttp.HeaderAccept)
}
