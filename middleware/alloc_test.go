package middleware_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// measure reports what one call of mw costs, wrapped around a handler that does
// nothing. It runs inside a handler because a *rice.Ctx is valid only there,
// and warms once first so one-time setup is not counted as steady state.
func measure(t *testing.T, mw rice.Middleware, headers map[string]string) float64 {
	t.Helper()
	wrapped := mw(func(c *rice.Ctx) error { return nil })
	var got float64
	app := rice.New()
	app.GET("/m", func(c *rice.Ctx) error {
		_ = wrapped(c) // warm
		got = testing.AllocsPerRun(1000, func() { _ = wrapped(c) })
		return nil
	})
	fctx := newRequest("GET", "/m", headers)
	app.FasthttpHandler()(fctx)
	return got
}

// newRequest builds a request to dispatch through FasthttpHandler.
func newRequest(method, uri string, headers map[string]string) *fasthttp.RequestCtx {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(method)
	fctx.Request.SetRequestURI(uri)
	for k, v := range headers {
		fctx.Request.Header.Set(k, v)
	}
	return fctx
}

// TestAllocBudgetRealIP pins RealIP's cost for one trusted hop and a
// two-entry header. Parsing the address allocates.
func TestAllocBudgetRealIP(t *testing.T) {
	const want float64 = 3

	got := measure(t, middleware.RealIP(1), map[string]string{
		"X-Forwarded-For": "198.51.100.7, 203.0.113.9",
	})
	if got != want {
		t.Errorf("RealIP allocated %.1f objects per call, want exactly %.0f", got, want)
	}
}

// TestAllocBudgetRequestIDGenerated pins RequestID's cost when it generates an
// id, the common case. Encoding the id and storing it both allocate.
func TestAllocBudgetRequestIDGenerated(t *testing.T) {
	const want float64 = 2

	got := measure(t, middleware.RequestID(), nil)
	if got != want {
		t.Errorf("RequestID (generating) allocated %.1f objects per call, want exactly %.0f", got, want)
	}
}

// assertAllocBudget fails when got misses the budget for name: an exact match
// to want off-race, or a ceiling of want+2 under -race.
//
// The +2 is not a guess: go1.25.6's log/slog/handler.go takes two buffers from
// one sync.Pool per record — commonHandler.handle at line 271
// (h.newHandleState(buffer.New(), true, "")) and newHandleState itself at line
// 402 (prefix: buffer.New()) — and frees both back to that pool in
// handleState.free, at lines 413 and 419. Under -race the pool drops some Puts
// and gets cleared by GC (see budget's doc comment in budget_test.go, package
// rice, for that general sync.Pool behaviour), so a cleared pool can cost at
// most two extra allocations per call, not one. Check these line numbers
// against your own Go version if this ever needs re-deriving.
func assertAllocBudget(t *testing.T, name string, got, want float64) {
	t.Helper()
	if raceDetector {
		if got > want+2 {
			t.Errorf("%s allocated %.1f objects per call, want at most %.0f under -race", name, got, want+2)
		}
		return
	}
	if got != want {
		t.Errorf("%s allocated %.1f objects per call, want exactly %.0f", name, got, want)
	}
}

// TestAllocBudgetLogger pins Logger's cost with slog's JSON handler writing to
// io.Discard, installed without RequestID. A different handler costs
// differently; the figure is for this one.
//
// This measures the five-attribute path only: method, path, status, latency
// and ip. log/slog/record.go's nAttrsInline is 5 — a Record holds that many
// attributes inline and spills any more into a heap slice — and with no
// RequestID installed, RequestIDFrom returns "" and Logger builds only five,
// so this figure is not what Logger costs in the configuration its own doc
// comment recommends. See TestAllocBudgetLoggerWithRequestID for that: the
// sixth attribute, request_id, is exactly what spills.
func TestAllocBudgetLogger(t *testing.T) {
	const want float64 = 3

	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	got := measure(t, middleware.Logger(l), nil)
	assertAllocBudget(t, "Logger", got, want)
}

// TestAllocBudgetLoggerWithRequestID pins the cost of Logger and RequestID
// together — the configuration Logger's own doc comment recommends — with
// slog's JSON handler writing to io.Discard.
//
// The sixth attribute, request_id, spills log/slog's five-attribute inline
// storage (see TestAllocBudgetLogger's doc comment) into a heap slice, so this
// figure is not TestAllocBudgetLogger's plus a fixed increment for the id;
// it is measured directly. It is measured combined, rather than isolating
// Logger's six-attribute cost from RequestID's own cost, because RequestID's
// store key is unexported: nothing outside middleware can install a fake id
// for Logger to read without running RequestID itself. This combined figure
// is also the one a user actually pays, since the two are meant to be
// installed together.
func TestAllocBudgetLoggerWithRequestID(t *testing.T) {
	const want float64 = 6

	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mw := func(next rice.Handler) rice.Handler { return middleware.Logger(l)(middleware.RequestID()(next)) }
	got := measure(t, mw, nil)
	assertAllocBudget(t, "Logger+RequestID", got, want)
}
