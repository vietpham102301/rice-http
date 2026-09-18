package rice

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

	"github.com/valyala/fasthttp"
)

// TestConnStateActiveAndIdleAreFree pins the per-request cost of D2's hook.
// fasthttp calls ConnState twice per request; those calls must neither
// allocate nor take the lock.
func TestConnStateActiveAndIdleAreFree(t *testing.T) {
	a := New()

	if got := testing.AllocsPerRun(1000, func() {
		a.connState(nil, fasthttp.StateActive)
		a.connState(nil, fasthttp.StateIdle)
	}); got != 0 {
		t.Errorf("connState(Active, Idle) allocates %v times, want 0", got)
	}

	a.connMu.Lock()
	defer a.connMu.Unlock()
	done := make(chan struct{})
	go func() {
		a.connState(nil, fasthttp.StateActive)
		a.connState(nil, fasthttp.StateIdle)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connState(Active, Idle) blocked on connMu")
	}
}

func TestConnStateTracksOpenConnections(t *testing.T) {
	a := New()
	c1, peer1 := net.Pipe()
	c2, peer2 := net.Pipe()
	c3, peer3 := net.Pipe()
	defer peer1.Close()
	defer peer2.Close()
	defer peer3.Close()

	a.connState(c1, fasthttp.StateNew)
	a.connState(c2, fasthttp.StateNew)
	a.connState(c3, fasthttp.StateNew)
	if len(a.conns) != 3 {
		t.Fatalf("tracking %d connections after three StateNew, want 3", len(a.conns))
	}

	a.connState(c1, fasthttp.StateClosed)
	a.connState(c2, fasthttp.StateHijacked)
	if len(a.conns) != 1 {
		t.Fatalf("tracking %d connections after a close and a hijack, want 1", len(a.conns))
	}
	if _, ok := a.conns[c3]; !ok {
		t.Fatal("the remaining tracked connection is not c3")
	}
}

func TestCloseConnsClosesTrackedAndLateConnections(t *testing.T) {
	a := New()
	tracked, trackedPeer := net.Pipe()
	defer trackedPeer.Close()
	a.connState(tracked, fasthttp.StateNew)

	a.closeConns()

	// A bounded deadline turns "closeConns didn't close it" into a fast,
	// visible failure instead of a hang: net.Pipe supports deadlines, and a
	// regression here (e.g. dropping the close loop) must fail the suite, not
	// stall it.
	_ = trackedPeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := trackedPeer.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("reading the peer of a force-closed connection: %v, want io.EOF", err)
	}

	// A connection accepted before the listener closed but reported after the
	// sweep is closed on arrival and never tracked.
	late, latePeer := net.Pipe()
	defer latePeer.Close()
	a.connState(late, fasthttp.StateNew)

	// Checked before the read so a regression that tracks (rather than closes)
	// a late connection fails on this named assertion first, rather than
	// blocking in the read below.
	if _, ok := a.conns[late]; ok {
		t.Error("a connection reported after the sweep was tracked")
	}

	_ = latePeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := latePeer.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("reading the peer of a late connection: %v, want io.EOF", err)
	}
}

