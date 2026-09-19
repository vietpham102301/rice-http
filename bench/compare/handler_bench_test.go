package compare

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/valyala/fasthttp"
)

// BenchmarkHandler measures each framework's own cost: routing, context and
// response code on top of an already-parsed request, through the framework's
// in-process entry point. net/http frameworks (Gin, Echo) and fasthttp ones
// (Fiber, rice) are driven through different request objects, so this level
// excludes parsing for both and compares the rest; the end-to-end level exists
// because of that.
func BenchmarkHandler(b *testing.B) {
	for _, s := range Scenarios() {
		for _, tg := range Targets() {
			b.Run(s.Name+"/"+tg.Name, func(b *testing.B) {
				if err := Check(tg, s); err != nil {
					b.Fatalf("equivalence gate: %v", err)
				}
				srv := tg.Build(s.App)
				if srv.HTTP != nil {
					benchHTTP(b, srv.HTTP, s)
				} else {
					benchFasthttp(b, srv.Fasthttp, s)
				}
			})
		}
	}
}

func benchHTTP(b *testing.B, h http.Handler, s Scenario) {
	body := &rewindBody{}
	req := httptest.NewRequest(s.Method, s.Path, nil)
	if s.Body != nil {
		req.Body = body
		req.ContentLength = int64(len(s.Body))
	}
	w := newDiscardWriter()
	serve := func() {
		if s.Body != nil {
			body.Reset(s.Body)
		}
		w.reset()
		h.ServeHTTP(w, req)
	}

	serve() // warm
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		serve()
	}
	b.StopTimer()
	if w.status != s.Status {
		b.Fatalf("last response: status %d, want %d", w.status, s.Status)
	}
}

func benchFasthttp(b *testing.B, h fasthttp.RequestHandler, s Scenario) {
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(s.Method)
	ctx.Request.SetRequestURI(s.Path)
	if s.Body != nil {
		ctx.Request.SetBody(s.Body)
	}

	h(&ctx) // warm
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(&ctx)
	}
	b.StopTimer()
	if got := ctx.Response.StatusCode(); got != s.Status {
		b.Fatalf("last response: status %d, want %d", got, s.Status)
	}
}
