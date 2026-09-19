package compare

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

// LoadConfig is one closed-loop run against one server.
type LoadConfig struct {
	Addr     string
	Scenario Scenario
	// Conns keep-alive connections, each sending one request at a time.
	Conns int
	// Warmup is run and discarded before Duration is measured.
	Warmup   time.Duration
	Duration time.Duration
	// Timeout is the per-request read and write timeout. Zero means 10s.
	Timeout time.Duration
}

// LoadResult is what a run measured. Requests, RPS and the percentiles cover
// the measured window only; Mismatches and Errors cover the whole run.
type LoadResult struct {
	Requests   uint64
	Mismatches uint64
	Errors     uint64
	RPS        float64
	P50        time.Duration
	P99        time.Duration
}

type worker struct {
	hist       Histogram
	requests   uint64
	mismatches uint64
	errors     uint64
}

// Load drives cfg.Addr with cfg.Conns closed-loop connections: each sends a
// request, waits for the response, and sends the next. It returns an error if
// any response had a status other than the scenario's or failed outright, so
// that a run which measured different work is never reported as a number.
func Load(cfg LoadConfig) (LoadResult, error) {
	if cfg.Conns < 1 {
		return LoadResult{}, fmt.Errorf("conns must be at least 1, got %d", cfg.Conns)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	hc := &fasthttp.HostClient{
		Addr:     cfg.Addr,
		MaxConns: cfg.Conns,
		// MaxIdemponentCallAttempts defaults to 5 and retries GET/HEAD/PUT
		// silently on any read error, including a timeout or a closed
		// keep-alive connection. That would hide a dropped connection inside
		// latency instead of counting it as an error, and could let one
		// stuck Do take up to 5x the timeout past end. Every failure must
		// reach w.errors exactly once.
		MaxIdemponentCallAttempts: 1,
		ReadTimeout:               timeout,
		WriteTimeout:              timeout,
		MaxIdleConnDuration:       time.Minute,
	}
	s := cfg.Scenario
	measureFrom := time.Now().Add(cfg.Warmup)
	end := measureFrom.Add(cfg.Duration)

	workers := make([]worker, cfg.Conns)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(w *worker) {
			defer wg.Done()
			req := fasthttp.AcquireRequest()
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseRequest(req)
			defer fasthttp.ReleaseResponse(resp)
			req.Header.SetMethod(s.Method)
			req.SetRequestURI("http://" + cfg.Addr + s.Path)
			if s.Body != nil {
				req.SetBody(s.Body)
			}
			for {
				t0 := time.Now()
				if !t0.Before(end) {
					return
				}
				err := hc.Do(req, resp)
				t1 := time.Now()
				if err != nil {
					w.errors++
					// The run already fails; spinning until end only burns
					// the client's CPU on a connection that just failed.
					return
				}
				if resp.StatusCode() != s.Status {
					w.mismatches++
				}
				if !t0.Before(measureFrom) {
					w.hist.Record(t1.Sub(t0))
					w.requests++
				}
			}
		}(&workers[i])
	}
	wg.Wait()

	var res LoadResult
	var all Histogram
	for i := range workers {
		all.Merge(&workers[i].hist)
		res.Requests += workers[i].requests
		res.Mismatches += workers[i].mismatches
		res.Errors += workers[i].errors
	}
	if cfg.Duration > 0 {
		res.RPS = float64(res.Requests) / cfg.Duration.Seconds()
	}
	res.P50 = all.Quantile(0.50)
	res.P99 = all.Quantile(0.99)
	if res.Mismatches > 0 || res.Errors > 0 {
		return res, fmt.Errorf("%s: %d status mismatches, %d errors", s.Name, res.Mismatches, res.Errors)
	}
	return res, nil
}

// WaitReady dials addr until it accepts a connection or timeout passes.
func WaitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			return conn.Close()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not accept a connection within %v: %w", addr, timeout, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
