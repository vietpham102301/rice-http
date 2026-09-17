//go:build !ricedebug

package rice

import (
	"runtime/debug"
	"testing"

	"github.com/valyala/fasthttp"
)

// handleUnpooled is handle as it was before M6: a fresh Ctx per request. It
// exists only so the pooled path can be measured against it in one session —
// comparing against M1's recorded file compares two sessions, and between-session
// drift has misled this project before. Keep it in step with handle apart from
// acquire and release.
func (a *App) handleUnpooled(fctx *fasthttp.RequestCtx) {
	c := a.newCtx()
	c.reset(a, fctx)

	defer func() {
		if r := recover(); r != nil {
			a.callErrorHandler(c, &PanicError{Value: r, Stack: debug.Stack()})
		}
	}()

	h, ok := a.lookup(fctx.Method(), fctx.Path(), &c.params)
	if !ok {
		a.callErrorHandler(c, ErrNotFound)
		return
	}
	if err := h(c); err != nil {
		a.callErrorHandler(c, err)
	}
}

func BenchmarkDispatchPooledVsUnpooled(b *testing.B) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return c.Bytes(200, c.Param("id")) })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")

	b.Run("pooled", func(b *testing.B) {
		app.handle(fctx)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			app.handle(fctx)
		}
	})

	b.Run("unpooled", func(b *testing.B) {
		app.handleUnpooled(fctx)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			app.handleUnpooled(fctx)
		}
	})
}
