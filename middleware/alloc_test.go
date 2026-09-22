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

// TestAllocBudgetLogger pins Logger's cost with slog's JSON handler writing to
// io.Discard. A different handler costs differently; the figure is for this one.
//
// want=3 is the exact figure, measured without -race, and off-race this stays
// an exact equality in both directions. Under -race it is a ceiling of want+1
// instead: the race detector drops some sync.Pool Puts and clears pools on GC,
// and slog's JSON handler takes exactly one pooled buffer per record, so a
// cleared pool costs at most one extra allocation per call. See budget's
// doc comment in budget_test.go (package rice) for the same sync.Pool
// behaviour under -race; this does not restate it.
func TestAllocBudgetLogger(t *testing.T) {
	const want float64 = 3

	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	got := measure(t, middleware.Logger(l), nil)
	if raceDetector {
		if got > want+1 {
			t.Errorf("Logger allocated %.1f objects per call, want at most %.0f under -race", got, want+1)
		}
		return
	}
	if got != want {
		t.Errorf("Logger allocated %.1f objects per call, want exactly %.0f", got, want)
	}
}
