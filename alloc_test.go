//go:build !ricedebug

package rice

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"

	"github.com/vietpham102301/rice-http/internal/router"
)

// This file is excluded from the ricedebug build, which allocates a fresh Ctx
// per request on purpose: a poisoned Ctx is never returned to the pool. The
// budget helper itself lives in budget_test.go, untagged, because
// method_test.go's TestAllocBudgetMethodIndex needs it too and is unaffected by
// ricedebug.

func TestAllocBudgetString(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.String", 0, func() {
		_ = c.String(200, "hello, world")
	})
}

func TestAllocBudgetBytes(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	body := []byte("hello, world")
	budget(t, "Ctx.Bytes", 0, func() {
		_ = c.Bytes(200, body)
	})
}

func TestAllocBudgetMethodAndPath(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")

	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.Method", 0, func() { _ = c.Method() })
	budget(t, "Ctx.Path", 0, func() { _ = c.Path() })
}

func TestAllocBudgetLookupHit(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/users")

	var p router.Params
	if _, ok := app.lookup(method, path, &p); !ok {
		t.Fatal("route not registered; the budget below would be measuring the miss path")
	}

	budget(t, "App.lookup hit", 0, func() {
		_, _ = app.lookup(method, path, &p)
	})
}

func TestAllocBudgetLookupMiss(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/absent")

	var p router.Params
	if _, ok := app.lookup(method, path, &p); ok {
		t.Fatal("route unexpectedly found; the budget below would be measuring a hit")
	}

	budget(t, "App.lookup miss", 0, func() {
		_, _ = app.lookup(method, path, &p)
	})
}

// TestAllocBudgetHandleDispatch pins the headline claim of M6: end-to-end
// dispatch on a warm server allocates nothing. Until M6 the budget was 1, the
// unpooled Ctx that M1 recorded as the baseline.
func TestAllocBudgetHandleDispatch(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users")

	var p router.Params
	if _, ok := app.lookup(fctx.Method(), fctx.Path(), &p); !ok {
		t.Fatal("route not registered; the budget below would be measuring the miss path")
	}

	app.Build()
	budget(t, "App.handle dispatch (hit)", 0, func() {
		app.handle(fctx)
	})
}

// TestAllocBudgetLookupAtScale guards a lookup against a large route set,
// where a per-request string conversion of the path would show up. If someone
// rewrites Tree.Lookup as key := string(path) the optimisation is lost and this
// fails, while every behavioural test keeps passing.
func TestAllocBudgetLookupAtScale(t *testing.T) {
	app := New()
	for i := 0; i < 1000; i++ {
		app.GET("/route/"+strconv.Itoa(i), func(c *Ctx) error { return nil })
	}

	method := []byte("GET")
	path := []byte("/route/500")

	var p router.Params
	if _, ok := app.lookup(method, path, &p); !ok {
		t.Fatal("route not registered")
	}

	budget(t, "App.lookup with 1000 routes", 0, func() {
		_, _ = app.lookup(method, path, &p)
	})
}

func TestAllocBudgetLookupOneParameter(t *testing.T) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/users/42")

	var p router.Params
	if _, ok := app.lookup(method, path, &p); !ok {
		t.Fatal("route not registered; the budget below would be measuring the miss path")
	}
	if got := string(p.Get("id")); got != "42" {
		t.Fatalf("captured id = %q, want 42; the budget below would not be measuring capture", got)
	}

	budget(t, "App.lookup with one parameter", 0, func() {
		p.Reset()
		_, _ = app.lookup(method, path, &p)
	})
}

