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
