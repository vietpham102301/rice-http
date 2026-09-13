package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// newParamCtx builds a Ctx with parameters already captured, the way a lookup
// would leave it.
func newParamCtx(t *testing.T, pairs ...string) (*Ctx, *fasthttp.RequestCtx) {
	t.Helper()
	if len(pairs)%2 != 0 {
		t.Fatal("newParamCtx needs key/value pairs")
	}

	app := New()
	app.GET("/x", func(c *Ctx) error { return nil })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/x")

	c := &Ctx{}
	c.reset(app, fctx)
	for i := 0; i < len(pairs); i += 2 {
		c.params.Set(pairs[i], []byte(pairs[i+1]))
	}
	return c, fctx
}

func TestParamReturnsTheCapturedValue(t *testing.T) {
	c, _ := newParamCtx(t, "id", "42")

	if got := string(c.Param("id")); got != "42" {
		t.Errorf("Param(\"id\") = %q, want %q", got, "42")
	}
}

func TestParamOnAnAbsentNameIsEmpty(t *testing.T) {
	c, _ := newParamCtx(t, "id", "42")

	if got := c.Param("missing"); len(got) != 0 {
		t.Errorf("Param on an absent name = %q, want empty", got)
	}
}

func TestParamOnARouteWithNoParametersIsEmpty(t *testing.T) {
	c, _ := newParamCtx(t)

	if got := c.Param("id"); len(got) != 0 {
		t.Errorf("Param = %q on a route with no parameters, want empty", got)
	}
}

func TestParamStringReturnsTheCapturedValue(t *testing.T) {
	c, _ := newParamCtx(t, "id", "42")

	if got := c.ParamString("id"); got != "42" {
		t.Errorf("ParamString(\"id\") = %q, want %q", got, "42")
	}
}

func TestParamStringOnAnAbsentNameIsTheEmptyString(t *testing.T) {
	c, _ := newParamCtx(t, "id", "42")

	if got := c.ParamString("missing"); got != "" {
		t.Errorf("ParamString on an absent name = %q, want the empty string", got)
	}
}

// TestParamIsBorrowedAndParamStringIsOwned is the borrow contract stated as a
// test. Param aliases the request buffer; ParamString copies out of it. This is
// the distinction docs/03-core-concepts.md promises and the reason the expensive
// accessor has the longer name.
func TestParamIsBorrowedAndParamStringIsOwned(t *testing.T) {
	backing := []byte("42")

	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)
	c.params.Set("id", backing)

	borrowed := c.Param("id")
	owned := c.ParamString("id")

	copy(backing, "99")

	if got := string(borrowed); got != "99" {
		t.Errorf("Param returned %q after the buffer changed; it is expected to alias, not copy", got)
	}
	if owned != "42" {
		t.Errorf("ParamString returned %q after the buffer changed; it is expected to copy", owned)
	}
}

// TestResetClearsParameters matters because M6 reuses a Ctx across requests: a
// parameter from the previous request must not be visible to the next handler.
func TestResetClearsParameters(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}

	c := &Ctx{}
	c.reset(app, fctx)
	c.params.Set("id", []byte("42"))

	c.reset(app, fctx)

	if got := c.Param("id"); len(got) != 0 {
		t.Errorf("Param(\"id\") = %q after reset, want empty", got)
	}
}

func TestDispatchExposesCapturedParametersToTheHandler(t *testing.T) {
	app := New()

	var gotID, gotPID string
	app.GET("/users/:id/posts/:pid", func(c *Ctx) error {
		gotID = c.ParamString("id")
		gotPID = c.ParamString("pid")
		return c.String(200, "ok")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42/posts/7")

	app.Build()
	app.handle(fctx)

	if fctx.Response.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", fctx.Response.StatusCode())
	}
	if gotID != "42" {
		t.Errorf("id = %q, want %q", gotID, "42")
	}
	if gotPID != "7" {
		t.Errorf("pid = %q, want %q", gotPID, "7")
	}
}

func TestDispatchExposesAWildcardToTheHandler(t *testing.T) {
	app := New()

	var got string
	app.GET("/files/*path", func(c *Ctx) error {
		got = c.ParamString("path")
		return c.String(200, "ok")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/files/a/b.txt")

	app.Build()
	app.handle(fctx)

	if got != "a/b.txt" {
		t.Errorf("path = %q, want %q", got, "a/b.txt")
	}
}