func TestAllocBudgetLookupFiveParameters(t *testing.T) {
	app := New()
	app.GET("/a/:p1/b/:p2/c/:p3/d/:p4/e/:p5", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/a/1/b/2/c/3/d/4/e/5")

	var p router.Params
	if _, ok := app.lookup(method, path, &p); !ok {
		t.Fatal("route not registered")
	}
	if p.Len() != 5 {
		t.Fatalf("captured %d parameters, want 5", p.Len())
	}

	budget(t, "App.lookup with five parameters", 0, func() {
		p.Reset()
		_, _ = app.lookup(method, path, &p)
	})
}

func TestAllocBudgetLookupWildcard(t *testing.T) {
	app := New()
	app.GET("/files/*path", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/files/a/b/c.txt")

	var p router.Params
	if _, ok := app.lookup(method, path, &p); !ok {
		t.Fatal("route not registered")
	}

	budget(t, "App.lookup with a wildcard", 0, func() {
		p.Reset()
		_, _ = app.lookup(method, path, &p)
	})
}

// TestAllocBudgetLookupBacktrack measures the worst case the design admits: a
// static branch that matches, strands a byte, and forces an unwind to the
// parameter child. It must still allocate nothing.
func TestAllocBudgetLookupBacktrack(t *testing.T) {
	app := New()
	app.GET("/users/new", func(c *Ctx) error { return nil })
	app.GET("/users/:id", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/users/newx")

	var p router.Params
	if _, ok := app.lookup(method, path, &p); !ok {
		t.Fatal("the backtracking route did not match; this budget would measure a miss")
	}
	if got := string(p.Get("id")); got != "newx" {
		t.Fatalf("captured id = %q, want newx", got)
	}

	budget(t, "App.lookup with backtracking", 0, func() {
		p.Reset()
		_, _ = app.lookup(method, path, &p)
	})
}

// paramSink keeps Param's result reachable so the compiler cannot elide the
// call as dead code. Without it the measurement below would read 0 no matter
// what Param does — including if Param stopped borrowing and started
// allocating a copy on every call — because a discarded result is dead code
// the compiler is free to remove. See TestAllocBudgetCtxParamString, whose
// string sink is the same fix for the same failure mode.
var paramSink []byte

func TestAllocBudgetCtxParam(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}

	c := &Ctx{}
	c.reset(app, fctx)
	c.params.Set("id", []byte("42"))

	if got := string(c.Param("id")); got != "42" {
		t.Fatalf("Param = %q, want 42", got)
	}

	budget(t, "Ctx.Param", 0, func() {
		paramSink = c.Param("id")
	})
}

// paramStringSink keeps ParamString's result reachable so the compiler cannot
// elide the conversion as dead code. Without it the measurement below reads 0,
// and the test would pass while measuring nothing — including if ParamString
// stopped copying and started aliasing, which would break the borrow contract.
var paramStringSink string

// TestAllocBudgetCtxParamString asserts exactly one allocation rather than at
// most one. This is the accessor that deliberately copies out of fasthttp's
// buffer so a value can outlive the handler, and docs/03-core-concepts.md budgets
// it at 1. Zero would mean either that the measurement is not reaching the
// conversion or that the copy is gone; both are defects, and an upper bound
// cannot tell either from success.
func TestAllocBudgetCtxParamString(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}

	c := &Ctx{}
	c.reset(app, fctx)
	c.params.Set("id", []byte("42"))

	if got := c.ParamString("id"); got != "42" {
		t.Fatalf("ParamString = %q, want 42", got)
	}

	got := testing.AllocsPerRun(1000, func() {
		paramStringSink = c.ParamString("id")
	})
	if got != 1 {
		t.Errorf("Ctx.ParamString allocated %.1f objects per call, want exactly 1", got)
	}
}

// TestAllocBudgetHandleDispatchParameterised keeps the end-to-end promise honest
// for a parameterised route: still nothing, now that the Ctx is pooled.
//
// The handler reads the parameter and writes it back, because that is the claim
// the budget is quoted for — a parameterised route read with Param. A handler
// that ignored the parameter would measure the same dispatch as the static
// budget above and pass while capture allocated, so the body is asserted too.
func TestAllocBudgetHandleDispatchParameterised(t *testing.T) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return c.Bytes(200, c.Param("id")) })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")

	app.Build()
	app.handle(fctx)
	if fctx.Response.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200; this budget would be measuring the 404 path", fctx.Response.StatusCode())
	}
	if got := string(fctx.Response.Body()); got != "42" {
		t.Fatalf("body = %q, want %q; this budget would not be measuring a parameter read", got, "42")
	}

	budget(t, "App.handle on a parameterised route", 0, func() {
		app.handle(fctx)
	})
}

