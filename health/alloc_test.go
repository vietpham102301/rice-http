//go:build !ricedebug

package health_test

import (
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/health"
)

// allocs dispatches GET /p to h through a probe App and returns the
// allocations per request after a warm-up.
func allocs(t *testing.T, h rice.Handler) float64 {
	t.Helper()
	p := rice.New()
	p.GET("/p", h)
	serve := p.FasthttpHandler()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/p")
	serve(fctx)
	return testing.AllocsPerRun(1000, func() { serve(fctx) })
}

// TestAllocBudgetLive pins Live at 0.
func TestAllocBudgetLive(t *testing.T) {
	if got := allocs(t, health.Live()); got != 0 {
		t.Errorf("Live allocated %.1f objects per probe, want exactly 0", got)
	}
}

// TestAllocBudgetReadyNoChecks pins a passing Ready with no checks at 0.
func TestAllocBudgetReadyNoChecks(t *testing.T) {
	if got := allocs(t, health.Ready(servingApp(t))); got != 0 {
		t.Errorf("Ready allocated %.1f objects per passing probe, want exactly 0", got)
	}
}

// TestAllocBudgetReadyNotReady pins the 503 of an App that is not ready.
func TestAllocBudgetReadyNotReady(t *testing.T) {
	if got := allocs(t, health.Ready(rice.New())); got != 0 {
		t.Errorf("Ready allocated %.1f objects per 503, want exactly 0", got)
	}
}
