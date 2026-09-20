package rice

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestCtxMethodAndPath(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("POST")
	fctx.Request.SetRequestURI("/users/42?q=x")

	c := &Ctx{}
	c.reset(nil, fctx)

	if got := string(c.Method()); got != "POST" {
		t.Errorf("Method() = %q, want %q", got, "POST")
	}
	if got := string(c.Path()); got != "/users/42" {
		t.Errorf("Path() = %q, want %q", got, "/users/42")
	}
}

func TestCtxRequestCtxReturnsUnderlyingContext(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}

	c := &Ctx{}
	c.reset(nil, fctx)

	if c.RequestCtx() != fctx {
		t.Error("RequestCtx() did not return the context passed to reset")
	}
}

func TestCtxResetRebindsToANewRequest(t *testing.T) {
	first := &fasthttp.RequestCtx{}
	first.Request.SetRequestURI("/first")

	second := &fasthttp.RequestCtx{}
	second.Request.SetRequestURI("/second")

	c := &Ctx{}
	c.reset(nil, first)
	c.reset(nil, second)

	if got := string(c.Path()); got != "/second" {
		t.Errorf("after reset, Path() = %q, want %q", got, "/second")
	}
}

func TestCtxQueryReturnsTheNamedValue(t *testing.T) {
	c, _ := newTestCtx("GET", "/search?q=rice&page=2&empty=")

	if got := string(c.Query("q")); got != "rice" {
		t.Errorf("Query(q) = %q, want %q", got, "rice")
	}
	if got := string(c.Query("page")); got != "2" {
		t.Errorf("Query(page) = %q, want %q", got, "2")
	}
	if got := c.Query("empty"); len(got) != 0 {
		t.Errorf("Query(empty) = %q, want empty", got)
	}
	if got := c.Query("missing"); len(got) != 0 {
		t.Errorf("Query(missing) = %q, want empty", got)
	}
}

func TestCtxQueryDecodesPercentEncoding(t *testing.T) {
	c, _ := newTestCtx("GET", "/search?q=hello%20world")

	if got := string(c.Query("q")); got != "hello world" {
		t.Errorf("Query(q) = %q, want %q", got, "hello world")
	}
}

func TestCtxHeaderIsCaseInsensitive(t *testing.T) {
	c, fctx := newTestCtx("GET", "/")
	fctx.Request.Header.Set("X-Request-Id", "abc123")

	for _, name := range []string{"X-Request-Id", "x-request-id", "X-REQUEST-ID"} {
		if got := string(c.Header(name)); got != "abc123" {
			t.Errorf("Header(%q) = %q, want %q", name, got, "abc123")
		}
	}
	if got := c.Header("X-Missing"); len(got) != 0 {
		t.Errorf("Header(X-Missing) = %q, want empty", got)
	}
}

func TestCtxBodyReturnsTheRequestBody(t *testing.T) {
	c, fctx := newTestCtx("POST", "/users")
	fctx.Request.SetBodyString(`{"name":"rice"}`)

	if got := string(c.Body()); got != `{"name":"rice"}` {
		t.Errorf("Body() = %q, want %q", got, `{"name":"rice"}`)
	}
}

func TestCtxBodyIsEmptyWithoutABody(t *testing.T) {
	c, _ := newTestCtx("GET", "/")

	if got := c.Body(); len(got) != 0 {
		t.Errorf("Body() = %q, want empty", got)
	}
}

// TestClientIPIgnoresXForwardedFor is a security claim, not an implementation
// detail: a ClientIP that trusted this header by default would let any client
// declare its own address, and the access log and every rate limiter built on
// it would be reporting attacker-supplied data. The trust policy lives in
// middleware.RealIP, which rewrites the remote address instead.
func TestClientIPIgnoresXForwardedFor(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.Set("X-Forwarded-For", "203.0.113.9")
	fctx.Request.Header.Set("X-Real-Ip", "203.0.113.9")
	c := &Ctx{}
	c.reset(nil, fctx)

	if got := c.ClientIP(); got.String() == "203.0.113.9" {
		t.Errorf("ClientIP() = %v, want the connection address: headers must not be trusted", got)
	}
}

// TestClientIPFollowsSetRemoteAddr pins the mechanism middleware.RealIP will
// use: it resolves the header against its trusted-hop count and rewrites
// fasthttp's remote address, and ClientIP reports the result without knowing
// that happened.
func TestClientIPFollowsSetRemoteAddr(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	fctx.SetRemoteAddr(&net.TCPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 1234})
	c := &Ctx{}
	c.reset(nil, fctx)

	if got := c.ClientIP(); got.String() != "203.0.113.9" {
		t.Errorf("ClientIP() = %v, want 203.0.113.9", got)
	}
}

// TestContextIsTheAppsBaseContextByDefault also pins that the budget tests'
// fixture is not enough for Context: it reads c.app, so a Ctx reset with a nil
// App has no context to return.
func TestContextIsTheAppsBaseContextByDefault(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)

	got := c.Context()
	if got == nil {
		t.Fatal("Context() = nil, want the App's base context")
	}
	if err := got.Err(); err != nil {
		t.Errorf("Context().Err() = %v, want nil: nothing has force-closed", err)
	}
	select {
	case <-got.Done():
		t.Error("Context() is already done, want live")
	default:
	}
}

func TestSetContextIsWhatContextReturns(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)

	type key struct{}
	want := context.WithValue(c.Context(), key{}, "v")
	c.SetContext(want)

	if got := c.Context(); got != want {
		t.Errorf("Context() = %v, want the context SetContext was given", got)
	}
	if got := c.Context().Value(key{}); got != "v" {
		t.Errorf("Context().Value(key) = %v, want %q", got, "v")
	}
}

// TestResetClearsTheContext keeps a released Ctx from handing a finished
// request's context to the next one.
func TestResetClearsTheContext(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)
	c.SetContext(context.WithValue(c.Context(), struct{}{}, "v"))

	c.reset(app, fctx)

	if got := c.Context(); got != app.baseCtx {
		t.Errorf("Context() after reset = %v, want the App's base context", got)
	}
}

// TestAContextOutlivesItsHandler pins the D4 exception: reset clears the field,
// which must not disturb a context a handler already took. Every other accessor
// on Ctx hands out something that dies here; this one does not.
func TestAContextOutlivesItsHandler(t *testing.T) {
	app := New()
	var kept context.Context
	app.GET("/keep", func(c *Ctx) error {
		kept = c.Context()
		return nil
	})
	dispatchCtx(app, "GET", "/keep")

	if kept == nil {
		t.Fatal("handler did not run")
	}
	if err := kept.Err(); err != nil {
		t.Errorf("Err() = %v after the handler returned, want nil: the context is owned, not borrowed", err)
	}
}

func TestSetContextPanicsOnNil(t *testing.T) {
	app := New()
	c := &Ctx{}
	c.reset(app, &fasthttp.RequestCtx{})

	v := mustPanic(t, "SetContext(nil)", func() { c.SetContext(nil) })
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, "rice: ") {
		t.Errorf("SetContext(nil) panicked with %v, want a string starting %q", v, "rice: ")
	}
}