// TestAllocBudgetPoolAcquireRelease pins the pool's own round trip, which
// docs/05-performance-model.md lists as a budgeted operation. The dispatch
// budgets above cover it in passing; this one measures it with nothing else in
// the frame, so a regression in acquire or release is attributed to acquire or
// release.
func TestAllocBudgetPoolAcquireRelease(t *testing.T) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return nil })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")

	budget(t, "pool acquire + release", 0, func() {
		app.release(app.acquire(fctx))
	})
}

// TestNewCtxStaysWithinThreeAllocations guards the ceiling the zero budgets in
// this file depend on under -race.
//
// In race builds sync.Pool drops one Put in four, so each drop sends the next
// acquire to newCtx and a nominally zero-allocation dispatch really averages a
// fraction of newCtx's cost. At three objects that fraction is about 0.75 for an
// App with a parameterised route, which AllocsPerRun's integer division reports
// as 0; a fourth object pushes it to roughly 1.0, on the boundary, and the
// parameterised budget starts failing at random. See budget_test.go.
//
// That ceiling was a comment on newCtx for the whole milestone, and a comment is
// not a guard: nothing failed when a fourth allocation was added. This test is
// the guard. It uses a three-parameter route because newCtx allocates the
// parameter slice only when there is a parameter to hold.
func TestNewCtxStaysWithinThreeAllocations(t *testing.T) {
	app := New()
	app.GET("/a/:x/:y/:z", func(c *Ctx) error { return nil })

	var sink *Ctx
	if got := testing.AllocsPerRun(1000, func() { sink = app.newCtx() }); got > 3 {
		t.Errorf("newCtx allocated %.0f objects, budget is 3; see the -race note in budget_test.go", got)
	}
	_ = sink
}

// chainSink counts middleware invocations. It is a package-level variable so the
// compiler cannot discard the increments as dead code, which would let these
// budgets measure a chain that was optimised away.
var chainSink int

// countingMW returns a middleware that increments chainSink on the way through.
func countingMW() Middleware {
	return func(next Handler) Handler {
		return func(c *Ctx) error {
			chainSink++
			return next(c)
		}
	}
}

func TestAllocBudgetDispatchNoMiddleware(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error { return c.String(200, "ok") })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/x")

	app.handle(fctx)
	if fctx.Response.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200; this budget would be measuring the 404 path", fctx.Response.StatusCode())
	}

	budget(t, "App.handle with no middleware", 0, func() {
		app.handle(fctx)
	})
}

// TestAllocBudgetDispatchFiveMiddleware is the milestone's central claim as a
// test: five middleware cost no allocations at all.
func TestAllocBudgetDispatchFiveMiddleware(t *testing.T) {
	app := New()
	app.Use(countingMW(), countingMW(), countingMW())
	g := app.Group("/api", countingMW())
	g.GET("/x", func(c *Ctx) error { return c.String(200, "ok") }, countingMW())
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/api/x")

	chainSink = 0
	app.handle(fctx)

	if fctx.Response.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", fctx.Response.StatusCode())
	}
	if chainSink != 5 {
		t.Fatalf("%d middleware ran, want 5; this budget would be measuring a shorter chain than it claims", chainSink)
	}

	budget(t, "App.handle with five middleware", 0, func() {
		app.handle(fctx)
	})
}

// TestAllocBudgetChainCompile pins the compiler itself, separately from dispatch.
func TestAllocBudgetChainCompile(t *testing.T) {
	h := Handler(func(c *Ctx) error { return nil })
	mws := []Middleware{countingMW(), countingMW(), countingMW(), countingMW(), countingMW()}

	// Compiling allocates — it builds closures. What must not allocate is calling
	// the result, which is what a request does.
	compiled := chainCompileForTest(h, mws)

	// nil is deliberate: this chain's middleware only counts, and the terminal
	// handler above only returns, so nothing here dereferences the *Ctx. If a
	// future edit adds a Ctx access to either, this will panic rather than
	// silently start allocating — that failure mode is preferable to this test
	// quietly measuring something other than allocations.
	chainSink = 0
	if err := compiled(nil); err != nil {
		t.Fatalf("compiled chain returned %v, want nil", err)
	}
	if chainSink != 5 {
		t.Fatalf("%d middleware ran, want 5", chainSink)
	}

	budget(t, "compiled five-middleware chain call", 0, func() {
		_ = compiled(nil)
	})
}

