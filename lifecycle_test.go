package rice_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	within(t, 2*time.Second, "the handler receiving the request", inFlight)

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
func TestRunContextServesUntilTheContextIsCancelled(t *testing.T) {
	app := rice.New()
	app.GET("/", func(c *rice.Ctx) error { return c.String(200, "up") })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- app.RunContext(ctx, "127.0.0.1:0", time.Second) }()

	addr := waitForAddr(t, app)
	if status, body := get(t, addr, "/"); status != 200 || body != "up" {
		t.Fatalf("got %d %q, want 200 %q", status, body, "up")
	}

	cancel()
	if err := within(t, 2*time.Second, "RunContext returning", errCh); err != nil {
		t.Errorf("RunContext returned %v, want nil", err)
	}
}

func TestRunContextLetsAnInFlightRequestFinishWithinGrace(t *testing.T) {
	release := make(chan struct{})
	inFlight := make(chan struct{})
	app := rice.New()
	app.GET("/slow", func(c *rice.Ctx) error {
		close(inFlight)
		<-release
		return c.String(200, "finished")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- app.RunContext(ctx, "127.0.0.1:0", 5*time.Second) }()
	addr := waitForAddr(t, app)

	bodyCh := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			bodyCh <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		bodyCh <- string(b)
	}()
	within(t, 2*time.Second, "the handler receiving the request", inFlight)

	cancel()
	time.Sleep(50 * time.Millisecond) // RunContext is now draining
	close(release)

	if body := within(t, 2*time.Second, "the in-flight response", bodyCh); body != "finished" {
		t.Errorf("in-flight body = %q, want %q", body, "finished")
	}
	if err := within(t, 2*time.Second, "RunContext returning", errCh); err != nil {
		t.Errorf("RunContext returned %v, want nil", err)
	}
}

func TestRunContextWithZeroGraceForceClosesAtOnce(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	inFlight := make(chan struct{})
	app := rice.New()
	app.GET("/slow", func(c *rice.Ctx) error {
		close(inFlight)
		<-release
		return c.String(200, "never seen")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- app.RunContext(ctx, "127.0.0.1:0", 0) }()
	addr := waitForAddr(t, app)

	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	within(t, 2*time.Second, "the handler receiving the request", inFlight)

	cancel()
	err := within(t, time.Second, "RunContext returning", errCh)
	if missing := errorsIsAll(err, rice.ErrShutdownTimeout, context.DeadlineExceeded); len(missing) > 0 {
		t.Errorf("RunContext returned %v, which does not match %v", err, missing)
	}
}

func TestRunContextReturnsABindErrorAtOnce(t *testing.T) {
	app := rice.New()
	errCh := make(chan error, 1)
	// Port 1 requires privileges this test does not have.
	go func() { errCh <- app.RunContext(context.Background(), "127.0.0.1:1", time.Second) }()

	if err := within(t, 2*time.Second, "RunContext returning", errCh); err == nil {
		t.Error("RunContext returned nil for an unbindable address, want an error")
	}
}

func TestRunContextReturnsAnOnStartErrorAtOnce(t *testing.T) {
	errBoom := errors.New("boom")
	app := rice.New()
	app.OnStart(func() error { return errBoom })

	errCh := make(chan error, 1)
	go func() { errCh <- app.RunContext(context.Background(), "127.0.0.1:0", time.Second) }()

	if err := within(t, 2*time.Second, "RunContext returning", errCh); !errors.Is(err, errBoom) {
		t.Errorf("RunContext returned %v, want the OnStart error", err)
	}
}

// TestRunContextWithACancelledContextReturns is finding 4 of the M7 design:
// Shutdown can run before fasthttp's Serve has recorded the listener. The
// window is narrow, so the test goes through it many times.
func TestRunContextWithACancelledContextReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for i := 0; i < 50; i++ {
		app := rice.New()
		errCh := make(chan error, 1)
		go func() { errCh <- app.RunContext(ctx, "127.0.0.1:0", time.Second) }()

		if err := within(t, 2*time.Second, fmt.Sprintf("RunContext returning (run %d)", i), errCh); err != nil {
			t.Fatalf("run %d: RunContext returned %v, want nil", i, err)
		}
	}
}

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

