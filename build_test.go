package rice

import (
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// traceMW returns a middleware that records its name on the way in and again on
// the way out, so one request produces the full order in both directions.
func traceMW(log *[]string, name string) Middleware {
	return func(next Handler) Handler {
		return func(c *Ctx) error {
			*log = append(*log, name+"-in")
			err := next(c)
			*log = append(*log, name+"-out")
			return err
		}
	}
}

func traceHandler(log *[]string) Handler {
	return func(c *Ctx) error {
		*log = append(*log, "handler")
		return c.String(200, "ok")
	}
}

// dispatch builds the app if needed and sends one GET to path, returning the
// status.
func dispatch(app *App, path string) int {
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI(path)
	app.handle(fctx)
	return fctx.Response.StatusCode()
}

func TestApplicationMiddlewareRunsAroundTheHandler(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "A"))
	app.GET("/x", traceHandler(&log))

	if got := dispatch(app, "/x"); got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}
	if got := strings.Join(log, " "); got != "A-in handler A-out" {
		t.Errorf("trace = %q, want %q", got, "A-in handler A-out")
	}
}

// TestUseAfterRegistrationStillApplies is the roadmap's named exit criterion, and
// the whole reason ADR-0003 puts compilation in a build phase.
func TestUseAfterRegistrationStillApplies(t *testing.T) {
	var log []string

	app := New()
	app.GET("/x", traceHandler(&log)) // route first
	app.Use(traceMW(&log, "late"))    // middleware second

	dispatch(app, "/x")

	if got := strings.Join(log, " "); got != "late-in handler late-out" {
		t.Errorf("trace = %q; middleware added after the route must still wrap it", got)
	}
}

func TestRouteMiddlewareRunsInsideApplicationMiddleware(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "app"))
	app.GET("/x", traceHandler(&log), traceMW(&log, "route"))

	dispatch(app, "/x")

	want := "app-in route-in handler route-out app-out"
	if got := strings.Join(log, " "); got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}

func TestUnwindOrderIsTheReverseOfEntryOrder(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "1"), traceMW(&log, "2"), traceMW(&log, "3"))
	app.GET("/x", traceHandler(&log))

	dispatch(app, "/x")

	want := "1-in 2-in 3-in handler 3-out 2-out 1-out"
	if got := strings.Join(log, " "); got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}

func TestBuildIsIdempotent(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "A"))
	app.GET("/x", traceHandler(&log))

	app.Build()
	app.Build()
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/x")
	app.handle(fctx)

	if got := strings.Join(log, " "); got != "A-in handler A-out" {
		t.Errorf("trace = %q after three Builds; middleware must not be applied more than once", got)
	}
}

func TestRegisteringAfterBuildPanics(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error { return nil })
	app.Build()

	v := mustPanic(t, "GET after Build", func() {
		app.GET("/y", func(c *Ctx) error { return nil })
	})

	msg, _ := v.(string)
	if !strings.Contains(msg, "after Build") {
		t.Errorf("panic %q should say the registration came after Build", v)
	}
	if !strings.Contains(msg, "/y") {
		t.Errorf("panic %q should name the route that was too late", v)
	}
}

func TestUseAfterBuildPanics(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error { return nil })
	app.Build()

	v := mustPanic(t, "Use after Build", func() {
		app.Use(func(next Handler) Handler { return next })
	})

	if msg, _ := v.(string); !strings.Contains(msg, "after Build") {
		t.Errorf("panic %q should say Use came after Build", v)
	}
}

// TestFasthttpHandlerBuilds matters because every benchmark reaches dispatch
// through it and never opens a socket. If it did not build, they would all
// measure routes with no middleware and report a number that means nothing.
func TestFasthttpHandlerBuilds(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "A"))
	app.GET("/x", traceHandler(&log))

	h := app.FasthttpHandler()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/x")
	h(fctx)

	if got := strings.Join(log, " "); got != "A-in handler A-out" {
		t.Errorf("trace = %q; FasthttpHandler must build before returning", got)
	}
}

// TestUseDoesNotAliasTheCallersSlice guards a Go hazard that a naive
// implementation walks straight into: keeping the variadic slice rather than
// copying into the App's own means a caller mutating theirs later can change
// ours.
//
// The mutation has to be an in-place overwrite of an element already in range,
// not a further append: an append past the alias's own length writes into
// spare capacity the alias's own reads never look beyond, so it would pass
// whether or not the slice was actually copied — proving nothing.
func TestUseDoesNotAliasTheCallersSlice(t *testing.T) {
	var log []string

	callerSlice := make([]Middleware, 1)
	callerSlice[0] = traceMW(&log, "A")

	app := New()
	app.Use(callerSlice...)
	app.GET("/x", traceHandler(&log))

	// The caller keeps using their slice after registering, overwriting the
	// element it already passed in.
	callerSlice[0] = traceMW(&log, "INTRUDER")

	dispatch(app, "/x")

	if got := strings.Join(log, " "); got != "A-in handler A-out" {
		t.Errorf("trace = %q; the caller's later mutation must not reach the App", got)
	}
}

// TestRouteMiddlewareDoesNotAliasTheCallersSlice is
// TestUseDoesNotAliasTheCallersSlice's counterpart for register: a route's own
// mw ...Middleware is copied the same way Use's is, and that copy is otherwise
// unguarded. See that test's comment for why the caller mutates in place
// rather than appending.
func TestRouteMiddlewareDoesNotAliasTheCallersSlice(t *testing.T) {
	var log []string

	callerSlice := make([]Middleware, 1)
	callerSlice[0] = traceMW(&log, "A")

	app := New()
	app.GET("/x", traceHandler(&log), callerSlice...)

	// The caller keeps using their slice after registering, overwriting the
	// element it already passed in.
	callerSlice[0] = traceMW(&log, "INTRUDER")

	dispatch(app, "/x")

	if got := strings.Join(log, " "); got != "A-in handler A-out" {
		t.Errorf("trace = %q; the caller's later mutation must not reach the route's chain", got)
	}
}
