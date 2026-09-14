package rice

import (
	"log"
	"net"
	"sync"

	"github.com/valyala/fasthttp"

	"github.com/vietpham102301/rice-http/internal/router"
)

// Option configures an App at construction time.
//
// Configuration happens here rather than through setters so that a serving App
// cannot be reconfigured underneath a request. M7 adds server timeouts.
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

	srv *fasthttp.Server

	mu sync.Mutex
	ln net.Listener
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
	a := &App{errorHandler: DefaultErrorHandler}
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
		a.callErrorHandler(c, ErrNotFound)
		return
	}

	if err := h(c); err != nil {
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
			respond(c, fasthttp.StatusInternalServerError, "Internal Server Error")
		}
	}()

	a.errorHandler(c, err)
}
