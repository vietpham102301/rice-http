package middleware_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

type user struct{ name string }

var userKey = rice.NewKey[*user]("test/user")

func basic(userpass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(userpass))
}

// basicApp protects GET /me, which answers the stored user's name. seen records
// what Validate received; calls counts how often it ran.
type basicApp struct {
	app   *rice.App
	seen  [2]string
	calls int
}

func newBasicApp(t *testing.T, result func(u, p string) (*user, bool, error), opts ...rice.Option) *basicApp {
	t.Helper()
	b := &basicApp{app: rice.New(opts...)}
	b.app.Use(middleware.BasicAuth(middleware.BasicAuthConfig[*user]{
		Key: userKey,
		Validate: func(ctx context.Context, u, p string) (*user, bool, error) {
			b.calls++
			b.seen = [2]string{u, p}
			return result(u, p)
		},
	}))
	b.app.GET("/me", func(c *rice.Ctx) error {
		u, ok := userKey.Get(c)
		if !ok {
			return errors.New("no user stored")
		}
		return c.String(200, u.name)
	})
	return b
}

func acceptAll(u, p string) (*user, bool, error) { return &user{name: u}, true, nil }

func TestBasicAuthParsesCredentials(t *testing.T) {
	cases := []struct {
		name, header   string
		user, password string
	}{
		{"plain", basic("ann:secret"), "ann", "secret"},
		{"lower-case scheme", "basic " + base64.StdEncoding.EncodeToString([]byte("ann:secret")), "ann", "secret"},
		{"upper-case scheme", "BASIC " + base64.StdEncoding.EncodeToString([]byte("ann:secret")), "ann", "secret"},
		{"several spaces", "Basic    " + base64.StdEncoding.EncodeToString([]byte("ann:secret")), "ann", "secret"},
		{"colon in the password", basic("ann:se:cr:et"), "ann", "se:cr:et"},
		{"empty user", basic(":secret"), "", "secret"},
		{"empty password", basic("ann:"), "ann", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBasicApp(t, acceptAll)
			fctx := newRequest("GET", "/me", map[string]string{"Authorization": tc.header})
			b.app.FasthttpHandler()(fctx)
			if got := fctx.Response.StatusCode(); got != 200 {
				t.Fatalf("status %d, want 200", got)
			}
			if b.seen != [2]string{tc.user, tc.password} {
				t.Errorf("Validate saw %q, want %q", b.seen, [2]string{tc.user, tc.password})
			}
			if got := string(fctx.Response.Body()); got != tc.user {
				t.Errorf("handler read user %q, want %q", got, tc.user)
			}
		})
	}
}

func TestBasicAuthRejectsMalformedCredentialsWithoutCallingValidate(t *testing.T) {
	cases := map[string]string{
		"missing":        "",
		"empty":          "Basic ",
		"other scheme":   "Bearer abc",
		"no space":       "Basic" + base64.StdEncoding.EncodeToString([]byte("ann:secret")),
		"invalid base64": "Basic !!!not-base64!!!",
		"no colon":       basic("annsecret"),
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			b := newBasicApp(t, acceptAll)
			headers := map[string]string{}
			if header != "" {
				headers["Authorization"] = header
			}
			fctx := newRequest("GET", "/me", headers)
			b.app.FasthttpHandler()(fctx)
			if got := fctx.Response.StatusCode(); got != 401 {
				t.Errorf("status %d, want 401", got)
			}
			if b.calls != 0 {
				t.Errorf("Validate ran %d times on malformed credentials", b.calls)
			}
			if got := hdr(fctx, "WWW-Authenticate"); got != `Basic realm="Restricted", charset="UTF-8"` {
				t.Errorf("WWW-Authenticate %q", got)
			}
		})
	}
}

