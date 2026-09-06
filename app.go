package rice

import (
	"net"
	"sync"

	"github.com/valyala/fasthttp"
)

// Option configures an App at construction time.
//
// M1 ships no options. The first ones arrive in M7 with server timeouts. The
// variadic parameter is present now so that adding them later does not change
// the signature of New.
type Option func(*App)

// App is the root of a rice application. It owns the handler, the fasthttp
// server, and the listener.
type App struct {
	h   Handler
	srv *fasthttp.Server

	mu sync.Mutex
	ln net.Listener
}

// New creates an App.
func New(opts ...Option) *App {
	a := &App{}
	a.srv = &fasthttp.Server{
		Handler: a.handle,
		Name:    "rice",
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// SetHandler installs the single handler this App serves.
//
// M1 scaffolding. rice has no router yet, so every request reaches the same
// handler. M2 replaces this with per-method route registration and removes it.
func (a *App) SetHandler(h Handler) { a.h = h }

// FasthttpHandler returns the request handler this App installs on its server.
//
// Use it to mount rice inside an existing fasthttp server, or to drive the
// dispatch path directly in benchmarks without opening a socket.
func (a *App) FasthttpHandler() fasthttp.RequestHandler { return a.handle }

// handle is the dispatch path: one request in, one response out.
func (a *App) handle(fctx *fasthttp.RequestCtx) {
	// M1 allocates a Ctx per request on purpose. This line is the baseline that
	// M6's sync.Pool is measured against. Do not optimise it here.
	c := &Ctx{}
	c.reset(a, fctx)

	if a.h == nil {
		fctx.SetStatusCode(fasthttp.StatusNotFound)
		return
	}

	if err := a.h(c); err != nil {
		a.handleError(c, err)
	}
}

// handleError is M1's error funnel.
//
// It discards any partially written body, responds 500, and never writes the
// cause to the response: leaking internal error strings to clients is how
// databases end up described in HTTP responses.
//
// M5 replaces this with a configurable ErrorHandler and the HTTPError type. The
// err parameter is unused until then and is present so that the signature does
// not change.
func (a *App) handleError(c *Ctx, err error) {
	c.fctx.ResetBody()
	c.fctx.SetStatusCode(fasthttp.StatusInternalServerError)
	c.fctx.SetContentType(MIMETextPlainUTF8)
	c.fctx.SetBodyString("Internal Server Error")
}
