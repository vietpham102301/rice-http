package otelrice

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// harness is an App with the middleware installed on an in-memory SDK.
type harness struct {
	app    *rice.App
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

func newHarness(opts ...rice.Option) *harness {
	h := &harness{
		app:    rice.New(opts...),
		spans:  tracetest.NewSpanRecorder(),
		reader: sdkmetric.NewManualReader(),
	}
	h.app.Use(Middleware(
		WithTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(h.spans))),
		WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(h.reader))),
		WithPropagators(propagation.TraceContext{}),
	))
	return h
}

// do dispatches one request and returns its context.
func (h *harness) do(method, uri string, headers ...string) *fasthttp.RequestCtx {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(method)
	fctx.Request.SetRequestURI(uri)
	for i := 0; i+1 < len(headers); i += 2 {
		fctx.Request.Header.Set(headers[i], headers[i+1])
	}
	h.app.FasthttpHandler()(fctx)
	return fctx
}

func (h *harness) onlySpan(t *testing.T) sdktrace.ReadOnlySpan {
	t.Helper()
	ended := h.spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("%d spans ended, want 1", len(ended))
	}
	return ended[0]
}

// duration collects the reader and returns the http.server.request.duration
// histogram, or nil when it was never recorded.
func (h *harness) duration(t *testing.T) *metricdata.Histogram[float64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "http.server.request.duration" {
				d := m.Data.(metricdata.Histogram[float64])
				return &d
			}
		}
	}
	return nil
}

