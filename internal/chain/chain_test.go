package chain

import (
	"reflect"
	"strings"
	"testing"
)

// The test types stand in for rice.Handler and rice.Middleware without importing
// them, which internal/ may not do. They have the same shape, which is the point:
// if Compile works here it works there.
type handler func(*[]string)

type middleware func(handler) handler

// mark returns a middleware that records its name on the way in and again on the
// way out, so a single trace shows both orders at once.
func mark(name string) middleware {
	return func(next handler) handler {
		return func(log *[]string) {
			*log = append(*log, name+"-in")
			next(log)
			*log = append(*log, name+"-out")
		}
	}
}

func run(h handler, mws ...middleware) string {
	var log []string
	Compile(h, mws)(&log)
	return strings.Join(log, " ")
}

func base() handler {
	return func(log *[]string) { *log = append(*log, "handler") }
}

func TestCompileWithNoMiddleware(t *testing.T) {
	if got := run(base()); got != "handler" {
		t.Errorf("run() = %q, want %q", got, "handler")
	}
}

// TestCompileWithNoMiddlewareReturnsTheHandlerItself checks that an empty chain
// does not wrap. A wrapper that only forwards would be invisible to the trace
// above but would cost a closure call on every request of every route that has
// no middleware, which is most of them.
// Uses reflect.ValueOf().Pointer() because Go does not allow direct comparison
// of func values with ==. This is in internal/chain, a test file, so ADR-0006's
// prohibition on reflect in core does not apply.
//
// Pointer() returns a *code* pointer, not an identity for the closure value: it
// cannot tell apart two distinct closures created from the same function
// literal. Both call sites in this file are sound, but for different reasons.
// Here the two sides are the same variable, h, so equality is exact. In
// TestCompileWithMiddlewareWrapsTheHandler the two sides are h and Compile's
// wrapper, which come from different function literals, so inequality is
// exact. Neither is a general function-identity check, and the pattern must
// not be extended to a case where two different closures could share the same
// underlying code.
func TestCompileWithNoMiddlewareReturnsTheHandlerItself(t *testing.T) {
	called := false
	h := handler(func(log *[]string) { called = true })

	got := Compile(h, ([]middleware)(nil))
	got(nil)

	if !called {
		t.Fatal("the compiled handler did not call the original")
	}

	// Compare code pointers: the compiled result must be the exact same handler
	// value, not a wrapper, to avoid the closure overhead on every request. This
	// only works because both sides are the same variable h — see the doc
	// comment above on what Pointer() can and cannot distinguish.
	if reflect.ValueOf(got).Pointer() != reflect.ValueOf(h).Pointer() {
		t.Fatal("Compile returned a wrapped handler instead of the original; this defeats the optimization")
	}
}

func TestCompileWithOneMiddleware(t *testing.T) {
	if got := run(base(), mark("A")); got != "A-in handler A-out" {
		t.Errorf("run() = %q, want %q", got, "A-in handler A-out")
	}
}

// TestCompileWithMiddlewareWrapsTheHandler asserts that when middleware is
// present, Compile returns a wrapped handler, not the original. This catches
// a Compile that silently dropped its middleware.
func TestCompileWithMiddlewareWrapsTheHandler(t *testing.T) {
	h := base()
	mws := []middleware{mark("A")}
	got := Compile(h, mws)

	if reflect.ValueOf(got).Pointer() == reflect.ValueOf(h).Pointer() {
		t.Fatal("Compile returned the original handler instead of wrapping it; middleware was silently dropped")
	}
}

// TestCompileOrder is the test this package exists for. The first middleware
// given is outermost: it runs first on the way in and last on the way out. A
// fold from the wrong end produces "C-in B-in A-in handler A-out B-out C-out",
// which is still a working chain and still passes any test that does not look
// at order.
func TestCompileOrder(t *testing.T) {
	got := run(base(), mark("A"), mark("B"), mark("C"))
	want := "A-in B-in C-in handler C-out B-out A-out"

	if got != want {
		t.Errorf("run() = %q, want %q", got, want)
	}
}

func TestCompileFiveMiddleware(t *testing.T) {
	got := run(base(), mark("1"), mark("2"), mark("3"), mark("4"), mark("5"))
	want := "1-in 2-in 3-in 4-in 5-in handler 5-out 4-out 3-out 2-out 1-out"

	if got != want {
		t.Errorf("run() = %q, want %q", got, want)
	}
}

// TestCompileShortCircuit records that stopping the chain is an ordinary return,
// which is ADR-0003's reason for choosing the decorator over an index walk: there
// is no rule to remember, because not calling next is visibly not calling next.
func TestCompileShortCircuit(t *testing.T) {
	stop := middleware(func(next handler) handler {
		return func(log *[]string) {
			*log = append(*log, "stopped")
			// next is deliberately not called
		}
	})

	got := run(base(), mark("A"), stop, mark("C"))
	want := "A-in stopped A-out"

	if got != want {
		t.Errorf("run() = %q, want %q; a middleware that does not call next must stop the chain", got, want)
	}
}

// TestCompileDoesNotRetainTheSlice guards against Compile keeping a reference to
// the caller's slice: the chain is fixed at compile time, and a later change to
// the slice must not reach the compiled handler.
func TestCompileDoesNotRetainTheSlice(t *testing.T) {
	mws := []middleware{mark("A"), mark("B")}

	compiled := Compile(base(), mws)

	mws[0] = mark("REPLACED")
	mws[1] = mark("ALSO-REPLACED")

	var log []string
	compiled(&log)

	if got := strings.Join(log, " "); got != "A-in B-in handler B-out A-out" {
		t.Errorf("after mutating the slice, the compiled chain ran %q; it should have been fixed at compile time", got)
	}
}
