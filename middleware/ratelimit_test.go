package middleware_test

import (
	"context"
	"errors"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// fakeStore answers every Take with wait and err, and records what it saw.
type fakeStore struct {
	wait  time.Duration
	err   error
	calls int
	keys  []string
	ctx   context.Context
	limit middleware.Limit
}

func (s *fakeStore) Take(ctx context.Context, key string, l middleware.Limit) (time.Duration, error) {
	s.calls++
	s.keys = append(s.keys, key)
	s.ctx, s.limit = ctx, l
	return s.wait, s.err
}

var perMinute = middleware.Limit{Rate: 60, Per: time.Minute}

// limitedApp serves GET /x behind RateLimit with cfg; ran reports whether the
// handler ran.
func limitedApp(cfg middleware.RateLimitConfig, ran *bool, opts ...rice.Option) *rice.App {
	app := rice.New(opts...)
	app.Use(middleware.RateLimit(cfg))
	app.GET("/x", func(c *rice.Ctx) error {
		*ran = true
		return c.String(200, "ok")
	})
	return app
}

func TestRateLimitOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		wait       time.Duration
		status     int
		retryAfter string
	}{
		{"allowed", 0, 200, ""},
		{"a wait under a second rounds up to 1", 300 * time.Millisecond, 429, "1"},
		{"a fractional wait rounds up", 1200 * time.Millisecond, 429, "2"},
		{"a whole wait stays whole", 2 * time.Second, 429, "2"},
		{"a wait of a nanosecond is 1", 1, 429, "1"},
		{"the longest wait does not wrap", math.MaxInt64, 429, "9223372037"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ran bool
			store := &fakeStore{wait: tc.wait}
			fctx := newRequest("GET", "/x", nil)
			limitedApp(middleware.RateLimitConfig{Limit: perMinute, Store: store}, &ran).FasthttpHandler()(fctx)
			if got := fctx.Response.StatusCode(); got != tc.status {
				t.Errorf("status %d, want %d", got, tc.status)
			}
			if got := hdr(fctx, "Retry-After"); got != tc.retryAfter {
				t.Errorf("Retry-After %q, want %q", got, tc.retryAfter)
			}
			if ran != (tc.status == 200) {
				t.Errorf("handler ran = %v with status %d", ran, tc.status)
			}
			if tc.status == 429 && string(fctx.Response.Body()) != "Too Many Requests" {
				t.Errorf("body %q", fctx.Response.Body())
			}
			if store.limit != perMinute {
				t.Errorf("Take got Limit %+v, want %+v", store.limit, perMinute)
			}
		})
	}
}

func TestRateLimitErrors(t *testing.T) {
	down := errors.New("store down")
	t.Run("a store error is a 500", func(t *testing.T) {
		var ran bool
		fctx := newRequest("GET", "/x", nil)
		limitedApp(middleware.RateLimitConfig{Limit: perMinute, Store: &fakeStore{wait: time.Second, err: down}}, &ran).FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 500 || ran || hdr(fctx, "Retry-After") != "" {
			t.Errorf("got %d, ran %v, Retry-After %q", fctx.Response.StatusCode(), ran, hdr(fctx, "Retry-After"))
		}
	})
	t.Run("a KeyFunc error is a 500 and never reaches the store", func(t *testing.T) {
		var ran bool
		store := &fakeStore{}
		keyFunc := func(*rice.Ctx) (string, error) { return "", down }
		fctx := newRequest("GET", "/x", nil)
		limitedApp(middleware.RateLimitConfig{Limit: perMinute, Store: store, KeyFunc: keyFunc}, &ran).FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 500 || ran || store.calls != 0 {
			t.Errorf("got %d, ran %v, Take called %d times", fctx.Response.StatusCode(), ran, store.calls)
		}
	})
	t.Run("a custom ErrorHandler sees the store's error", func(t *testing.T) {
		var ran bool
		var seen error
		fctx := newRequest("GET", "/x", nil)
		limitedApp(middleware.RateLimitConfig{Limit: perMinute, Store: &fakeStore{err: down}}, &ran,
			rice.WithErrorHandler(func(c *rice.Ctx, err error) { seen = err; _ = c.String(503, "later") })).FasthttpHandler()(fctx)
		if !errors.Is(seen, down) || fctx.Response.StatusCode() != 503 {
			t.Errorf("ErrorHandler saw %v and answered %d", seen, fctx.Response.StatusCode())
		}
	})
}

func TestRateLimitKeyFuncChoosesTheKey(t *testing.T) {
	var ran bool
	store := &fakeStore{}
	keyFunc := func(c *rice.Ctx) (string, error) { return string(c.Header("X-User")), nil }
	app := limitedApp(middleware.RateLimitConfig{Limit: perMinute, Store: store, KeyFunc: keyFunc}, &ran)
	app.FasthttpHandler()(newRequest("GET", "/x", map[string]string{"X-User": "ann"}))
	app.FasthttpHandler()(newRequest("GET", "/x", nil))
	if len(store.keys) != 2 || store.keys[0] != "ann" || store.keys[1] != "" {
		t.Errorf("keys %q, want [ann \"\"]", store.keys)
	}
}

func TestRateLimitPassesTheRequestContext(t *testing.T) {
	store := &fakeStore{}
	app := rice.New()
	app.Use(middleware.Timeout(time.Minute), middleware.RateLimit(middleware.RateLimitConfig{Limit: perMinute, Store: store}))
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	app.FasthttpHandler()(newRequest("GET", "/x", nil))
	if store.ctx == nil {
		t.Fatal("Take was not called")
	}
	if _, ok := store.ctx.Deadline(); !ok {
		t.Error("Take did not receive the Timeout's deadline")
	}
}

