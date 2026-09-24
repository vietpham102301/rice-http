package bench

import (
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
)

// BenchmarkRiceDispatch measures the full dispatch path: acquire a pooled Ctx,
// bind it, call the handler, write a plaintext body, release.
//
// It is expected to report zero allocations. Until M6 it reported one, the
// unpooled Ctx; bench/results/M1-minimal-server.txt holds that baseline. Compare
// against BenchmarkFasthttpBaseline, which is the same work with no framework.
func BenchmarkRiceDispatch(b *testing.B) {
	app := rice.New()
	app.GET("/hello", func(c *rice.Ctx) error {
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

// BenchmarkRiceDispatchNotFound measures the path where no route matches the
// request, which is rejected by the router lookup before a handler is ever
// reached.
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
	app.GET("/hdr", func(c *rice.Ctx) error {
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

// BenchmarkDispatchParameterised is the full dispatch path for a route read with
// Param — the second zero M6's exit criteria claim. Until M6 only tree-level
// parameter lookups were benchmarked.
func BenchmarkDispatchParameterised(b *testing.B) {
	app := rice.New()
	app.GET("/users/:id", func(c *rice.Ctx) error {
		return c.Bytes(fasthttp.StatusOK, c.Param("id"))
	})

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/users/42")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

type benchUser struct{ id int }

var benchUserKey = rice.NewKey[*benchUser]("user")

// BenchmarkCtxSetGet measures one Key.Set and one Key.Get with a pointer value, the
// allocation-free way to use the store.
func BenchmarkCtxSetGet(b *testing.B) {
	u := &benchUser{id: 1}
	app := rice.New()
	app.GET("/x", func(c *rice.Ctx) error {
		benchUserKey.Set(c, u)
		v, _ := benchUserKey.Get(c)
		if v != u {
			return rice.NewHTTPError(500, "store lost the value")
		}
		return nil
	})

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/x")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkDispatchParallel exercises sync.Pool's per-P caches, which a
// single-goroutine benchmark cannot show.
func BenchmarkDispatchParallel(b *testing.B) {
	app := rice.New()
	app.GET("/users/:id", func(c *rice.Ctx) error {
		return c.Bytes(fasthttp.StatusOK, c.Param("id"))
	})
	h := app.FasthttpHandler()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		fctx := newRequestCtx("GET", "/users/42")
		for pb.Next() {
			h(fctx)
		}
	})
}
