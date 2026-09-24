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
// original mechanism missed: a Ctx retained from an earlier request, called
// while a later request still owns it. Under ADR-0005's original mechanism —
// where reset clears the poison on acquire and a released Ctx goes back to the
// pool — request 2's acquire would already have un-poisoned this same object
// before the stale call below runs, so the call would return request 2's
// parameter ("2") instead of panicking.
//
// The stale call has to happen while request 2 is still live. Checking
// afterwards, once both requests have finished, cannot see the failure: by
// then, either request 2 reused the same object and its own release re-marked
// it, or request 2 got a different object and retained still carries request
// 1's own mark from request 1's release. Either way a post-hoc check passes
// regardless of whether the mechanism actually protects the live window.
func TestARetainedCtxPanicsEvenAfterAnotherRequest(t *testing.T) {
	var retained *Ctx
	var recovered any
	var staleResult string
	secondRequestRan := false

	app := New()
	app.GET("/users/:id", func(c *Ctx) error {
		if retained == nil {
			retained = c
			return nil
		}

		// This is request 2, and it still owns c. retained is request 1's Ctx,
		// called here while request 2 is live — the exact window ADR-0005's
		// original mechanism missed.
		secondRequestRan = true
		func() {
			defer func() { recovered = recover() }()
			staleResult = string(retained.Param("id"))
		}()
		return nil
	})
	app.Build()

	for _, uri := range []string{"/users/1", "/users/2"} {
		fctx := &fasthttp.RequestCtx{}
		fctx.Request.Header.SetMethod("GET")
		fctx.Request.SetRequestURI(uri)
		app.handle(fctx)
	}

	if !secondRequestRan {
		t.Fatal("request 2's handler did not run; the stale call was never attempted")
	}
	if recovered != errUseAfterRelease {
		t.Errorf("stale call on request 1's Ctx while request 2 held it: recovered %v (returned %q), want the use-after-release panic", recovered, staleResult)
	}
}

func TestTheDebugBuildNeverReusesAContext(t *testing.T) {
	if poolReuse {
		t.Error("poolReuse is true under ricedebug; a poisoned Ctx would be un-poisoned by its next acquire")
	}
}

// TestKeyMethodsPanicAfterRelease covers what the reflection walk above cannot:
// Key's methods are methods on Key, not on *Ctx. A zero Key is included so the
// use-after-release panic is shown to come before the zero-key check.
func TestKeyMethodsPanicAfterRelease(t *testing.T) {
	c := releasedCtx(t)
	k := NewKey[string]("k")
	var zero Key[string]
	for name, call := range map[string]func(){
		"Set":          func() { k.Set(c, "v") },
		"Get":          func() { k.Get(c) },
		"zero key Set": func() { zero.Set(c, "v") },
		"zero key Get": func() { zero.Get(c) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != errUseAfterRelease {
					t.Errorf("%s on a released Ctx: recovered %v, want the use-after-release panic", name, r)
				}
			}()
			call()
		})
	}
}
