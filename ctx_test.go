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
