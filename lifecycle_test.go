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
