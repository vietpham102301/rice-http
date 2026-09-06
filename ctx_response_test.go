package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

func newTestCtx(method, uri string) (*Ctx, *fasthttp.RequestCtx) {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(method)
	fctx.Request.SetRequestURI(uri)

	c := &Ctx{}
	c.reset(nil, fctx)
	return c, fctx
}

func TestStringWritesStatusBodyAndContentType(t *testing.T) {
	c, fctx := newTestCtx("GET", "/hello")

	if err := c.String(201, "hello"); err != nil {
		t.Fatalf("String returned %v, want nil", err)
	}

	if got := fctx.Response.StatusCode(); got != 201 {
		t.Errorf("status = %d, want 201", got)
	}
	if got := string(fctx.Response.Body()); got != "hello" {
		t.Errorf("body = %q, want %q", got, "hello")
	}
	if got := string(fctx.Response.Header.ContentType()); got != MIMETextPlainUTF8 {
		t.Errorf("content type = %q, want %q", got, MIMETextPlainUTF8)
	}
}

func TestBytesWritesStatusAndBodyWithoutSettingContentType(t *testing.T) {
	c, fctx := newTestCtx("GET", "/raw")
	c.SetContentType("application/octet-stream")

	if err := c.Bytes(200, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatalf("Bytes returned %v, want nil", err)
	}

	if got := fctx.Response.StatusCode(); got != 200 {
		t.Errorf("status = %d, want 200", got)
	}
	if got := string(fctx.Response.Body()); got != "\x01\x02\x03" {
		t.Errorf("body = %q, want three raw bytes", got)
	}
	if got := string(fctx.Response.Header.ContentType()); got != "application/octet-stream" {
		t.Errorf("Bytes overwrote the content type: got %q", got)
	}
}

func TestStatusIsChainable(t *testing.T) {
	c, fctx := newTestCtx("GET", "/chain")

	if c.Status(418) != c {
		t.Error("Status did not return the same *Ctx")
	}
	if got := fctx.Response.StatusCode(); got != 418 {
		t.Errorf("status = %d, want 418", got)
	}
}

func TestSetHeaderWritesAResponseHeader(t *testing.T) {
	c, fctx := newTestCtx("GET", "/hdr")

	c.SetHeader("X-Trace", "abc123")

	if got := string(fctx.Response.Header.Peek("X-Trace")); got != "abc123" {
		t.Errorf("X-Trace = %q, want %q", got, "abc123")
	}
}

func TestBodyIsOverwrittenNotAppendedOnSecondWrite(t *testing.T) {
	c, fctx := newTestCtx("GET", "/twice")

	_ = c.String(200, "first")
	_ = c.String(200, "second")

	if got := string(fctx.Response.Body()); got != "second" {
		t.Errorf("body = %q, want %q; writes must replace, not append", got, "second")
	}
}
