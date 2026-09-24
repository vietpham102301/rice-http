package middleware_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

const allowedOrigin = "https://app.example.com"

// baseCORS is the configuration a JSON API behind a reverse proxy needs: one
// origin, the two request headers the browser's safelist does not cover, the
// request id exposed so a client can quote it, and a cached preflight.
func baseCORS() middleware.CORSConfig {
	return middleware.CORSConfig{
		Origins:       []string{allowedOrigin},
		AllowHeaders:  []string{"Authorization", "Content-Type"},
		ExposeHeaders: []string{"X-Request-Id"},
		MaxAge:        10 * time.Minute,
	}
}

// corsApp returns an App with CORS(cfg) installed with app.Use — the only
// placement that sees a preflight, since a preflight is a route miss — and a
// handler on GET and POST /r that records whether it ran. Extra middleware
// goes inside CORS.
func corsApp(cfg middleware.CORSConfig, ran *bool, inside ...rice.Middleware) *rice.App {
	app := rice.New()
	app.Use(append([]rice.Middleware{middleware.CORS(cfg)}, inside...)...)
	h := func(c *rice.Ctx) error {
		*ran = true
		return c.String(200, "ok")
	}
	app.GET("/r", h)
	app.POST("/r", h)
	return app
}

// serveCORS dispatches one request with the given headers and returns the
// response.
func serveCORS(t *testing.T, app *rice.App, method, path string, headers map[string]string) *fasthttp.RequestCtx {
	t.Helper()
	fctx := newRequest(method, path, headers)
	app.FasthttpHandler()(fctx)
	return fctx
}

// hdr returns a response header's value, or "" when it is absent. Vary may
// carry several values; read it with PeekAll.
func hdr(fctx *fasthttp.RequestCtx, name string) string {
	return string(fctx.Response.Header.Peek(name))
}

func TestCORSPanicsOnAConfigurationThatCannotWork(t *testing.T) {
	cases := []struct {
		name string
		cfg  middleware.CORSConfig
		want string // a substring of the panic message
	}{
		{"no origins", middleware.CORSConfig{}, "Origins is empty"},
		{"wildcard", middleware.CORSConfig{Origins: []string{"*"}}, `"*"`},
		{"trailing slash", middleware.CORSConfig{Origins: []string{"https://app.example.com/"}}, "https://app.example.com/"},
		{"path", middleware.CORSConfig{Origins: []string{"https://app.example.com/login"}}, "/login"},
		{"query", middleware.CORSConfig{Origins: []string{"https://app.example.com?x=1"}}, "?x=1"},
		{"upper case", middleware.CORSConfig{Origins: []string{"https://App.example.com"}}, "App"},
		{"no scheme", middleware.CORSConfig{Origins: []string{"app.example.com"}}, "app.example.com"},
		{"empty host", middleware.CORSConfig{Origins: []string{"https://"}}, `"https://"`},
		{"null", middleware.CORSConfig{Origins: []string{"null"}}, `"null"`},
		{"negative max age", middleware.CORSConfig{Origins: []string{allowedOrigin}, MaxAge: -time.Second}, "MaxAge"},
		{"empty method", middleware.CORSConfig{Origins: []string{allowedOrigin}, AllowMethods: []string{""}}, "AllowMethods"},
		{"comma in header", middleware.CORSConfig{Origins: []string{allowedOrigin}, AllowHeaders: []string{"A,B"}}, "AllowHeaders"},
		{"space in exposed header", middleware.CORSConfig{Origins: []string{allowedOrigin}, ExposeHeaders: []string{"X Y"}}, "ExposeHeaders"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("CORS did not panic")
				}
				msg, _ := r.(string)
				if !strings.HasPrefix(msg, "rice: middleware.CORS: ") {
					t.Errorf("panic = %q, want the rice: middleware.CORS: prefix", msg)
				}
				if !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %q, want it to name %q", msg, tc.want)
				}
			}()
			middleware.CORS(tc.cfg)
		})
	}
}

func TestCORSAcceptsTheOriginsABrowserSends(t *testing.T) {
	for _, o := range []string{"https://app.example.com", "http://localhost:3000", "https://[::1]:8443", "https://app.example.com:8443"} {
		t.Run(o, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("CORS panicked on %q: %v", o, r)
				}
			}()
			middleware.CORS(middleware.CORSConfig{Origins: []string{o}})
		})
	}
}

func TestCORSPassesARequestWithoutOriginThrough(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(baseCORS(), &ran), "GET", "/r", nil)

	if !ran {
		t.Error("the handler did not run")
	}
	if got := fctx.Response.StatusCode(); got != 200 {
		t.Errorf("status = %d, want 200", got)
	}
	if got := hdr(fctx, "Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want absent on a request with no Origin", got)
	}
	if got := hdr(fctx, "Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin: a cache must learn the response depends on Origin from the response that had none", got)
	}
}

