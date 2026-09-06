package rice

import (
	"errors"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestHandleInvokesTheRegisteredHandler(t *testing.T) {
	app := New()

	called := false
	app.SetHandler(func(c *Ctx) error {
		called = true
		return c.String(200, "ok")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/anything")

	app.handle(fctx)

	if !called {
		t.Fatal("handler was not invoked")
	}
	if got := fctx.Response.StatusCode(); got != 200 {
		t.Errorf("status = %d, want 200", got)
	}
	if got := string(fctx.Response.Body()); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
}

func TestHandleWithNoRegisteredHandlerReturns404(t *testing.T) {
	app := New()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/anything")

	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestHandleConvertsAReturnedErrorInto500(t *testing.T) {
	app := New()
	app.SetHandler(func(c *Ctx) error {
		return errors.New("the database is on fire")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/boom")

	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want 500", got)
	}
	if got := string(fctx.Response.Body()); got == "the database is on fire" {
		t.Error("the error cause leaked into the response body")
	}
}

func TestHandleDiscardsAPartialBodyWhenTheHandlerErrors(t *testing.T) {
	app := New()
	app.SetHandler(func(c *Ctx) error {
		_ = c.String(200, "partial output")
		return errors.New("failed after writing")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/partial")

	app.handle(fctx)

	if got := string(fctx.Response.Body()); got == "partial output" {
		t.Error("a partially written body survived an error return")
	}
	if got := fctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want 500", got)
	}
}

func TestFasthttpHandlerDispatchesLikeHandle(t *testing.T) {
	app := New()
	app.SetHandler(func(c *Ctx) error { return c.String(200, "via fasthttp handler") })

	h := app.FasthttpHandler()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/x")
	h(fctx)

	if got := string(fctx.Response.Body()); got != "via fasthttp handler" {
		t.Errorf("body = %q, want %q", got, "via fasthttp handler")
	}
}

func TestNewAppliesOptions(t *testing.T) {
	marker := false
	opt := func(a *App) { marker = true }

	New(opt)

	if !marker {
		t.Error("New did not apply the option it was given")
	}
}
