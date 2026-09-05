package bench

import "github.com/valyala/fasthttp"

// newRequestCtx builds a reusable request context for handler-level benchmarks.
// It is created once outside the benchmark loop so that request construction
// does not pollute the allocation count of the code under test.
func newRequestCtx(method, uri string) *fasthttp.RequestCtx {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(method)
	fctx.Request.SetRequestURI(uri)
	return fctx
}
