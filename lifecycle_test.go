package rice_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// serve starts app on an ephemeral port and returns its address, once Addr
// reports it, and a channel that receives Serve's result.
func serve(t *testing.T, app *rice.App) (string, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- app.Serve(ln) }()
	return waitForAddr(t, app), errCh
}

// within returns the next value from ch, or fails the test if none arrives in d.
// Every lifecycle test waits through it, so a regression fails instead of hanging.
func within[T any](t *testing.T, d time.Duration, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("%s did not happen within %v", what, d)
		var zero T
		return zero
	}
}

// TestShutdownDrainsAnInFlightRequestAndRefusesNewConnections is roadmap exit
// criterion 1.
func TestShutdownDrainsAnInFlightRequestAndRefusesNewConnections(t *testing.T) {
	release := make(chan struct{})
	inFlight := make(chan struct{})

	app := rice.New()
	app.GET("/slow", func(c *rice.Ctx) error {
		close(inFlight)
		<-release
		return c.String(200, "drained")
	})
	addr, _ := serve(t, app)

	type result struct {
		status int
		body   string
		err    error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		resCh <- result{resp.StatusCode, string(b), err}
	}()
	<-inFlight

	shutCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutCh <- app.Shutdown(ctx)
	}()

	// The listener closes as soon as Shutdown starts. Wait until a dial is refused.
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("new connections were still accepted 2s into Shutdown")
		}
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case err := <-shutCh:
		t.Fatalf("Shutdown returned %v while a request was still in flight", err)
	default:
	}

	close(release)

	res := within(t, 2*time.Second, "the in-flight response", resCh)
	if res.err != nil || res.status != 200 || res.body != "drained" {
		t.Errorf("in-flight request: status %d body %q err %v, want 200 %q nil",
			res.status, res.body, res.err, "drained")
	}
	if err := within(t, 2*time.Second, "Shutdown returning", shutCh); err != nil {
		t.Errorf("Shutdown returned %v after a clean drain, want nil", err)
	}
}

// TestServeAfterShutdownReturnsWithoutServing is spec D5: an App serves once.
func TestServeAfterShutdownReturnsWithoutServing(t *testing.T) {
	app := rice.New()
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on an App that never served returned %v, want nil", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- app.Serve(ln) }()

	if err := within(t, 2*time.Second, "Serve returning", errCh); err != nil {
		t.Errorf("Serve after Shutdown returned %v, want nil", err)
	}
	if conn, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Error("Serve after Shutdown left the listener open")
	}
}

// TestShutdownTwiceIsSafe pins that fasthttp forgets its listeners on the first
// call, so the second does not report a close error.
func TestShutdownTwiceIsSafe(t *testing.T) {
	app := rice.New()
	app.GET("/", func(c *rice.Ctx) error { return c.String(200, "up") })
	_, errCh := serve(t, app)

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown returned %v, want nil", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown returned %v, want nil", err)
	}
	if err := within(t, 2*time.Second, "Serve returning", errCh); err != nil {
		t.Errorf("Serve returned %v, want nil", err)
	}
}

// errorsIsAll reports which of targets err does not match.
func errorsIsAll(err error, targets ...error) []error {
	var missing []error
	for _, target := range targets {
		if !errors.Is(err, target) {
			missing = append(missing, target)
		}
	}
	return missing
}