// keyFor returns the default key RateLimit hands the store for a request from
// addr, with headers.
func keyFor(t *testing.T, addr string, headers map[string]string, mws ...rice.Middleware) string {
	t.Helper()
	store := &fakeStore{}
	app := rice.New()
	app.Use(append(mws, middleware.RateLimit(middleware.RateLimitConfig{Limit: perMinute, Store: store}))...)
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	fctx := newRequest("GET", "/x", headers)
	fctx.SetRemoteAddr(&net.TCPAddr{IP: net.ParseIP(addr), Port: 4711})
	app.FasthttpHandler()(fctx)
	if len(store.keys) != 1 {
		t.Fatalf("Take called %d times", len(store.keys))
	}
	return store.keys[0]
}

func TestRateLimitDefaultKey(t *testing.T) {
	same := func(a, b string) bool { return keyFor(t, a, nil) == keyFor(t, b, nil) }
	if same("203.0.113.1", "203.0.113.2") {
		t.Error("two IPv4 clients share a key")
	}
	if !same("2001:db8:1:2::1", "2001:db8:1:2:ffff::9") {
		t.Error("two addresses in one IPv6 /64 are two keys")
	}
	if same("2001:db8:1:2::1", "2001:db8:1:3::1") {
		t.Error("two IPv6 /64s share a key")
	}
	if !same("::ffff:203.0.113.1", "203.0.113.1") {
		t.Error("an IPv4-mapped IPv6 address is not its IPv4 client's key")
	}
	if same("203.0.113.1", "::") {
		t.Error("an IPv4 client shares the unspecified IPv6 address's key")
	}
	t.Run("behind RealIP the forwarded address is the key", func(t *testing.T) {
		fwd := map[string]string{"X-Forwarded-For": "198.51.100.7"}
		if keyFor(t, "10.0.0.1", fwd, middleware.RealIP(1)) != keyFor(t, "198.51.100.7", nil) {
			t.Error("the key is not the forwarded client's")
		}
	})
}

func TestRateLimitWithTheMemoryStore(t *testing.T) {
	var ran bool
	app := limitedApp(middleware.RateLimitConfig{Limit: middleware.Limit{Rate: 1, Per: time.Hour}}, &ran)
	serve := func(addr string) *fasthttp.RequestCtx {
		fctx := newRequest("GET", "/x", nil)
		fctx.SetRemoteAddr(&net.TCPAddr{IP: net.ParseIP(addr), Port: 1})
		app.FasthttpHandler()(fctx)
		return fctx
	}
	if got := serve("203.0.113.1").Response.StatusCode(); got != 200 {
		t.Fatalf("first request %d, want 200", got)
	}
	second := serve("203.0.113.1")
	if second.Response.StatusCode() != 429 || hdr(second, "Retry-After") != "3600" {
		t.Errorf("second request %d with Retry-After %q, want 429 and 3600", second.Response.StatusCode(), hdr(second, "Retry-After"))
	}
	if got := serve("203.0.113.2").Response.StatusCode(); got != 200 {
		t.Errorf("another client got %d, want 200", got)
	}
}

func TestRateLimitPlacement(t *testing.T) {
	deny := &fakeStore{wait: time.Second}
	t.Run("a 429 carries the CORS headers", func(t *testing.T) {
		app := rice.New()
		app.Use(middleware.CORS(middleware.CORSConfig{Origins: []string{"https://app.example.com"}}),
			middleware.RateLimit(middleware.RateLimitConfig{Limit: perMinute, Store: deny}))
		app.GET("/x", func(c *rice.Ctx) error { return nil })
		fctx := newRequest("GET", "/x", map[string]string{"Origin": "https://app.example.com"})
		app.FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 429 || hdr(fctx, "Access-Control-Allow-Origin") != "https://app.example.com" {
			t.Errorf("got %d with Allow-Origin %q", fctx.Response.StatusCode(), hdr(fctx, "Access-Control-Allow-Origin"))
		}
	})
	t.Run("with app.Use a miss is counted", func(t *testing.T) {
		store := &fakeStore{wait: time.Second}
		app := rice.New()
		app.Use(middleware.RateLimit(middleware.RateLimitConfig{Limit: perMinute, Store: store}))
		app.GET("/x", func(c *rice.Ctx) error { return nil })
		fctx := newRequest("GET", "/nowhere", nil)
		app.FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 429 || store.calls != 1 {
			t.Errorf("a miss got %d after %d Take calls, want 429 after 1", fctx.Response.StatusCode(), store.calls)
		}
	})
	t.Run("on a group other routes are not limited", func(t *testing.T) {
		app := rice.New()
		api := app.Group("/api", middleware.RateLimit(middleware.RateLimitConfig{Limit: perMinute, Store: deny}))
		api.GET("/x", func(c *rice.Ctx) error { return nil })
		app.GET("/public", func(c *rice.Ctx) error { return c.String(200, "open") })
		fctx := newRequest("GET", "/public", nil)
		app.FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 200 {
			t.Errorf("status %d, want 200", got)
		}
	})
}

func TestRateLimitPanicsOnALimitThatCannotWork(t *testing.T) {
	cases := map[string]middleware.Limit{
		"zero Rate":         {Per: time.Second},
		"zero Per":          {Rate: 1},
		"negative Burst":    {Rate: 1, Per: time.Second, Burst: -1},
		"sub-ns interval":   {Rate: 2, Per: 1},
		"overflowing burst": {Rate: 1, Per: time.Hour, Burst: math.MaxInt},
		"Per of forever":    {Rate: 1, Per: math.MaxInt64},
	}
	for name, l := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.HasPrefix(msg, "rice: middleware.RateLimit:") {
					t.Errorf("panic %q", msg)
				}
			}()
			middleware.RateLimit(middleware.RateLimitConfig{Limit: l})
		})
	}
}
