package rice

import (
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