func TestCORSDecoratesAResponseForAnAllowedOrigin(t *testing.T) {
	for _, credentials := range []bool{false, true} {
		t.Run(map[bool]string{false: "without credentials", true: "with credentials"}[credentials], func(t *testing.T) {
			cfg := baseCORS()
			cfg.AllowCredentials = credentials
			var ran bool
			fctx := serveCORS(t, corsApp(cfg, &ran), "GET", "/r", map[string]string{"Origin": allowedOrigin})

			if !ran {
				t.Error("the handler did not run")
			}
			if got := hdr(fctx, "Access-Control-Allow-Origin"); got != allowedOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
			}
			if got := hdr(fctx, "Access-Control-Expose-Headers"); got != "X-Request-Id" {
				t.Errorf("Access-Control-Expose-Headers = %q, want X-Request-Id", got)
			}
			want := ""
			if credentials {
				want = "true"
			}
			if got := hdr(fctx, "Access-Control-Allow-Credentials"); got != want {
				t.Errorf("Access-Control-Allow-Credentials = %q, want %q", got, want)
			}
			for _, name := range []string{"Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Max-Age"} {
				if got := hdr(fctx, name); got != "" {
					t.Errorf("%s = %q on a real response, want it only on a preflight", name, got)
				}
			}
			if got := hdr(fctx, "Vary"); got != "Origin" {
				t.Errorf("Vary = %q, want Origin", got)
			}
		})
	}
}

// TestCORSMatchesTheOriginExactly pins the comparison: byte-for-byte against
// the configured string. A browser never sends the first two forms; they prove
// there is no case folding and no normalisation. The third is what an
// attacker registers to defeat a prefix match.
func TestCORSMatchesTheOriginExactly(t *testing.T) {
	for _, origin := range []string{
		"https://APP.example.com",
		"https://app.example.com/",
		"https://app.example.com.evil.com",
		"https://evil.example",
	} {
		t.Run(origin, func(t *testing.T) {
			var ran bool
			fctx := serveCORS(t, corsApp(baseCORS(), &ran), "GET", "/r", map[string]string{"Origin": origin})

			if !ran {
				t.Error("the handler did not run: an unknown origin is still served; the browser does the blocking")
			}
			if got := fctx.Response.StatusCode(); got != 200 {
				t.Errorf("status = %d, want 200", got)
			}
			for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Expose-Headers", "Access-Control-Allow-Credentials"} {
				if got := hdr(fctx, name); got != "" {
					t.Errorf("%s = %q for an origin not in the list, want absent", name, got)
				}
			}
			if got := hdr(fctx, "Vary"); got != "Origin" {
				t.Errorf("Vary = %q, want Origin even when the origin is not allowed", got)
			}
		})
	}
}

// TestCORSKeepsItsOwnCopyOfTheOrigins pins that CORS copies cfg.Origins at
// construction. A caller that reuses or mutates its slice afterwards must not
// change the allowed set under a running server.
func TestCORSKeepsItsOwnCopyOfTheOrigins(t *testing.T) {
	cfg := baseCORS()
	var ran bool
	app := corsApp(cfg, &ran)
	cfg.Origins[0] = "https://evil.example"

	fctx := serveCORS(t, app, "GET", "/r", map[string]string{"Origin": allowedOrigin})

	if got := hdr(fctx, "Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q: the middleware must not share the caller's slice", got, allowedOrigin)
	}
}

func TestCORSTreatsAnEmptyOriginAsAbsent(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(baseCORS(), &ran), "GET", "/r", map[string]string{"Origin": ""})

	if !ran {
		t.Error("the handler did not run")
	}
	if got := hdr(fctx, "Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want absent for an empty Origin", got)
	}
}

// TestCORSHeadersSurviveTheFunnel is the reason the headers are written
// before next: the funnel sets status, content type and body and nothing
// else, so a 401 reaches the browser as a 401 and not as a CORS failure.
// The panic cases pin it on both the Recover path and core's own recovery.
func TestCORSHeadersSurviveTheFunnel(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		h      rice.Handler
		inside []rice.Middleware
		want   int
	}{
		{"plain error", "/e", func(c *rice.Ctx) error { return errors.New("db down") }, nil, 500},
		{"HTTPError", "/e", func(c *rice.Ctx) error { return rice.NewHTTPError(401, "who are you") }, nil, 401},
		{"panic under Recover", "/e", panickingHandler, []rice.Middleware{middleware.Recover()}, 500},
		{"panic with core recovery only", "/e", panickingHandler, nil, 500},
		{"no route", "/nowhere", nil, nil, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ran bool
			app := corsApp(baseCORS(), &ran, tc.inside...)
			if tc.h != nil {
				app.GET("/e", tc.h)
			}

			fctx := serveCORS(t, app, "GET", tc.path, map[string]string{"Origin": allowedOrigin})

			if got := fctx.Response.StatusCode(); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
			if got := hdr(fctx, "Access-Control-Allow-Origin"); got != allowedOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q on a %d, want %q: the browser would report a CORS failure instead of the status", got, tc.want, allowedOrigin)
			}
			if got := hdr(fctx, "Vary"); got != "Origin" {
				t.Errorf("Vary = %q, want Origin", got)
			}
		})
	}
}

// TestCORSKeepsAHandlersOwnVary is why Vary is written with Add and not Set.
func TestCORSKeepsAHandlersOwnVary(t *testing.T) {
	var ran bool
	app := corsApp(baseCORS(), &ran)
	app.GET("/v", func(c *rice.Ctx) error {
		c.RequestCtx().Response.Header.Add("Vary", "Accept")
		return c.String(200, "ok")
	})

	fctx := serveCORS(t, app, "GET", "/v", map[string]string{"Origin": allowedOrigin})

	var got []string
	for _, v := range fctx.Response.Header.PeekAll("Vary") {
		got = append(got, string(v))
	}
	if len(got) != 2 || got[0] != "Origin" || got[1] != "Accept" {
		t.Errorf("Vary = %q, want [Origin Accept]", got)
	}
}