// TestNothingIsServedAfterATimedOutShutdown pins finding 2 of the M7 design:
// fasthttp's ShutdownWithContext resets its stop flag when it times out, so a
// busy keep-alive connection goes on serving new requests after Shutdown has
// returned. Rice closes it instead.
func TestNothingIsServedAfterATimedOutShutdown(t *testing.T) {
	release := make(chan struct{})
	inFlight := make(chan struct{})

	app := rice.New()
	app.GET("/ok", func(c *rice.Ctx) error { return c.String(200, "ok") })
	app.GET("/slow", func(c *rice.Ctx) error {
		close(inFlight)
		<-release
		return c.String(200, "slow")
	})
	app.GET("/third", func(c *rice.Ctx) error { return c.String(200, "third") })
	addr, _ := serve(t, app)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)

	// Request 1 completes, so the connection is a live keep-alive connection.
	fmt.Fprint(conn, "GET /ok HTTP/1.1\r\nHost: rice\r\n\r\n")
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("reading the first response: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// Request 2 blocks in its handler past the deadline.
	fmt.Fprint(conn, "GET /slow HTTP/1.1\r\nHost: rice\r\n\r\n")
	within(t, 2*time.Second, "the handler receiving the request", inFlight)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := app.Shutdown(ctx); !errors.Is(err, rice.ErrShutdownTimeout) {
		t.Fatalf("Shutdown returned %v, want ErrShutdownTimeout", err)
	}

	close(release)

	// Request 3 on the same connection must not be answered, and the connection
	// must be closed rather than merely silent. The write may fail: that is fine.
	_, _ = fmt.Fprint(conn, "GET /third HTTP/1.1\r\nHost: rice\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	rest, err := io.ReadAll(br)

	if strings.Contains(string(rest), "third") {
		t.Errorf("the connection served a request after Shutdown returned: %q", rest)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Error("the connection was still open 2s after a timed-out Shutdown")
	}
}

// TestShutdownClosesIdleKeepAliveConnections: an idle connection does not hold
// the drain open, and is closed by it.
func TestShutdownClosesIdleKeepAliveConnections(t *testing.T) {
	app := rice.New()
	app.GET("/ok", func(c *rice.Ctx) error { return c.String(200, "ok") })
	addr, _ := serve(t, app)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)

	fmt.Fprint(conn, "GET /ok HTTP/1.1\r\nHost: rice\r\n\r\n")
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Shutdown took %v with only an idle connection open", elapsed)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Error("the idle connection is still open after Shutdown")
	}
}

// TestForceCloseUnderConcurrentLoad runs Shutdown with a deadline too short to
// drain while clients keep connecting. It asserts nothing about who got served;
// it exists for the race detector and to prove Shutdown returns.
func TestForceCloseUnderConcurrentLoad(t *testing.T) {
	app := rice.New()
	app.GET("/work", func(c *rice.Ctx) error {
		time.Sleep(20 * time.Millisecond)
		return c.String(200, "w")
	})
	addr, errCh := serve(t, app)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 2 * time.Second}
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, err := client.Get("http://" + addr + "/work")
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
			}
		}()
	}

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	shutCh := make(chan error, 1)
	go func() { shutCh <- app.Shutdown(ctx) }()

	err := within(t, 2*time.Second, "Shutdown returning", shutCh)
	close(stop)
	wg.Wait()

	if err != nil && !errors.Is(err, rice.ErrShutdownTimeout) {
		t.Errorf("Shutdown returned %v, want nil or ErrShutdownTimeout", err)
	}
	if err := within(t, 2*time.Second, "Serve returning", errCh); err != nil {
		t.Errorf("Serve returned %v, want nil", err)
	}
}

