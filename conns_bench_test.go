package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// BenchmarkDispatchConnStateHook measures what D2's hook adds to a request:
// fasthttp calls ConnState with StateActive before the handler and StateIdle
// after. Both arms run in one session, so the delta is not between-session drift.
func BenchmarkDispatchConnStateHook(b *testing.B) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return c.Bytes(200, c.Param("id")) })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")

	b.Run("dispatch", func(b *testing.B) {
		app.handle(fctx)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			app.handle(fctx)
		}
	})

	b.Run("dispatch+connstate", func(b *testing.B) {
		app.handle(fctx)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			app.connState(nil, fasthttp.StateActive)
			app.handle(fctx)
			app.connState(nil, fasthttp.StateIdle)
		}
	})
}
