package otelrice

import (
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
	mnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tnoop "go.opentelemetry.io/otel/trace/noop"
)

// BenchmarkMiddleware measures the middleware around a handler that returns at
// once, with no-op providers: the cost rice adds on top of an application that
// has not installed an SDK.
func BenchmarkMiddleware(b *testing.B) {
	app := rice.New()
	app.Use(Middleware(WithTracerProvider(tnoop.NewTracerProvider()), WithMeterProvider(mnoop.NewMeterProvider()), WithPropagators(propagation.TraceContext{})))
	app.GET("/users/:id", func(c *rice.Ctx) error { return nil })
	h := app.FasthttpHandler()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")
	h(fctx)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}
