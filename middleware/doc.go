// Package middleware holds rice's optional, opt-in middleware.
//
// Nothing here is imported by package rice. Importing rice must not drag in
// anything a user did not ask for, and the import graph is the honest signal of
// what costs what — which is why this is a separate package rather than a
// subdirectory of helpers or a set of methods on App.
//
// The dependency runs one way: middleware imports rice. Nothing under
// internal/ may import rice; middleware/ is not under internal/, so it may.
//
// # What is here
//
// Recover turns a panic into an error that outer middleware can see. RealIP
// makes c.ClientIP report the client's address behind a known number of
// trusted proxies. RequestID gives every request an id, read back with
// RequestIDFrom. Logger writes one log line per request with the status the
// client actually receives. Timeout gives the rest of the chain a deadline on
// c.Context() and answers 503 when that deadline is what made it fail.
//
// Installed with App.Use, each of them also runs on a request that matched no
// route, so Logger records 404s and RequestID gives them an id. On such a
// request c.Param is empty. See docs/adr/0012-application-middleware-runs-on-route-misses.md.
//
// # The recommended order
//
//	app.Use(
//		middleware.Logger(l),              // outermost: times everything, settles errors, logs last
//		middleware.Recover(),              // inside Logger: a panic becomes an error Logger can see
//		middleware.RealIP(1),              // only behind a proxy; 1 is the number of trusted hops
//		middleware.RequestID(),
//		middleware.Timeout(5*time.Second), // inside Logger, so the 503 it returns is what Logger records
//	)
//
// Logger is outermost because it consumes the error: it settles it with
// c.HandleError, so the status it records is the one the ErrorHandler wrote,
// and then returns nil. Nothing outside Logger sees that error. Middleware that
// inspects or rewrites errors belongs inside it. See
// docs/adr/0013-middleware-can-settle-a-request.md.
//
// Logger reads the address and the request id after the chain returns, so
// although it is outermost it records what RealIP and RequestID did inside it.
//
// Timeout is cooperative: work that honours c.Context() stops at the deadline,
// but a handler that ignores its context runs to completion and its late answer
// is returned.
// See docs/adr/0014-timeout-is-cooperative.md.
//
// # The one trap in that order
//
// A request that panics is not logged unless Recover is installed inside
// Logger. The panic unwinds through Logger's frame before Logger can record
// anything. Core's own recovery still answers the request with a 500, but
// Logger writes no line for it. With Recover directly inside Logger, the panic
// becomes an ordinary error and is logged as a 500.
//
// Logger does not recover and re-panic to close this gap: that would move the
// stack trace core records to Logger's frame and point whoever debugs the panic
// at the wrong code.
//
// # RealIP trusts a count, not a header
//
// Install RealIP only when the proxies in front of the service are ones you
// run, and give it their number. Without a proxy, the rightmost entry of
// X-Forwarded-For is whatever the client sent, and RealIP(1) would report it.
//
// RealIP parses each X-Forwarded-For entry as a bare IP address. An entry with
// a port, such as 203.0.113.9:4711, or a bracketed IPv6 entry, such as
// [2001:db8::1], does not parse, and the request falls back to the connection's
// own address. That fallback is safe — it never attributes a request to an
// address the client chose — but behind a proxy that always appends a port,
// RealIP always reports the proxy's address.
//
// # What they cost
//
// Unlike core, these allocate. Measured with the fixtures named in
// docs/05-performance-model.md: RealIP 3 objects per request, RequestID 2 when
// it generates an id, Logger 3 with slog's JSON handler, Logger around
// RequestID 6, and Timeout 4.
package middleware
