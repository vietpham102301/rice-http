package rice

import (
	"testing"
	"testing/fstest"

	"github.com/valyala/fasthttp"
)

// routeOf dispatches method and path on app and returns what c.Route()
// reported inside the handler that ran, or inside the miss chain.
func routeOf(t *testing.T, app *App, method, path string) string {
	t.Helper()
	got := "<not called>"
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			err := next(c)
			got = c.Route()
			return err
		}
	})
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(method)
	fctx.Request.SetRequestURI(path)
	app.Build()
	app.handle(fctx)
	return got
}

func TestRouteReportsTheMatchedPattern(t *testing.T) {
	noop := func(c *Ctx) error { return nil }
	cases := []struct {
		name, method, path, want string
		register                 func(a *App)
	}{
		{"static", "GET", "/health", "/health", func(a *App) { a.GET("/health", noop) }},
		{"parameter", "GET", "/users/42", "/users/:id", func(a *App) { a.GET("/users/:id", noop) }},
		{"wildcard", "GET", "/files/a/b.txt", "/files/*path", func(a *App) { a.GET("/files/*path", noop) }},
		{"nested group", "POST", "/api/v1/orders/7", "/api/v1/orders/:id", func(a *App) {
			a.Group("/api").Group("/v1").POST("/orders/:id", noop)
		}},
		{"uncommon verb", "PROPFIND", "/dav/x", "/dav/:name", func(a *App) { a.Handle("PROPFIND", "/dav/:name", noop) }},
		{"static files", "GET", "/assets/a.txt", "/assets/*filepath", func(a *App) {
			a.Static("/assets", fstest.MapFS{"a.txt": {Data: []byte("x")}})
		}},
		{"miss", "GET", "/nowhere", "", func(a *App) { a.GET("/health", noop) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := New()
			tc.register(app)
			if got := routeOf(t, app, tc.method, tc.path); got != tc.want {
				t.Errorf("Route() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRouteIsClearedByReset(t *testing.T) {
	c := &Ctx{route: "/users/:id"}
	c.reset(nil, &fasthttp.RequestCtx{})
	if got := c.Route(); got != "" {
		t.Errorf("Route() after reset = %q, want empty", got)
	}
}

// TestRouteIsClearedOnThePooledCtxBetweenRequests dispatches a hit and then a
// miss through the same App and the same handle path a server uses, and
// checks that the second request does not still report the first request's
// pattern. Unlike TestRouteIsClearedByReset, which calls reset directly on a
// Ctx built by hand, this goes through acquire and release exactly as the
// server does, so a regression in that wiring — not just in reset itself —
// would fail it.
//
// Whether the two requests actually shared one *Ctx is logged, not asserted:
// sync.Pool gives up its local objects across a GC, so reuse is likely in a
// release build (poolReuse true) but not guaranteed run to run, and ricedebug
// disables it by design (poolReuse false) so a released Ctx never goes back
// to the pool at all. Either way Route() being cleared on the miss is what
// this test checks, and it holds regardless of whether reuse happened.
func TestRouteIsClearedOnThePooledCtxBetweenRequests(t *testing.T) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return nil })

	var ptrs [2]*Ctx
	var routes [2]string
	i := 0
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			err := next(c)
			ptrs[i], routes[i] = c, c.Route()
			i++
			return err
		}
	})

	app.Build()

	hit := &fasthttp.RequestCtx{}
	hit.Request.Header.SetMethod("GET")
	hit.Request.SetRequestURI("/users/42")
	app.handle(hit)

	miss := &fasthttp.RequestCtx{}
	miss.Request.Header.SetMethod("GET")
	miss.Request.SetRequestURI("/nowhere")
	app.handle(miss)

	if routes[0] != "/users/:id" {
		t.Fatalf("first request Route() = %q, want /users/:id", routes[0])
	}
	t.Logf("second request reused the first's Ctx: %v (%p vs %p)", ptrs[1] == ptrs[0], ptrs[1], ptrs[0])
	if routes[1] != "" {
		t.Errorf("Route() on the miss, from the Ctx the earlier hit used = %q, want empty", routes[1])
	}
}