func TestBasicAuthOutcomes(t *testing.T) {
	t.Run("rejected", func(t *testing.T) {
		b := newBasicApp(t, func(u, p string) (*user, bool, error) { return nil, false, nil })
		fctx := newRequest("GET", "/me", map[string]string{"Authorization": basic("ann:wrong")})
		b.app.FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 401 || string(fctx.Response.Body()) != "Unauthorized" {
			t.Errorf("got %d %q, want 401 Unauthorized", fctx.Response.StatusCode(), fctx.Response.Body())
		}
		if hdr(fctx, "WWW-Authenticate") == "" {
			t.Error("no challenge on a rejected request")
		}
	})
	t.Run("store error is a 500", func(t *testing.T) {
		b := newBasicApp(t, func(u, p string) (*user, bool, error) { return nil, false, errors.New("db down") })
		fctx := newRequest("GET", "/me", map[string]string{"Authorization": basic("ann:secret")})
		b.app.FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 500 {
			t.Errorf("status %d, want 500", got)
		}
		if hdr(fctx, "WWW-Authenticate") != "" {
			t.Error("a store failure carried a challenge, as if the credentials were wrong")
		}
	})
	t.Run("error wins over ok", func(t *testing.T) {
		b := newBasicApp(t, func(u, p string) (*user, bool, error) { return &user{}, true, errors.New("partial") })
		fctx := newRequest("GET", "/me", map[string]string{"Authorization": basic("ann:secret")})
		b.app.FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 500 {
			t.Errorf("status %d, want 500", got)
		}
	})
	t.Run("a custom ErrorHandler sees the callback's error", func(t *testing.T) {
		cause := errors.New("db down")
		var seen error
		b := newBasicApp(t, func(u, p string) (*user, bool, error) { return nil, false, cause },
			rice.WithErrorHandler(func(c *rice.Ctx, err error) { seen = err; _ = c.String(503, "later") }))
		fctx := newRequest("GET", "/me", map[string]string{"Authorization": basic("ann:secret")})
		b.app.FasthttpHandler()(fctx)
		if !errors.Is(seen, cause) || fctx.Response.StatusCode() != 503 {
			t.Errorf("ErrorHandler saw %v and answered %d", seen, fctx.Response.StatusCode())
		}
	})
}

func TestBasicAuthRealm(t *testing.T) {
	app := rice.New()
	app.Use(middleware.BasicAuth(middleware.BasicAuthConfig[*user]{Key: userKey, Validate: func(context.Context, string, string) (*user, bool, error) { return nil, false, nil }, Realm: "admin area"}))
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	fctx := newRequest("GET", "/x", nil)
	app.FasthttpHandler()(fctx)
	if got := hdr(fctx, "WWW-Authenticate"); got != `Basic realm="admin area", charset="UTF-8"` {
		t.Errorf("WWW-Authenticate %q", got)
	}
}

func TestBasicAuthPassesTheRequestContext(t *testing.T) {
	var hadDeadline bool
	app := rice.New()
	app.Use(
		middleware.Timeout(time.Minute),
		middleware.BasicAuth(middleware.BasicAuthConfig[*user]{Key: userKey, Validate: func(ctx context.Context, u, p string) (*user, bool, error) {
			_, hadDeadline = ctx.Deadline()
			return &user{}, true, nil
		}}),
	)
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	app.FasthttpHandler()(newRequest("GET", "/x", map[string]string{"Authorization": basic("a:b")}))
	if !hadDeadline {
		t.Error("Validate did not receive the Timeout's deadline")
	}
}

func TestBasicAuthPanicsOnAConfigurationThatCannotWork(t *testing.T) {
	ok := func(context.Context, string, string) (*user, bool, error) { return nil, false, nil }
	cases := map[string]middleware.BasicAuthConfig[*user]{
		"nil Validate":     {Key: userKey},
		"zero Key":         {Validate: ok},
		"quote in Realm":   {Key: userKey, Validate: ok, Realm: `a"b`},
		"backslash":        {Key: userKey, Validate: ok, Realm: `a\b`},
		"control in Realm": {Key: userKey, Validate: ok, Realm: "a\nb"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.HasPrefix(msg, "rice: middleware.BasicAuth:") {
					t.Errorf("panic %q", msg)
				}
			}()
			middleware.BasicAuth(cfg)
		})
	}
}