func attr(kvs []attribute.KeyValue, key string) (attribute.Value, bool) {
	for _, kv := range kvs {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestSpanIsNamedAfterTheRoute(t *testing.T) {
	cases := []struct {
		method, uri  string
		wantName     string
		wantMethod   string
		wantOriginal string // "" means http.request.method_original must be absent
	}{
		{"GET", "/users/42", "GET /users/:id", "GET", ""},
		{"GET", "/nowhere", "GET", "GET", ""},
		{"BREW", "/users/42", "HTTP", "_OTHER", "BREW"},
	}
	for _, tc := range cases {
		h := newHarness()
		h.app.GET("/users/:id", func(c *rice.Ctx) error { return nil })
		h.do(tc.method, tc.uri)
		s := h.onlySpan(t)
		if got := s.Name(); got != tc.wantName {
			t.Errorf("%s %s: span name %q, want %q", tc.method, tc.uri, got, tc.wantName)
		}
		if got, _ := attr(s.Attributes(), "http.request.method"); got.Emit() != tc.wantMethod {
			t.Errorf("%s %s: http.request.method %q, want %q", tc.method, tc.uri, got.Emit(), tc.wantMethod)
		}
		got, ok := attr(s.Attributes(), "http.request.method_original")
		wantOK := tc.wantOriginal != ""
		if ok != wantOK {
			t.Errorf("%s %s: method_original present %v, want %v", tc.method, tc.uri, ok, wantOK)
		}
		if wantOK && got.Emit() != tc.wantOriginal {
			t.Errorf("%s %s: method_original %q, want %q", tc.method, tc.uri, got.Emit(), tc.wantOriginal)
		}
	}
}

func TestSpanIsNamedHTTPWhenAnUnknownMethodMatchesARoute(t *testing.T) {
	h := newHarness()
	h.app.Handle("BREW", "/coffee/:kind", func(c *rice.Ctx) error { return nil })
	h.do("BREW", "/coffee/latte")
	s := h.onlySpan(t)
	if got := s.Name(); got != "HTTP /coffee/:kind" {
		t.Errorf("span name %q, want %q", got, "HTTP /coffee/:kind")
	}
	if got, _ := attr(s.Attributes(), "http.request.method"); got.Emit() != "_OTHER" {
		t.Errorf("http.request.method %q, want _OTHER", got.Emit())
	}
	if got, ok := attr(s.Attributes(), "http.request.method_original"); !ok || got.Emit() != "BREW" {
		t.Errorf("method_original %q (present %v), want BREW", got.Emit(), ok)
	}
	if got, ok := attr(s.Attributes(), "http.route"); !ok || got.Emit() != "/coffee/:kind" {
		t.Errorf("http.route %q (present %v), want /coffee/:kind", got.Emit(), ok)
	}
}

func TestQUERYIsAKnownMethod(t *testing.T) {
	h := newHarness()
	h.app.Handle("QUERY", "/x", func(c *rice.Ctx) error { return nil })
	h.do("QUERY", "/x")
	s := h.onlySpan(t)
	if got := s.Name(); got != "QUERY /x" {
		t.Errorf("span name %q, want QUERY /x", got)
	}
	if got, _ := attr(s.Attributes(), "http.request.method"); got.Emit() != "QUERY" {
		t.Errorf("http.request.method %q, want QUERY", got.Emit())
	}
	if _, ok := attr(s.Attributes(), "http.request.method_original"); ok {
		t.Error("method_original set for a known method")
	}
}

func TestSpanAttributes(t *testing.T) {
	h := newHarness()
	h.app.GET("/users/:id", func(c *rice.Ctx) error { return c.String(200, "ok") })
	h.do("GET", "/users/42?token=secret", "User-Agent", "probe/1")
	s := h.onlySpan(t)
	if s.SpanKind() != trace.SpanKindServer {
		t.Errorf("kind %v, want server", s.SpanKind())
	}
	want := map[string]string{
		"http.request.method":       "GET",
		"http.route":                "/users/:id",
		"http.response.status_code": "200",
		"url.path":                  "/users/42",
		"url.scheme":                "http",
		"user_agent.original":       "probe/1",
		// The test harness dispatches through a bare fasthttp.RequestCtx, whose
		// RemoteAddr defaults to 0.0.0.0:0; c.ClientIP() reports its IP.
		"client.address": "0.0.0.0",
	}
	for k, v := range want {
		got, ok := attr(s.Attributes(), k)
		if !ok || got.Emit() != v {
			t.Errorf("%s = %q (present %v), want %q", k, got.Emit(), ok, v)
		}
	}
	for _, kv := range s.Attributes() {
		if strings.Contains(kv.Value.Emit(), "secret") {
			t.Errorf("attribute %s carries the query string: %q", kv.Key, kv.Value.Emit())
		}
	}
}

func TestSpanContinuesTheCallersTrace(t *testing.T) {
	h := newHarness()
	h.app.GET("/x", func(c *rice.Ctx) error { return nil })
	h.do("GET", "/x", "traceparent", parent)
	s := h.onlySpan(t)
	if got := s.SpanContext().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace %s, want the caller's", got)
	}
	if got := s.Parent().SpanID().String(); got != "00f067aa0ba902b7" {
		t.Errorf("parent %s, want the caller's span", got)
	}

	h2 := newHarness()
	h2.app.GET("/x", func(c *rice.Ctx) error { return nil })
	h2.do("GET", "/x")
	if h2.onlySpan(t).Parent().IsValid() {
		t.Error("a request without traceparent got a parent")
	}
}

func TestSpanStatusFollowsTheResponse(t *testing.T) {
	cases := []struct {
		code  int
		error bool
	}{{200, false}, {404, false}, {418, false}, {500, true}, {503, true}}
	for _, tc := range cases {
		h := newHarness()
		h.app.GET("/x", func(c *rice.Ctx) error { return rice.NewHTTPError(tc.code, "") })
		fctx := h.do("GET", "/x")
		if got := fctx.Response.StatusCode(); got != tc.code {
			t.Errorf("status %d, want %d", got, tc.code)
		}
		isErr := h.onlySpan(t).Status().Code == codes.Error
		if isErr != tc.error {
			t.Errorf("%d: span error %v, want %v", tc.code, isErr, tc.error)
		}
	}
}

func TestACustomErrorHandlersStatusIsRecorded(t *testing.T) {
	h := newHarness(rice.WithErrorHandler(func(c *rice.Ctx, err error) { _ = c.String(502, "bad gateway") }))
	h.app.GET("/x", func(c *rice.Ctx) error { return context.Canceled })
	h.do("GET", "/x")
	s := h.onlySpan(t)
	if v, _ := attr(s.Attributes(), "http.response.status_code"); v.AsInt64() != 502 {
		t.Errorf("status attribute %v, want 502", v.Emit())
	}
	if s.Status().Code != codes.Error {
		t.Error("a 502 did not mark the span as an error")
	}
}

func TestAnErrorThatProducesA500RecordsAnExceptionEvent(t *testing.T) {
	h := newHarness()
	h.app.GET("/x", func(c *rice.Ctx) error { return rice.NewHTTPError(500, "boom") })
	h.do("GET", "/x")
	s := h.onlySpan(t)
	if v, ok := attr(s.Attributes(), "error.type"); !ok || v.Emit() != "500" {
		t.Errorf("error.type %q (present %v), want 500", v.Emit(), ok)
	}
	if !hasExceptionEvent(s) {
		t.Error("no exception event recorded for a 500")
	}

	h2 := newHarness()
	h2.app.GET("/x", func(c *rice.Ctx) error { return rice.NewHTTPError(404, "missing") })
	h2.do("GET", "/x")
	s2 := h2.onlySpan(t)
	if _, ok := attr(s2.Attributes(), "error.type"); ok {
		t.Error("error.type set for a 404")
	}
	if hasExceptionEvent(s2) {
		t.Error("exception event recorded for a 404")
	}
}

func hasExceptionEvent(s sdktrace.ReadOnlySpan) bool {
	for _, e := range s.Events() {
		if e.Name == "exception" {
			return true
		}
	}
	return false
}

func TestTheHandlerAndInnerMiddlewareSeeTheSpan(t *testing.T) {
	var inHandler trace.SpanContext
	var afterChain trace.SpanContext
	h := newHarness()
	h.app.Use(func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			err := next(c)
			afterChain = trace.SpanContextFromContext(c.Context())
			return err
		}
	})
	h.app.GET("/x", func(c *rice.Ctx) error {
		inHandler = trace.SpanContextFromContext(c.Context())
		return nil
	})
	h.do("GET", "/x")
	s := h.onlySpan(t)
	if inHandler.SpanID() != s.SpanContext().SpanID() {
		t.Error("the handler's context does not carry the request span")
	}
	if !afterChain.IsValid() {
		t.Error("a middleware inside lost the span before returning")
	}
}

