package rice

import (
	"github.com/valyala/fasthttp"

	"github.com/vietpham102301/rice-http/internal/router"
)

// Ctx is a borrowed handle to one in-flight request.
//
// It is valid only for the duration of the handler that received it, along with
// every []byte it hands out. See the borrow contract in doc.go.
type Ctx struct {
	// poison is first on purpose. It is zero-sized in release builds, and Go pads
	// a zero-sized final field so a pointer to it cannot point past the struct.
	poison poison

	fctx *fasthttp.RequestCtx
	app  *App

	// params is held by value, not by pointer, so that capturing route
	// parameters needs no allocation of its own. This is what ADR-0005's
	// Lookup(path, *Params) signature exists to make possible, and it is why the
	// Ctx must be constructed before the lookup runs.
	params router.Params

	// store backs Set and Get. newCtx pre-sizes it to storeCapacity.
	store []entry
}

// reset binds the context to a request, or unbinds it when app and fctx are nil.
//
// acquire calls it to bind a pooled Ctx and release calls it to unbind one, so
// it clears everything a previous request could have left behind rather than
// trusting it to be empty.
func (c *Ctx) reset(app *App, fctx *fasthttp.RequestCtx) {
	c.app = app
	c.fctx = fctx
	c.params.Reset()
	c.resetStore()
}

// RequestCtx exposes the underlying fasthttp context.
//
// It is the escape hatch for anything rice does not wrap. Everything the
// borrow contract says about Ctx applies to what you reach through it.
func (c *Ctx) RequestCtx() *fasthttp.RequestCtx {
	c.poison.check()
	return c.fctx
}

// Method returns the HTTP verb.
//
// Borrowed: the returned slice is valid only until the handler returns.
func (c *Ctx) Method() []byte {
	c.poison.check()
	return c.fctx.Method()
}

// Path returns the request path, without the query string.
//
// Borrowed: the returned slice is valid only until the handler returns.
func (c *Ctx) Path() []byte {
	c.poison.check()
	return c.fctx.Path()
}
