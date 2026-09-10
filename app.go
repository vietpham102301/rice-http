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
	// M3 Task 5 moves this onto the Ctx, which is where it belongs and where the
	// pool in M6 can reuse it. It is a stack local here only so that Task 4's
	// change stays confined to the router.
	var params router.Params

	h, ok := a.lookup(fctx.Method(), fctx.Path(), &params)
	if !ok {
		// M1 allocated the Ctx before deciding whether it had a handler, so a
		// miss paid for a context nobody read. M2 looks up first. The miss path
		// still constructs a Ctx, because the funnel takes a *Ctx and M5 wants
		// a real one to build a custom 404 from.
		//
		// It costs zero allocations today, but only by accident: this Ctx is
		// passed solely to handleError, a concrete method the compiler can see
		// through, so escape analysis keeps it on the stack (go build
		// -gcflags=-m reports "does not escape" here and "escapes to heap" on
		// the hit path below, where h is a function value). That is a
		// compiler-visibility effect, not a design guarantee. When M5 turns the
		// funnel into a configurable ErrorHandler function value, this Ctx will
		// escape too and the miss path goes back to one allocation. Do not
		// build anything on the zero.
		c := &Ctx{}
		c.reset(a, fctx)
		a.handleError(c, ErrNotFound)
		return
	}

	// M2 allocates a Ctx per request on purpose. This is the baseline M6's
	// sync.Pool is measured against. Do not optimise it here.
	c := &Ctx{}
	c.reset(a, fctx)

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