// recorder collects hook and handler events in order, from any goroutine.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func equalEvents(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOnStartHooksRunInOrderBeforeTheFirstRequest(t *testing.T) {
	var rec recorder
	app := rice.New()
	app.OnStart(func() error { rec.add("start 1"); return nil })
	app.OnStart(func() error { rec.add("start 2"); return nil })
	app.GET("/", func(c *rice.Ctx) error { rec.add("request"); return c.String(200, "up") })

	addr, _ := serve(t, app)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	get(t, addr, "/")

	if got, want := rec.get(), []string{"start 1", "start 2", "request"}; !equalEvents(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestAnOnStartErrorStopsServeAndFreesThePort(t *testing.T) {
	errBoom := errors.New("boom")
	var laterRan atomic.Bool

	app := rice.New()
	app.OnStart(func() error { return errBoom })
	app.OnStart(func() error { laterRan.Store(true); return nil })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	errCh := make(chan error, 1)
	go func() { errCh <- app.Serve(ln) }()

	if err := within(t, 2*time.Second, "Serve returning", errCh); !errors.Is(err, errBoom) {
		t.Errorf("Serve returned %v, want the OnStart error", err)
	}
	if laterRan.Load() {
		t.Error("an OnStart hook ran after an earlier one failed")
	}
	if app.Addr() != "" {
		t.Errorf("Addr() = %q after a failed start, want empty", app.Addr())
	}

	again, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the port is still held after a failed start: %v", err)
	}
	_ = again.Close()
}

func TestOnShutdownHooksRunInReverseAfterTheDrain(t *testing.T) {
	type ctxKey struct{}
	var rec recorder
	var handlerDone atomic.Bool
	release := make(chan struct{})
	inFlight := make(chan struct{})

	app := rice.New()
	app.GET("/slow", func(c *rice.Ctx) error {
		close(inFlight)
		<-release
		handlerDone.Store(true)
		return c.String(200, "done")
	})
	for _, name := range []string{"hook 1", "hook 2", "hook 3"} {
		app.OnShutdown(func(ctx context.Context) error {
			if !handlerDone.Load() {
				rec.add(name + " before the drain")
			}
			if ctx.Value(ctxKey{}) != "shutdown ctx" {
				rec.add(name + " got a different ctx")
			}
			rec.add(name)
			return nil
		})
	}
	addr, _ := serve(t, app)

	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	within(t, 2*time.Second, "the handler receiving the request", inFlight)

	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), ctxKey{}, "shutdown ctx"), 5*time.Second)
	defer cancel()
	shutCh := make(chan error, 1)
	go func() { shutCh <- app.Shutdown(ctx) }()

	time.Sleep(50 * time.Millisecond) // Shutdown is now draining
	close(release)

	if err := within(t, 2*time.Second, "Shutdown returning", shutCh); err != nil {
		t.Fatalf("Shutdown returned %v, want nil", err)
	}
	if got, want := rec.get(), []string{"hook 3", "hook 2", "hook 1"}; !equalEvents(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestEveryOnShutdownHookRunsWhenOneFails(t *testing.T) {
	errA := errors.New("hook a")
	errB := errors.New("hook b")
	var rec recorder

	app := rice.New()
	app.OnShutdown(func(context.Context) error { rec.add("a"); return errA })
	app.OnShutdown(func(context.Context) error { rec.add("ok"); return nil })
	app.OnShutdown(func(context.Context) error { rec.add("b"); return errB })

	err := app.Shutdown(context.Background())

	if got, want := rec.get(), []string{"b", "ok", "a"}; !equalEvents(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if missing := errorsIsAll(err, errA, errB); len(missing) > 0 {
		t.Errorf("Shutdown returned %v, which does not match %v", err, missing)
	}
}

func TestOnShutdownErrorsJoinADrainTimeout(t *testing.T) {
	errHook := errors.New("hook")
	release := make(chan struct{})
	inFlight := make(chan struct{})

	app := rice.New()
	app.GET("/slow", func(c *rice.Ctx) error {
		close(inFlight)
		<-release
		return c.String(200, "late")
	})
	hookRan := make(chan struct{})
	app.OnShutdown(func(context.Context) error { close(hookRan); return errHook })
	addr, _ := serve(t, app)

	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	within(t, 2*time.Second, "the handler receiving the request", inFlight)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := app.Shutdown(ctx)
	close(release)

	within(t, time.Second, "the OnShutdown hook running", hookRan)
	if missing := errorsIsAll(err, rice.ErrShutdownTimeout, context.DeadlineExceeded, errHook); len(missing) > 0 {
		t.Errorf("Shutdown returned %v, which does not match %v", err, missing)
	}
}

func TestShutdownRunsHooksOnce(t *testing.T) {
	var calls atomic.Int32
	app := rice.New()
	app.OnShutdown(func(context.Context) error { calls.Add(1); return nil })

	_ = app.Shutdown(context.Background())
	_ = app.Shutdown(context.Background())

	if n := calls.Load(); n != 1 {
		t.Errorf("OnShutdown hook ran %d times over two Shutdowns, want 1", n)
	}
}

func TestServeAfterShutdownRunsNoOnStartHooks(t *testing.T) {
	var ran atomic.Bool
	app := rice.New()
	app.OnStart(func() error { ran.Store(true); return nil })
	_ = app.Shutdown(context.Background())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- app.Serve(ln) }()

	within(t, 2*time.Second, "Serve returning", errCh)
	if ran.Load() {
		t.Error("an OnStart hook ran on an App that was already shut down")
	}
}

// TestShutdownDuringOnStartStopsServe is D5 step 4: Shutdown lands while a start
// hook is running; Serve must not begin serving when the hook returns.
func TestShutdownDuringOnStartStopsServe(t *testing.T) {
	entered := make(chan struct{})
	proceed := make(chan struct{})
	app := rice.New()
	app.OnStart(func() error {
		close(entered)
		<-proceed
		return nil
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- app.Serve(ln) }()

	within(t, 2*time.Second, "the OnStart hook running", entered)
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned %v, want nil", err)
	}
	close(proceed)

	if err := within(t, 2*time.Second, "Serve returning", errCh); err != nil {
		t.Errorf("Serve returned %v, want nil", err)
	}
	if app.Addr() != "" {
		t.Errorf("Addr() = %q, want empty: Serve published a listener after Shutdown", app.Addr())
	}
}

// TestReadTimeoutDisconnectsAStalledClient sends half a request line and stops.
func TestReadTimeoutDisconnectsAStalledClient(t *testing.T) {
	app := rice.New(rice.WithReadTimeout(100 * time.Millisecond))
	app.GET("/", func(c *rice.Ctx) error { return c.String(200, "up") })
	addr, _ := serve(t, app)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET / HTTP/1.1\r\n")

	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = io.ReadAll(conn)

	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("a stalled client was still connected after 2s with a 100ms ReadTimeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the stalled client was disconnected after %v, want about 100ms", elapsed)
	}
}

