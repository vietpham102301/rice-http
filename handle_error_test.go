package rice

import (
	"errors"
	"testing"

	"github.com/valyala/fasthttp"
)

// statusRecorder is what a request logger does: it settles the error with
// HandleError, then reads the status the client will receive.
func statusRecorder(seen *int) Middleware {
	return func(next Handler) Handler {
		return func(c *Ctx) error {
			if err := next(c); err != nil {
				c.HandleError(err)
			}
			*seen = c.fctx.Response.StatusCode()
			return nil
		}
	}
}

// TestMiddlewareSeesTheFinalStatus is the regression test for the whole
// branch. Before HandleError and the miss chain existed, a middleware reading
// the status after next saw 200, 200, 200 and nothing, while the client
// received 200, 418, 500 and 404: the funnel writes the status only after the
// whole chain has returned. See ADR-0013.
func TestMiddlewareSeesTheFinalStatus(t *testing.T) {
	var seen int
	app := New()
	app.Use(statusRecorder(&seen))
	app.GET("/ok", func(c *Ctx) error { return c.String(200, "ok") })
	app.GET("/teapot", func(c *Ctx) error { return NewHTTPError(418, "teapot") })
	app.GET("/boom", func(c *Ctx) error { return errors.New("db down") })

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/ok", 200},
		{"/teapot", 418},
		{"/boom", 500},
		{"/nope", 404},
	} {
		seen = -1
		fctx := dispatchCtx(app, "GET", tc.path)
		if seen != tc.want {
			t.Errorf("%s: middleware saw %d, want %d", tc.path, seen, tc.want)
		}
		if got := fctx.Response.StatusCode(); got != tc.want {
			t.Errorf("%s: client received %d, want %d", tc.path, got, tc.want)
		}
	}
}

// countingApp returns an App whose ErrorHandler counts its calls.
func countingApp(calls *int) *App {
	return New(WithErrorHandler(func(c *Ctx, err error) {
		*calls++
		DefaultErrorHandler(c, err)
	}))
}

// TestHandleErrorRunsTheErrorHandlerExactlyOnce covers the case the handled
// flag exists for: a middleware that settles the error and then still returns
// it. Without the flag, handle would run the funnel a second time and write
// the response twice.
func TestHandleErrorRunsTheErrorHandlerExactlyOnce(t *testing.T) {
	var calls int
	app := countingApp(&calls)
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			err := next(c)
			c.HandleError(err)
			return err // returned anyway, on purpose
		}
	})
	app.GET("/teapot", func(c *Ctx) error { return NewHTTPError(418, "teapot") })

	fctx := dispatchCtx(app, "GET", "/teapot")

	if calls != 1 {
		t.Errorf("ErrorHandler ran %d times, want exactly 1", calls)
	}
	if code := fctx.Response.StatusCode(); code != 418 {
		t.Errorf("status = %d, want 418", code)
	}
}

func TestHandleErrorWritesTheResponseNow(t *testing.T) {
	var during int
	app := New()
	app.GET("/teapot", func(c *Ctx) error {
		c.HandleError(NewHTTPError(418, "teapot"))
		during = c.fctx.Response.StatusCode()
		return nil
	})

	dispatchCtx(app, "GET", "/teapot")

	if during != 418 {
		t.Errorf("status read straight after HandleError = %d, want 418", during)
	}
}

func TestHandleErrorNilIsANoOp(t *testing.T) {
	var calls int
	app := countingApp(&calls)
	app.GET("/ok", func(c *Ctx) error {
		c.HandleError(nil)
		return c.String(200, "ok")
	})

	fctx := dispatchCtx(app, "GET", "/ok")

	if calls != 0 {
		t.Errorf("ErrorHandler ran %d times for HandleError(nil), want 0", calls)
	}
	if code := fctx.Response.StatusCode(); code != 200 {
		t.Errorf("status = %d, want 200", code)
	}
}

func TestHandleErrorTwiceKeepsTheFirst(t *testing.T) {
	var calls int
	app := countingApp(&calls)
	app.GET("/x", func(c *Ctx) error {
		c.HandleError(NewHTTPError(418, "first"))
		c.HandleError(NewHTTPError(409, "second"))
		return nil
	})

	fctx := dispatchCtx(app, "GET", "/x")

	if calls != 1 {
		t.Errorf("ErrorHandler ran %d times, want 1", calls)
	}
	if code := fctx.Response.StatusCode(); code != 418 {
		t.Errorf("status = %d, want 418: the first HandleError settles the request", code)
	}
}

// TestAPanicAfterHandleErrorIsStill500 pins that the panic path ignores the
// handled flag. M5 established that a recovered panic anywhere in the chain is
// always a 500, and the response has not been sent yet, so overwriting it is
// correct.
func TestAPanicAfterHandleErrorIsStill500(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error {
		c.HandleError(NewHTTPError(418, "teapot"))
		panic("after settling")
	})

	fctx := dispatchCtx(app, "GET", "/x")

	if code := fctx.Response.StatusCode(); code != 500 {
		t.Errorf("status = %d, want 500", code)
	}
}

// TestResetClearsHandled keeps a pooled Ctx from carrying one request's flag
// into the next, which would silently suppress that request's error response.
func TestResetClearsHandled(t *testing.T) {
	app := New()
	c := &Ctx{}
	c.reset(app, &fasthttp.RequestCtx{})
	c.HandleError(NewHTTPError(418, "teapot"))

	c.reset(app, &fasthttp.RequestCtx{})

	if c.handled {
		t.Error("handled survived reset; the next request's error would never be answered")
	}
}

// reentrantApp returns an App whose ErrorHandler counts its calls and then
// calls HandleError itself, as a handler delegating back to the funnel might.
// ADR-0013 promises a request is settled once whatever the entry point, so
// that inner call must be a no-op and the count must end at exactly one.
func reentrantApp(calls *int) *App {
	return New(WithErrorHandler(func(c *Ctx, err error) {
		*calls++
		c.HandleError(err)
		DefaultErrorHandler(c, err)
	}))
}

func TestAReentrantErrorHandlerRunsOnceForAReturnedError(t *testing.T) {
	var calls int
	app := reentrantApp(&calls)
	app.GET("/teapot", func(c *Ctx) error { return NewHTTPError(418, "teapot") })

	fctx := dispatchCtx(app, "GET", "/teapot")

	if calls != 1 {
		t.Errorf("ErrorHandler ran %d times, want 1", calls)
	}
	if code := fctx.Response.StatusCode(); code != 418 {
		t.Errorf("status = %d, want 418", code)
	}
}

func TestAReentrantErrorHandlerRunsOnceForAMiss(t *testing.T) {
	var calls int
	app := reentrantApp(&calls)
	app.GET("/ok", func(c *Ctx) error { return nil })

	fctx := dispatchCtx(app, "GET", "/nope")

	if calls != 1 {
		t.Errorf("ErrorHandler ran %d times, want 1", calls)
	}
	if code := fctx.Response.StatusCode(); code != 404 {
		t.Errorf("status = %d, want 404", code)
	}
}

func TestAReentrantErrorHandlerRunsOnceForAPanic(t *testing.T) {
	var calls int
	app := reentrantApp(&calls)
	app.GET("/boom", func(c *Ctx) error { panic("handler exploded") })

	fctx := dispatchCtx(app, "GET", "/boom")

	if calls != 1 {
		t.Errorf("ErrorHandler ran %d times, want 1", calls)
	}
	if code := fctx.Response.StatusCode(); code != 500 {
		t.Errorf("status = %d, want 500", code)
	}
}
