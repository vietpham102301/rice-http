package bench

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// BenchmarkFasthttpBaseline measures a bare fasthttp handler writing a
// plaintext response, with no framework in the path. This is the zero point.
// Any allocation rice reports above this number is rice's own.
func BenchmarkFasthttpBaseline(b *testing.B) {
	h := func(fctx *fasthttp.RequestCtx) {
		fctx.SetStatusCode(fasthttp.StatusOK)
		fctx.SetContentType("text/plain; charset=utf-8")
		fctx.SetBodyString("hello")
	}

	fctx := newRequestCtx("GET", "/hello")
	h(fctx) // warm the response buffers, as a live server would be warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}
