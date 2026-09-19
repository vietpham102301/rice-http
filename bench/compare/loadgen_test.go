package compare

import (
	"net"
	"strings"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

func TestHistogramQuantilesAreWithinOneSixteenth(t *testing.T) {
	var h Histogram
	for i := 1; i <= 1000; i++ {
		h.Record(time.Duration(i) * time.Microsecond)
	}
	if h.Count() != 1000 {
		t.Fatalf("Count() = %d, want 1000", h.Count())
	}
	for _, c := range []struct {
		q    float64
		want time.Duration
	}{{0.50, 500 * time.Microsecond}, {0.99, 990 * time.Microsecond}} {
		got := h.Quantile(c.q)
		if got > c.want || got < c.want-c.want/16 {
			t.Errorf("Quantile(%v) = %v, want within 1/16 below %v", c.q, got, c.want)
		}
	}
}

func TestHistogramRecordDoesNotAllocate(t *testing.T) {
	var h Histogram
	if got := testing.AllocsPerRun(1000, func() { h.Record(123 * time.Microsecond) }); got != 0 {
		t.Errorf("Record allocates %v times, want 0", got)
	}
}

func TestHistogramMerge(t *testing.T) {
	var a, b Histogram
	a.Record(time.Millisecond)
	b.Record(time.Millisecond)
	b.Record(time.Millisecond)
	a.Merge(&b)
	if a.Count() != 3 {
		t.Errorf("Count() after Merge = %d, want 3", a.Count())
	}
}

// serveRice starts rice's small app on an ephemeral port for the load tests.
func serveRice(t *testing.T, status int) string {
	t.Helper()
	r := rice.New()
	r.GET("/hello", func(c *rice.Ctx) error { return c.String(status, "hello") })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = r.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	if err := WaitReady(ln.Addr().String(), 2*time.Second); err != nil {
		t.Fatal(err)
	}
	return ln.Addr().String()
}

func TestLoadCountsRequestsAndLatencies(t *testing.T) {
	addr := serveRice(t, 200)
	s, _ := ScenarioByName("static")
	res, err := Load(LoadConfig{Addr: addr, Scenario: s, Conns: 4, Warmup: 50 * time.Millisecond, Duration: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.Requests == 0 || res.RPS <= 0 {
		t.Errorf("Requests = %d, RPS = %v, want both positive", res.Requests, res.RPS)
	}
	if res.P50 <= 0 || res.P99 < res.P50 {
		t.Errorf("P50 = %v, P99 = %v, want 0 < P50 <= P99", res.P50, res.P99)
	}
}

func TestLoadFailsOnAStatusMismatch(t *testing.T) {
	addr := serveRice(t, 500)
	s, _ := ScenarioByName("static")
	res, err := Load(LoadConfig{Addr: addr, Scenario: s, Conns: 2, Warmup: 0, Duration: 100 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("Load returned %v, want a mismatch error", err)
	}
	if res.Mismatches == 0 {
		t.Error("Mismatches = 0, want the 500s counted")
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens there now
	start := time.Now()
	if err := WaitReady(addr, 200*time.Millisecond); err == nil {
		t.Fatal("WaitReady returned nil for an address nothing listens on")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("WaitReady took %v with a 200ms timeout", elapsed)
	}
}
