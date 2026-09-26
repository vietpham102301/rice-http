package rice

import (
	"context"
	"net"
	"time"

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

	// ctx is nil unless a middleware called SetContext. Context falls back to
	// the App's base context, so binding a request costs no assignment here
	// beyond the clearing reset already owes.
	ctx context.Context

	// params is held by value, not by pointer, so that capturing route
	// parameters needs no allocation of its own. This is what ADR-0005's
	// Lookup(path, *Params) signature exists to make possible, and it is why the
	// Ctx must be constructed before the lookup runs.
	params router.Params

	// store backs Set and Get. newCtx pre-sizes it to storeCapacity.
	store []entry

	// handled is set by HandleError, and by handle before it calls the
	// funnel itself. handle reads it so that an error a middleware has already
	// settled is not answered a second time, and HandleError reads it so that
	// an ErrorHandler calling HandleError does not run itself again.
	handled bool

	// streamFn is the callback Stream or SSE recorded, and streamHeartbeat
	// its heartbeat. streamSet records that either was called, including on a
	// HEAD request, where no callback is kept. handle starts the stream only
	// after this Ctx is released; see startStream and ADR-0019.
	streamFn        func(*Stream) error
	streamHeartbeat time.Duration
	streamSet       bool
}

// reset binds the context to a request, or unbinds it when app and fctx are nil.
//
// acquire calls it to bind a pooled Ctx and release calls it to unbind one, so
// it clears everything a previous request could have left behind rather than
// trusting it to be empty.
func (c *Ctx) reset(app *App, fctx *fasthttp.RequestCtx) {
	c.app = app
	c.fctx = fctx
	c.ctx = nil
	c.handled = false
	c.streamFn = nil
	c.streamHeartbeat = 0
	c.streamSet = false
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

// Context returns the context to pass to work the request triggers: a database
// query, an outbound HTTP call.
//
// Unlike everything else reachable from a Ctx, the returned context is owned,
// not borrowed. It belongs to the App and stays valid after the handler
// returns.
//
// It is cancelled when a Shutdown gives up waiting and force-closes, meaning
// the response is about to be discarded. It is not cancelled when the client
// disconnects: fasthttp does not report that, and detecting it would cost a
// channel per request. It carries no deadline and no values of its own.
//
// See docs/adr/0010-request-context-cancels-at-force-close.md.
func (c *Ctx) Context() context.Context {
	c.poison.check()
	if c.ctx != nil {
		return c.ctx
	}
	return c.app.baseCtx
}

// SetContext replaces what Context returns for the rest of this request.
//
// It is how a middleware adds a deadline or a value. Derive from c.Context(),
// not from context.Background(): a context derived from Background silently
// drops the force-close signal.
//
// A nil context panics, as every other configuration mistake in rice does.
func (c *Ctx) SetContext(ctx context.Context) {
	c.poison.check()
	if ctx == nil {
		panic("rice: SetContext: context is nil")
	}
	c.ctx = ctx
}

// HandleError answers err through the App's ErrorHandler now, instead of after
// the chain has returned.
//
// The funnel normally runs only once every middleware has returned, so a
// middleware that reads the response status after next(c) reads it too early:
// a request that ends in a 500 still shows 200 there. A request logger calls
// HandleError first and then reads the status the client will receive. See
// docs/adr/0013-middleware-can-settle-a-request.md.
//
// A request is settled once. A nil error does nothing, and a second call does
// nothing: the first settles the request. An error returned after it has been
// handled is not answered again. A panic is still a 500 even after a request
// was settled.
func (c *Ctx) HandleError(err error) {
	c.poison.check()
	if err == nil || c.handled {
		return
	}
	c.handled = true
	c.app.callErrorHandler(c, err)
}
