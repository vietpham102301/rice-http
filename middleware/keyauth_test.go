package middleware_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// keyApp protects GET /me with KeyAuth reading header ("" for Bearer); the
// callback accepts only "k1" and records what it saw.
func keyApp(t *testing.T, header string, seen *string, calls *int) *rice.App {
	t.Helper()
	app := rice.New()
	app.Use(middleware.KeyAuth(middleware.KeyAuthConfig[*user]{
		Key:    userKey,
		Header: header,
		Validate: func(ctx context.Context, key string) (*user, bool, error) {
			*calls++
			*seen = key
			if key == "k1" {
				return &user{name: "svc"}, true, nil
			}
			return nil, false, nil
		},
	}))
	app.GET("/me", func(c *rice.Ctx) error {
		u, _ := userKey.Get(c)
		return c.String(200, u.name)
	})
	return app
}

func TestKeyAuthBearer(t *testing.T) {
	cases := []struct {
		name, header string
		status       int
		validated    bool
	}{
		{"valid", "Bearer k1", 200, true},
		{"lower-case scheme", "bearer k1", 200, true},
		{"trailing spaces", "Bearer k1  ", 200, true},
		{"wrong key", "Bearer nope", 401, true},
		{"empty token", "Bearer ", 401, false},
		{"only spaces", "Bearer    ", 401, false},
		{"basic scheme", "Basic k1", 401, false},
		{"another scheme of the same length", "Tokens k1", 401, false},
		{"missing", "", 401, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			var calls int
			app := keyApp(t, "", &seen, &calls)
			headers := map[string]string{}
			if tc.header != "" {
				headers["Authorization"] = tc.header
			}
			fctx := newRequest("GET", "/me", headers)
			app.FasthttpHandler()(fctx)
			if got := fctx.Response.StatusCode(); got != tc.status {
				t.Errorf("status %d, want %d", got, tc.status)
			}
			if (calls > 0) != tc.validated {
				t.Errorf("Validate called %d times, want called=%v", calls, tc.validated)
			}
			if tc.status == 401 && hdr(fctx, "WWW-Authenticate") != "Bearer" {
				t.Errorf("WWW-Authenticate %q, want Bearer", hdr(fctx, "WWW-Authenticate"))
			}
			if tc.status == 200 && (seen != "k1" || string(fctx.Response.Body()) != "svc") {
				t.Errorf("Validate saw %q, handler answered %q", seen, fctx.Response.Body())
			}
		})
	}
}

func TestKeyAuthCustomHeader(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"valid", map[string]string{"X-API-Key": "k1"}, 200},
		{"surrounding whitespace", map[string]string{"X-API-Key": " \tk1\t "}, 200},
		{"empty", map[string]string{"X-API-Key": "  "}, 401},
		{"only Authorization sent", map[string]string{"Authorization": "Bearer k1"}, 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			var calls int
			app := keyApp(t, "X-API-Key", &seen, &calls)
			fctx := newRequest("GET", "/me", tc.headers)
			app.FasthttpHandler()(fctx)
			if got := fctx.Response.StatusCode(); got != tc.status {
				t.Errorf("status %d, want %d", got, tc.status)
			}
			if tc.status == 401 && hdr(fctx, "WWW-Authenticate") != "" {
				t.Errorf("a custom header got a challenge %q", hdr(fctx, "WWW-Authenticate"))
			}
		})
	}
}

func TestKeyAuthStoreErrorIsA500(t *testing.T) {
	app := rice.New()
	app.Use(middleware.KeyAuth(middleware.KeyAuthConfig[*user]{Key: userKey, Validate: func(context.Context, string) (*user, bool, error) {
		return nil, false, errors.New("db down")
	}}))
	app.GET("/me", func(c *rice.Ctx) error { return nil })
	fctx := newRequest("GET", "/me", map[string]string{"Authorization": "Bearer k1"})
	app.FasthttpHandler()(fctx)
	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status %d, want 500", got)
	}
}

func TestKeyAuthPlacement(t *testing.T) {
	newApp := func() *rice.App {
		app := rice.New()
		app.Use(middleware.CORS(middleware.CORSConfig{Origins: []string{"https://app.example.com"}, AllowHeaders: []string{"Authorization"}}))
		api := app.Group("/api", middleware.KeyAuth(middleware.KeyAuthConfig[*user]{Key: userKey, Validate: func(ctx context.Context, key string) (*user, bool, error) {
			return &user{name: "svc"}, key == "k1", nil
		}}))
		api.GET("/me", func(c *rice.Ctx) error { return c.String(200, "me") })
		app.GET("/public", func(c *rice.Ctx) error { return c.String(200, "open") })
		return app
	}

	t.Run("a preflight passes without credentials", func(t *testing.T) {
		fctx := newRequest("OPTIONS", "/api/me", map[string]string{
			"Origin": "https://app.example.com", "Access-Control-Request-Method": "GET",
		})
		newApp().FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 204 {
			t.Errorf("preflight status %d, want 204", got)
		}
	})
	t.Run("a 401 carries the CORS headers", func(t *testing.T) {
		fctx := newRequest("GET", "/api/me", map[string]string{"Origin": "https://app.example.com"})
		newApp().FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 401 || hdr(fctx, "Access-Control-Allow-Origin") != "https://app.example.com" {
			t.Errorf("got %d with Allow-Origin %q", fctx.Response.StatusCode(), hdr(fctx, "Access-Control-Allow-Origin"))
		}
	})
	t.Run("routes outside the group stay open", func(t *testing.T) {
		fctx := newRequest("GET", "/public", nil)
		newApp().FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 200 {
			t.Errorf("status %d, want 200", got)
		}
	})
	t.Run("with app.Use a miss is a 401", func(t *testing.T) {
		app := rice.New()
		app.Use(middleware.KeyAuth(middleware.KeyAuthConfig[*user]{Key: userKey, Validate: func(context.Context, string) (*user, bool, error) { return nil, false, nil }}))
		app.GET("/x", func(c *rice.Ctx) error { return nil })
		fctx := newRequest("GET", "/nowhere", nil)
		app.FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 401 {
			t.Errorf("status %d, want 401: a miss must not reveal that the route is absent", got)
		}
	})
}

func TestKeyAuthPanicsOnAConfigurationThatCannotWork(t *testing.T) {
	ok := func(context.Context, string) (*user, bool, error) { return nil, false, nil }
	cases := map[string]middleware.KeyAuthConfig[*user]{
		"nil Validate":    {Key: userKey},
		"zero Key":        {Validate: ok},
		"space in Header": {Key: userKey, Validate: ok, Header: "X API Key"},
		"colon in Header": {Key: userKey, Validate: ok, Header: "X-API-Key:"},
		"Authorization":   {Key: userKey, Validate: ok, Header: "Authorization"},
		"authorization":   {Key: userKey, Validate: ok, Header: "authorization"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.HasPrefix(msg, "rice: middleware.KeyAuth:") {
					t.Errorf("panic %q", msg)
				}
			}()
			middleware.KeyAuth(cfg)
		})
	}
}
