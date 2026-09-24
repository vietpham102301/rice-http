package middleware_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
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
		{"https default port", middleware.CORSConfig{Origins: []string{"https://app.example.com:443"}}, "https://app.example.com:443"},
		{"http default port", middleware.CORSConfig{Origins: []string{"http://app.example.com:80"}}, "http://app.example.com:80"},
		{"negative max age", middleware.CORSConfig{Origins: []string{allowedOrigin}, MaxAge: -time.Second}, "MaxAge"},
		{"sub-second positive max age", middleware.CORSConfig{Origins: []string{allowedOrigin}, MaxAge: 500 * time.Millisecond}, "MaxAge"},
		{"empty method", middleware.CORSConfig{Origins: []string{allowedOrigin}, AllowMethods: []string{""}}, "AllowMethods"},
		{"comma in header", middleware.CORSConfig{Origins: []string{allowedOrigin}, AllowHeaders: []string{"A,B"}}, "AllowHeaders"},
		{"space in exposed header", middleware.CORSConfig{Origins: []string{allowedOrigin}, ExposeHeaders: []string{"X Y"}}, "ExposeHeaders"},
		{"newline in header", middleware.CORSConfig{Origins: []string{allowedOrigin}, AllowHeaders: []string{"Authorization\nContent-Type"}}, "AllowHeaders"},
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

// preflight is what a browser sends before a cross-origin POST with a JSON
// body and a bearer token. The -Headers value is deliberately not what the
// configuration allows: the middleware must not read it.
func preflight(origin string) map[string]string {
	return map[string]string{
		"Origin":                         origin,
		"Access-Control-Request-Method":  "DELETE",
		"Access-Control-Request-Headers": "authorization, content-type, x-not-allowed",
	}
}

