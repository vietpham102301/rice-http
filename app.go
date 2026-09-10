package rice

import (
	"errors"
	"net"
	"sync"

	"github.com/valyala/fasthttp"

	"github.com/vietpham102301/rice-http/internal/router"
)

// Option configures an App at construction time.
//
// M1 ships no options. The first ones arrive in M7 with server timeouts. The
// variadic parameter is present now so that adding them later does not change
// the signature of New.
type Option func(*App)

// App is the root of a rice application. It owns the routes, the fasthttp
// server, and the listener.
type App struct {
	// trees holds one route tree per common verb, indexed by a method constant.
	// It is an array of values, so every element starts as a zero Tree whose
	// inner map is nil until its first Insert.
	trees [methodCount]router.Tree[Handler]

	// rare holds trees for verbs without a reserved slot. It stays nil for
	// applications that never register one.
	rare map[string]*router.Tree[Handler]

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

// FasthttpHandler returns the request handler this App installs on its server.
//
// Use it to mount rice inside an existing fasthttp server, or to drive the
// dispatch path directly in benchmarks without opening a socket.
func (a *App) FasthttpHandler() fasthttp.RequestHandler { return a.handle }

// handle is the dispatch path: one request in, one response out.
func (a *App) handle(fctx *fasthttp.RequestCtx) {
	// The Ctx is constructed before the lookup, which reverses the ordering M2
	// chose. It is not a regression walked back: the lookup fills a *Params, and
	// Params lives on the Ctx so that capturing parameters costs no allocation
	// of its own. That is ADR-0005's design, and the price of it is that the
	// borrowed handle must exist before anything can fill it.
	//
	// The consequence is that a miss now pays for a Ctx again, and the
	// zero-allocation 404 path M2 measured is gone. M2 predicted M5's
	// configurable ErrorHandler would end it; M3 ended it first, for a different
	// reason. That is recorded in ADR-0005.
	c := &Ctx{}
	c.reset(a, fctx)

	// M3 allocates a Ctx per request on purpose. This is the baseline M6's
	// sync.Pool is measured against. Do not optimise it here.
	h, ok := a.lookup(fctx.Method(), fctx.Path(), &c.params)
	if !ok {
		a.handleError(c, ErrNotFound)
		return
	}

	if err := h(c); err != nil {
		a.handleError(c, err)
	}
}

// handleError is M2's error funnel.
//
// It discards any partially written body and never writes the cause to the
// response: leaking internal error strings to clients is how databases end up
// described in HTTP responses. ErrNotFound is the one error it recognises.
//
// M5 replaces this with a configurable ErrorHandler and the HTTPError type,
// at which point the errors.Is check below generalises rather than disappears.
func (a *App) handleError(c *Ctx, err error) {
	status := fasthttp.StatusInternalServerError
	body := "Internal Server Error"

	if errors.Is(err, ErrNotFound) {
		status = fasthttp.StatusNotFound
		body = "Not Found"
	}

	c.fctx.ResetBody()
	c.fctx.SetStatusCode(status)
	c.fctx.SetContentType(MIMETextPlainUTF8)
	c.fctx.SetBodyString(body)
}
