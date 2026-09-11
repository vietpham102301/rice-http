package bench

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
)

// staticPaths builds n distinct static route paths and the one to probe, taken
// from the middle of the set so a lookup is not measuring a best or worst case.
func staticPaths(n int) ([]string, string) {
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		paths[i] = "/route/" + strconv.Itoa(i)
	}
	return paths, "/route/" + strconv.Itoa(n/2)
}

// benchmarkMapLookup measures the frozen M2 map on nothing but the probe, with no
// framework around it. Paired with benchmarkTreeLookup below, which does the same
// for the tree, this is the comparison M3 exists to produce.
func benchmarkMapLookup(b *testing.B, n int) {
	paths, probe := staticPaths(n)

	var t mapTree
	for i, p := range paths {
		t.insert(p, i)
	}

	path := []byte(probe)
	if _, ok := t.lookup(path); !ok {
		b.Fatal("probe path is not registered; this would measure a miss")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.lookup(path)
	}
}

func BenchmarkMapLookup10(b *testing.B)   { benchmarkMapLookup(b, 10) }
func BenchmarkMapLookup100(b *testing.B)  { benchmarkMapLookup(b, 100) }
func BenchmarkMapLookup1000(b *testing.B) { benchmarkMapLookup(b, 1000) }

// benchmarkTreeLookup measures the radix tree on the same route set as
// benchmarkMapLookup, through rice's dispatch path, since the tree is not
// reachable from this package any other way.
//
// Do not divide a BenchmarkTreeLookup* figure by a BenchmarkMapLookup* figure.
// They do not measure comparable work: benchmarkMapLookup times a bare
// map[string]int probe with no framework around it, while this function times
// a full dispatch through FasthttpHandler — allocating a Ctx, calling the
// handler, and writing a response body — of which the lookup is only one part.
// A ratio between the two numbers is not a router comparison; it is an
// artifact of comparing a data-structure probe to an end-to-end request.
//
// The comparison these two functions support is each series against itself:
// BenchmarkMapLookup10/100/1000 show whether map cost scales with route count,
// and BenchmarkTreeLookup10/100/1000 show whether tree dispatch cost scales
// with route count. Since everything in the tree's dispatch path except the
// lookup is constant across n, that series' shape is attributable to the
// lookup even though its absolute value is not comparable to the map's.
func benchmarkTreeLookup(b *testing.B, n int) {
	paths, probe := staticPaths(n)

	app := rice.New()
	for _, p := range paths {
		app.GET(p, func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "ok") })
	}

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", probe)
	h(fctx)
	if fctx.Response.StatusCode() != fasthttp.StatusOK {
		b.Fatalf("probe returned %d; this would measure the 404 path", fctx.Response.StatusCode())
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

func BenchmarkTreeLookup10(b *testing.B)   { benchmarkTreeLookup(b, 10) }
func BenchmarkTreeLookup100(b *testing.B)  { benchmarkTreeLookup(b, 100) }
func BenchmarkTreeLookup1000(b *testing.B) { benchmarkTreeLookup(b, 1000) }

// benchmarkPattern dispatches one request against one registered pattern.
func benchmarkPattern(b *testing.B, pattern, path string) {
	app := rice.New()
	app.GET(pattern, func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "ok") })

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", path)
	h(fctx)
	if fctx.Response.StatusCode() != fasthttp.StatusOK {
		b.Fatalf("%s did not match %s; this would measure the 404 path", pattern, path)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkTreeLookup1Param measures the case the map cannot express at all.
func BenchmarkTreeLookup1Param(b *testing.B) {
	benchmarkPattern(b, "/users/:id", "/users/42")
}

func BenchmarkTreeLookup5Params(b *testing.B) {
	benchmarkPattern(b, "/a/:p1/b/:p2/c/:p3/d/:p4/e/:p5", "/a/1/b/2/c/3/d/4/e/5")
}

func BenchmarkTreeLookupWildcard(b *testing.B) {
	benchmarkPattern(b, "/files/*path", "/files/a/b/c.txt")
}

// BenchmarkTreeLookupBacktrack measures the worst case the design admits: the
// static branch matches, strands a byte, and lookup unwinds to the parameter
// child. A worst case nobody measured is a worst case nobody knows.
func BenchmarkTreeLookupBacktrack(b *testing.B) {
	app := rice.New()
	app.GET("/users/new", func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "static") })
	app.GET("/users/:id", func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "param") })

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/users/newx")
	h(fctx)
	if got := string(fctx.Response.Body()); got != "param" {
		b.Fatalf("body = %q, want param; the backtracking path is not being measured", got)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkTreeMiss measures the 404 path with 1000 routes registered.
func BenchmarkTreeMiss(b *testing.B) {
	paths, _ := staticPaths(1000)

	app := rice.New()
	for _, p := range paths {
		app.GET(p, func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "ok") })
	}

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/not-registered")
	h(fctx)
	if fctx.Response.StatusCode() != fasthttp.StatusNotFound {
		b.Fatalf("probe returned %d, want 404", fctx.Response.StatusCode())
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}
