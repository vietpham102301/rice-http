// Package otelrice traces and measures rice requests with OpenTelemetry.
//
// It depends on the OpenTelemetry API only. The application installs an SDK
// and an exporter; without one, the global providers are no-ops and the
// middleware costs a few allocations and records nothing. See ADR-0020.
package otelrice

import (
	"fmt"
	"time"

	rice "github.com/vietpham102301/rice-http"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

// scope names the tracer and the meter.
const scope = "github.com/vietpham102301/rice-http/otelrice"

// Option configures Middleware.
type Option func(*config)

type config struct {
	tp   trace.TracerProvider
	mp   metric.MeterProvider
	prop propagation.TextMapPropagator
}

// WithTracerProvider uses tp instead of the global tracer provider. A nil tp
// panics.
func WithTracerProvider(tp trace.TracerProvider) Option {
	if tp == nil {
		panic("rice: otelrice: WithTracerProvider: provider is nil")
	}
	return func(c *config) { c.tp = tp }
}

// WithMeterProvider uses mp instead of the global meter provider. A nil mp
// panics.
func WithMeterProvider(mp metric.MeterProvider) Option {
	if mp == nil {
		panic("rice: otelrice: WithMeterProvider: provider is nil")
	}
	return func(c *config) { c.mp = mp }
}

// WithPropagators uses p instead of the global propagator to read the caller's
// trace context from the request headers. A nil p panics.
func WithPropagators(p propagation.TextMapPropagator) Option {
	if p == nil {
		panic("rice: otelrice: WithPropagators: propagator is nil")
	}
	return func(c *config) { c.prop = p }
}

// durationBuckets are the HTTP semantic conventions' advised boundaries for
// http.server.request.duration, in seconds.
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

// Middleware starts a server span for each request and records its duration,
// following OpenTelemetry's HTTP semantic conventions.
//
// The span is named after the matched route — "GET /users/:id" — or the method
// alone when no route matched, so a raw path never becomes a span name or a
// metric attribute. The caller's trace context is read from the request
// headers, and the span's context is installed with c.SetContext for the rest
// of the chain, then the previous context is restored.
//
// Install it outermost, so its span covers every other middleware and Logger
// logs inside it:
//
//	app.Use(otelrice.Middleware(), middleware.Logger(l), middleware.Recover(), middleware.RealIP(1))
//
// It settles an error the chain returns with c.HandleError and returns nil, so
// the status it records is the one the client receives. A status of 500 or
// more marks the span as an error; 4xx does not. The query string is never
// recorded.
//
// For c.Stream and c.SSE the span ends when the handler returns, not when the
// stream does, and the stream's context is not the span's. See ADR-0020.
func Middleware(opts ...Option) rice.Middleware {
	cfg := config{
		tp:   otel.GetTracerProvider(),
		mp:   otel.GetMeterProvider(),
		prop: otel.GetTextMapPropagator(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	tracer := cfg.tp.Tracer(scope)
	duration, err := cfg.mp.Meter(scope).Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of HTTP server requests."),
		metric.WithExplicitBucketBoundaries(durationBuckets...),
	)
	if err != nil {
		panic("rice: otelrice: creating the duration histogram: " + err.Error())
	}
	prop := cfg.prop

	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			start := time.Now()
			fctx := c.RequestCtx()
			method := methodName(fctx.Method())
			route := c.Route()

			prev := c.Context()
			ctx := prop.Extract(prev, carrier{&fctx.Request.Header})
			name := method
			if route != "" {
				name = method + " " + route
			}
			ctx, span := tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindServer))

			// A panic that reaches this frame — no Recover inside — still ends
			// the span, marked as an error, and keeps unwinding.
			defer func() {
				if r := recover(); r != nil {
					span.SetStatus(codes.Error, "panic")
					span.AddEvent("panic", trace.WithAttributes(attribute.String("panic.value", fmt.Sprint(r))))
					span.End()
					panic(r)
				}
			}()

			c.SetContext(ctx)
			if err := next(c); err != nil {
				c.HandleError(err)
			}
			c.SetContext(prev)

			status := fctx.Response.StatusCode()
			scheme := "http"
			if fctx.IsTLS() {
				scheme = "https"
			}

			attrs := make([]attribute.KeyValue, 0, 8)
			attrs = append(attrs,
				semconv.HTTPRequestMethodKey.String(method),
				semconv.HTTPResponseStatusCodeKey.Int(status),
				semconv.URLSchemeKey.String(scheme),
			)
			if route != "" {
				attrs = append(attrs, semconv.HTTPRouteKey.String(route))
			}
			duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))

			if span.IsRecording() {
				span.SetAttributes(attrs...)
				span.SetAttributes(semconv.URLPathKey.String(string(c.Path())))
				if ip := c.ClientIP(); ip != nil {
					span.SetAttributes(semconv.ClientAddressKey.String(ip.String()))
				}
				if ua := fctx.Request.Header.UserAgent(); len(ua) > 0 {
					span.SetAttributes(semconv.UserAgentOriginalKey.String(string(ua)))
				}
			}
			if status >= 500 {
				span.SetStatus(codes.Error, "")
			}
			span.End()
			return nil
		}
	}
}

// methodName returns the method as the conventions name it: one of the known
// methods, or _OTHER, so an arbitrary verb never becomes a span name or a
// metric attribute.
func methodName(m []byte) string {
	switch string(m) {
	case "GET":
		return "GET"
	case "HEAD":
		return "HEAD"
	case "POST":
		return "POST"
	case "PUT":
		return "PUT"
	case "PATCH":
		return "PATCH"
	case "DELETE":
		return "DELETE"
	case "OPTIONS":
		return "OPTIONS"
	case "CONNECT":
		return "CONNECT"
	case "TRACE":
		return "TRACE"
	}
	return "_OTHER"
}
