package rice_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// shutdownAsync starts Shutdown with ctx and returns a channel that receives
// its result.
func shutdownAsync(app *rice.App, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(ctx) }()
	return done
}

// waitNotReady polls app.Ready until it is false, or fails the test.
func waitNotReady(t *testing.T, app *rice.App) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for app.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("Ready stayed true after Shutdown began")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDrainDelayKeepsServingThenCloses(t *testing.T) {
	const delay = 300 * time.Millisecond
	app := rice.New(rice.WithDrainDelay(delay))
	app.GET("/x", func(c *rice.Ctx) error { return c.String(200, "ok") })
	addr, _ := serve(t, app)

	start := time.Now()
	done := shutdownAsync(app, context.Background())
	waitNotReady(t, app)

	// A new connection during the delay is still served, and told to close.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: false}}
	resp, err := client.Get("http://" + addr + "/x")
	if err != nil {
		t.Fatalf("GET during the drain: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status %d during the drain, want 200", resp.StatusCode)
	}
	if !resp.Close {
		t.Error("a response during the drain did not carry Connection: close")
	}

	if err := within(t, 3*time.Second, "Shutdown", done); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if d := time.Since(start); d < delay {
		t.Errorf("Shutdown returned after %v, before the %v drain delay", d, delay)
	}
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		conn.Close()
		t.Error("a connection was accepted after the drain")
	}
}

func TestDrainIsBoundedByShutdownsContext(t *testing.T) {
	app := rice.New(rice.WithDrainDelay(10 * time.Second))
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	addr, _ := serve(t, app)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	// Nothing is in flight, so the drain after the delay is clean even with
	// ctx done: the result is nil. What matters is that ctx cut the delay short.
	within(t, 2*time.Second, "Shutdown", shutdownAsync(app, ctx))
	if d := time.Since(start); d > time.Second {
		t.Errorf("Shutdown took %v; its ctx ended after 100ms", d)
	}
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		conn.Close()
		t.Error("the listener is still open after Shutdown returned")
	}
}

func TestDrainIsSkippedForAnAppThatNeverServed(t *testing.T) {
	app := rice.New(rice.WithDrainDelay(10 * time.Second))
	if err := within(t, time.Second, "Shutdown", shutdownAsync(app, context.Background())); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestConcurrentShutdownsShareOneDrain(t *testing.T) {
	const delay = time.Second
	app := rice.New(rice.WithDrainDelay(delay))
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	serve(t, app)

	start := time.Now()
	first := shutdownAsync(app, context.Background())
	time.Sleep(delay / 2)
	second := shutdownAsync(app, context.Background())
	within(t, 3*time.Second, "the first Shutdown", first)
	within(t, 3*time.Second, "the second Shutdown", second)
	if d := time.Since(start); d > delay+400*time.Millisecond {
		t.Errorf("both Shutdowns took %v; the second waited a drain of its own", d)
	}
}

func TestReadyFollowsTheLifecycle(t *testing.T) {
	release := make(chan struct{})
	app := rice.New()
	app.OnStart(func() error { <-release; return nil })
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	if app.Ready() {
		t.Fatal("Ready before Serve")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- app.Serve(ln) }()
	time.Sleep(50 * time.Millisecond)
	if app.Ready() {
		t.Error("Ready while an OnStart hook is running")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for !app.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("Ready never turned true once serving")
		}
		time.Sleep(time.Millisecond)
	}

	done := shutdownAsync(app, context.Background())
	waitNotReady(t, app)
	within(t, 3*time.Second, "Shutdown", done)
	within(t, 3*time.Second, "Serve", served)
	if app.Ready() {
		t.Error("Ready after Serve returned")
	}
}

func TestReadyIsFalseAfterAnOnStartError(t *testing.T) {
	app := rice.New()
	app.OnStart(func() error { return errors.New("no database") })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Serve(ln); err == nil {
		t.Fatal("Serve succeeded despite the OnStart error")
	}
	if app.Ready() {
		t.Error("Ready after an OnStart error")
	}
}

func TestRunContextGivesGraceAfterTheDrain(t *testing.T) {
	const delay, grace = 300 * time.Millisecond, 500 * time.Millisecond
	started := make(chan struct{})
	app := rice.New(rice.WithDrainDelay(delay))
	app.GET("/slow", func(c *rice.Ctx) error {
		close(started)
		time.Sleep(600 * time.Millisecond) // past grace alone, within delay+grace
		return c.String(200, "done")
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() { ran <- app.RunContext(ctx, addr, grace) }()
	waitForAddr(t, app)

	got := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			got <- 0
			return
		}
		resp.Body.Close()
		got <- resp.StatusCode
	}()
	<-started
	cancel()
	if code := within(t, 3*time.Second, "the slow request", got); code != 200 {
		t.Errorf("the in-flight request got %d, want 200: it had the drain plus grace to finish", code)
	}
	if err := within(t, 3*time.Second, "RunContext", ran); err != nil {
		t.Errorf("RunContext: %v", err)
	}
}

func TestADrainStopsStreamsAtOnce(t *testing.T) {
	stopped := make(chan time.Time, 1)
	app := rice.New(rice.WithDrainDelay(time.Second))
	app.GET("/s", func(c *rice.Ctx) error {
		return c.Stream(func(s *rice.Stream) error {
			_, _ = s.WriteString("open\n")
			_ = s.Flush()
			<-s.Context().Done()
			stopped <- time.Now()
			return s.Context().Err()
		})
	})
	addr, _ := serve(t, app)
	_, r := rawRequest(t, addr, "GET", "/s")
	readUntil(t, r, "open", 2*time.Second)

	start := time.Now()
	done := shutdownAsync(app, context.Background())
	if at := within(t, 2*time.Second, "the stream stopping", stopped); at.Sub(start) > 300*time.Millisecond {
		t.Errorf("the stream stopped %v after Shutdown began; streams stop at once, not after the drain", at.Sub(start))
	}
	within(t, 3*time.Second, "Shutdown", done)
}

func TestWithDrainDelayPanicsOnANegativeDuration(t *testing.T) {
	defer func() {
		if msg, _ := recover().(string); msg != "rice: WithDrainDelay: duration is negative" {
			t.Errorf("panic %q", msg)
		}
	}()
	rice.WithDrainDelay(-1)
}
