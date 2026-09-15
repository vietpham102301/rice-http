package bench

import (
	"errors"
	"testing"

	rice "github.com/vietpham102301/rice-http"
)

// noRecover and withRecover are deliberately identical apart from the deferred
// recover, so the difference between them is the mechanism and nothing else.
//
// This exists because comparing M5's BenchmarkChainDispatch0 against M4's
// recorded file compares two sessions, and between-session drift already
// misled this project once, in M4's middleware sweep. Two arms measured in one
// run is the only honest way to price a defer.
//
//go:noinline
func noRecover(n int) int { return n + 1 }

//go:noinline
func withRecover(n int) (out int) {
	defer func() {
		if r := recover(); r != nil {
			out = -1
		}
	}()
	return n + 1
}

func BenchmarkNoRecoverBaseline(b *testing.B) {
	sink := 0
	for i := 0; i < b.N; i++ {
		sink = noRecover(sink)
	}
	globalSink = sink
}

func BenchmarkDeferRecoverOverhead(b *testing.B) {
	sink := 0
	for i := 0; i < b.N; i++ {
		sink = withRecover(sink)
	}
	globalSink = sink
}

var globalSink int

func BenchmarkDispatch404(b *testing.B) {
	app := rice.New()
	app.GET("/users", func(c *rice.Ctx) error { return nil })
	h := app.FasthttpHandler()

	fctx := newRequestCtx("GET", "/missing")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

func BenchmarkDispatchHTTPError(b *testing.B) {
	app := rice.New()
	app.GET("/bad", func(c *rice.Ctx) error { return rice.NewHTTPError(400, "bad request") })
	h := app.FasthttpHandler()

	fctx := newRequestCtx("GET", "/bad")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

func BenchmarkDispatchPlainError(b *testing.B) {
	err := errors.New("ordinary failure")

	// A quiet handler: DefaultErrorHandler logs every unrecognised error, and a
	// benchmark that logs is measuring the logger.
	app := rice.New(rice.WithErrorHandler(func(c *rice.Ctx, err error) {}))
	app.GET("/err", func(c *rice.Ctx) error { return err })
	h := app.FasthttpHandler()

	fctx := newRequestCtx("GET", "/err")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkDispatchPanic prices the whole panic path including debug.Stack().
// It is expected to be microseconds. It is recorded so that the cost of a panic
// is a number in a file rather than folklore in a code review.
func BenchmarkDispatchPanic(b *testing.B) {
	// Quiet, for the same reason as above: this path logs a full stack trace
	// through DefaultErrorHandler on every single iteration.
	app := rice.New(rice.WithErrorHandler(func(c *rice.Ctx, err error) {}))
	app.GET("/boom", func(c *rice.Ctx) error { panic("benchmark panic") })
	h := app.FasthttpHandler()

	fctx := newRequestCtx("GET", "/boom")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}
