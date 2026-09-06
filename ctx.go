package rice

import "github.com/valyala/fasthttp"

// Ctx is a borrowed handle to one in-flight request.
//
// It is valid only for the duration of the handler that received it, along with
// every []byte it hands out. See the borrow contract in doc.go.
type Ctx struct {
	fctx *fasthttp.RequestCtx
	app  *App
}

// reset rebinds the context to a new request.
//
// M1 constructs a fresh Ctx per request, so reset is called exactly once per
// instance. M6 introduces a sync.Pool, at which point reset becomes the point
// where a recycled Ctx drops every reference to the previous request.
func (c *Ctx) reset(app *App, fctx *fasthttp.RequestCtx) {
	c.app = app
	c.fctx = fctx
}

// RequestCtx exposes the underlying fasthttp context.
//
// It is the escape hatch for anything rice does not wrap. Everything the
// borrow contract says about Ctx applies to what you reach through it.
func (c *Ctx) RequestCtx() *fasthttp.RequestCtx { return c.fctx }

// Method returns the HTTP verb.
//
// Borrowed: the returned slice is valid only until the handler returns.
func (c *Ctx) Method() []byte { return c.fctx.Method() }

// Path returns the request path, without the query string.
//
// Borrowed: the returned slice is valid only until the handler returns.
func (c *Ctx) Path() []byte { return c.fctx.Path() }