// TestAllocBudgetDispatchWithRecover is the load-bearing row of M5's budget
// table: an installed recovery that never fires must cost nothing. The whole
// justification for making recovery core behaviour rather than opt-in rests on
// this being 0.
func TestAllocBudgetDispatchWithRecover(t *testing.T) {
	app := New()
	app.GET("/ok", func(c *Ctx) error { return c.String(200, "ok") })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/ok")

	budget(t, "dispatch with recovery installed", 0, func() {
		app.handle(fctx)
	})
}

// TestAllocBudget404 pins D2 of the M5 design with the pool in place: a miss
// costs what a hit costs, and both cost nothing.
func TestAllocBudget404(t *testing.T) {
	app := New()
	app.GET("/ok", func(c *Ctx) error { return nil })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/missing")

	budget(t, "404 through the funnel", 0, func() {
		app.handle(fctx)
	})
}

// TestAllocBudget404WithAppMiddleware pins that the miss chain costs nothing:
// it is compiled once at Build, and ErrNotFound is a package variable.
func TestAllocBudget404WithAppMiddleware(t *testing.T) {
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error { return next(c) }
	})
	app.GET("/ok", func(c *Ctx) error { return nil })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/missing")

	budget(t, "404 through a miss chain with application middleware", 0, func() {
		app.handle(fctx)
	})
}

// TestAllocBudgetHTTPErrorReturn records the cost of a handler constructing an
// error: the HTTPError, and nothing else now that the Ctx is pooled. It is 1 and
// it is meant to be 1 — a budget that documents a cost rather than forbidding one.
func TestAllocBudgetHTTPErrorReturn(t *testing.T) {
	app := New()
	app.GET("/bad", func(c *Ctx) error { return NewHTTPError(400, "bad request") })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/bad")

	budget(t, "handler returning a fresh HTTPError", 1, func() {
		app.handle(fctx)
	})
}

// storeSink keeps Get's result reachable so the compiler cannot elide the call.
var storeSink any

// TestAllocBudgetKeySetPointer pins Set with a pointer T, which boxes
// nothing. Measured at 0 on darwin arm64 (go1.25.6) and in a Linux arm64
// container (golang:1.25.14), with and without -race.
func TestAllocBudgetKeySetPointer(t *testing.T) {
	c := New().newCtx()
	k := NewKey[*struct{ n int }]("k")
	v := &struct{ n int }{}

	budget(t, "Key.Set with a pointer value", 0, func() {
		c.resetStore()
		k.Set(c, v)
	})
}

// TestAllocBudgetKeyGet pins Get with a pointer T. Measured at 0 on darwin
// arm64 (go1.25.6) and in a Linux arm64 container (golang:1.25.14), with and
// without -race.
func TestAllocBudgetKeyGet(t *testing.T) {
	c := New().newCtx()
	k := NewKey[*struct{ n int }]("k")
	k.Set(c, &struct{ n int }{})

	budget(t, "Key.Get with a pointer value", 0, func() {
		storeSink, _ = k.Get(c)
	})
}

// TestAllocBudgetKeySetString asserts exactly one allocation, and the
// allocation is not rice's. Converting a non-constant string to the store's
// any boxes it; Set itself allocates nothing, as
// TestAllocBudgetKeySetPointer shows. The budget is exact rather than an
// upper bound so that a change to Go's boxing rules shows up here instead of
// silently changing what the documentation says. Measured at 1 on darwin
// arm64 (go1.25.6) and in a Linux arm64 container (golang:1.25.14), with and
// without -race.
func TestAllocBudgetKeySetString(t *testing.T) {
	c := New().newCtx()
	k := NewKey[string]("k")
	s := strconv.Itoa(123456)

	got := testing.AllocsPerRun(1000, func() {
		c.resetStore()
		k.Set(c, s)
	})
	if got != 1 {
		t.Errorf("Key.Set with a non-constant string allocated %.1f objects per call, want exactly 1 (the caller's boxing)", got)
	}
}

// stringSink keeps Get's string result reachable.
var stringSink string

