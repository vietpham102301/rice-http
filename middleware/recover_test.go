package middleware_test

import (
	"errors"
	"testing"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

func dispatch(t *testing.T, app *rice.App, path string) *fasthttp.RequestCtx {
	t.Helper()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI(path)
	app.FasthttpHandler()(fctx) // FasthttpHandler calls Build
	return fctx
}

func TestRecoverTurnsAPanicInto500(t *testing.T) {
	app := rice.New()
	app.Use(middleware.Recover())
	app.GET("/boom", func(c *rice.Ctx) error { panic("exploded") })

	fctx := dispatch(t, app, "/boom")

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500", got)
	}
}

// TestRecoverLetsOuterMiddlewareSeeTheError is the only reason this package
// exists. Core recovery sits outside the whole chain, so by the time it runs
// every middleware frame has unwound and a middleware written as
// "err := next(c); record(err); return err" never reaches its record call.
// With Recover installed beneath it, the panic arrives as an ordinary return.
func TestRecoverLetsOuterMiddlewareSeeTheError(t *testing.T) {
	var observed error
	recorded := false

	app := rice.New()
	app.Use(func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			err := next(c)
			recorded = true
			observed = err
			return err
		}
	})
	app.Use(middleware.Recover())
	app.GET("/boom", func(c *rice.Ctx) error { panic("exploded") })

	dispatch(t, app, "/boom")

	if !recorded {
		t.Fatal("the outer middleware never resumed; the panic unwound past it")
	}
	var pe *rice.PanicError
	if !errors.As(observed, &pe) {
		t.Fatalf("the outer middleware observed %v, want a *rice.PanicError", observed)
	}
	if pe.Value != "exploded" {
		t.Errorf("Value = %v, want %q", pe.Value, "exploded")
	}
}

// TestWithoutRecoverTheOuterMiddlewareIsSkipped is the control. It is what
// makes the test above evidence rather than decoration: the same chain without
// Recover must fail to record, or the property is not Recover's doing.
func TestWithoutRecoverTheOuterMiddlewareIsSkipped(t *testing.T) {
	recorded := false

	app := rice.New()
	app.Use(func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			err := next(c)
			recorded = true
			return err
		}
	})
	app.GET("/boom", func(c *rice.Ctx) error { panic("exploded") })

	dispatch(t, app, "/boom")

	if recorded {
		t.Error("the outer middleware resumed without Recover installed; core recovery is not supposed to make that possible")
	}
}

func TestRecoverPassesASuccessfulRequestThrough(t *testing.T) {
	app := rice.New()
	app.Use(middleware.Recover())
	app.GET("/ok", func(c *rice.Ctx) error { return c.String(200, "fine") })

	fctx := dispatch(t, app, "/ok")

	if got := string(fctx.Response.Body()); got != "fine" {
		t.Errorf("body = %q, want %q", got, "fine")
	}
}

func TestRecoverPassesAReturnedErrorThroughUnchanged(t *testing.T) {
	sentinel := errors.New("ordinary failure")
	var observed error

	app := rice.New(rice.WithErrorHandler(func(c *rice.Ctx, err error) { observed = err }))
	app.Use(middleware.Recover())
	app.GET("/err", func(c *rice.Ctx) error { return sentinel })

	dispatch(t, app, "/err")

	if !errors.Is(observed, sentinel) {
		t.Errorf("the funnel received %v, want the handler's own error", observed)
	}
}

func TestRecoverCapturesAStack(t *testing.T) {
	var pe *rice.PanicError
	app := rice.New(rice.WithErrorHandler(func(c *rice.Ctx, err error) {
		_ = errors.As(err, &pe)
	}))
	app.Use(middleware.Recover())
	app.GET("/boom", func(c *rice.Ctx) error { panic("exploded") })

	dispatch(t, app, "/boom")

	if pe == nil {
		t.Fatal("the funnel did not receive a *rice.PanicError")
	}
	if len(pe.Stack) == 0 {
		t.Error("Stack is empty")
	}
}
