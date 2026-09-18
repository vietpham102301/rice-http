// Package rice is a small HTTP framework built on fasthttp.
//
// # Borrow contract
//
// Every value reachable from a *Ctx is borrowed, not owned. It is valid only
// until the handler returns. That includes the *Ctx itself, the per-request store
// behind Set and Get, and every []byte it hands out: route parameters, the path,
// and the method. The *Ctx is returned to a pool and handed to another request;
// the memory behind those slices belongs to fasthttp and is reused for the next
// request on the same connection.
//
// Accessors that return []byte are free and borrowed. Accessors that return
// string copy, cost one allocation, and are safe to keep. The naming makes the
// expensive choice the longer one to type:
//
//	id := c.Param("id")        // borrowed: valid until the handler returns
//	id := c.ParamString("id")  // owned: safe to keep, costs one allocation
//
// # Debug build
//
// Build or test with -tags ricedebug to make any use of a *Ctx after its handler
// returns panic. The check costs nothing in a normal build.
//
// It catches misuse of the *Ctx only. A []byte kept from Param or Path, or the
// *fasthttp.RequestCtx kept from RequestCtx, points into fasthttp's memory, which
// rice cannot poison; keeping one past the handler is still a bug and the debug
// build will not report it.
//
// See docs/adr/0005-context-pooling-and-borrow-contract.md.
//
// # Lifecycle
//
// RunContext serves until its context is done, then calls Shutdown with a grace
// period; pair it with signal.NotifyContext to stop on SIGINT or SIGTERM. rice
// itself does not catch signals.
//
// OnStart hooks run in registration order before the first connection is
// accepted, and the first error stops serving. OnShutdown hooks run in reverse
// registration order after the drain, on the first Shutdown only, and every one
// runs even when another fails.
//
// Nothing is served after Shutdown returns. If in-flight requests have not
// finished when its context ends, Shutdown closes their connections and returns
// an error wrapping ErrShutdownTimeout; the handlers run to completion and their
// responses are lost. See docs/adr/0009-shutdown-force-closes-at-deadline.md.
// Shutdown may be called more than once and concurrently: the calls take
// turns, and one whose context ends while it waits force-closes and returns.
package rice
