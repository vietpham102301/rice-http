package compare

import (
	"bytes"
	"net"
	"strings"
	"sync"
	"sync/atomic"
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

// TestLoadFailsWhenTheServerNeverAnswers asserts, deterministically, that a
// worker which hits an error makes exactly one attempt and stops: it counts
// the connections accepted rather than timing the run, because with
// fasthttp's default retries the run still finishes inside a generous time
// bound, only slower — a timing assertion cannot tell "retried" from "slow".
func TestLoadFailsWhenTheServerNeverAnswers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var accepted atomic.Int32
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			// Accept the connection and never write a response.
		}
	}()
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})

	s, _ := ScenarioByName("static")
	const wantConns = 2
	start := time.Now()
	res, err := Load(LoadConfig{
		Addr:     ln.Addr().String(),
		Scenario: s,
		Conns:    wantConns,
		Warmup:   0,
		Duration: 300 * time.Millisecond,
		Timeout:  100 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Error("Load returned nil, want an error when the server never answers")
	}
	if res.Errors == 0 {
		t.Error("Errors = 0, want the read timeouts counted")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Load took %v, want it to return within 2s", elapsed)
	}
	// Each worker makes one attempt and stops at its first error, so exactly
	// Conns connections are accepted. With fasthttp's default retries
	// (MaxIdemponentCallAttempts = 5) each worker redials on every failed
	// attempt, so the count would climb toward Conns * 5 = 10.
	if got := accepted.Load(); got != wantConns {
		t.Errorf("accepted %d connections, want exactly %d (one attempt per worker, no retries)", got, wantConns)
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

func TestSummarizeTakesTheMedianPerFrameworkAndScenario(t *testing.T) {
	in := strings.Join([]string{
		"rice static 1 100 10 20",
		"rice static 2 300 30 60",
		"rice static 3 200 20 40",
		"gin static 1 50 5 9",
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Summarize(strings.NewReader(in), &out); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"## static",
		"| rice | 200 | 100–300 | 20 | 40 |",
		"| gin | 50 | 50–50 | 5 | 9 |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "## param") {
		t.Errorf("output has a table for a scenario with no samples:\n%s", got)
	}
}

func TestSummarizeRejectsAMalformedLine(t *testing.T) {
	if err := Summarize(strings.NewReader("rice static one 100 10 20\n"), &bytes.Buffer{}); err == nil {
		t.Error("Summarize accepted a round that is not a number")
	}
}
