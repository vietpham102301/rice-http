package bench

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
)

// benchSink counts middleware invocations. It is a package-level variable so the
// compiler cannot discard the increments and optimise an entire chain away, which
// would make these benchmarks measure a bare handler while claiming otherwise.
var benchSink int

// passthroughMW does the least work a middleware can do while still being
// impossible to elide: one increment of a package-level counter, then next.
func passthroughMW() rice.Middleware {
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			benchSink++
			return next(c)
		}
	}
}

// chainApp builds an app with n application-level middleware on one static route,
// and returns its handler plus a warmed request context.
func chainApp(b *testing.B, n int) (fasthttp.RequestHandler, *fasthttp.RequestCtx) {
	b.Helper()

	app := rice.New()
	for i := 0; i < n; i++ {
		app.Use(passthroughMW())
	}
	app.GET("/x", func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "ok") })

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/x")

	benchSink = 0
	h(fctx)

	if fctx.Response.StatusCode() != fasthttp.StatusOK {
		b.Fatalf("probe returned %d; this would measure the 404 path", fctx.Response.StatusCode())
	}
	if benchSink != n {
		b.Fatalf("%d middleware ran, want %d; this benchmark would measure a shorter chain than it claims", benchSink, n)
	}
	return h, fctx
}

func benchmarkChainDispatch(b *testing.B, n int) {
	h, fctx := chainApp(b, n)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkChainDispatch0 is the control: the same dispatch with no middleware.
// Every other figure in this file is read against it.
func BenchmarkChainDispatch0(b *testing.B) { benchmarkChainDispatch(b, 0) }

func BenchmarkChainDispatch1(b *testing.B) { benchmarkChainDispatch(b, 1) }

// BenchmarkChainDispatch5 is the roadmap's stated case. Against
// BenchmarkChainDispatch0 it answers the milestone's question: whether compiling
// at build time removes the per-request cost, or whether the closure indirection
// eats the gain.
func BenchmarkChainDispatch5(b *testing.B) { benchmarkChainDispatch(b, 5) }

// BenchmarkChainDispatch5Grouped runs the same five middleware, arriving through
// three nested groups instead of five Use calls. Groups exist only at registration
// time, so this should be indistinguishable from BenchmarkChainDispatch5 — and if
// it is not, groups are costing something at request time that they should not.
func BenchmarkChainDispatch5Grouped(b *testing.B) {
	app := rice.New()
	app.Use(passthroughMW())

	outer := app.Group("/a", passthroughMW())
	inner := outer.Group("/b", passthroughMW(), passthroughMW())
	inner.GET("/c", func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "ok") }, passthroughMW())

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/a/b/c")

	benchSink = 0
	h(fctx)

	if fctx.Response.StatusCode() != fasthttp.StatusOK {
		b.Fatalf("probe returned %d; this would measure the 404 path", fctx.Response.StatusCode())
	}
	if benchSink != 5 {
		b.Fatalf("%d middleware ran, want 5", benchSink)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkBuild1000Routes measures the build phase itself. The design inserts
// every route twice on purpose — once at registration to reject bad configuration
// where it was written, once in build with the compiled chain — so the startup
// cost of that choice should be a number rather than a shrug.
func BenchmarkBuild1000Routes(b *testing.B) {
	paths := make([]string, 1000)
	for i := range paths {
		paths[i] = "/route/" + strconv.Itoa(i)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		app := rice.New()
		app.Use(passthroughMW(), passthroughMW(), passthroughMW())
		for _, p := range paths {
			app.GET(p, func(c *rice.Ctx) error { return nil })
		}
		b.StartTimer()

		app.Build()
	}
}