func TestDurationIsRecordedPerRoute(t *testing.T) {
	h := newHarness()
	h.app.GET("/users/:id", func(c *rice.Ctx) error { return nil })
	h.do("GET", "/users/1")
	h.do("GET", "/users/2")
	hist := h.duration(t)
	if hist == nil {
		t.Fatal("no http.server.request.duration")
	}
	if len(hist.DataPoints) != 1 {
		t.Fatalf("%d series, want 1: two ids of one route must share a series", len(hist.DataPoints))
	}
	dp := hist.DataPoints[0]
	if dp.Count != 2 {
		t.Errorf("count %d, want 2", dp.Count)
	}
	if v, ok := dp.Attributes.Value("http.route"); !ok || v.AsString() != "/users/:id" {
		t.Errorf("http.route %v, want /users/:id", v.Emit())
	}
	if v, ok := dp.Attributes.Value("http.request.method"); !ok || v.AsString() != "GET" {
		t.Errorf("http.request.method %v, want GET", v.Emit())
	}
	if v, ok := dp.Attributes.Value("http.response.status_code"); !ok || v.AsInt64() != 200 {
		t.Errorf("http.response.status_code %v, want 200", v.Emit())
	}
	if v, ok := dp.Attributes.Value("url.scheme"); !ok || v.AsString() != "http" {
		t.Errorf("url.scheme %v, want http", v.Emit())
	}
	if _, ok := dp.Attributes.Value("url.path"); ok {
		t.Error("url.path is a metric attribute; it is unbounded")
	}
	if !slices.Equal(dp.Bounds, durationBuckets) {
		t.Errorf("bounds %v, want %v", dp.Bounds, durationBuckets)
	}
}

func TestAPanicEndsTheSpanAsAnError(t *testing.T) {
	h := newHarness()
	h.app.GET("/x", func(c *rice.Ctx) error { panic("boom") })
	fctx := h.do("GET", "/x")
	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status %d, want 500 from rice's own recovery", got)
	}
	s := h.onlySpan(t)
	if s.Status().Code != codes.Error {
		t.Error("the span of a panicking request is not an error")
	}
	want := map[string]string{
		"http.request.method":       "GET",
		"http.route":                "/x",
		"http.response.status_code": "500",
		"error.type":                "500",
	}
	for k, v := range want {
		got, ok := attr(s.Attributes(), k)
		if !ok || got.Emit() != v {
			t.Errorf("panic span %s = %q (present %v), want %q", k, got.Emit(), ok, v)
		}
	}
	if !hasExceptionEvent(s) {
		t.Error("no exception event recorded for the panic")
	}

	hist := h.duration(t)
	if hist == nil {
		t.Fatal("no http.server.request.duration recorded for the panicking request")
	}
	if len(hist.DataPoints) != 1 {
		t.Fatalf("%d series, want 1", len(hist.DataPoints))
	}
	dp := hist.DataPoints[0]
	if dp.Count != 1 {
		t.Errorf("count %d, want 1", dp.Count)
	}
	if v, ok := dp.Attributes.Value("http.response.status_code"); !ok || v.AsInt64() != 500 {
		t.Errorf("metric http.response.status_code %v, want 500", v.Emit())
	}
}

func TestNilOptionsPanic(t *testing.T) {
	for name, f := range map[string]func(){
		"tracer":     func() { WithTracerProvider(nil) },
		"meter":      func() { WithMeterProvider(nil) },
		"propagator": func() { WithPropagators(nil) },
	} {
		func() {
			defer func() {
				if msg, _ := recover().(string); !strings.HasPrefix(msg, "rice: otelrice:") {
					t.Errorf("%s: panic %q", name, msg)
				}
			}()
			f()
		}()
	}
}

func TestThePreviousContextIsRestoredForMiddlewareOutside(t *testing.T) {
	var outside trace.SpanContext
	app := rice.New()
	app.Use(
		func(next rice.Handler) rice.Handler {
			return func(c *rice.Ctx) error {
				err := next(c)
				outside = trace.SpanContextFromContext(c.Context())
				return err
			}
		},
		Middleware(WithTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder())))),
	)
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/x")
	app.FasthttpHandler()(fctx)
	if outside.IsValid() {
		t.Error("a middleware outside still sees the request span: the previous context was not restored")
	}
}
