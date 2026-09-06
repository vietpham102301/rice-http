// Package rice is a small HTTP framework built on fasthttp.
//
// # Borrow contract
//
// Every value reachable from a *Ctx is borrowed, not owned. It is valid only
// until the handler returns. That includes the *Ctx itself and every []byte it
// hands out: route parameters, headers, the path, and the request body. The
// memory behind those slices belongs to fasthttp and is reused for the next
// request on the same connection.
//
// Accessors that return []byte are free and borrowed. Accessors that return
// string copy, cost one allocation, and are safe to keep. The naming makes the
// expensive choice the longer one to type:
//
//	id := c.Param("id")        // borrowed: valid until the handler returns
//	id := c.ParamString("id")  // owned: safe to keep, costs one allocation
//
// See docs/adr/0005-context-pooling-and-borrow-contract.md.
package rice
