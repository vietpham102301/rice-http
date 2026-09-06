package bench

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
)

// newRoutedApp registers n distinct static GET routes and returns the app
// alongside a path that is guaranteed to be registered, taken from the middle
// of the set so a lookup is not accidentally measuring a best or worst case.
func newRoutedApp(n int) (*rice.App, string) {
	app := rice.New()
	for i := 0; i < n; i++ {
		app.GET("/route/"+strconv.Itoa(i), func(c *rice.Ctx) error {
			return c.String(fasthttp.StatusOK, "ok")
		})
	}
	return app, "/route/" + strconv.Itoa(n/2)
}

// benchmarkLookup measures a full dispatch against an app with n routes.
//
// It includes the Ctx allocation and the response write, not only the lookup,
// because a socket-free dispatch is the smallest thing package bench can reach
// through the exported API. Everything except the lookup is constant across n,
// so the difference between the 10, 100 and 1000 figures is lookup scaling and
// nothing else. That difference is what M3's radix tree is judged on.
func benchmarkLookup(b *testing.B, n int) {
	app, path := newRoutedApp(n)

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", path)
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

func BenchmarkStaticRouterLookup10(b *testing.B)   { benchmarkLookup(b, 10) }
func BenchmarkStaticRouterLookup100(b *testing.B)  { benchmarkLookup(b, 100) }
func BenchmarkStaticRouterLookup1000(b *testing.B) { benchmarkLookup(b, 1000) }

// BenchmarkStaticRouterMiss measures the 404 path with 1000 routes registered.
// M1's retrospective flagged the miss path as the one hostile traffic hits
// hardest, so it gets its own number rather than being inferred.
func BenchmarkStaticRouterMiss(b *testing.B) {
	app, _ := newRoutedApp(1000)

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/not-registered")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkStaticRouterWrongVerb measures a request whose path exists under a
// different verb. In M2 this is an ordinary miss; the benchmark exists so that
// the cost is already recorded if 405 is ever added.
func BenchmarkStaticRouterWrongVerb(b *testing.B) {
	app, path := newRoutedApp(1000)

	h := app.FasthttpHandler()
	fctx := newRequestCtx("POST", path)
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}
