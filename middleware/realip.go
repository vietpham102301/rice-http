package middleware

import (
	"bytes"
	"net"

	rice "github.com/vietpham102301/rice-http"
)

// RealIP makes c.ClientIP report the client's address when the service sits
// behind trustedHops proxies that each append to X-Forwarded-For.
//
// It counts from the right. Each proxy appends the address it received a
// connection from, so only the right-hand entries — written by proxies this
// service trusts — cannot be forged; the left-hand ones are whatever the client
// sent. The client is the entry trustedHops from the right. With fewer entries
// than that, or an entry that is not an IP address, c.ClientIP reports the
// connection's own address. It never falls back to the leftmost entry.
//
// An entry may carry a port, as some load balancers write it:
// 203.0.113.9:4711, or [2001:db8::1]:4711 for IPv6, which must be bracketed to
// carry one. The port is dropped. An unbracketed IPv6 address is read whole
// and never split at its last colon.
//
// It sets the address on every request, resolved or not. fasthttp serves every
// request on a connection from one context and clears a rewritten address only
// when the connection closes, so a middleware that sometimes skipped the rewrite
// would attribute a request to whichever client came before it on the same
// keep-alive connection — behind a reverse proxy, a different client.
//
// Install it before anything that reads c.ClientIP. A trustedHops below 1
// panics.
func RealIP(trustedHops int) rice.Middleware {
	if trustedHops < 1 {
		panic("rice: middleware.RealIP: trustedHops must be at least 1")
	}
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			fctx := c.RequestCtx()
			// PeekAll's result is overwritten by the next Peek, so it is
			// consumed here before any other header is read.
			fctx.SetRemoteAddr(resolve(fctx.Request.Header.PeekAll("X-Forwarded-For"), trustedHops))
			return next(c)
		}
	}
}

// resolve returns the entry trustedHops from the right across every header
// line, or nil when there is none that parses as an IP address.
//
// An empty entry — from a trailing comma, say — counts as a position and, not
// being an IP address, forces the fallback rather than being skipped. So does
// any entry parseEntry rejects.
//
// A nil return is an untyped nil net.Addr, which SetRemoteAddr documents as
// restoring the connection's address. Returning a typed nil *net.TCPAddr
// instead would make a non-nil interface and break that.
func resolve(lines [][]byte, trustedHops int) net.Addr {
	seen := 0
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		for len(line) > 0 {
			var entry []byte
			if j := bytes.LastIndexByte(line, ','); j >= 0 {
				entry, line = line[j+1:], line[:j]
			} else {
				entry, line = line, nil
			}
			seen++
			if seen < trustedHops {
				continue
			}
			ip := parseEntry(entry)
			if ip == nil {
				return nil
			}
			return &net.TCPAddr{IP: ip}
		}
	}
	return nil
}

// parseEntry returns the address in one X-Forwarded-For entry, or nil.
//
// It accepts the shapes load balancers write: a bare address, IPv4 or IPv6;
// an IPv4 address with a port, 203.0.113.9:4711; and a bracketed IPv6
// address with or without a port, [2001:db8::1] and [2001:db8::1]:4711. The
// port is dropped. It rejects everything else, including brackets around an
// IPv4 address and a port outside 1–65535, so a malformed entry falls back to
// the connection's address rather than to a guess.
//
// An entry with more than one colon and no brackets is an IPv6 address and is
// read whole. It is never split at its last colon: 2001:db8::1:4711 is itself
// a valid address, and reading 4711 as a port would attribute the request to
// 2001:db8::1, an address nobody wrote.
func parseEntry(entry []byte) net.IP {
	entry = bytes.TrimSpace(entry)
	if len(entry) > 0 && entry[0] == '[' {
		end := bytes.IndexByte(entry, ']')
		if end < 0 || !validPortSuffix(entry[end+1:]) {
			return nil
		}
		host := entry[1:end]
		// Written with a colon or it is not IPv6: [203.0.113.9] is rejected,
		// while an IPv4-mapped [::ffff:203.0.113.9] is accepted.
		if bytes.IndexByte(host, ':') < 0 {
			return nil
		}
		return net.ParseIP(string(host))
	}
	if i := bytes.IndexByte(entry, ':'); i >= 0 && bytes.IndexByte(entry[i+1:], ':') < 0 {
		// Exactly one colon: an IPv4 address and a port. The host has no
		// colon, so ParseIP can only return IPv4 or nil.
		if !validPort(entry[i+1:]) {
			return nil
		}
		return net.ParseIP(string(entry[:i]))
	}
	return net.ParseIP(string(entry))
}

// validPortSuffix reports whether what follows a closing bracket is nothing or
// a colon and a valid port.
func validPortSuffix(rest []byte) bool {
	return len(rest) == 0 || rest[0] == ':' && validPort(rest[1:])
}

// validPort reports whether p is a decimal port from 1 to 65535, written with
// digits only: no sign, no space, at most five characters.
func validPort(p []byte) bool {
	if len(p) == 0 || len(p) > 5 {
		return false
	}
	n := 0
	for _, b := range p {
		if b < '0' || b > '9' {
			return false
		}
		n = n*10 + int(b-'0')
	}
	return n >= 1 && n <= 65535
}
