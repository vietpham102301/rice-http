package rice

import (
	"net"

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

// Query returns the first value of the named query-string parameter, decoded,
// or an empty slice if it is absent.
//
// Borrowed: the returned slice is valid only until the handler returns.
func (c *Ctx) Query(name string) []byte {
	c.poison.check()
	return c.fctx.QueryArgs().Peek(name)
}

// Header returns the value of the named request header, or an empty slice if it
// is absent. The name is matched case-insensitively.
//
// Borrowed: the returned slice is valid only until the handler returns.
func (c *Ctx) Header(name string) []byte {
	c.poison.check()
	return c.fctx.Request.Header.Peek(name)
}

// Body returns the request body, or an empty slice if there is none.
//
// Borrowed: the returned slice is valid only until the handler returns. To
// decode it, json.Unmarshal copies what it keeps, so the result is yours.
func (c *Ctx) Body() []byte {
	c.poison.check()
	return c.fctx.PostBody()
}

// ClientIP returns the address the request came from: the connection's peer,
// never a header.
//
// Behind a reverse proxy that is the proxy's address. X-Forwarded-For is not
// consulted, because any client can set it; a middleware that resolves it
// against a known number of trusted hops rewrites the connection address with
// fasthttp's SetRemoteAddr, and this accessor then reports the result.
//
// Borrowed: net.IP is a []byte, and it is valid only until the handler returns.
func (c *Ctx) ClientIP() net.IP {
	c.poison.check()
	return c.fctx.RemoteIP()
}
