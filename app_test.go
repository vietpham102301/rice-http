package rice

import (
	"errors"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestHandleInvokesTheRegisteredHandler(t *testing.T) {
	app := New()

	called := false
	app.GET("/anything", func(c *Ctx) error {
		called = true
		return c.String(200, "ok")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/anything")

	app.Build()
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

	app.Build()
	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestHandleConvertsAReturnedErrorInto500(t *testing.T) {
	app := New()
	app.GET("/boom", func(c *Ctx) error {
		return errors.New("the database is on fire")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/boom")

	app.Build()
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
	app.GET("/partial", func(c *Ctx) error {
		_ = c.String(200, "partial output")
		return errors.New("failed after writing")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/partial")

	app.Build()
	app.handle(fctx)

	if got := string(fctx.Response.Body()); strings.Contains(got, "partial output") {
		t.Error("a partially written body survived an error return")
	}
	if got := fctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want 500", got)
	}
}

func TestFasthttpHandlerDispatchesLikeHandle(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error { return c.String(200, "via fasthttp handler") })

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

func TestHandleReturns404ForAnUnregisteredPath(t *testing.T) {
	app := New()
	app.GET("/registered", func(c *Ctx) error { return c.String(200, "ok") })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/not-registered")

	app.Build()
	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestHandleReturns404ForTheRightPathUnderTheWrongVerb(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return c.String(200, "ok") })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("POST")
	fctx.Request.SetRequestURI("/users")

	app.Build()
	app.handle(fctx)

	// M2 deliberately answers 404 rather than 405: computing Allow would cost a
	// scan of every other verb's tree on the miss path. See the M2 design doc.
	if got := fctx.Response.StatusCode(); got != fasthttp.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestThe404BodyDoesNotLeakTheSentinelMessage(t *testing.T) {
	app := New()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/missing")

	app.Build()
	app.handle(fctx)

	body := string(fctx.Response.Body())
	if body != "Not Found" {
		t.Errorf("body = %q, want %q", body, "Not Found")
	}
	if strings.Contains(body, "404:") {
		t.Errorf("the sentinel's Error() rendering leaked into the response: %q", body)
	}
}

func TestAHandlerErrorStillProduces500AfterTheFunnelLearnedAbout404(t *testing.T) {
	app := New()
	app.GET("/boom2", func(c *Ctx) error { return errors.New("unrelated failure") })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/boom2")

	app.Build()
	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want 500", got)
	}
	if got := string(fctx.Response.Body()); got != "Internal Server Error" {
		t.Errorf("body = %q, want %q", got, "Internal Server Error")
	}
}

// TestAHandlerMayReturnErrNotFoundToGetA404 records that the funnel switches on
// the error rather than on where it came from, so a handler can opt into a 404.
func TestAHandlerMayReturnErrNotFoundToGetA404(t *testing.T) {
	app := New()
	app.GET("/maybe", func(c *Ctx) error { return ErrNotFound })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/maybe")

	app.Build()
	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}
