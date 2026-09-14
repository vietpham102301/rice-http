package rice

import (
	"errors"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// dispatchCtx drives one request and hands back the whole RequestCtx.
//
// Package rice already has a dispatch helper, in build_test.go, but it returns
// only the status code and these tests assert on bodies too. The name is
// different because two helpers with one name in one package is a compile
// error, not a style question.
func dispatchCtx(app *App, method, path string) *fasthttp.RequestCtx {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(method)
	fctx.Request.SetRequestURI(path)
	app.Build()
	app.handle(fctx)
	return fctx
}

func TestAPanickingHandlerProduces500(t *testing.T) {
	app := New()
	app.GET("/boom", func(c *Ctx) error { panic("handler exploded") })

	fctx := dispatchCtx(app, "GET", "/boom")

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500", got)
	}
	if got := string(fctx.Response.Body()); got != "Internal Server Error" {
		t.Errorf("body = %q, want the generic body", got)
	}
}

func TestAPanickingHandlerDoesNotLeakThePanicValueIntoTheBody(t *testing.T) {
	const token = "SECRET-PANIC-VALUE"
	app := New()
	app.GET("/boom", func(c *Ctx) error { panic(token) })

	fctx := dispatchCtx(app, "GET", "/boom")

	if body := string(fctx.Response.Body()); strings.Contains(body, token) {
		t.Errorf("the panic value leaked into the response: %q", body)
	}
}

func TestAPanickingHandlerIsLoggedWithItsStack(t *testing.T) {
	logged := captureLog(t)
	app := New()
	app.GET("/boom", func(c *Ctx) error { panic("handler exploded") })

	dispatchCtx(app, "GET", "/boom")

	out := logged()
	if !strings.Contains(out, "handler exploded") {
		t.Errorf("the panic value was not logged, got %q", out)
	}
	if !strings.Contains(out, "panic_test.go") {
		t.Errorf("the log carries no stack naming this test file, got %q", out)
	}
}

func TestAPanicWithANonStringValueIsHandled(t *testing.T) {
	app := New()
	app.GET("/boom", func(c *Ctx) error { panic(42) })

	fctx := dispatchCtx(app, "GET", "/boom")

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500", got)
	}
}

func TestAPanicInsideMiddlewareIsRecovered(t *testing.T) {
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error { panic("middleware exploded") }
	})
	app.GET("/x", func(c *Ctx) error { return c.String(200, "never reached") })

	fctx := dispatchCtx(app, "GET", "/x")

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500", got)
	}
}

// TestAPanickedHTTPErrorIsStill500 pins D5's ordering. PanicError.Unwrap
// returns the panicked value, so without checking *PanicError first this would
// answer 400 — a panic quietly becoming a client error.
func TestAPanickedHTTPErrorIsStill500(t *testing.T) {
	app := New()
	app.GET("/boom", func(c *Ctx) error { panic(NewHTTPError(400, "not your fault")) })

	fctx := dispatchCtx(app, "GET", "/boom")

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500 — a panic is always a bug, never a client error", got)
	}
}

// TestACustomErrorHandlerReceivesAPanicError proves the panic reaches the same
// funnel as everything else, which is what "one funnel" means.
func TestACustomErrorHandlerReceivesAPanicError(t *testing.T) {
	var got *PanicError
	app := New(WithErrorHandler(func(c *Ctx, err error) {
		_ = errors.As(err, &got)
		respond(c, 500, "custom")
	}))
	app.GET("/boom", func(c *Ctx) error { panic("inspect me") })

	dispatchCtx(app, "GET", "/boom")

	if got == nil {
		t.Fatal("the custom handler did not receive a *PanicError")
	}
	if got.Value != "inspect me" {
		t.Errorf("Value = %v, want %q", got.Value, "inspect me")
	}
	if len(got.Stack) == 0 {
		t.Error("Stack is empty")
	}
}

func TestASuccessfulRequestIsUnaffectedByTheRecovery(t *testing.T) {
	app := New()
	app.GET("/ok", func(c *Ctx) error { return c.String(200, "fine") })

	fctx := dispatchCtx(app, "GET", "/ok")

	if got := fctx.Response.StatusCode(); got != 200 {
		t.Errorf("status = %d, want 200", got)
	}
	if got := string(fctx.Response.Body()); got != "fine" {
		t.Errorf("body = %q, want %q", got, "fine")
	}
}