// TestAllocBudgetKeyGetString pins that Get with a non-pointer T copies the
// value out of the store's any without allocating: the assertion v.(T) reads
// the boxed string's header, it does not build one. Measured at 0 on darwin
// arm64 (go1.25.6) and in a Linux arm64 container (golang:1.25.14), with and
// without -race.
func TestAllocBudgetKeyGetString(t *testing.T) {
	c := New().newCtx()
	k := NewKey[string]("k")
	k.Set(c, strconv.Itoa(123456))

	budget(t, "Key.Get with a string value", 0, func() {
		stringSink, _ = k.Get(c)
	})
}

func TestAllocBudgetStatus(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.Status", 0, func() { _ = c.Status(201) })
}

func TestAllocBudgetSetHeader(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.SetHeader", 0, func() { c.SetHeader("X-Rice", "1") })
}

func TestAllocBudgetSetContentType(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.SetContentType", 0, func() { c.SetContentType("application/json") })
}

func TestAllocBudgetNoContent(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.NoContent", 0, func() { _ = c.NoContent(204) })
}

func TestAllocBudgetClientIP(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	fctx.SetRemoteAddr(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5000})
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.ClientIP", 0, func() { _ = c.ClientIP() })
}

func TestAllocBudgetRequestCtx(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.RequestCtx", 0, func() { _ = c.RequestCtx() })
}

func TestAllocBudgetQueryHeaderBody(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/search?q=rice&page=2")
	fctx.Request.Header.Set("X-Request-Id", "abc123")
	fctx.Request.SetBodyString(`{"name":"rice"}`)
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.Query", 0, func() { _ = c.Query("page") })
	budget(t, "Ctx.Header", 0, func() { _ = c.Header("x-request-id") })
	budget(t, "Ctx.Body", 0, func() { _ = c.Body() })
}

