package rice

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestGetOnAnAbsentKey(t *testing.T) {
	c := &Ctx{}

	v, ok := c.Get("missing")
	if v != nil || ok {
		t.Errorf("Get(\"missing\") = %v, %v; want nil, false", v, ok)
	}
}

func TestSetThenGet(t *testing.T) {
	c := &Ctx{}
	c.Set("user", "alice")

	v, ok := c.Get("user")
	if !ok || v != "alice" {
		t.Errorf("Get(\"user\") = %v, %v; want alice, true", v, ok)
	}
}

func TestSetReplacesAnExistingKey(t *testing.T) {
	c := &Ctx{}
	c.Set("k", 1)
	c.Set("k", 2)

	if v, _ := c.Get("k"); v != 2 {
		t.Errorf("Get(\"k\") = %v, want 2", v)
	}
	if n := len(c.store); n != 1 {
		t.Errorf("store holds %d entries after setting one key twice, want 1", n)
	}
}

// TestTheStoreHoldsMoreThanItsInitialCapacity crosses storeCapacity, the
// documented cliff, and checks nothing is lost on the way over it.
func TestTheStoreHoldsMoreThanItsInitialCapacity(t *testing.T) {
	c := &Ctx{}
	for i := 0; i < storeCapacity+2; i++ {
		c.Set("k"+strconv.Itoa(i), i)
	}

	for i := 0; i < storeCapacity+2; i++ {
		if v, ok := c.Get("k" + strconv.Itoa(i)); !ok || v != i {
			t.Errorf("Get(k%d) = %v, %v; want %d, true", i, v, ok, i)
		}
	}
}

// TestMiddlewareCanPassAValueToTheHandler is the use the store exists for.
func TestMiddlewareCanPassAValueToTheHandler(t *testing.T) {
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			c.Set("request_id", "abc")
			return next(c)
		}
	})
	app.GET("/x", func(c *Ctx) error {
		v, _ := c.Get("request_id")
		return c.String(200, v.(string))
	})

	fctx := dispatchCtx(app, "GET", "/x")

	if got := string(fctx.Response.Body()); got != "abc" {
		t.Errorf("body = %q, want %q", got, "abc")
	}
}

func TestARequestSeesNoStoreEntriesFromThePreviousOne(t *testing.T) {
	var leaked bool
	app := New()
	app.GET("/set", func(c *Ctx) error { c.Set("k", "v"); return nil })
	app.GET("/get", func(c *Ctx) error { _, leaked = c.Get("k"); return nil })
	app.Build()

	for _, uri := range []string{"/set", "/get"} {
		fctx := &fasthttp.RequestCtx{}
		fctx.Request.Header.SetMethod("GET")
		fctx.Request.SetRequestURI(uri)
		app.handle(fctx)
	}

	if leaked {
		t.Error("a store entry set by one request was visible to the next")
	}
}
