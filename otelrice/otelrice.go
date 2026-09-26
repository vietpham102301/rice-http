// Package otelrice traces and measures rice requests with OpenTelemetry.
//
// It depends on the OpenTelemetry API only. The application installs an SDK
// and an exporter; without one, the global providers are no-ops and the
// middleware costs a few allocations and records nothing. See ADR-0020.
package otelrice

import (
	"context"
	"fmt"
	"net"
	"strconv"
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
// The span is named after the matched route — "GET /users/:id" — or the
// method alone when no route matched, so a raw path never becomes a span
// name or a metric attribute. A method outside the known list (see
// methodName) is recorded as _OTHER in http.request.method, named "HTTP" in
// the span in place of the method, and carries the real verb in
// http.request.method_original on the span only — never on the metric. The
// caller's trace context is read from the request headers, and the span's
// context is installed with c.SetContext for the rest of the chain, then the
// previous context is restored.
//
// url.scheme comes from the connection (fctx.IsTLS()); behind a
// TLS-terminating proxy the middleware still reports "http", since fasthttp
// never sees the original scheme.
//
// Install it outermost, so its span covers every other middleware and Logger
// logs inside it:
//
//	app.Use(otelrice.Middleware(), middleware.Logger(l), middleware.Recover(), middleware.RealIP(1))
//
// It settles an error the chain returns with c.HandleError and returns nil, so
// the status it records is the one the client receives. A status of 500 or
// more marks the span as an error and sets error.type to the status code; 4xx
// does neither.
//
// It records that error on the span itself only when the chain returns it to
// this middleware directly — never true in the placement above, since
// middleware.Logger installed inside settles the error with its own
// c.HandleError and returns nil, so otelrice never sees it. To get the error's
// text onto the span in that recommended placement, record it explicitly, in
// a custom ErrorHandler:
//
//	rice.WithErrorHandler(func(c *rice.Ctx, err error) {
//		trace.SpanFromContext(c.Context()).RecordError(err)
//		rice.DefaultErrorHandler(c, err)
//	})
//
// c.Context() there is still the span's context: otelrice restores the
// previous one only after the whole chain returns, and an ErrorHandler called
// through HandleError runs mid-chain, before that.
//
// The query string is never recorded.
//
// A panic reaching this middleware — with no Recover installed beneath it —
// ends the span with status 500 before the panic reaches the App's
// ErrorHandler, so a custom ErrorHandler that answers a *rice.PanicError with
// a different status still gets 500 recorded here.
//
// For c.Stream and c.SSE the span ends when the handler returns, not when the
// stream does, and the stream's context is not the span's: a callback that
// wants the request's trace must carry it explicitly. The callback runs on its
// own goroutine after the handler has returned and the Ctx has been released
// (ADR-0019 in core), so it must not touch c itself — capture the span context
// in the handler, before calling Stream, and close over that instead:
//
//	sc := trace.SpanContextFromContext(c.Context())
//	return c.Stream(func(s *rice.Stream) error {
//		ctx := trace.ContextWithSpanContext(s.Context(), sc)
//		_, span := tracer.Start(ctx, "stream.chunk")
//		defer span.End()
//		// ...
//	})
//
// See ADR-0020.
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
			verb := fctx.Method()
			method := methodName(verb)
			unknown := method == "_OTHER"
			route := c.Route()

			prev := c.Context()
			ctx := prop.Extract(prev, carrier{&fctx.Request.Header})

			nameMethod := method
			if unknown {
				nameMethod = "HTTP"
			}
			name := nameMethod
			if route != "" {
				name = nameMethod + " " + route
			}

			scheme := "http"
			if fctx.IsTLS() {
				scheme = "https"
			}

			// Everything known before the chain runs — sampler-relevant, and set
			// exactly once, at start.
			startAttrs := make([]attribute.KeyValue, 0, 6)
			startAttrs = append(startAttrs,
				semconv.HTTPRequestMethodKey.String(method),
				semconv.URLPathKey.String(string(c.Path())),
				semconv.URLSchemeKey.String(scheme),
			)
			if route != "" {
				startAttrs = append(startAttrs, semconv.HTTPRouteKey.String(route))
			}
			if unknown {
				startAttrs = append(startAttrs, semconv.HTTPRequestMethodOriginalKey.String(string(verb)))
			}
			if ua := fctx.Request.Header.UserAgent(); len(ua) > 0 {
				startAttrs = append(startAttrs, semconv.UserAgentOriginalKey.String(string(ua)))
			}

			ctx, span := tracer.Start(ctx, name,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(startAttrs...),
			)

			// A panic that reaches this frame — no Recover inside — still
			// restores the previous context and finishes the span exactly as a
			// normal response does (through finish), with status 500 and
			// error.type "500", before the panic keeps unwinding to the App's
			// own recovery and its ErrorHandler. This middleware has already
			// ended the span by then and cannot see what that ErrorHandler
			// answers: a custom one that maps a *rice.PanicError to a status
			// other than 500 still gets 500 recorded here, disagreeing with
			// what the client actually receives. The panic itself is recorded
			// as an exception, with a stack trace, before the span ends.
			defer func() {
				if r := recover(); r != nil {
					c.SetContext(prev)
					span.RecordError(fmt.Errorf("panic: %v", r), trace.WithStackTrace(true))
					finish(ctx, span, duration, time.Since(start), method, route, scheme, 500, c.ClientIP())
					panic(r)
				}
			}()

			c.SetContext(ctx)
			handlerErr := next(c)
			if handlerErr != nil {
				c.HandleError(handlerErr)
			}
			c.SetContext(prev)

			status := fctx.Response.StatusCode()
			if status >= 500 && handlerErr != nil {
				span.RecordError(handlerErr)
			}
			finish(ctx, span, duration, time.Since(start), method, route, scheme, status, c.ClientIP())
			return nil
		}
	}
}