// TestIdleTimeoutClosesAnIdleKeepAliveConnection completes one request and then
// leaves the connection idle.
func TestIdleTimeoutClosesAnIdleKeepAliveConnection(t *testing.T) {
	app := rice.New(rice.WithIdleTimeout(100 * time.Millisecond))
	app.GET("/", func(c *rice.Ctx) error { return c.String(200, "up") })
	addr, _ := serve(t, app)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)

	fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: rice\r\n\r\n")
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = br.ReadByte()

	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("an idle keep-alive connection was still open after 2s with a 100ms IdleTimeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the idle connection was closed after %v, want about 100ms", elapsed)
	}
}

// slowApp returns an App whose /slow handler signals inFlight and then blocks
// until release is called. release is idempotent and also runs at cleanup, so
// a failing test never leaves the handler blocked.
func slowApp(t *testing.T) (app *rice.App, inFlight <-chan struct{}, release func()) {
	t.Helper()
	entered := make(chan struct{})
	gate := make(chan struct{})
	app = rice.New()
	app.GET("/slow", func(c *rice.Ctx) error {
		close(entered)
		<-gate
		return c.String(200, "slow")
	})
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	return app, entered, release
}

// sendSlow opens a connection to addr and sends GET /slow on it.
func sendSlow(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	fmt.Fprint(conn, "GET /slow HTTP/1.1\r\nHost: rice\r\n\r\n")
	return conn
}

// waitUntilRefused polls until a dial to addr is refused: the listener has
// closed, so a Shutdown is inside its drain.
func waitUntilRefused(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("new connections were still accepted 2s into Shutdown")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertClosed fails unless the server has closed conn: a read must end, not
// time out.
func assertClosed(t *testing.T, conn net.Conn, what string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := io.ReadAll(conn)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Errorf("%s: the connection was still open 2s later", what)
	}
}

