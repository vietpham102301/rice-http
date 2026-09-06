package rice

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"
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

	if _, ok := app.lookup(method, path); !ok {
		t.Fatal("route not registered; the budget below would be measuring the miss path")
	}

	budget(t, "App.lookup hit", 0, func() {
		_, _ = app.lookup(method, path)
	})
}

func TestAllocBudgetLookupMiss(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/absent")

	if _, ok := app.lookup(method, path); ok {
		t.Fatal("route unexpectedly found; the budget below would be measuring a hit")
	}

	budget(t, "App.lookup miss", 0, func() {
		_, _ = app.lookup(method, path)
	})
}

// TestAllocBudgetLookupAtScale guards the map probe specifically. If someone
// rewrites Tree.Lookup as key := string(path) the optimisation is lost and this
// fails, while every behavioural test keeps passing.
func TestAllocBudgetLookupAtScale(t *testing.T) {
	app := New()
	for i := 0; i < 1000; i++ {
		app.GET("/route/"+strconv.Itoa(i), func(c *Ctx) error { return nil })
	}

	method := []byte("GET")
	path := []byte("/route/500")

	if _, ok := app.lookup(method, path); !ok {
		t.Fatal("route not registered")
	}

	budget(t, "App.lookup with 1000 routes", 0, func() {
		_, _ = app.lookup(method, path)
	})
}
