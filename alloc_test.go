package rice

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"

	"github.com/vietpham102301/rice-http/internal/router"
)

// budget asserts that fn allocates no more than want objects per call.
//
// The response buffers are warmed before measuring, because a live server is
// warm. Measuring a cold buffer would measure one-time setup, not steady state.
func budget(t *testing.T, name string, want float64, fn func()) {
	t.Helper()
	fn() // warm
	if got := testing.AllocsPerRun(1000, fn); got > want {
		t.Errorf("%s allocated %.1f objects per call, budget is %.0f", name, got, want)
	}
}

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

// TestAllocBudgetHandleDispatch pins the "End to end: single handler, unpooled
// Ctx (M1 baseline)" row in docs/05-performance-model.md. That row previously
// existed only as a benchmark number, and a benchmark asserts nothing — it can
// regress silently forever. M2 allocates a Ctx per request on purpose (see
// app.go), so the budget here is 1, not 0.
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

	budget(t, "App.handle dispatch (hit)", 1, func() {
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
// for a parameterised route: still exactly one allocation, the Ctx.
func TestAllocBudgetHandleDispatchParameterised(t *testing.T) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return c.String(200, "ok") })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")

	app.handle(fctx)
	if fctx.Response.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200; this budget would be measuring the 404 path", fctx.Response.StatusCode())
	}

	budget(t, "App.handle on a parameterised route", 1, func() {
		app.handle(fctx)
	})
}