// finish records this request's duration, sets the attributes known only
// after the chain has run, marks the span's status, and ends it. The normal
// path and the panic path both call it, so the two cannot drift: a panic
// finishes its span exactly the way a normal 500 response does.
func finish(ctx context.Context, span trace.Span, duration metric.Float64Histogram, elapsed time.Duration, method, route, scheme string, status int, ip net.IP) {
	isError := status >= 500

	metricAttrs := make([]attribute.KeyValue, 0, 5)
	metricAttrs = append(metricAttrs,
		semconv.HTTPRequestMethodKey.String(method),
		semconv.HTTPResponseStatusCodeKey.Int(status),
		semconv.URLSchemeKey.String(scheme),
	)
	if route != "" {
		metricAttrs = append(metricAttrs, semconv.HTTPRouteKey.String(route))
	}
	if isError {
		metricAttrs = append(metricAttrs, semconv.ErrorTypeKey.String(strconv.Itoa(status)))
	}
	duration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(metricAttrs...))

	if span.IsRecording() {
		span.SetAttributes(semconv.HTTPResponseStatusCodeKey.Int(status))
		if ip != nil {
			span.SetAttributes(semconv.ClientAddressKey.String(ip.String()))
		}
		if isError {
			span.SetAttributes(semconv.ErrorTypeKey.String(strconv.Itoa(status)))
		}
	}
	if isError {
		span.SetStatus(codes.Error, "")
	}
	span.End()
}

// methodName returns the method as the conventions name it: one of the known
// methods, or _OTHER, so an arbitrary verb never becomes a span name or a
// metric attribute. The list is fixed: the HTTP semantic conventions ask
// instrumentation that maps an unlisted method to _OTHER to say so, rather
// than let a caller extend it, so there is no option for that here.
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
	case "QUERY":
		return "QUERY"
	}
	return "_OTHER"
}
