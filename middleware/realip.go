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
			ip := net.ParseIP(string(bytes.TrimSpace(entry)))
			if ip == nil {
				return nil
			}
			return &net.TCPAddr{IP: ip}
		}
	}
	return nil
}
