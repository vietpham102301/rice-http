//go:build ricedebug

package rice

import (
	"reflect"
	"testing"

	"github.com/valyala/fasthttp"
)

// releasedCtx dispatches one request whose handler retains its Ctx, and returns
// that Ctx after release.
func releasedCtx(t *testing.T) *Ctx {
	t.Helper()
	var retained *Ctx
	app := New()
	app.GET("/users/:id", func(c *Ctx) error {
		retained = c
		return nil
	})
	dispatchCtx(app, "GET", "/users/42")
	if retained == nil {
		t.Fatal("handler did not run")
	}
	return retained
}

// TestEveryCtxMethodPanicsAfterRelease walks the exported method set by
// reflection, so a method added later without a check() fails here without
// anyone remembering to extend a list. reflect is confined to this test file;
// ADR-0006 governs production code.
func TestEveryCtxMethodPanicsAfterRelease(t *testing.T) {
	c := releasedCtx(t)
	v := reflect.ValueOf(c)
	typ := v.Type()

	if typ.NumMethod() == 0 {
		t.Fatal("*Ctx has no exported methods; this test is measuring nothing")
	}

	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		args := make([]reflect.Value, m.Type.NumIn()-1) // In(0) is the receiver
		for j := range args {
			args[j] = reflect.Zero(m.Type.In(j + 1))
		}

		t.Run(m.Name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r != errUseAfterRelease {
					t.Errorf("%s on a released Ctx: recovered %v, want the use-after-release panic", m.Name, r)
				}
			}()
			v.Method(i).Call(args)
		})
	}
}

// TestARetainedCtxPanicsEvenAfterAnotherRequest is the sequence ADR-0005's
// original mechanism missed: a retained Ctx used after a later request has run.
// With a poisoned Ctx returned to the pool, the second request would un-poison
// it and this call would return the second request's parameter instead of
// panicking.
func TestARetainedCtxPanicsEvenAfterAnotherRequest(t *testing.T) {
	var retained *Ctx
	app := New()
	app.GET("/users/:id", func(c *Ctx) error {
		if retained == nil {
			retained = c
		}
		return nil
	})
	app.Build()

	for _, uri := range []string{"/users/1", "/users/2"} {
		fctx := &fasthttp.RequestCtx{}
		fctx.Request.Header.SetMethod("GET")
		fctx.Request.SetRequestURI(uri)
		app.handle(fctx)
	}

	defer func() {
		if r := recover(); r != errUseAfterRelease {
			t.Errorf("retained Ctx after a second request: recovered %v, want the use-after-release panic", r)
		}
	}()
	_ = retained.Param("id")
}

func TestTheDebugBuildNeverReusesAContext(t *testing.T) {
	if poolReuse {
		t.Error("poolReuse is true under ricedebug; a poisoned Ctx would be un-poisoned by its next acquire")
	}
}
