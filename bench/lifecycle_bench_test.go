package bench

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// BenchmarkShutdownLatency answers M7's question with a number: how long after
// the last request finishes does Shutdown return? fasthttp's drain polls every
// 100ms, so the expectation is 0-100ms. ns/op is meaningless here; read the
// ms/shutdown metric.
func BenchmarkShutdownLatency(b *testing.B) {
	b.Run("after-last-request", func(b *testing.B) {
		var total time.Duration
		for i := 0; i < b.N; i++ {
			release := make(chan struct{})
			inFlight := make(chan struct{})
			app := rice.New()
			app.GET("/slow", func(c *rice.Ctx) error {
				close(inFlight)
				<-release
				return c.String(200, "ok")
			})
			addr := serveForBench(b, app)

			go func() {
				resp, err := http.Get("http://" + addr + "/slow")
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
			}()
			select {
			case <-inFlight:
			case <-time.After(5 * time.Second):
				b.Fatal("request never became in-flight within 5s")
			}

			done := make(chan time.Time, 1)
			go func() {
				_ = app.Shutdown(context.Background())
				done <- time.Now()
			}()
			time.Sleep(5 * time.Millisecond) // let Shutdown enter its poll loop

			start := time.Now()
			close(release)
			select {
			case t := <-done:
				total += t.Sub(start)
			case <-time.After(5 * time.Second):
				b.Fatal("Shutdown did not return within 5s")
			}
		}
		b.ReportMetric(float64(total.Microseconds())/1000/float64(b.N), "ms/shutdown")
	})

	b.Run("idle-keepalive-only", func(b *testing.B) {
		var total time.Duration
		for i := 0; i < b.N; i++ {
			app := rice.New()
			app.GET("/", func(c *rice.Ctx) error { return c.String(200, "ok") })
			addr := serveForBench(b, app)

			conn, err := net.Dial("tcp", addr)
			if err != nil {
				b.Fatalf("dial: %v", err)
			}
			br := bufio.NewReader(conn)
			fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: rice\r\n\r\n")
			resp, err := http.ReadResponse(br, nil)
			if err != nil {
				b.Fatalf("reading the response: %v", err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			start := time.Now()
			_ = app.Shutdown(context.Background())
			total += time.Since(start)
			_ = conn.Close()
		}
		b.ReportMetric(float64(total.Microseconds())/1000/float64(b.N), "ms/shutdown")
	})
}

// serveForBench starts app on an ephemeral port and waits until it is serving.
func serveForBench(b *testing.B, app *rice.App) string {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	deadline := time.Now().Add(2 * time.Second)
	for app.Addr() == "" {
		if time.Now().After(deadline) {
			b.Fatal("server did not bind within 2s")
		}
		time.Sleep(time.Millisecond)
	}
	return app.Addr()
}
