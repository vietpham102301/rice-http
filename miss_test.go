package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// A route miss used to go straight to the error funnel, so no middleware ever
// saw it. That made a request logger blind to 404s and made CORS preflight
// impossible. See ADR-0012.

func TestAppMiddlewareRunsOnAMiss(t *testing.T) {
	ran := false
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			ran = true
			return next(c)
		}
	})
	app.GET("/ok", func(c *Ctx) error { return nil })

	fctx := dispatchCtx(app, "GET", "/nope")

	if !ran {
		t.Error("application middleware did not run on a route miss")
	}
	if code := fctx.Response.StatusCode(); code != 404 {
		t.Errorf("status = %d, want 404", code)
	}
}

// TestGroupMiddlewareDoesNotRunOnAMiss pins that only the application's own
// middleware wraps the miss chain: no group matched, so no group's middleware
// has any claim on the request.
func TestGroupMiddlewareDoesNotRunOnAMiss(t *testing.T) {
	ran := false
	app := New()
	api := app.Group("/api", func(next Handler) Handler {
		return func(c *Ctx) error {
			ran = true
			return next(c)
		}
	})
	api.GET("/users", func(c *Ctx) error { return nil })

	dispatchCtx(app, "GET", "/api/nope")

	if ran {
		t.Error("group middleware ran on a miss; only application middleware may")
	}
}

func TestAWrongMethodTakesTheMissPath(t *testing.T) {
	ran := false
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			ran = true
			return next(c)
		}
	})
	app.GET("/users", func(c *Ctx) error { return nil })

	fctx := dispatchCtx(app, "DELETE", "/users")

	if !ran {
		t.Error("application middleware did not run for a wrong method on an existing path")
	}
	if code := fctx.Response.StatusCode(); code != 404 {
		t.Errorf("status = %d, want 404", code)
	}
}

// TestAnUnbuiltAppAnswersAMiss pins why the miss chain is initialised in New
// and not only in Build: the dispatch path is reachable before Build, and this
// package's own tests call handle on unbuilt Apps throughout. A miss chain that
// existed only after Build would be nil here.
func TestAnUnbuiltAppAnswersAMiss(t *testing.T) {
	app := New()
	app.GET("/ok", func(c *Ctx) error { return nil })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/nope")
	app.handle(fctx) // deliberately no Build

	if code := fctx.Response.StatusCode(); code != 404 {
		t.Errorf("status = %d, want 404", code)
	}
}

func TestAMissWithNoMiddlewareIsUnchanged(t *testing.T) {
	app := New()
	app.GET("/ok", func(c *Ctx) error { return nil })

	fctx := dispatchCtx(app, "GET", "/nope")

	if code := fctx.Response.StatusCode(); code != 404 {
		t.Errorf("status = %d, want 404", code)
	}
	if body := string(fctx.Response.Body()); body != "Not Found" {
		t.Errorf("body = %q, want %q", body, "Not Found")
	}
}

// TestParamIsEmptyOnAMiss rests on the router's documented contract: "params
// is filled only on a match. On a miss it is left as the caller passed it", and
// the caller passes a Ctx that reset has just cleared.
//
// ran guards the test itself: without it, a miss chain that stopped running
// application middleware would leave got nil and this test would still pass,
// unable to fail for the reason its name gives.
func TestParamIsEmptyOnAMiss(t *testing.T) {
	ran := false
	var got []byte
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			ran = true
			got = c.Param("id")
			return next(c)
		}
	})
	app.GET("/users/:id/posts", func(c *Ctx) error { return nil })

	dispatchCtx(app, "GET", "/users/42/nope") // matches :id, then misses

	if !ran {
		t.Error("application middleware did not run on a route miss")
	}
	if len(got) != 0 {
		t.Errorf("Param(\"id\") = %q on a miss, want empty", got)
	}
}
