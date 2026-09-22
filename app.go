package rice

import (
	"context"
	"errors"
	"log"
	"net"
	"runtime/debug"
	"sync"
	"time"

	"github.com/valyala/fasthttp"

	"github.com/vietpham102301/rice-http/internal/router"
)

// Option configures an App at construction time.
//
// Configuration happens here rather than through setters so that a serving App
// cannot be reconfigured underneath a request.
type Option func(*App)

// WithErrorHandler replaces the ErrorHandler an App uses for every failure:
// returned errors, route misses, and recovered panics alike.
//
// A nil handler panics here rather than falling back to the default silently,
// in the style of every other configuration mistake in rice.
func WithErrorHandler(h ErrorHandler) Option {
	if h == nil {
		panic("rice: WithErrorHandler: handler is nil")
	}
	return func(a *App) { a.errorHandler = h }
}

// WithReadTimeout limits how long the server waits to read a full request,
// including its body. Zero, the default, means no limit. Set it in production:
// without it a client that sends half a request holds its connection forever.
func WithReadTimeout(d time.Duration) Option {
	if d < 0 {
		panic("rice: WithReadTimeout: duration is negative")
	}
	return func(a *App) { a.srv.ReadTimeout = d }
}

// WithWriteTimeout limits how long the server spends writing a response. The
// clock starts after the handler returns. Zero, the default, means no limit.
func WithWriteTimeout(d time.Duration) Option {
	if d < 0 {
		panic("rice: WithWriteTimeout: duration is negative")
	}
	return func(a *App) { a.srv.WriteTimeout = d }
}

// WithIdleTimeout limits how long a keep-alive connection may sit idle between
// requests. Zero, the default, falls back to the read timeout, and so means no
// limit when that is unset too.
func WithIdleTimeout(d time.Duration) Option {
	if d < 0 {
		panic("rice: WithIdleTimeout: duration is negative")
	}
	return func(a *App) { a.srv.IdleTimeout = d }
}

// WithMaxBodySize limits the request body to n bytes. A request whose body is
// larger is answered 413 Request Entity Too Large by the transport, before
// routing, so no handler, middleware or ErrorHandler sees it. Without this
// option the limit is fasthttp's default, 4 MiB. n must be positive.
func WithMaxBodySize(n int) Option {
	if n <= 0 {
		panic("rice: WithMaxBodySize: size must be positive")
	}
	return func(a *App) { a.srv.MaxRequestBodySize = n }
}