// TestAShutdownWaitingForAnotherHonoursItsOwnContext is the first case of the
// M7 final review's Important 1. A Shutdown called while another is draining
// must return when its own ctx ends, having force-closed the connections, not
// wait out the other's drain.
func TestAShutdownWaitingForAnotherHonoursItsOwnContext(t *testing.T) {
	app, inFlight, release := slowApp(t)
	addr, errCh := serve(t, app)

	conn := sendSlow(t, addr)
	within(t, 2*time.Second, "the handler receiving the request", inFlight)

	firstCh := make(chan error, 1)
	go func() { firstCh <- app.Shutdown(context.Background()) }()
	waitUntilRefused(t, addr)

	secondCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		secondCh <- app.Shutdown(ctx)
	}()

	err := within(t, 2*time.Second, "the second Shutdown returning at its own deadline", secondCh)
	if missing := errorsIsAll(err, rice.ErrShutdownTimeout, context.DeadlineExceeded); len(missing) > 0 {
		t.Errorf("second Shutdown returned %v, which does not match %v", err, missing)
	}
	assertClosed(t, conn, "after the second Shutdown timed out")

	select {
	case err := <-firstCh:
		t.Fatalf("first Shutdown returned %v while its handler was still running", err)
	default:
	}

	release()
	if err := within(t, 2*time.Second, "the first Shutdown returning", firstCh); err != nil {
		t.Errorf("first Shutdown returned %v once the handler finished, want nil", err)
	}
	if err := within(t, 2*time.Second, "Serve returning", errCh); err != nil {
		t.Errorf("Serve returned %v, want nil", err)
	}
}

// TestRunContextWaitsForAShutdownCalledElsewhere: Serve returns as soon as the
// listener closes, but a RunContext returning then would let main exit
// mid-drain, before the OnShutdown hooks ran.
func TestRunContextWaitsForAShutdownCalledElsewhere(t *testing.T) {
	app, inFlight, release := slowApp(t)
	var hookRan atomic.Bool
	app.OnShutdown(func(context.Context) error { hookRan.Store(true); return nil })

	runCh := make(chan error, 1)
	go func() { runCh <- app.RunContext(context.Background(), "127.0.0.1:0", time.Second) }()
	addr := waitForAddr(t, app)

	sendSlow(t, addr)
	within(t, 2*time.Second, "the handler receiving the request", inFlight)

	shutCh := make(chan error, 1)
	go func() { shutCh <- app.Shutdown(context.Background()) }()
	waitUntilRefused(t, addr)

	select {
	case err := <-runCh:
		t.Fatalf("RunContext returned %v while a Shutdown called elsewhere was still draining", err)
	case <-time.After(300 * time.Millisecond):
	}

	release()
	if err := within(t, 2*time.Second, "Shutdown returning", shutCh); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
	if err := within(t, 2*time.Second, "RunContext returning", runCh); err != nil {
		t.Errorf("RunContext returned %v, want nil", err)
	}
	if !hookRan.Load() {
		t.Error("RunContext returned before the OnShutdown hook ran")
	}
}

// TestRunContextCutsAShutdownCalledElsewhereShortAtGrace: while RunContext
// waits for a Shutdown called elsewhere, its own ctx ending still means what it
// always does: shut down, giving in-flight requests grace and no more.
func TestRunContextCutsAShutdownCalledElsewhereShortAtGrace(t *testing.T) {
	app, inFlight, release := slowApp(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCh := make(chan error, 1)
	go func() { runCh <- app.RunContext(ctx, "127.0.0.1:0", 100*time.Millisecond) }()
	addr := waitForAddr(t, app)

	conn := sendSlow(t, addr)
	within(t, 2*time.Second, "the handler receiving the request", inFlight)

	shutCh := make(chan error, 1)
	go func() { shutCh <- app.Shutdown(context.Background()) }()
	waitUntilRefused(t, addr)

	cancel()
	err := within(t, 2*time.Second, "RunContext returning at grace", runCh)
	if missing := errorsIsAll(err, rice.ErrShutdownTimeout, context.DeadlineExceeded); len(missing) > 0 {
		t.Errorf("RunContext returned %v, which does not match %v", err, missing)
	}
	assertClosed(t, conn, "after RunContext's grace ran out")

	release()
	if err := within(t, 2*time.Second, "Shutdown returning", shutCh); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}

// TestAFirstShutdownWithAnEndedContextStillRunsTheHooks: a Shutdown whose ctx
// has already ended, with no other Shutdown in progress, is the one that does
// the work: its turn must win over its ctx. Twenty runs make a coin-flip
// select fail with probability 1 - 2^-20.
func TestAFirstShutdownWithAnEndedContextStillRunsTheHooks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for i := 0; i < 20; i++ {
		var ran atomic.Bool
		app := rice.New()
		app.OnShutdown(func(context.Context) error { ran.Store(true); return nil })

		if err := app.Shutdown(ctx); err != nil {
			t.Fatalf("run %d: Shutdown on an App that never served returned %v, want nil", i, err)
		}
		if !ran.Load() {
			t.Fatalf("run %d: the OnShutdown hook did not run", i)
		}
	}
}