// TestConnectionsAreUntrackedAfterACleanDrain: nothing leaks through the map.
// fasthttp reports StateClosed after it decrements its open count, so
// ShutdownWithContext can return a moment before the last removal; poll.
func TestConnectionsAreUntrackedAfterACleanDrain(t *testing.T) {
	a := New()
	a.GET("/", func(c *Ctx) error { return c.String(200, "up") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = a.Serve(ln) }()

	for i := 0; i < 5; i++ {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned %v, want nil", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		a.connMu.Lock()
		n := len(a.conns)
		a.connMu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections still tracked 1s after a clean drain", n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRunContextPanicsOnNegativeGrace(t *testing.T) {
	v := mustPanic(t, "RunContext(-1)", func() {
		_ = New().RunContext(context.Background(), "127.0.0.1:0", -1)
	})
	if s, ok := v.(string); !ok || !strings.HasPrefix(s, "rice: ") {
		t.Errorf("panicked with %v, want a string starting %q", v, "rice: ")
	}
}

func TestHookRegistrationPanics(t *testing.T) {
	cases := []struct {
		name string
		fn   func(a *App)
	}{
		{"OnStart(nil)", func(a *App) { a.OnStart(nil) }},
		{"OnShutdown(nil)", func(a *App) { a.OnShutdown(nil) }},
		{"OnStart after Build", func(a *App) { a.Build(); a.OnStart(func() error { return nil }) }},
		{"OnShutdown after Build", func(a *App) {
			a.Build()
			a.OnShutdown(func(context.Context) error { return nil })
		}},
	}
	for _, tc := range cases {
		v := mustPanic(t, tc.name, func() { tc.fn(New()) })
		if s, ok := v.(string); !ok || !strings.HasPrefix(s, "rice: ") {
			t.Errorf("%s panicked with %v, want a string starting %q", tc.name, v, "rice: ")
		}
	}
}

// TestTimeoutOptionsReachTheServer checks each option sets its fasthttp field.
// WriteTimeout is only checked here: an end-to-end slow reader is flaky on
// loopback, where socket buffers absorb the whole response.
func TestTimeoutOptionsReachTheServer(t *testing.T) {
	a := New(
		WithReadTimeout(1*time.Second),
		WithWriteTimeout(2*time.Second),
		WithIdleTimeout(3*time.Second),
	)
	if a.srv.ReadTimeout != 1*time.Second {
		t.Errorf("ReadTimeout = %v, want 1s", a.srv.ReadTimeout)
	}
	if a.srv.WriteTimeout != 2*time.Second {
		t.Errorf("WriteTimeout = %v, want 2s", a.srv.WriteTimeout)
	}
	if a.srv.IdleTimeout != 3*time.Second {
		t.Errorf("IdleTimeout = %v, want 3s", a.srv.IdleTimeout)
	}

	if d := New(); d.srv.ReadTimeout != 0 || d.srv.WriteTimeout != 0 || d.srv.IdleTimeout != 0 {
		t.Error("an App without timeout options has a non-zero timeout; the default must stay unlimited")
	}
}

func TestTimeoutOptionsPanicOnNegativeDurations(t *testing.T) {
	cases := map[string]func(){
		"WithReadTimeout(-1)":  func() { WithReadTimeout(-1) },
		"WithWriteTimeout(-1)": func() { WithWriteTimeout(-1) },
		"WithIdleTimeout(-1)":  func() { WithIdleTimeout(-1) },
	}
	for name, fn := range cases {
		v := mustPanic(t, name, fn)
		if s, ok := v.(string); !ok || !strings.HasPrefix(s, "rice: ") {
			t.Errorf("%s panicked with %v, want a string starting %q", name, v, "rice: ")
		}
	}
}

// recv returns the next value from ch, or fails the test if none arrives in d.
func recv[T any](t *testing.T, d time.Duration, what string, ch <-chan T) T {
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

// gate is a pause point a test opens: entered is closed when something reaches
// it, and that something waits there until the test calls open, or 5s pass.
type gate struct {
	once     sync.Once
	entered  chan struct{}
	proceed  chan struct{}
	openOnce func()
}

func newGate(t *testing.T) *gate {
	g := &gate{entered: make(chan struct{}), proceed: make(chan struct{})}
	g.openOnce = sync.OnceFunc(func() { close(g.proceed) })
	t.Cleanup(g.openOnce)
	return g
}

// pause blocks the first caller at the gate; later callers pass straight through.
func (g *gate) pause() {
	g.once.Do(func() {
		close(g.entered)
		select {
		case <-g.proceed:
		case <-time.After(5 * time.Second):
		}
	})
}

func (g *gate) open() { g.openOnce() }

// slowCloseConn is a connection whose first Close blocks at a gate, as a
// tls.Conn's can while it sends close_notify to a peer that is not reading.
type slowCloseConn struct {
	net.Conn
	g *gate
}

func (c *slowCloseConn) Close() error {
	c.g.pause()
	return c.Conn.Close()
}

func newSlowCloseConn(t *testing.T) *slowCloseConn {
	c, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = c.Close() })
	return &slowCloseConn{Conn: c, g: newGate(t)}
}

// TestCloseConnsDoesNotHoldTheLockWhileClosing: a slow Close must not stall
// connState, which every connection's open and close goes through.
func TestCloseConnsDoesNotHoldTheLockWhileClosing(t *testing.T) {
	a := New()
	slow := newSlowCloseConn(t)
	a.connState(slow, fasthttp.StateNew)

	go a.closeConns()
	recv(t, 2*time.Second, "closeConns reaching the slow Close", slow.g.entered)

	other, otherPeer := net.Pipe()
	defer otherPeer.Close()
	done := make(chan struct{})
	go func() {
		a.connState(other, fasthttp.StateNew) // closed on arrival: forceClosed is set
		a.connState(other, fasthttp.StateClosed)
		close(done)
	}()
	recv(t, time.Second, "connState returning while closeConns closes a slow connection", done)
	slow.g.open()
}

// slowApp returns a built App whose /slow handler signals inFlight and blocks
// until release is called; release also runs at cleanup.
func slowApp(t *testing.T) (a *App, inFlight <-chan struct{}, release func()) {
	entered := make(chan struct{})
	unblock := make(chan struct{})
	a = New()
	a.GET("/slow", func(c *Ctx) error {
		close(entered)
		<-unblock
		return c.String(200, "slow")
	})
	a.GET("/ok", func(c *Ctx) error { return c.String(200, "ok") })
	release = sync.OnceFunc(func() { close(unblock) })
	t.Cleanup(release)
	return a, entered, release
}

// TestAConcurrentShutdownReturnsOnlyAfterTheForceClose is the second case of
// the M7 final review's Important 1. A second Shutdown called while the first
// is force-closing must not return, and must not run the OnShutdown hooks,
// before that force-close is done: its caller would tear down resources that
// connections still being closed are using.
func TestAConcurrentShutdownReturnsOnlyAfterTheForceClose(t *testing.T) {
	a, inFlight, _ := slowApp(t)
	var hookRuns atomic.Int32
	a.OnShutdown(func(context.Context) error { hookRuns.Add(1); return nil })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveCh := make(chan error, 1)
	go func() { serveCh <- a.Serve(ln) }()

	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		if resp, err := client.Get("http://" + ln.Addr().String() + "/slow"); err == nil {
			_ = resp.Body.Close()
		}
	}()
	recv(t, 2*time.Second, "the handler receiving the request", inFlight)

	// A tracked connection whose Close blocks holds the first Shutdown inside
	// its force-close for as long as the test wants.
	slow := newSlowCloseConn(t)
	a.connState(slow, fasthttp.StateNew)

	firstCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		firstCh <- a.Shutdown(ctx)
	}()
	recv(t, 2*time.Second, "the first Shutdown reaching its force-close", slow.g.entered)

	secondCh := make(chan error, 1)
	go func() { secondCh <- a.Shutdown(context.Background()) }()
	select {
	case err := <-secondCh:
		t.Fatalf("second Shutdown returned %v (hooks run %d times) while the first was still force-closing",
			err, hookRuns.Load())
	case <-time.After(300 * time.Millisecond):
	}

	slow.g.open()
	if err := recv(t, 2*time.Second, "the first Shutdown returning", firstCh); !errors.Is(err, ErrShutdownTimeout) {
		t.Errorf("first Shutdown returned %v, want ErrShutdownTimeout", err)
	}
	if err := recv(t, 2*time.Second, "the second Shutdown returning", secondCh); err != nil {
		t.Errorf("second Shutdown returned %v, want nil", err)
	}
	if n := hookRuns.Load(); n != 1 {
		t.Errorf("OnShutdown hook ran %d times, want 1", n)
	}
	if err := recv(t, 2*time.Second, "Serve returning", serveCh); err != nil {
		t.Errorf("Serve returned %v, want nil", err)
	}
}

