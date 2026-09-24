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

// keep the imports used until later tasks add their tests
var _ = errors.New