// TestRunContextWaitsForAnotherShutdownsHooksAfterGrace is the M7 follow-up's
// second item. Another goroutine's Shutdown is running a slow OnShutdown hook
// when RunContext's ctx ends. RunContext's own Shutdown gives up waiting at
// grace, but RunContext itself must not return while the hooks are still
// running: main would exit mid-teardown.
func TestRunContextWaitsForAnotherShutdownsHooksAfterGrace(t *testing.T) {
	hookStarted := make(chan struct{})
	releaseHook := make(chan struct{})
	var hookDone atomic.Bool

	app := rice.New()
	app.OnShutdown(func(context.Context) error {
		close(hookStarted)
		<-releaseHook
		hookDone.Store(true)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCh := make(chan error, 1)
	go func() { runCh <- app.RunContext(ctx, "127.0.0.1:0", 50*time.Millisecond) }()
	waitForAddr(t, app)

	go func() { _ = app.Shutdown(context.Background()) }()
	within(t, 2*time.Second, "the other Shutdown's hook starting", hookStarted)

	cancel()
	select {
	case err := <-runCh:
		t.Fatalf("RunContext returned %v while another Shutdown's OnShutdown hook was still running", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(releaseHook)
	// RunContext's own Shutdown lost the turn and gave up at grace.
	err := within(t, 2*time.Second, "RunContext returning", runCh)
	if missing := errorsIsAll(err, rice.ErrShutdownTimeout, context.DeadlineExceeded); len(missing) > 0 {
		t.Errorf("RunContext returned %v, which does not match %v", err, missing)
	}
	if !hookDone.Load() {
		t.Error("RunContext returned before the OnShutdown hook finished")
	}
}

// TestASecondConcurrentServeIsRejected is the M7 follow-up's third item: an
// App serves once, so a Serve while another is running returns
// ErrAlreadyServing and closes the listener it was handed.
func TestASecondConcurrentServeIsRejected(t *testing.T) {
	entered := make(chan struct{})
	proceed := make(chan struct{})
	app := rice.New()
	app.OnStart(func() error {
		close(entered)
		<-proceed
		return nil
	})

	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	firstCh := make(chan error, 1)
	go func() { firstCh <- app.Serve(first) }()
	within(t, 2*time.Second, "the first Serve's OnStart hook running", entered)

	second, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	secondCh := make(chan error, 1)
	go func() { secondCh <- app.Serve(second) }()
	if err := within(t, 2*time.Second, "the second Serve returning", secondCh); !errors.Is(err, rice.ErrAlreadyServing) {
		t.Errorf("second Serve returned %v, want ErrAlreadyServing", err)
	}
	if conn, err := net.DialTimeout("tcp", second.Addr().String(), 200*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Error("the rejected Serve left its listener open")
	}

	close(proceed)
	waitForAddr(t, app)
	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
	if err := within(t, 2*time.Second, "the first Serve returning", firstCh); err != nil {
		t.Errorf("first Serve returned %v, want nil", err)
	}
}

// TestServeCanBeRetriedAfterAFailedStart: ErrAlreadyServing guards a Serve
// that is running, not one that already returned. A Serve whose OnStart hook
// failed can be called again.
func TestServeCanBeRetriedAfterAFailedStart(t *testing.T) {
	var attempts atomic.Int32
	app := rice.New()
	app.OnStart(func() error {
		if attempts.Add(1) == 1 {
			return errors.New("not ready yet")
		}
		return nil
	})
	app.GET("/", func(c *rice.Ctx) error { return c.String(200, "up") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := app.Serve(ln); err == nil {
		t.Fatal("first Serve returned nil, want the OnStart error")
	}

	addr, errCh := serve(t, app)
	if status, body := get(t, addr, "/"); status != 200 || body != "up" {
		t.Errorf("after a retried Serve: got %d %q, want 200 %q", status, body, "up")
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
	if err := within(t, 2*time.Second, "Serve returning", errCh); err != nil {
		t.Errorf("retried Serve returned %v, want nil", err)
	}
}

// TestShutdownFromAHookWaitsOnlyUntilItsOwnCtxEnds pins the OnShutdown doc:
// a Shutdown called from inside a hook cannot take the turn its caller holds,
// so it returns ErrShutdownTimeout when its own ctx ends. With a ctx that
// never ends it would block forever, which is why the doc forbids that.
func TestShutdownFromAHookWaitsOnlyUntilItsOwnCtxEnds(t *testing.T) {
	inner := make(chan error, 1)
	app := rice.New()
	app.OnShutdown(func(context.Context) error {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		inner <- app.Shutdown(ctx)
		return nil
	})

	outer := make(chan error, 1)
	go func() { outer <- app.Shutdown(context.Background()) }()

	if err := within(t, 2*time.Second, "the hook's Shutdown returning", inner); !errors.Is(err, rice.ErrShutdownTimeout) {
		t.Errorf("Shutdown inside a hook returned %v, want ErrShutdownTimeout", err)
	}
	if err := within(t, 2*time.Second, "the outer Shutdown returning", outer); err != nil {
		t.Errorf("outer Shutdown returned %v, want nil", err)
	}
}

// TestRunContextWaitsForRunningHooksWhenStartFails: a Shutdown called while an
// OnStart hook runs does not wait for it, so its OnShutdown hooks can be running
// when that OnStart hook then fails. RunContext returns the OnStart error, but
// not while those hooks still run.
func TestRunContextWaitsForRunningHooksWhenStartFails(t *testing.T) {
	startEntered := make(chan struct{})
	failStart := make(chan struct{})
	hookStarted := make(chan struct{})
	releaseHook := make(chan struct{})
	var hookDone atomic.Bool
	errStart := errors.New("start failed")

	app := rice.New()
	app.OnStart(func() error {
		close(startEntered)
		<-failStart
		return errStart
	})
	app.OnShutdown(func(context.Context) error {
		close(hookStarted)
		<-releaseHook
		hookDone.Store(true)
		return nil
	})

	runCh := make(chan error, 1)
	go func() { runCh <- app.RunContext(context.Background(), "127.0.0.1:0", time.Second) }()
	within(t, 2*time.Second, "the OnStart hook running", startEntered)

	go func() { _ = app.Shutdown(context.Background()) }()
	within(t, 2*time.Second, "the OnShutdown hook starting", hookStarted)

	close(failStart)
	select {
	case err := <-runCh:
		t.Fatalf("RunContext returned %v while an OnShutdown hook was still running", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(releaseHook)
	if err := within(t, 2*time.Second, "RunContext returning", runCh); !errors.Is(err, errStart) {
		t.Errorf("RunContext returned %v, want the OnStart error", err)
	}
	if !hookDone.Load() {
		t.Error("RunContext returned before the OnShutdown hook finished")
	}
}

// ctxApp returns an App whose /ctx handler publishes the context it was given,
// then blocks until release is called or that context is done, and answers with
// why it stopped. release is idempotent and also runs at cleanup.
func ctxApp(t *testing.T) (app *rice.App, got <-chan context.Context, stopped <-chan string, release func()) {
	t.Helper()
	ctxCh := make(chan context.Context, 1)
	stoppedCh := make(chan string, 1)
	gate := make(chan struct{})

	app = rice.New()
	app.GET("/ctx", func(c *rice.Ctx) error {
		reqCtx := c.Context()
		ctxCh <- reqCtx
		select {
		case <-gate:
			stoppedCh <- "released"
		case <-reqCtx.Done():
			stoppedCh <- "cancelled"
		}
		return c.String(200, "done")
	})
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	return app, ctxCh, stoppedCh, release
}

// sendCtx opens a connection to addr and sends GET /ctx on it.
func sendCtx(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	fmt.Fprint(conn, "GET /ctx HTTP/1.1\r\nHost: rice\r\n\r\n")
	return conn
}

// TestACleanShutdownDoesNotCancelAnInFlightContext pins the decision not to
// cancel when Shutdown begins. fasthttp closes its own server-wide channel
// there, which is why rice does not hand that channel to handlers: cutting off
// a request that the grace period was about to let finish is precisely what
// M7's drain exists to prevent.
func TestACleanShutdownDoesNotCancelAnInFlightContext(t *testing.T) {
	app, got, stopped, release := ctxApp(t)
	addr, _ := serve(t, app)
	sendCtx(t, addr)

	reqCtx := within(t, 2*time.Second, "the handler starting", got)

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- app.Shutdown(ctx)
	}()

	// The drain is under way and the handler is still blocked: its context must
	// still be live.
	time.Sleep(100 * time.Millisecond)
	if err := reqCtx.Err(); err != nil {
		t.Errorf("Err() = %v during a clean drain, want nil", err)
	}

	release()
	if why := within(t, 2*time.Second, "the handler finishing", stopped); why != "released" {
		t.Errorf("the handler stopped because %q, want %q", why, "released")
	}
	if err := within(t, 3*time.Second, "Shutdown returning", done); err != nil {
		t.Errorf("Shutdown() = %v, want nil", err)
	}
}

// TestATimedOutDrainCancelsAnInFlightContext is the signal this feature exists
// for. OnShutdown's documentation already says a handler cut off by a timed-out
// drain may still be running; until now it had no way to find out.
func TestATimedOutDrainCancelsAnInFlightContext(t *testing.T) {
	app, got, stopped, _ := ctxApp(t)
	addr, _ := serve(t, app)
	sendCtx(t, addr)

	reqCtx := within(t, 2*time.Second, "the handler starting", got)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := app.Shutdown(ctx); !errors.Is(err, rice.ErrShutdownTimeout) {
		t.Fatalf("Shutdown() = %v, want an error wrapping ErrShutdownTimeout", err)
	}

	if why := within(t, 2*time.Second, "the handler noticing", stopped); why != "cancelled" {
		t.Errorf("the handler stopped because %q, want %q", why, "cancelled")
	}
	if err := reqCtx.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("Err() = %v, want context.Canceled", err)
	}
}

// TestASecondShutdownDoesNotPanicOnTheCancel pins that cancelling twice is
// safe: context.CancelFunc is idempotent, and Shutdown may be called more than
// once.
func TestASecondShutdownDoesNotPanicOnTheCancel(t *testing.T) {
	app, _, _, _ := ctxApp(t)
	addr, _ := serve(t, app)
	_ = addr

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = app.Shutdown(ctx)
	_ = app.Shutdown(ctx)
}

// TestAServeRetriedAfterAFailedStartKeepsALiveContext pins that the OnStart
// error path never force-closes, so the base context outlives a failed start
// and the App can still be served.
func TestAServeRetriedAfterAFailedStartKeepsALiveContext(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)

	app := rice.New()
	app.OnStart(func() error {
		if fail.Load() {
			return errors.New("not today")
		}
		return nil
	})
	live := make(chan error, 1)
	app.GET("/ctx", func(c *rice.Ctx) error {
		live <- c.Context().Err()
		return c.String(200, "ok")
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := app.Serve(ln); err == nil {
		t.Fatal("Serve() = nil after a failing OnStart hook, want the hook's error")
	}

	fail.Store(false)
	addr, _ := serve(t, app)
	resp, err := http.Get("http://" + addr + "/ctx")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if err := within(t, 2*time.Second, "the handler running", live); err != nil {
		t.Errorf("Context().Err() = %v on a retried Serve, want nil", err)
	}
}