// gatedListener pauses its first Close at a gate.
type gatedListener struct {
	net.Listener
	g *gate
}

func (l *gatedListener) Close() error {
	l.g.pause()
	return l.Listener.Close()
}

// TestShutdownDrainsAConnectionAcceptedBeforeFasthttpRecordedTheListener is the
// M7 final review's Important 2. Serve publishes a.ln and then hands ln to
// fasthttp, which records it under its own lock. A Shutdown landing between
// the two finds no listener in fasthttp, so its drain returns at once; if
// fasthttp then records ln and accepts before rice closes ln, that connection
// was never drained and fasthttp's stop flag is already reset, so it would go
// on serving after Shutdown returned.
//
// The test plays Serve's part itself to hold it inside that window: it
// publishes a.ln as Serve does, and calls fasthttp's Serve only once
// Shutdown's first drain has returned and Shutdown is closing ln.
func TestShutdownDrainsAConnectionAcceptedBeforeFasthttpRecordedTheListener(t *testing.T) {
	a, inFlight, release := slowApp(t)
	a.Build()

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := &gatedListener{Listener: raw, g: newGate(t)}
	a.mu.Lock()
	a.ln = ln // Serve's publish step; fasthttp has not been handed ln yet.
	a.mu.Unlock()

	shutCh := make(chan error, 1)
	go func() { shutCh <- a.Shutdown(context.Background()) }()
	recv(t, 2*time.Second, "Shutdown closing the listener", ln.g.entered)

	// Now fasthttp records ln and accepts a connection, which starts a request.
	serveCh := make(chan error, 1)
	go func() { serveCh <- a.srv.Serve(ln) }()
	conn, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	fmt.Fprint(conn, "GET /slow HTTP/1.1\r\nHost: rice\r\n\r\n")
	recv(t, 2*time.Second, "the handler receiving the request", inFlight)

	ln.g.open()
	select {
	case err := <-shutCh:
		t.Fatalf("Shutdown returned %v while a request on a connection accepted in the window was in flight", err)
	case <-time.After(300 * time.Millisecond):
	}

	release()
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("reading the in-flight response: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if err := recv(t, 2*time.Second, "Shutdown returning", shutCh); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}

	// Nothing more is served on that connection.
	_, _ = fmt.Fprint(conn, "GET /ok HTTP/1.1\r\nHost: rice\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	rest, err := io.ReadAll(br)
	if strings.Contains(string(rest), "ok") {
		t.Errorf("the connection served a request after Shutdown returned: %q", rest)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Error("the connection was still open 2s after Shutdown returned")
	}
	if err := recv(t, 2*time.Second, "fasthttp's Serve returning", serveCh); err != nil {
		t.Errorf("fasthttp's Serve returned %v, want nil", err)
	}
}

// TestShutdownForceClosesAConnectionAcceptedBeforeFasthttpRecordedTheListener
// is the same window as the test above, with a deadline the connection's
// request outlasts: the second drain times out, and the connection is closed.
func TestShutdownForceClosesAConnectionAcceptedBeforeFasthttpRecordedTheListener(t *testing.T) {
	a, inFlight, release := slowApp(t)
	a.Build()

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := &gatedListener{Listener: raw, g: newGate(t)}
	a.mu.Lock()
	a.ln = ln
	a.mu.Unlock()

	shutCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		shutCh <- a.Shutdown(ctx)
	}()
	recv(t, 2*time.Second, "Shutdown closing the listener", ln.g.entered)

	serveCh := make(chan error, 1)
	go func() { serveCh <- a.srv.Serve(ln) }()
	conn, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /slow HTTP/1.1\r\nHost: rice\r\n\r\n")
	recv(t, 2*time.Second, "the handler receiving the request", inFlight)
	ln.g.open()

	err = recv(t, 2*time.Second, "Shutdown returning at its deadline", shutCh)
	if !errors.Is(err, ErrShutdownTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown returned %v, want ErrShutdownTimeout and context.DeadlineExceeded", err)
	}

	// The connection is closed, not merely silent, even before the handler returns.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = io.ReadAll(conn)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Error("the connection was still open 2s after a timed-out Shutdown")
	}
	release()
	if err := recv(t, 2*time.Second, "fasthttp's Serve returning", serveCh); err != nil {
		t.Errorf("fasthttp's Serve returned %v, want nil", err)
	}
}
