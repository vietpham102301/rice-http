package otelrice

import "github.com/valyala/fasthttp"

// carrier adapts a fasthttp request header to OpenTelemetry's
// propagation.TextMapCarrier, so a propagator can read traceparent and
// tracestate from the request.
type carrier struct{ h *fasthttp.RequestHeader }

// Get returns the value of key, copied: a propagator may keep what it reads,
// and fasthttp reuses the header's memory once the request is done.
func (c carrier) Get(key string) string { return string(c.h.Peek(key)) }

// Set sets key. A server extracting a context does not call it; it completes
// the interface.
func (c carrier) Set(key, value string) { c.h.Set(key, value) }

// Keys lists the header names. Only propagators that scan every header call
// it; the W3C trace-context propagator does not.
func (c carrier) Keys() []string {
	keys := make([]string, 0, c.h.Len())
	for k := range c.h.All() {
		keys = append(keys, string(k))
	}
	return keys
}
