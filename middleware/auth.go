package middleware

import (
	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
)

// errUnauthorized is the 401 BasicAuth and KeyAuth answer with. It is shared,
// like rice.ErrNotFound, so a rejected request allocates nothing of its own;
// never mutate it.
var errUnauthorized = &rice.HTTPError{Code: fasthttp.StatusUnauthorized, Message: "Unauthorized"}

// unauthorized sets the challenge, when there is one, and returns the 401. The
// funnel does not reset headers, so the challenge reaches the client.
func unauthorized(c *rice.Ctx, challenge string) error {
	if challenge != "" {
		c.SetHeader("WWW-Authenticate", challenge)
	}
	return errUnauthorized
}

// cutScheme reports whether v is scheme, compared case-insensitively, followed
// by at least one space, and returns what follows with the spaces removed.
func cutScheme(v []byte, scheme string) ([]byte, bool) {
	n := len(scheme)
	if len(v) <= n || v[n] != ' ' || !equalFoldASCII(v[:n], scheme) {
		return nil, false
	}
	rest := v[n:]
	for len(rest) > 0 && rest[0] == ' ' {
		rest = rest[1:]
	}
	return rest, true
}

// trimOWS trims spaces and tabs from both ends.
func trimOWS(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

// equalFoldASCII compares b and s, folding ASCII letters only.
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

// isToken reports whether s is a non-empty RFC 9110 token, the grammar of a
// header name.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '!', c == '#', c == '$', c == '%', c == '&', c == '\'', c == '*',
			c == '+', c == '-', c == '.', c == '^', c == '_', c == '`', c == '|', c == '~':
		default:
			return false
		}
	}
	return true
}

// validRealm reports whether r can sit inside a quoted-string without escaping:
// no quote, no backslash, no control byte.
func validRealm(r string) bool {
	for i := 0; i < len(r); i++ {
		c := r[i]
		if c == '"' || c == '\\' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}