// jsonPayload is the shape the comparison's json scenario encodes.
type jsonPayload struct {
	ID   int      `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// TestAllocBudgetJSON pins JSON's cost exactly, in both directions, as
// TestAllocBudgetCtxParamString does: a drop would mean the encoder changed under
// us and the documented number is stale. Through a pointer the one allocation is
// the encoded bytes; passing the struct by value adds the caller's boxing into
// v any, which is the caller's cost and is recorded so it is not mistaken for
// rice's.
func TestAllocBudgetJSON(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	ptr := &jsonPayload{ID: 42, Name: "rice", Tags: []string{"a", "b"}}
	val := *ptr

	for _, tc := range []struct {
		name string
		want float64
		fn   func()
	}{
		{"Ctx.JSON with a pointer", 1, func() { _ = c.JSON(200, ptr) }},
		{"Ctx.JSON with a struct value (the caller's boxing)", 2, func() { _ = c.JSON(200, val) }},
	} {
		tc.fn() // warm
		if got := testing.AllocsPerRun(1000, tc.fn); got != tc.want {
			t.Errorf("%s allocated %.1f objects per call, want exactly %.0f", tc.name, got, tc.want)
		}
	}
}

// TestAllocBudgetHandleError pins HandleError at zero allocations of its own,
// measured with ErrNotFound through the default funnel, which is itself free.
// The flag is cleared inside the measured closure: HandleError is a no-op once
// a request is settled, so without clearing it every iteration after the
// first would measure the no-op instead of the work.
func TestAllocBudgetHandleError(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)

	budget(t, "Ctx.HandleError", 0, func() {
		c.handled = false
		c.HandleError(ErrNotFound)
	})
}

// TestAllocBudgetContext uses a real App, unlike the other budgets in this
// file: Context falls back to the App's base context, so a Ctx reset with a nil
// App would panic rather than measure anything.
func TestAllocBudgetContext(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)

	budget(t, "Ctx.Context", 0, func() { _ = c.Context() })

	ctx := context.Background()
	budget(t, "Ctx.SetContext", 0, func() { c.SetContext(ctx) })
}

// staticFctx returns a request context for a static budget. Init gives it a
// logger, which fasthttp.FS needs on its miss path.
func staticFctx(uri string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod("GET")
	req.SetRequestURI(uri)
	fctx := &fasthttp.RequestCtx{}
	fctx.Init(&req, nil, discardLogger{})
	return fctx
}

// TestAllocBudgetStaticFile pins a GET for a small file already in fasthttp's
// handle cache. Static files are outside the zero-allocation claim; this is a
// regression guard, not a target. Measured on darwin arm64, with and without
// -race. The response is reset between calls, as fasthttp's server does.
func TestAllocBudgetStaticFile(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()
	fctx := staticFctx("/assets/a.txt")

	const want float64 = 0
	budget(t, "static file from the handle cache", want, func() {
		fctx.Response.Reset()
		app.handle(fctx)
	})
}

// TestAllocBudgetStatic404 pins a missing file through a Static route, the path
// a scanner exercises: fasthttp's failed open and its log line, then the funnel.
func TestAllocBudgetStatic404(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()
	fctx := staticFctx("/assets/missing")

	const want float64 = 20
	budget(t, "static 404", want, func() {
		fctx.Response.Reset()
		app.handle(fctx)
	})
}

// TestAllocBudgetStatic404WithHeaders pins the same miss behind one middleware
// that sets a header, so serve saves the header before fasthttp resets it and
// puts it back after. Measured on darwin arm64: 17 without -race, 21 with it,
// where sync.Pool's dropped Puts make the saved header allocate now and then.
func TestAllocBudgetStatic404WithHeaders(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS(), setHeader("X-Request-Id", "r1"))
	app.Build()
	fctx := staticFctx("/assets/missing")

	const want float64 = 21
	budget(t, "static 404 with a middleware header", want, func() {
		fctx.Response.Reset()
		app.handle(fctx)
	})
}

// chromeAccept is Chrome's Accept header for a navigation, the header a browser
// hitting a negotiating endpoint actually sends.
const chromeAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"

// acceptSink keeps Accepts' result reachable so the call cannot be elided.
var acceptSink string

// acceptBudgetCtx returns a Ctx whose request carries accept, or no Accept
// header when accept is empty.
func acceptBudgetCtx(accept string) *Ctx {
	fctx := &fasthttp.RequestCtx{}
	if accept != "" {
		fctx.Request.Header.Set(fasthttp.HeaderAccept, accept)
	}
	c := &Ctx{}
	c.reset(nil, fctx)
	return c
}

// TestAllocBudgetAccepts pins Accepts on Chrome's header with two offers,
// including adding Vary: the Vary line is deleted each call so the add path is
// what is measured, as it is on every live request.
func TestAllocBudgetAccepts(t *testing.T) {
	c := acceptBudgetCtx(chromeAccept)
	budget(t, "Ctx.Accepts", 0, func() {
		c.fctx.Response.Header.Del(fasthttp.HeaderVary)
		acceptSink = c.Accepts(MIMEApplicationJSON, "text/html")
	})
}

// TestAllocBudgetAcceptsNoHeader pins the path a client without Accept takes.
func TestAllocBudgetAcceptsNoHeader(t *testing.T) {
	c := acceptBudgetCtx("")
	budget(t, "Ctx.Accepts without Accept", 0, func() {
		c.fctx.Response.Header.Del(fasthttp.HeaderVary)
		acceptSink = c.Accepts(MIMEApplicationJSON, "text/html")
	})
}

// TestAllocBudgetAcceptsTwice pins two calls in one request: the second finds
// Vary: Accept already there and adds nothing.
func TestAllocBudgetAcceptsTwice(t *testing.T) {
	c := acceptBudgetCtx(chromeAccept)
	budget(t, "Ctx.Accepts twice", 0, func() {
		c.fctx.Response.Header.Del(fasthttp.HeaderVary)
		acceptSink = c.Accepts(MIMEApplicationJSON, "text/html")
		acceptSink = c.Accepts("text/html")
	})
}

// TestAllocBudgetNotAcceptable pins a handler that finds nothing acceptable and
// returns ErrNotAcceptable through the funnel.
func TestAllocBudgetNotAcceptable(t *testing.T) {
	app := New()
	app.GET("/u", func(c *Ctx) error {
		if c.Accepts(MIMEApplicationJSON) == "" {
			return ErrNotAcceptable
		}
		return nil
	})
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/u")
	fctx.Request.Header.Set(fasthttp.HeaderAccept, "text/html")

	budget(t, "406 through the funnel", 0, func() {
		fctx.Response.Header.Del(fasthttp.HeaderVary)
		app.handle(fctx)
	})
}
