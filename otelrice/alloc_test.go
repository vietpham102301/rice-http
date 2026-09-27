//go:build !ricedebug

package otelrice

import (
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
	mnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tnoop "go.opentelemetry.io/otel/trace/noop"
)

// This file is excluded from the ricedebug build, which allocates a fresh Ctx
// per request on purpose (see rice's own alloc_test.go): a poisoned Ctx is
// never returned to the pool, so the figures measured here do not hold under
// that tag.

// allocsPerRequest dispatches one warm-up request through mw, then reports the
// mean allocations of 1000 further requests to the same route.
func allocsPerRequest(mw rice.Middleware, headers ...string) float64 {
	app := rice.New()
	app.Use(mw)
	app.GET("/users/:id", func(c *rice.Ctx) error { return nil })
	h := app.FasthttpHandler()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")
	for i := 0; i+1 < len(headers); i += 2 {
		fctx.Request.Header.Set(headers[i], headers[i+1])
	}
	h(fctx)
	return testing.AllocsPerRun(1000, func() { h(fctx) })
}

// TestAllocBudgetNoop measures a GET to a matched route through Middleware
// configured with no-op tracer and meter providers and no traceparent header:
// the cost rice adds on top of an application that has not installed an SDK.
// Measured on darwin/arm64, three runs each with and without -race: 11.00 in
// every run.
func TestAllocBudgetNoop(t *testing.T) {
	const want float64 = 11
	mw := Middleware(WithTracerProvider(tnoop.NewTracerProvider()), WithMeterProvider(mnoop.NewMeterProvider()), WithPropagators(propagation.TraceContext{}))
	if got := allocsPerRequest(mw); got > want {
		t.Errorf("noop middleware allocated %.1f per request, budget is %.0f", got, want)
	}
}

// TestAllocBudgetNoopTraceparent measures the same request as
// TestAllocBudgetNoop, but with a traceparent header, so the propagator's
// Extract path is exercised. Measured on darwin/arm64, three runs each with
// and without -race: 13.00 in every run.
func TestAllocBudgetNoopTraceparent(t *testing.T) {
	const want float64 = 13
	mw := Middleware(WithTracerProvider(tnoop.NewTracerProvider()), WithMeterProvider(mnoop.NewMeterProvider()), WithPropagators(propagation.TraceContext{}))
	if got := allocsPerRequest(mw, "traceparent", parent); got > want {
		t.Errorf("noop middleware with traceparent allocated %.1f per request, budget is %.0f", got, want)
	}
}

// TestAllocBudgetSDK measures the same request as TestAllocBudgetNoop, but
// with a real SDK tracer provider (backed by an in-memory span recorder) and
// meter provider installed, so the exporter path is exercised. Measured on
// darwin/arm64, three runs each with and without -race: 19.00 without -race,
// 21.00 with -race, stable within each mode across all three runs.
func TestAllocBudgetSDK(t *testing.T) {
	const want float64 = 21
	mw := Middleware(
		WithTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()))),
		WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))),
		WithPropagators(propagation.TraceContext{}))
	if got := allocsPerRequest(mw); got > want {
		t.Errorf("SDK middleware allocated %.1f per request, budget is %.0f", got, want)
	}
}
