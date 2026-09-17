package rice

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestRegisterTracksTheLargestParameterCount(t *testing.T) {
	app := New()
	app.GET("/a", func(c *Ctx) error { return nil })
	app.GET("/users/:id", func(c *Ctx) error { return nil })
	app.POST("/x/:a/:b/*rest", func(c *Ctx) error { return nil })
	app.Handle("PURGE", "/cache/:key", func(c *Ctx) error { return nil })

	if app.maxParams != 3 {
		t.Errorf("maxParams = %d, want 3", app.maxParams)
	}
}

// TestReleaseDropsEveryReference is the reason release exists as more than a
// Put. A pooled Ctx that kept its fctx, its App or its parameter values would
// keep a finished request's memory reachable for as long as it sat in the pool.
func TestReleaseDropsEveryReference(t *testing.T) {
	var retained *Ctx
	app := New()
	app.GET("/users/:id", func(c *Ctx) error {
		retained = c
		return c.String(200, "ok")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")
	app.handle(fctx)

	if retained == nil {
		t.Fatal("handler did not run")
	}
	if retained.fctx != nil {
		t.Error("released Ctx still holds its *fasthttp.RequestCtx")
	}
	if retained.app != nil {
		t.Error("released Ctx still holds its *App")
	}
	if n := retained.params.Len(); n != 0 {
		t.Errorf("released Ctx still holds %d parameters", n)
	}
}

func TestReleaseRunsWhenTheHandlerPanics(t *testing.T) {
	var retained *Ctx
	app := New(WithErrorHandler(func(c *Ctx, err error) { respond(c, 500, "x") }))
	app.GET("/boom", func(c *Ctx) error {
		retained = c
		panic("boom")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/boom")
	app.handle(fctx)

	if retained == nil || retained.fctx != nil {
		t.Error("a panicking handler's Ctx was not released")
	}
}

// TestErrorHandlerReceivesALiveCtx pins the ordering in handle's defer: release
// runs after the ErrorHandler, never before it.
func TestErrorHandlerReceivesALiveCtx(t *testing.T) {
	var path string
	app := New(WithErrorHandler(func(c *Ctx, err error) {
		path = string(c.Path())
		respond(c, 500, "x")
	}))
	app.GET("/fail", func(c *Ctx) error { return NewHTTPError(500, "x") })
	app.GET("/boom", func(c *Ctx) error { panic("boom") })

	for _, uri := range []string{"/fail", "/boom", "/missing"} {
		path = ""
		fctx := &fasthttp.RequestCtx{}
		fctx.Request.Header.SetMethod("GET")
		fctx.Request.SetRequestURI(uri)
		app.handle(fctx)

		if path != uri {
			t.Errorf("%s: ErrorHandler saw Path() = %q, want %q", uri, path, uri)
		}
	}
}

func TestARequestSeesNoParametersFromThePreviousOne(t *testing.T) {
	var seen []string
	app := New()
	app.GET("/a/:x/:y", func(c *Ctx) error { return nil })
	app.GET("/b/:z", func(c *Ctx) error {
		seen = []string{c.ParamString("x"), c.ParamString("y"), c.ParamString("z")}
		return nil
	})

	for _, uri := range []string{"/a/1/2", "/b/3"} {
		fctx := &fasthttp.RequestCtx{}
		fctx.Request.Header.SetMethod("GET")
		fctx.Request.SetRequestURI(uri)
		app.handle(fctx)
	}

	if seen[0] != "" || seen[1] != "" || seen[2] != "3" {
		t.Errorf("second request saw x=%q y=%q z=%q; want only z=3", seen[0], seen[1], seen[2])
	}
}

func TestNewCtxIsSizedForTheLargestRoute(t *testing.T) {
	app := New()
	app.GET("/a/:p1/:p2/:p3", func(c *Ctx) error { return nil })
	app.GET("/b/:q", func(c *Ctx) error { return nil })

	c := app.newCtx()

	if got := c.params.Cap(); got != 3 {
		t.Errorf("newCtx params capacity = %d, want 3 (the largest route's parameter count)", got)
	}
}

// TestACtxBuiltBeforeALargerRouteStillCaptures is D3's reason for append: a
// Ctx sized for an earlier, smaller route set must grow rather than fail to match.
func TestACtxBuiltBeforeALargerRouteStillCaptures(t *testing.T) {
	app := New()
	app.GET("/a/:x", func(c *Ctx) error { return nil })
	c := app.newCtx() // sized for one parameter

	app.GET("/b/:p1/:p2/:p3", func(c *Ctx) error { return nil })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/b/1/2/3")
	c.reset(app, fctx)

	if _, ok := app.lookup(fctx.Method(), fctx.Path(), &c.params); !ok {
		t.Fatal("lookup missed; an undersized Ctx must still match")
	}
	if got := string(c.Param("p3")); got != "3" {
		t.Errorf("p3 = %q, want %q", got, "3")
	}
}

func TestARouteWithMoreThanEightParametersDispatches(t *testing.T) {
	pattern, uri := "", ""
	for i := 0; i < 10; i++ {
		pattern += "/:p" + strconv.Itoa(i)
		uri += "/" + strconv.Itoa(i)
	}

	app := New()
	app.GET(pattern, func(c *Ctx) error { return c.String(200, c.ParamString("p9")) })

	fctx := dispatchCtx(app, "GET", uri)

	if got := string(fctx.Response.Body()); got != "9" {
		t.Errorf("body = %q, want %q", got, "9")
	}
}
