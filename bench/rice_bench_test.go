package bench

import (
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
)

// BenchmarkRiceDispatch measures the full M1 dispatch path: allocate a Ctx,
// bind it, call the handler, write a plaintext body.
//
// It is expected to report exactly one allocation per request, for the Ctx.
// That allocation is deliberate (see app.go) and is the number M6's sync.Pool
// has to remove. Compare against BenchmarkFasthttpBaseline, which is the same
// work with no framework.
func BenchmarkRiceDispatch(b *testing.B) {
	app := rice.New()
	app.SetHandler(func(c *rice.Ctx) error {
		return c.String(fasthttp.StatusOK, "hello")
	})

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/hello")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkRiceDispatchNotFound measures the path where no handler is
// registered, which skips the handler call entirely.
func BenchmarkRiceDispatchNotFound(b *testing.B) {
	app := rice.New()

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/missing")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkCtxSetHeader measures a response header write. The performance model
// records this as amortised zero; this benchmark is where that claim is checked
// rather than assumed.
func BenchmarkCtxSetHeader(b *testing.B) {
	app := rice.New()
	app.SetHandler(func(c *rice.Ctx) error {
		c.SetHeader("X-Trace", "abc123")
		return c.String(fasthttp.StatusOK, "hello")
	})

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/hdr")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}
