package rice

import (
	"errors"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// TestMiddlewareCanShortCircuit records ADR-0003's reason for the decorator
// shape: stopping the chain is an ordinary return, not a rule about calling Next.
func TestMiddlewareCanShortCircuit(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "outer"))
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			log = append(log, "gate")
			return c.String(403, "denied")
		}
	})
	app.GET("/x", traceHandler(&log))

	status := dispatch(app, "/x")

	if status != 403 {
		t.Errorf("status = %d, want 403", status)
	}
	if strings.Contains(strings.Join(log, " "), "handler") {
		t.Errorf("trace = %q; the handler must not run when a middleware short-circuits", log)
	}
	if got := strings.Join(log, " "); got != "outer-in gate outer-out" {
		t.Errorf("trace = %q, want %q", got, "outer-in gate outer-out")
	}
}

func TestMiddlewareErrorReachesTheErrorFunnel(t *testing.T) {
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error { return errors.New("middleware failed") }
	})
	app.GET("/x", func(c *Ctx) error { return c.String(200, "unreachable") })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/x")
	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want 500", got)
	}
	if got := string(fctx.Response.Body()); strings.Contains(got, "middleware failed") {
		t.Errorf("body %q leaked the error cause", got)
	}
}

// TestMiddlewareSeesAnErrorFromTheHandler is the other direction: a middleware
// wrapping a failing handler observes the error on the way out, which is what the
// decorator shape buys over an index walk.
func TestMiddlewareSeesAnErrorFromTheHandler(t *testing.T) {
	var seen error

	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			err := next(c)
			seen = err
			return err
		}
	})
	app.GET("/x", func(c *Ctx) error { return errors.New("handler failed") })

	dispatch(app, "/x")

	if seen == nil {
		t.Fatal("the middleware did not observe the handler's error")
	}
	if seen.Error() != "handler failed" {
		t.Errorf("middleware saw %q, want %q", seen, "handler failed")
	}
}

// TestMiddlewareCanReadRouteParameters checks the two features compose: a chain
// wraps a parameterised route and the parameters are already captured.
func TestMiddlewareCanReadRouteParameters(t *testing.T) {
	var seen string

	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			seen = c.ParamString("id")
			return next(c)
		}
	})
	app.GET("/users/:id", func(c *Ctx) error { return c.String(200, "ok") })

	if got := dispatch(app, "/users/42"); got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}
	if seen != "42" {
		t.Errorf("middleware read id = %q, want %q", seen, "42")
	}
}