// App is the root of a rice application. It owns the routes, the fasthttp
// server, and the listener.
type App struct {
	// trees holds one route tree per common verb, indexed by a method constant.
	// Registration inserts raw handlers here so that a bad configuration panics
	// at the call site that caused it; Build discards these and rebuilds from
	// routes with compiled chains. See the M4 design doc, D1.
	trees [methodCount]router.Tree[Handler]

	// rare holds trees for verbs without a reserved slot. It stays nil for
	// applications that never register one.
	rare map[string]*router.Tree[Handler]

	// mws is the application-level middleware, outermost in every chain.
	mws []Middleware

	// routes records every registration in the order it happened. It is what
	// Build compiles from; the registration-time trees are only a validator.
	routes []route

	buildOnce sync.Once

	// built is read by registration to reject a late route, without a lock. That
	// is safe only because the API is a single-goroutine setup phase — register,
	// register, ..., Build — followed by serving, never concurrent registration.
	// sync.Once's happens-before applies between goroutines that both call Do;
	// register never does, so it gets no guarantee from that alone. It doesn't
	// need one: a goroutine registering concurrently with another already races
	// on routes and the trees, so this unlocked read is not the weak link.
	built bool

	// errorHandler converts every failure into a response. New sets it to
	// DefaultErrorHandler before applying options, so it is never nil and the
	// dispatch path never checks.
	//
	// It is written once at construction and read on every failing request, with
	// no lock. That is safe because it is set before the App can serve: unlike
	// routes and middleware, there is no setter, so there is no window in which
	// a serving App can be reconfigured.
	errorHandler ErrorHandler

	// miss answers every request the lookup did not match. New sets it to
	// notFound so that an unbuilt App, which the package's tests dispatch to
	// throughout, still has one; Build replaces it with notFound wrapped in the
	// application's middleware. Group and route middleware never wrap it: no
	// group matched, so none has a claim on the request. See ADR-0012.
	miss Handler

	// pool holds idle contexts. It is created in New, not Build, because the
	// dispatch path is reachable before Build: package tests call handle on unbuilt
	// Apps throughout. See the M6 design doc, D1.
	pool sync.Pool

	// maxParams is the largest parameter count of any registered route. newCtx
	// sizes parameter storage from it.
	//
	// It is written during registration and read by newCtx while serving, with no
	// lock, on the same argument as built above: registration is a
	// single-goroutine phase that ends before serving begins.
	maxParams int

	srv *fasthttp.Server

	// mu guards ln, closed and serving, which Serve and Shutdown use to agree on
	// whether serving may begin. See the M7 design doc, D5.
	mu sync.Mutex
	ln net.Listener

	// closed is set by the first Shutdown. A Serve that sees it closes its
	// listener and returns without serving: an App serves once.
	closed bool

	// serving is set while a Serve call runs, from before its OnStart hooks
	// until it returns. A second Serve that sees it is rejected with
	// ErrAlreadyServing.
	serving bool

	// connMu guards conns and forceClosed. connState takes it only when a
	// connection opens or closes, never per request. See the M7 design doc, D2.
	connMu sync.Mutex

	// conns is every connection fasthttp has reported open and not yet closed.
	// Shutdown closes them when its deadline passes. Allocated on first use.
	conns map[net.Conn]struct{}

	// forceClosed is set by the sweep. A connection accepted before the
	// listener closed but reported after the sweep is closed on arrival.
	forceClosed bool

	// onStart and onShutdown are the lifecycle hooks, in registration order.
	// They are written during registration and read by Serve and Shutdown with
	// no lock, on the same argument as built: registration ends before serving.
	onStart    []func() error
	onShutdown []func(context.Context) error

	// shutdownOnce makes the OnShutdown hooks run on the first Shutdown only.
	shutdownOnce sync.Once

	// shutdownSem, capacity 1, lets one Shutdown at a time drive fasthttp's
	// drain, the force-close and the hooks. It is a channel rather than a mutex
	// so that a Shutdown waiting for its turn can give up when its own ctx ends.
	// See the M7 design doc, D4, as corrected after the whole-branch review.
	shutdownSem chan struct{}

	// shutdownDone is closed once the first Shutdown has drained and run the
	// OnShutdown hooks. RunContext waits on it when a Shutdown called elsewhere
	// stopped Serve.
	shutdownDone chan struct{}

	// hooksStarted is closed when the first Shutdown begins running the
	// OnShutdown hooks. RunContext uses it to wait for hooks that are running
	// without waiting on a drain that may never end.
	hooksStarted chan struct{}

	// baseCtx is what Ctx.Context returns when no middleware has replaced it.
	// It is cancelled by closeConns, so a handler still running when Shutdown
	// gives up learns that its response is about to be discarded. It is never
	// cancelled by a clean shutdown, which has no handler left to tell, nor by a
	// failed start, which leaves Serve retryable. See ADR-0010.
	//
	// It carries no values. rice's per-request store is Set and Get; a second
	// store with a different lifetime would be one too many.
	baseCtx    context.Context
	cancelBase context.CancelFunc
}

// route is one registration, recorded for Build to compile.
type route struct {
	method string
	path   string // already joined with any group prefix
	h      Handler
	group  *Group // nil when registered directly on the App
	mws    []Middleware
}

