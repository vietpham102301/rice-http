package middleware_test

import (
	"io"
	"log/slog"
	"testing"
	"time"

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

// TestAllocBudgetRealIPWithPort pins RealIP's cost when the chosen entry
// carries a port, as some load balancers write it. Checking the port reads the
// bytes in place, so the figure is the bare entry's: the string converted for
// net.ParseIP is the host alone, and nothing else is built.
//
// Measured at 3 on darwin arm64 (go1.25.6) and in a Linux arm64 container
// (golang:1.25.14), with and without -race, so the race slack is 0: no
// mechanism in assertAllocBudget's doc comment applies.
func TestAllocBudgetRealIPWithPort(t *testing.T) {
	const want float64 = 3

	got := measure(t, middleware.RealIP(1), map[string]string{
		"X-Forwarded-For": "198.51.100.7, 203.0.113.9:4711",
	})
	assertAllocBudget(t, "RealIP (entry with a port)", got, want, 0)
}

// TestAllocBudgetRequestIDGenerated pins RequestID's cost when it generates an
// id, the common case. Encoding the id and storing it both allocate.
//
// Under -race the ceiling is one higher, and only because of Linux. Built
// with the race detector for Linux, the compiler moves newRequestID's 16-byte
// buffer to the heap — `go build -race -gcflags=-m` reports "moved to heap:
// b" there and nothing on darwin — because crypto/rand's Linux source ends in
// a syscall.Syscall that the race build makes its buffer argument escape
// through. A memory profile under linux/-race shows exactly one extra
// allocation per call, in newRequestID itself. Without -race the buffer stays
// on the stack on every platform, so production pays 2.
func TestAllocBudgetRequestIDGenerated(t *testing.T) {
	const want float64 = 2

	got := measure(t, middleware.RequestID(), nil)
	assertAllocBudget(t, "RequestID (generating)", got, want, 1)
}

// assertAllocBudget fails when got misses the budget for name: an exact match
// to want off-race, or a ceiling of want+raceSlack under -race.
//
// raceSlack is not a tolerance to be tuned until a test goes green. Each
// budget that passes one derives it, in its own doc comment, from a specific
// allocation the race build can add, and a budget with no such mechanism
// passes 0 and stays exact under -race too. Two mechanisms exist today:
//
//   - log/slog's handler takes two buffers from one sync.Pool per record.
//     go1.25.6's log/slog/handler.go: commonHandler.handle at line 271
//     (h.newHandleState(buffer.New(), true, "")) and newHandleState itself at
//     line 402 (prefix: buffer.New()), both freed at lines 413 and 419. Under
//     -race the pool drops some Puts and GC clears it (see budget's doc comment
//     in budget_test.go, package rice), costing at most two extra per call.
//   - RequestID's random buffer escapes under -race on Linux only; see
//     TestAllocBudgetRequestIDGenerated.
//
// Check these line numbers against your own Go version if this ever needs
// re-deriving.
func assertAllocBudget(t *testing.T, name string, got, want, raceSlack float64) {
	t.Helper()
	if raceDetector {
		if got > want+raceSlack {
			t.Errorf("%s allocated %.1f objects per call, want at most %.0f under -race", name, got, want+raceSlack)
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
	assertAllocBudget(t, "Logger", got, want, 2)
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
//
// Its race slack is the two mechanisms in assertAllocBudget's doc comment
// added together: two from slog's pooled buffers, and one from RequestID's
// buffer escaping on Linux.
func TestAllocBudgetLoggerWithRequestID(t *testing.T) {
	const want float64 = 6

	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mw := func(next rice.Handler) rice.Handler { return middleware.Logger(l)(middleware.RequestID()(next)) }
	got := measure(t, mw, nil)
	assertAllocBudget(t, "Logger+RequestID", got, want, 3)
}

// TestAllocBudgetTimeout pins Timeout's cost around a handler that returns at
// once. context.WithTimeout allocates.
//
// Measured at 4 on darwin and in a Linux container, with and without -race,
// so the race slack is 0: no mechanism in assertAllocBudget's doc comment
// applies here, and none was found to add an allocation on either platform.
func TestAllocBudgetTimeout(t *testing.T) {
	const want float64 = 4

	got := measure(t, middleware.Timeout(time.Second), nil)
	assertAllocBudget(t, "Timeout", got, want, 0)
}

// measureResetting is measure for a middleware that writes response headers:
// it resets the response header before each call, as fasthttp does between
// requests on a connection, so that Vary added a thousand times is not
// counted as growth. Reset keeps the header's buffers, so after the warm call
// every Set and Add writes into memory that already exists.
func measureResetting(t *testing.T, mw rice.Middleware, method, uri string, headers map[string]string) float64 {
	t.Helper()
	wrapped := mw(func(c *rice.Ctx) error { return nil })
	var got float64
	app := rice.New()
	app.Handle(method, "/m", func(c *rice.Ctx) error {
		h := &c.RequestCtx().Response.Header
		_ = wrapped(c) // warm
		got = testing.AllocsPerRun(1000, func() {
			h.Reset()
			_ = wrapped(c)
		})
		return nil
	})
	fctx := newRequest(method, uri, headers)
	app.FasthttpHandler()(fctx)
	return got
}

// corsBudgetConfig is the fixture for the three CORS budgets: the
// configuration a JSON API behind a proxy needs, with credentials on so that
// every pair CORS can build is built.
var corsBudgetConfig = middleware.CORSConfig{
	Origins:          []string{"https://app.example.com"},
	AllowHeaders:     []string{"Authorization", "Content-Type"},
	ExposeHeaders:    []string{"X-Request-Id"},
	MaxAge:           10 * time.Minute,
	AllowCredentials: true,
}

// TestAllocBudgetCORSNoOrigin pins the branch a same-origin request takes:
// one Vary added, nothing compared.
//
// Measured at 0 on darwin arm64 go1.25.6 and in a Linux arm64 golang:1.25.14
// container, with and without -race, so the race slack is 0: no mechanism in
// assertAllocBudget's doc comment applies to Header.Add, and none was found
// to add an allocation on either platform.
func TestAllocBudgetCORSNoOrigin(t *testing.T) {
	const want float64 = 0

	got := measureResetting(t, middleware.CORS(corsBudgetConfig), "GET", "/m", nil)
	assertAllocBudget(t, "CORS (no Origin)", got, want, 0)
}

// TestAllocBudgetCORSAllowedOrigin pins a real request from an allowed
// origin: the comparison, Allow-Origin, Allow-Credentials, Expose-Headers.
//
// Measured at 0 on darwin arm64 go1.25.6 and in a Linux arm64 golang:1.25.14
// container, with and without -race, so the race slack is 0: no mechanism in
// assertAllocBudget's doc comment applies — matchOrigin's string(origin) ==
// o comparison and the Header.Set calls all write into memory the compiled
// pairs and the reset header already own — and none was found to add an
// allocation on either platform.
func TestAllocBudgetCORSAllowedOrigin(t *testing.T) {
	const want float64 = 0

	got := measureResetting(t, middleware.CORS(corsBudgetConfig), "GET", "/m", map[string]string{
		"Origin": "https://app.example.com",
	})
	assertAllocBudget(t, "CORS (allowed origin)", got, want, 0)
}

// TestAllocBudgetCORSPreflight pins a preflight from an allowed origin: the
// comparison, the four preflight pairs, and NoContent.
//
// Measured at 0 on darwin arm64 go1.25.6 and in a Linux arm64 golang:1.25.14
// container, with and without -race, so the race slack is 0: no mechanism in
// assertAllocBudget's doc comment applies — isPreflight's checks, the four
// Header.Set calls and c.NoContent all write into memory the compiled pairs
// and the reset header already own, and NoContent sets a status and writes
// no body — and none was found to add an allocation on either platform.
func TestAllocBudgetCORSPreflight(t *testing.T) {
	const want float64 = 0

	got := measureResetting(t, middleware.CORS(corsBudgetConfig), "OPTIONS", "/m", map[string]string{
		"Origin":                         "https://app.example.com",
		"Access-Control-Request-Method":  "DELETE",
		"Access-Control-Request-Headers": "authorization, content-type",
	})
	assertAllocBudget(t, "CORS (preflight)", got, want, 0)
}