func TestCORSAnswersAPreflight(t *testing.T) {
	for _, credentials := range []bool{false, true} {
		t.Run(map[bool]string{false: "without credentials", true: "with credentials"}[credentials], func(t *testing.T) {
			cfg := baseCORS()
			cfg.AllowCredentials = credentials
			var ran bool
			fctx := serveCORS(t, corsApp(cfg, &ran), "OPTIONS", "/r", preflight(allowedOrigin))

			if ran {
				t.Error("the handler ran: a preflight is answered by CORS and never reaches the chain")
			}
			if got := fctx.Response.StatusCode(); got != 204 {
				t.Errorf("status = %d, want 204", got)
			}
			if got := fctx.Response.Body(); len(got) != 0 {
				t.Errorf("body = %q, want empty", got)
			}
			// Header.ContentType() cannot pin this: fasthttp substitutes its
			// default (text/plain; charset=utf-8) whenever the field is unset,
			// masking whether a Content-Type was actually sent. Assert against
			// the bytes on the wire instead.
			if s := fctx.Response.Header.String(); strings.Contains(s, "Content-Type:") {
				t.Errorf("response header %q contains Content-Type, want none on a preflight", s)
			}
			credWant := ""
			if credentials {
				credWant = "true"
			}
			for name, want := range map[string]string{
				"Access-Control-Allow-Origin":      allowedOrigin,
				"Access-Control-Allow-Methods":     "GET, HEAD, POST, PUT, PATCH, DELETE",
				"Access-Control-Allow-Headers":     "Authorization, Content-Type",
				"Access-Control-Max-Age":           "600",
				"Access-Control-Allow-Credentials": credWant,
				"Access-Control-Expose-Headers":    "", // a preflight has no body for a script to read from
				"Vary":                             "Origin",
			} {
				if got := hdr(fctx, name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// TestCORSStatesThePolicyRatherThanEchoingTheRequest is the design: the
// browser compares what it asked for with what the server allows, so the
// server never reads Access-Control-Request-Headers. A middleware that echoed
// the request would send x-not-allowed here.
func TestCORSStatesThePolicyRatherThanEchoingTheRequest(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(baseCORS(), &ran), "OPTIONS", "/r", preflight(allowedOrigin))

	if got := hdr(fctx, "Access-Control-Allow-Headers"); strings.Contains(strings.ToLower(got), "x-not-allowed") {
		t.Errorf("Access-Control-Allow-Headers = %q echoes the request; want the configured list only", got)
	}
}

func TestCORSAnswersAPreflightFromAnUnknownOriginWithNoHeaders(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(baseCORS(), &ran), "OPTIONS", "/r", preflight("https://evil.example"))

	if ran {
		t.Error("the handler ran: a preflight never reaches the chain, allowed or not")
	}
	if got := fctx.Response.StatusCode(); got != 204 {
		t.Errorf("status = %d, want 204: the absence of headers is the answer", got)
	}
	for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Max-Age"} {
		if got := hdr(fctx, name); got != "" {
			t.Errorf("%s = %q for an origin not in the list, want absent", name, got)
		}
	}
	if got := hdr(fctx, "Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
}

func TestCORSAnswersAPreflightForAPathWithNoRoute(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(baseCORS(), &ran), "OPTIONS", "/nowhere", preflight(allowedOrigin))

	if got := fctx.Response.StatusCode(); got != 204 {
		t.Errorf("status = %d, want 204: the middleware cannot tell a miss from a route, and the real request will get its 404 with the headers on", got)
	}
	if got := hdr(fctx, "Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
}

// TestCORSPreflightNeedsAllThreeSignals pins what a preflight is: OPTIONS,
// Origin, and Access-Control-Request-Method. Missing any one, the request is
// a real request and reaches the chain.
func TestCORSPreflightNeedsAllThreeSignals(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		headers map[string]string
	}{
		{"OPTIONS without a request method", "OPTIONS", map[string]string{"Origin": allowedOrigin}},
		{"OPTIONS with an empty request method", "OPTIONS", map[string]string{"Origin": allowedOrigin, "Access-Control-Request-Method": ""}},
		{"request method on a POST", "POST", preflight(allowedOrigin)},
		{"OPTIONS with a request method and no Origin", "OPTIONS", map[string]string{"Access-Control-Request-Method": "DELETE"}},
		{"OPTIONS with an empty Origin and a request method", "OPTIONS", map[string]string{"Origin": "", "Access-Control-Request-Method": "DELETE"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ran bool
			app := corsApp(baseCORS(), &ran)
			app.OPTIONS("/r", func(c *rice.Ctx) error {
				ran = true
				return c.String(200, "an OPTIONS route of the application's own")
			})

			fctx := serveCORS(t, app, tc.method, "/r", tc.headers)

			if !ran {
				t.Error("the handler did not run: this is not a preflight")
			}
			if got := fctx.Response.StatusCode(); got != 200 {
				t.Errorf("status = %d, want 200 from the handler", got)
			}
			if tc.headers["Origin"] != "" {
				if got := hdr(fctx, "Access-Control-Allow-Origin"); got != allowedOrigin {
					t.Errorf("Access-Control-Allow-Origin = %q, want %q: a real request from an allowed origin is decorated", got, allowedOrigin)
				}
				if got := hdr(fctx, "Access-Control-Allow-Methods"); got != "" {
					t.Errorf("Access-Control-Allow-Methods = %q on a real request, want absent", got)
				}
			}
		})
	}
}

// TestCORSPreflightOmitsWhatIsNotConfigured pins the empty-field rule: no
// pair is built, so no header is sent. MaxAge below one second renders as 0.
func TestCORSPreflightOmitsWhatIsNotConfigured(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(middleware.CORSConfig{Origins: []string{allowedOrigin}, AllowMethods: []string{"GET", "POST"}}, &ran), "OPTIONS", "/r", preflight(allowedOrigin))

	for name, want := range map[string]string{
		"Access-Control-Allow-Methods":     "GET, POST",
		"Access-Control-Allow-Headers":     "",
		"Access-Control-Max-Age":           "",
		"Access-Control-Allow-Credentials": "",
	} {
		if got := hdr(fctx, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	cfg := middleware.CORSConfig{Origins: []string{allowedOrigin}, MaxAge: 1500 * time.Millisecond}
	fctx = serveCORS(t, corsApp(cfg, &ran), "OPTIONS", "/r", preflight(allowedOrigin))
	if got := hdr(fctx, "Access-Control-Max-Age"); got != "1" {
		t.Errorf("Access-Control-Max-Age = %q for 1.5s, want whole seconds: 1", got)
	}
}

// TestCORSPreflightIsLoggedAs204 is why CORS goes inside Logger.
func TestCORSPreflightIsLoggedAs204(t *testing.T) {
	var buf bytes.Buffer
	app := rice.New()
	app.Use(middleware.Logger(slog.New(slog.NewJSONHandler(&buf, nil))), middleware.CORS(baseCORS()))
	app.GET("/r", func(c *rice.Ctx) error { return c.String(200, "ok") })

	serveCORS(t, app, "OPTIONS", "/r", preflight(allowedOrigin))

	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("log line is not one JSON object: %v: %s", err, buf.Bytes())
	}
	if got := line["status"]; got != float64(204) {
		t.Errorf("logged status = %v, want 204", got)
	}
	if got := line["method"]; got != "OPTIONS" {
		t.Errorf("logged method = %v, want OPTIONS", got)
	}
}