// New creates an App.
func New(opts ...Option) *App {
	a := &App{
		errorHandler: DefaultErrorHandler,
		miss:         notFound,
		shutdownSem:  make(chan struct{}, 1),
		shutdownDone: make(chan struct{}),
		hooksStarted: make(chan struct{}),
	}
	a.baseCtx, a.cancelBase = context.WithCancel(context.Background())
	a.pool.New = func() any { return a.newCtx() }
	a.srv = &fasthttp.Server{
		Handler:      a.handle,
		Name:         "rice",
		ConnState:    a.connState,
		ErrorHandler: transportError,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// transportError answers a request fasthttp could not read, before any route or
// Ctx exists, so rice's ErrorHandler never sees it. It is fasthttp's default
// error handler with one branch added: a body over the size limit is 413, where
// fasthttp answers every error it does not recognise with 400 "Error when
// parsing request" and tells the client its request was malformed.
func transportError(ctx *fasthttp.RequestCtx, err error) {
	var small *fasthttp.ErrSmallBuffer
	var netErr *net.OpError
	switch {
	case errors.Is(err, fasthttp.ErrBodyTooLarge):
		ctx.Error("Request Entity Too Large", fasthttp.StatusRequestEntityTooLarge)
	case errors.As(err, &small):
		ctx.Error("Too big request header", fasthttp.StatusRequestHeaderFieldsTooLarge)
	case errors.As(err, &netErr) && netErr.Timeout():
		ctx.Error("Request timeout", fasthttp.StatusRequestTimeout)
	default:
		ctx.Error("Error when parsing request", fasthttp.StatusBadRequest)
	}
}

// notFound is the handler a route miss runs. Returning the package-level
// ErrNotFound rather than constructing an error keeps the miss path at zero
// allocations.
func notFound(*Ctx) error { return ErrNotFound }

// FasthttpHandler returns the request handler this App installs on its server.
//
// It calls Build, because a handler that dispatched uncompiled routes would skip
// every middleware silently. Use it to mount rice inside an existing fasthttp
// server, or to drive the dispatch path directly in benchmarks without opening a
// socket.
func (a *App) FasthttpHandler() fasthttp.RequestHandler {
	a.Build()
	return a.handle
}

// handle is the dispatch path: one request in, one response out.
func (a *App) handle(fctx *fasthttp.RequestCtx) {
	// The Ctx is acquired before the lookup because the lookup fills c.params in
	// place. That ordering is ADR-0005's design; see the Ctx.params comment.
	c := a.acquire(fctx)

	// fasthttp has no panic hook. Its only recover() on the request path guards
	// body-stream writes — a second one exists in fasthttpadaptor/adaptor.go,
	// which rice does not use; server.go calls the handler bare from a
	// worker-pool goroutine, so an unrecovered panic here takes the whole
	// process down, not just this connection. Recovering is therefore core
	// behaviour rather than opt-in middleware, which is a deliberate exception
	// to design principle 7 — see ADR-0008.
	//
	// release shares this closure rather than taking a defer of its own, so the
	// recovery's measured cost remains the only defer cost on the path. It runs
	// after the ErrorHandler, which may still use c, and it runs whether the
	// handler returned, errored or panicked — the property that makes the pool
	// safe.
	//
	// One hole is accepted. If respond panicked inside callErrorHandler's
	// last-resort recover, nothing would catch it and release would not run.
	// That is unreachable today (see callErrorHandler), and if it became
	// reachable the cost is one Ctx lost to the pool, not a corrupted one.
	defer func() {
		if r := recover(); r != nil {
			a.callErrorHandler(c, &PanicError{Value: r, Stack: debug.Stack()})
		}
		a.release(c)
	}()

	h, ok := a.lookup(fctx.Method(), fctx.Path(), &c.params)
	if !ok {
		// A miss runs the application's middleware like any route, so a
		// logger sees 404s and a CORS middleware can answer a preflight for a
		// path that has no OPTIONS route. See ADR-0012.
		h = a.miss
	}

	if err := h(c); err != nil && !c.handled {
		a.callErrorHandler(c, err)
	}
}

// callErrorHandler runs the App's ErrorHandler with a last-resort net beneath it.
//
// Without this, a panic inside a user's ErrorHandler reintroduces exactly the
// failure the recovery in handle removes — and reintroduces it in its worst
// form, because it fires only when something has already gone wrong, which
// makes it intermittent and hard to reproduce.
//
// It costs nothing on the hot path: a request that succeeds never gets here.
// Every funnel entry point calls this rather than a.errorHandler directly.
//
// The last-resort response goes through respond, the same one every other
// error in the funnel uses. There is only one way this framework writes an
// error body; a second one would just be a second thing to keep correct.
func (a *App) callErrorHandler(c *Ctx, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("rice: ErrorHandler panicked: %v", r)
			// A panic from respond itself, right here, would not be caught by
			// anything: this recover has already fired and cannot catch a
			// second panic raised from within its own deferred function, and
			// if this call stack was reached via handle's recover — the panic
			// path, not a plain returned error — that recover has already
			// fired too, for the same reason. It is unreachable today because
			// respond only touches c.fctx, which c.reset guarantees is
			// non-nil before dispatch ever begins, and does nothing else that
			// can fail. The moment respond is asked to do more than that,
			// this stops being hypothetical.
			respond(c, fasthttp.StatusInternalServerError, "Internal Server Error")
		}
	}()

	a.errorHandler(c, err)
}
