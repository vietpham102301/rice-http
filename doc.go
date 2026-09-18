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
package rice
