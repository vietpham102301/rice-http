# M7 Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make stopping a rice server reliable: drain in-flight requests up to a deadline, guarantee nothing is served once `Shutdown` returns, run user teardown after the server's, expose the three server timeouts, and give programs a context-driven `RunContext`.

**Architecture:** `Shutdown` delegates the drain to fasthttp's `ShutdownWithContext`. On a timeout rice closes every connection it tracks through `fasthttp.Server.ConnState`, because fasthttp resets its stop flag on return and would otherwise keep serving keep-alive connections. A `closed` flag shared by `Serve` and `Shutdown` removes the shutdown-before-serve hang. `OnStart` hooks run FIFO before accepting; `OnShutdown` hooks run LIFO after the drain, once, with errors joined. `RunContext` wraps `Serve` + `Shutdown` around a context the program wires to `signal.NotifyContext`.

**Tech Stack:** Go 1.25, fasthttp v1.73.0.

**Spec:** [docs/superpowers/specs/2026-09-18-m7-lifecycle-design.md](../specs/2026-09-18-m7-lifecycle-design.md)

## Global Constraints

- `github.com/valyala/fasthttp` is the only runtime dependency. Package `rice` production code must not import `reflect`, `encoding/json` (ADR-0006) or `os/signal` (spec D6).
- Nothing under `internal/` may import package `rice`.
- Every user-facing panic is prefixed `rice: `.
- Work on branch `m7-lifecycle` (already created; the spec is committed there).
- Dispatch budgets stay at **0** allocations for a static route, a parameterised route read as bytes, and a 404. `connState` for `StateActive`/`StateIdle` must be 0 allocations and must not take a lock.
- Registration rules match the rest of rice: a nil hook or option value that cannot be meaningful panics; registering a hook after `Build` panics.
- `make lint`, `make test`, `make test-debug` and `make cover` must exit 0 at the end of every task. Root package coverage must not drop below 98.8%.
- Every new guard is broken on purpose and watched to fail, with the failure text recorded in the task report. A guard nobody watched go red is not a guard.
- Every test that waits on a server does so with a bounded wait (the `within` helper, or a deadline loop). A regression must fail visibly, never hang the suite.
- Commit messages follow the repo's style (`feat:`, `fix:`, `test:`, `docs:`, `bench:`, `refactor:`) and end with:
  `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`

## File map

| File | Status | Responsibility |
| --- | --- | --- |
| `server.go` | modify | `Serve`, `Shutdown`, `RunContext`, `ErrShutdownTimeout` |
| `conns.go` | create | `connState` callback, `closeConns` |
| `lifecycle.go` | create | `OnStart`, `OnShutdown`, `runStart`, `runShutdown` |
| `app.go` | modify | new `App` fields, `ConnState` in `New`, the three timeout options |
| `lifecycle_test.go` | create | black-box lifecycle tests (package `rice_test`) |
| `lifecycle_internal_test.go` | create | panics, connection tracking, option fields (package `rice`) |
| `server_test.go` | modify | the existing deadline test switches to `errors.Is` |
| `conns_bench_test.go` | create | `BenchmarkDispatchConnStateHook` (package `rice`) |
| `bench/lifecycle_bench_test.go` | create | `BenchmarkShutdownLatency` |

---

### Task 1: `Shutdown` on `ShutdownWithContext`, the wrapped timeout error, and the `closed` flag

Spec D1, D3, D5 (without hooks). Findings 1 and 4.

**Files:**
- Modify: `server.go` (whole file)
- Modify: `app.go` (the `App` struct: add `closed`)
- Modify: `server_test.go` (`TestShutdownReturnsErrShutdownTimeoutWhenTheDeadlinePasses`)
- Create: `lifecycle_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `App.closed bool`, guarded by `a.mu`.
  - `Shutdown` returns `fmt.Errorf("%w: %w", ErrShutdownTimeout, ctx.Err())` on a timeout.
  - Test helpers in `lifecycle_test.go` (package `rice_test`), used by every later task:
    - `func serve(t *testing.T, app *rice.App) (addr string, errCh <-chan error)`
    - `func within[T any](t *testing.T, d time.Duration, what string, ch <-chan T) T`

- [ ] **Step 1: Write the failing tests**

Create `lifecycle_test.go`:

```go
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
```

In `server_test.go`, replace the last block of `TestShutdownReturnsErrShutdownTimeoutWhenTheDeadlinePasses` — from `ctx, cancel := context.WithTimeout(...)` to the end of the function — with:

```go
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = app.Shutdown(ctx)
	elapsed := time.Since(start)
	close(release) // let the handler finish so the goroutine does not leak

	if missing := errorsIsAll(err, rice.ErrShutdownTimeout, context.DeadlineExceeded); len(missing) > 0 {
		t.Errorf("Shutdown returned %v, which does not match %v", err, missing)
	}
	// Roadmap exit criterion 2: the deadline is honoured. 500ms of slack for CI.
	if elapsed > 600*time.Millisecond {
		t.Errorf("Shutdown took %v with a 100ms deadline", elapsed)
	}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -race -count=1 -run 'TestShutdown|TestServeAfterShutdown'`
Expected: FAIL. `TestServeAfterShutdownReturnsWithoutServing` fails with `Serve returning did not happen within 2s` (Serve blocks on the fresh listener). `TestShutdownReturnsErrShutdownTimeoutWhenTheDeadlinePasses` fails with `which does not match [context deadline exceeded]`.

- [ ] **Step 3: Add the `closed` field**

In `app.go`, replace the tail of the `App` struct:

```go
	srv *fasthttp.Server

	mu sync.Mutex
	ln net.Listener
}
```

with:

```go
	srv *fasthttp.Server

	// mu guards ln and closed, which Serve and Shutdown use to agree on whether
	// serving may begin. See the M7 design doc, D5.
	mu sync.Mutex
	ln net.Listener

	// closed is set by the first Shutdown. A Serve that sees it closes its
	// listener and returns without serving: an App serves once.
	closed bool
}
```

- [ ] **Step 4: Rewrite `server.go`**

Replace the whole file with:

```go
package rice

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// ErrShutdownTimeout is returned by Shutdown when in-flight requests did not
// finish before the context ended. The returned error also wraps the context's
// own error, so errors.Is matches context.DeadlineExceeded or context.Canceled
// as well.
//
// When Shutdown returns it, the connections those requests were using have
// been closed: nothing more is served. The handlers themselves run to
// completion, because a goroutine cannot be stopped; their responses are lost.
var ErrShutdownTimeout = errors.New("rice: shutdown timed out")

// Run binds addr and serves until Shutdown is called.
//
// It blocks. Use "127.0.0.1:0" to bind an ephemeral port and read the result
// back with Addr. It builds before binding, so a bad configuration fails before
// a port is taken.
func (a *App) Run(addr string) error {
	a.Build()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return a.Serve(ln)
}

// Serve serves on an existing listener and blocks until Shutdown is called. It
// builds first, so it is safe to call directly without going through Run.
//
// An App serves once. If Shutdown has already been called, Serve closes ln and
// returns nil without serving.
func (a *App) Serve(ln net.Listener) error {
	a.Build()

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	a.ln = ln
	a.mu.Unlock()

	return a.srv.Serve(ln)
}

// Addr returns the bound address, or the empty string if the App is not serving.
func (a *App) Addr() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.ln == nil {
		return ""
	}
	return a.ln.Addr().String()
}

// Shutdown stops accepting new connections and waits for in-flight requests to
// finish, or for ctx to end, whichever comes first. It returns nil after a clean
// drain and an error wrapping ErrShutdownTimeout when ctx ended first.
//
// The drain is fasthttp's: it closes idle keep-alive connections and polls for
// the rest every 100ms, so Shutdown may return up to that long after the last
// request finished.
func (a *App) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	ln := a.ln
	a.mu.Unlock()

	err := a.srv.ShutdownWithContext(ctx)
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		err = fmt.Errorf("%w: %w", ErrShutdownTimeout, ctxErr)
	}

	// A Serve that published ln but had not yet handed it to fasthttp is
	// invisible to ShutdownWithContext, and would block forever. Closing ln here
	// makes fasthttp's Serve return nil. It must come after ShutdownWithContext:
	// closing first makes fasthttp's own close fail, and it reports that error.
	// When fasthttp did record ln this is a second close, and its error is noise.
	if ln != nil {
		_ = ln.Close()
	}
	return err
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test . -race -count=1 -run 'TestShutdown|TestServeAfterShutdown'`
Expected: PASS.

- [ ] **Step 6: Fault injection — the listener close after `ShutdownWithContext`**

Move the `if ln != nil { _ = ln.Close() }` block to *before* `a.srv.ShutdownWithContext(ctx)`. Run `go test . -race -count=1 -run 'TestShutdownTwiceIsSafe|TestShutdownDrains'`. Expected: FAIL with `Shutdown returned close tcp ...: use of closed network connection`. Record the failure text, then restore the block to where it was.

- [ ] **Step 7: Run the full gate**

Run: `make lint && make test && make test-debug && make cover`
Expected: all exit 0; coverage ≥ 98.8%.

- [ ] **Step 8: Commit**

```bash
git add server.go app.go server_test.go lifecycle_test.go
git commit -m "feat: drain through ShutdownWithContext and refuse to serve after Shutdown

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Track connections and force-close them at the deadline

Spec D2. Finding 2.

**Files:**
- Create: `conns.go`
- Create: `lifecycle_internal_test.go`
- Modify: `app.go` (`App` fields; `ConnState` in `New`)
- Modify: `server.go` (`Shutdown` calls `closeConns` on a timeout)
- Modify: `lifecycle_test.go` (append tests)

**Interfaces:**
- Consumes: `serve`, `within`, `errorsIsAll` from Task 1.
- Produces:
  - `func (a *App) connState(c net.Conn, s fasthttp.ConnState)` — installed as `fasthttp.Server.ConnState`.
  - `func (a *App) closeConns()` — sets `forceClosed` and closes every tracked connection.
  - `App` fields `connMu sync.Mutex`, `conns map[net.Conn]struct{}`, `forceClosed bool`.

- [ ] **Step 1: Write the failing black-box tests**

In `lifecycle_test.go`, change the import block to:

```go
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
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)
```

and append:

```go
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
	<-inFlight

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
```

- [ ] **Step 2: Run them to verify the finding-2 test fails**

Run: `go test . -race -count=1 -run 'TestNothingIsServed|TestShutdownClosesIdle|TestForceClose'`
Expected: `TestNothingIsServedAfterATimedOutShutdown` FAILS with `the connection served a request after Shutdown returned` (and `still open 2s after`). The other two pass — they are regression guards for behaviour fasthttp already provides.

- [ ] **Step 3: Write the failing internal tests**

Create `lifecycle_internal_test.go`:

```go
package rice

import (
	"context"
	"io"
	"net"
	"net/http"
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

	if _, err := trackedPeer.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("reading the peer of a force-closed connection: %v, want io.EOF", err)
	}

	// A connection accepted before the listener closed but reported after the
	// sweep is closed on arrival and never tracked.
	late, latePeer := net.Pipe()
	defer latePeer.Close()
	a.connState(late, fasthttp.StateNew)

	if _, err := latePeer.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("reading the peer of a late connection: %v, want io.EOF", err)
	}
	if _, ok := a.conns[late]; ok {
		t.Error("a connection reported after the sweep was tracked")
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

```

- [ ] **Step 4: Run them to verify they fail**

Run: `go test . -race -count=1 -run 'TestConnState|TestCloseConns|TestConnectionsAreUntracked'`
Expected: FAIL to compile with `a.connState undefined` and `a.conns undefined`.

- [ ] **Step 5: Add the fields and install the hook**

In `app.go`, append to the `App` struct after `closed bool`:

```go

	// connMu guards conns and forceClosed. connState takes it only when a
	// connection opens or closes, never per request. See the M7 design doc, D2.
	connMu sync.Mutex

	// conns is every connection fasthttp has reported open and not yet closed.
	// Shutdown closes them when its deadline passes. Allocated on first use.
	conns map[net.Conn]struct{}

	// forceClosed is set by the sweep. A connection reported open after it was
	// accepted before the listener closed, and is closed on arrival.
	forceClosed bool
```

In `New`, change the server literal to:

```go
	a.srv = &fasthttp.Server{
		Handler:   a.handle,
		Name:      "rice",
		ConnState: a.connState,
	}
```

- [ ] **Step 6: Create `conns.go`**

```go
package rice

import (
	"net"

	"github.com/valyala/fasthttp"
)

// connState is installed as fasthttp.Server.ConnState. It keeps the set of open
// connections that Shutdown force-closes when its deadline passes: fasthttp's
// ShutdownWithContext resets its stop flag when it times out, so a busy
// keep-alive connection would otherwise go on serving after Shutdown returned.
// See ADR-0009.
//
// fasthttp calls it on every request with StateActive and StateIdle. Those
// return before touching the lock, so the per-request cost is the call itself.
func (a *App) connState(c net.Conn, s fasthttp.ConnState) {
	switch s {
	case fasthttp.StateNew:
		a.connMu.Lock()
		if a.forceClosed {
			a.connMu.Unlock()
			_ = c.Close()
			return
		}
		if a.conns == nil {
			a.conns = make(map[net.Conn]struct{})
		}
		a.conns[c] = struct{}{}
		a.connMu.Unlock()
	case fasthttp.StateClosed, fasthttp.StateHijacked:
		a.connMu.Lock()
		delete(a.conns, c)
		a.connMu.Unlock()
	}
}

// closeConns closes every tracked connection and makes connState close any
// connection reported after it. Each serving goroutine's next read or write
// then fails, and fasthttp reports StateClosed, which untracks it.
func (a *App) closeConns() {
	a.connMu.Lock()
	defer a.connMu.Unlock()

	a.forceClosed = true
	for c := range a.conns {
		_ = c.Close()
	}
}
```

- [ ] **Step 7: Call it from `Shutdown`**

In `server.go`, replace:

```go
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		err = fmt.Errorf("%w: %w", ErrShutdownTimeout, ctxErr)
	}
```

with:

```go
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		a.closeConns()
		err = fmt.Errorf("%w: %w", ErrShutdownTimeout, ctxErr)
	}
```

- [ ] **Step 8: Run all the new tests to verify they pass**

Run: `go test . -race -count=1 -run 'TestNothingIsServed|TestShutdownClosesIdle|TestForceClose|TestConnState|TestCloseConns|TestConnectionsAreUntracked'`
Expected: PASS.

- [ ] **Step 9: Fault injection**

1. Delete the `a.closeConns()` line from `Shutdown`. Run `go test . -race -count=1 -run TestNothingIsServed`. Expected: FAIL with `the connection served a request after Shutdown returned`. Record, restore.
2. In `connState`, delete the `if a.forceClosed { ... }` block. Run `go test . -race -count=1 -run TestCloseConns`. Expected: FAIL with `a connection reported after the sweep was tracked`. Record, restore.
3. In `connState`, move `a.connMu.Lock()` above the `switch`, with a matching `defer a.connMu.Unlock()`, removing the per-case locking. Run `go test . -race -count=1 -run TestConnStateActiveAndIdleAreFree`. Expected: FAIL with `blocked on connMu`. Record, restore.

- [ ] **Step 10: Run the full gate**

Run: `make lint && make test && make test-debug && make cover`
Expected: all exit 0; coverage ≥ 98.8%.

- [ ] **Step 11: Commit**

```bash
git add conns.go app.go server.go lifecycle_test.go lifecycle_internal_test.go
git commit -m "feat: force-close tracked connections when Shutdown's deadline passes

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `OnStart` and `OnShutdown`

Spec D4, and D5 steps 3–4.

**Files:**
- Create: `lifecycle.go`
- Modify: `app.go` (`App` fields)
- Modify: `server.go` (`Serve` runs start hooks; `Shutdown` runs shutdown hooks)
- Modify: `lifecycle_test.go`, `lifecycle_internal_test.go` (append tests)

**Interfaces:**
- Consumes: `serve`, `within`, `errorsIsAll`, `mustPanic` (package `rice`, `route_test.go`).
- Produces:
  - `func (a *App) OnStart(fn func() error)`
  - `func (a *App) OnShutdown(fn func(context.Context) error)`
  - `func (a *App) runStart() error`
  - `func (a *App) runShutdown(ctx context.Context) []error`
  - `App` fields `onStart []func() error`, `onShutdown []func(context.Context) error`, `shutdownOnce sync.Once`.

- [ ] **Step 1: Write the failing black-box tests**

In `lifecycle_test.go`, add `"sync/atomic"` to the imports, and append:

```go
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
	<-inFlight

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
	<-inFlight

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

	<-entered
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
```

- [ ] **Step 2: Write the failing internal panic tests**

Append to `lifecycle_internal_test.go`:

```go
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
		if s, ok := v.(string); !ok || len(s) < 6 || s[:6] != "rice: " {
			t.Errorf("%s panicked with %v, want a string starting %q", tc.name, v, "rice: ")
		}
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test . -race -count=1 -run 'TestOnStart|TestAnOnStart|TestOnShutdown|TestEveryOnShutdown|TestShutdownRunsHooks|TestServeAfterShutdownRunsNo|TestShutdownDuringOnStart|TestHookRegistration'`
Expected: FAIL to compile with `app.OnStart undefined`.

- [ ] **Step 4: Add the fields**

In `app.go`, append to the `App` struct after `forceClosed bool`:

```go

	// onStart and onShutdown are the lifecycle hooks, in registration order.
	// They are written during registration and read by Serve and Shutdown with
	// no lock, on the same argument as built: registration ends before serving.
	onStart    []func() error
	onShutdown []func(context.Context) error

	// shutdownOnce makes the OnShutdown hooks run on the first Shutdown only.
	shutdownOnce sync.Once
```

Add `"context"` to `app.go`'s imports.

- [ ] **Step 5: Create `lifecycle.go`**

```go
package rice

import "context"

// OnStart registers fn to run when the App starts serving: after the listener
// is bound and before any connection is accepted. Hooks run in registration
// order. The first error stops the sequence, closes the listener, and is
// returned by Serve or Run; no connection is ever accepted.
//
// Use it for work that must succeed before traffic arrives, such as checking a
// database is reachable.
func (a *App) OnStart(fn func() error) {
	if fn == nil {
		panic("rice: OnStart: hook is nil")
	}
	if a.built {
		panic("rice: cannot call OnStart after Build; hooks must be registered before serving begins")
	}
	a.onStart = append(a.onStart, fn)
}

// OnShutdown registers fn to run when Shutdown is called, after in-flight
// requests have drained, or been cut off at the deadline. Hooks run in reverse
// registration order, as defer does, so a resource opened first is released
// last. Each receives the ctx passed to Shutdown.
//
// Every hook runs even when an earlier one fails or the drain timed out, and
// Shutdown returns all their errors joined with its own. Hooks run on the first
// Shutdown only. A panicking hook is not recovered.
func (a *App) OnShutdown(fn func(context.Context) error) {
	if fn == nil {
		panic("rice: OnShutdown: hook is nil")
	}
	if a.built {
		panic("rice: cannot call OnShutdown after Build; hooks must be registered before serving begins")
	}
	a.onShutdown = append(a.onShutdown, fn)
}

// runStart runs the OnStart hooks in order and returns the first error.
func (a *App) runStart() error {
	for _, fn := range a.onStart {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}

// runShutdown runs the OnShutdown hooks in reverse order, once per App, and
// returns every error they reported.
func (a *App) runShutdown(ctx context.Context) []error {
	var errs []error
	a.shutdownOnce.Do(func() {
		for i := len(a.onShutdown) - 1; i >= 0; i-- {
			if err := a.onShutdown[i](ctx); err != nil {
				errs = append(errs, err)
			}
		}
	})
	return errs
}
```

- [ ] **Step 6: Wire the hooks into `Serve` and `Shutdown`**

In `server.go`, replace the body of `Serve` from `a.mu.Lock()` to the end with:

```go
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		_ = ln.Close()
		return nil
	}

	if err := a.runStart(); err != nil {
		_ = ln.Close()
		return err
	}

	// Shutdown may have run while the hooks did. Publishing ln and checking
	// closed under one lock is what lets Shutdown find it.
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	a.ln = ln
	a.mu.Unlock()

	return a.srv.Serve(ln)
```

Update `Serve`'s doc comment to add, after the "An App serves once" paragraph:

```go
//
// OnStart hooks run first, before any connection is accepted. If one fails,
// Serve closes ln and returns its error.
```

In `Shutdown`, replace the final `return err` with:

```go
	hookErrs := a.runShutdown(ctx)
	if len(hookErrs) == 0 {
		return err
	}
	return errors.Join(append([]error{err}, hookErrs...)...)
```

and append to `Shutdown`'s doc comment:

```go
//
// OnShutdown hooks then run, after the drain; their errors are joined with the
// drain's into the result. See OnShutdown.
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test . -race -count=1 -run 'TestOnStart|TestAnOnStart|TestOnShutdown|TestEveryOnShutdown|TestShutdownRunsHooks|TestServeAfterShutdownRunsNo|TestShutdownDuringOnStart|TestHookRegistration'`
Expected: PASS.

- [ ] **Step 8: Fault injection**

1. In `runShutdown`, iterate forwards (`for i := 0; i < len(a.onShutdown); i++`). Run `-run TestOnShutdownHooksRunInReverse`. Expected: FAIL with `events = [hook 1 hook 2 hook 3], want [hook 3 hook 2 hook 1]`. Record, restore.
2. In `runShutdown`, replace `a.shutdownOnce.Do(func() { ... })` with a direct call of the closure. Run `-run TestShutdownRunsHooksOnce`. Expected: FAIL with `ran 2 times`. Record, restore.
3. In `Serve`, delete the second `if a.closed { ... }` check. Run `-run TestShutdownDuringOnStart`. Expected: FAIL with `Serve returning did not happen within 2s`. Record, restore.
4. In `runShutdown`, `return` on the first error. Run `-run TestEveryOnShutdown`. Expected: FAIL. Record, restore.

- [ ] **Step 9: Run the full gate**

Run: `make lint && make test && make test-debug && make cover`
Expected: all exit 0; coverage ≥ 98.8%.

- [ ] **Step 10: Commit**

```bash
git add lifecycle.go app.go server.go lifecycle_test.go lifecycle_internal_test.go
git commit -m "feat: add OnStart and OnShutdown hooks around serving and draining

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: `RunContext`

Spec D6.

**Files:**
- Modify: `server.go` (add `RunContext`)
- Modify: `lifecycle_test.go`, `lifecycle_internal_test.go` (append tests)

**Interfaces:**
- Consumes: `serve`, `within`, `errorsIsAll`, `mustPanic`, `waitForAddr`, `get`.
- Produces: `func (a *App) RunContext(ctx context.Context, addr string, grace time.Duration) error`.

- [ ] **Step 1: Write the failing tests**

Append to `lifecycle_test.go`:

```go
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
	<-inFlight

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
	<-inFlight

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
```

Append to `lifecycle_internal_test.go`:

```go
func TestRunContextPanicsOnNegativeGrace(t *testing.T) {
	v := mustPanic(t, "RunContext(-1)", func() {
		_ = New().RunContext(context.Background(), "127.0.0.1:0", -1)
	})
	if s, ok := v.(string); !ok || s[:6] != "rice: " {
		t.Errorf("panicked with %v, want a string starting %q", v, "rice: ")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -race -count=1 -run TestRunContext`
Expected: FAIL to compile with `app.RunContext undefined`.

- [ ] **Step 3: Implement `RunContext`**

In `server.go`, add `"time"` to the imports and add after `Run`:

```go
// RunContext binds addr and serves until ctx is done, then shuts down, giving
// in-flight requests up to grace to finish. It returns once serving has
// stopped: nil after a clean shutdown, an error wrapping ErrShutdownTimeout if
// grace ran out, or the error that stopped serving early, such as a failed
// bind or OnStart hook.
//
// It is the building block for signal handling, which rice leaves to the
// standard library so the program chooses the signals:
//
//	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
//	defer stop()
//	err := app.RunContext(ctx, ":8080", 10*time.Second)
//
// A grace of zero cuts every in-flight request off at once. A negative grace
// panics.
func (a *App) RunContext(ctx context.Context, addr string, grace time.Duration) error {
	if grace < 0 {
		panic("rice: RunContext: grace is negative")
	}
	a.Build()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.Serve(ln) }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	// Not derived from ctx: ctx is already done, and the grace period is new time.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	shutdownErr := a.Shutdown(shutdownCtx)
	return errors.Join(shutdownErr, <-serveErr)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -race -count=1 -run TestRunContext`
Expected: PASS.

- [ ] **Step 5: Fault injection**

1. In `Shutdown`, delete the `if ln != nil { _ = ln.Close() }` block. Run `go test . -race -count=1 -run TestRunContextWithACancelledContextReturns`. Expected: FAIL with `RunContext returning (run N) did not happen within 2s` for some N. Record which N and the failure text, restore. If it passes, the race window was not hit in 50 runs: raise the count to 500 for the injection only, record that, and restore both.
2. Derive the shutdown context from `ctx` (`context.WithTimeout(ctx, grace)`). Run `-run TestRunContextLetsAnInFlightRequestFinish`. Expected: FAIL — the drain is cut off at once. Record, restore.

- [ ] **Step 6: Run the full gate**

Run: `make lint && make test && make test-debug && make cover`
Expected: all exit 0; coverage ≥ 98.8%.

- [ ] **Step 7: Commit**

```bash
git add server.go lifecycle_test.go lifecycle_internal_test.go
git commit -m "feat: add RunContext, serving until a context ends then shutting down

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Server timeouts as options

Spec D7.

**Files:**
- Modify: `app.go` (three options; the `Option` comment)
- Modify: `lifecycle_test.go`, `lifecycle_internal_test.go` (append tests)

**Interfaces:**
- Consumes: `serve`, `within`, `mustPanic`.
- Produces:
  - `func WithReadTimeout(d time.Duration) Option`
  - `func WithWriteTimeout(d time.Duration) Option`
  - `func WithIdleTimeout(d time.Duration) Option`

- [ ] **Step 1: Write the failing tests**

Append to `lifecycle_test.go`:

```go
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
```

Append to `lifecycle_internal_test.go`:

```go
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
		if s, ok := v.(string); !ok || s[:6] != "rice: " {
			t.Errorf("%s panicked with %v, want a string starting %q", name, v, "rice: ")
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -race -count=1 -run 'Timeout'`
Expected: FAIL to compile with `undefined: rice.WithReadTimeout`.

- [ ] **Step 3: Implement the options**

In `app.go`, add `"time"` to the imports. Change the `Option` comment's last sentence from `M7 adds server timeouts.` to nothing (delete that sentence), and add after `WithErrorHandler`:

```go
// WithReadTimeout limits how long the server waits to read a full request,
// including its body. Zero, the default, means no limit. Set it in production:
// without it a client that sends half a request holds its connection forever.
func WithReadTimeout(d time.Duration) Option {
	if d < 0 {
		panic("rice: WithReadTimeout: duration is negative")
	}
	return func(a *App) { a.srv.ReadTimeout = d }
}

// WithWriteTimeout limits how long the server spends writing a response. The
// clock starts after the handler returns. Zero, the default, means no limit.
func WithWriteTimeout(d time.Duration) Option {
	if d < 0 {
		panic("rice: WithWriteTimeout: duration is negative")
	}
	return func(a *App) { a.srv.WriteTimeout = d }
}

// WithIdleTimeout limits how long a keep-alive connection may sit idle between
// requests. Zero, the default, falls back to the read timeout, and so means no
// limit when that is unset too.
func WithIdleTimeout(d time.Duration) Option {
	if d < 0 {
		panic("rice: WithIdleTimeout: duration is negative")
	}
	return func(a *App) { a.srv.IdleTimeout = d }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -race -count=1 -run 'Timeout'`
Expected: PASS.

- [ ] **Step 5: Fault injection**

Change `WithReadTimeout`'s closure to set `a.srv.WriteTimeout`. Run `-run 'TestReadTimeoutDisconnects|TestTimeoutOptionsReach'`. Expected: both FAIL. Record, restore.

- [ ] **Step 6: Run the full gate**

Run: `make lint && make test && make test-debug && make cover`
Expected: all exit 0; coverage ≥ 98.8%.

- [ ] **Step 7: Commit**

```bash
git add app.go lifecycle_test.go lifecycle_internal_test.go
git commit -m "feat: expose the read, write and idle timeouts as options

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Benchmarks

Spec "Benchmarks".

**Files:**
- Create: `conns_bench_test.go`
- Create: `bench/lifecycle_bench_test.go`
- Create: `bench/results/M7-lifecycle.txt` (generated)

**Interfaces:**
- Consumes: `App.handle`, `App.connState` (Task 2).
- Produces: `BenchmarkDispatchConnStateHook`, `BenchmarkShutdownLatency`.

- [ ] **Step 1: Write the `ConnState` benchmark**

Create `conns_bench_test.go`:

```go
package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// BenchmarkDispatchConnStateHook measures what D2's hook adds to a request:
// fasthttp calls ConnState with StateActive before the handler and StateIdle
// after. Both arms run in one session, so the delta is not between-session drift.
func BenchmarkDispatchConnStateHook(b *testing.B) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return c.Bytes(200, c.Param("id")) })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")

	b.Run("dispatch", func(b *testing.B) {
		app.handle(fctx)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			app.handle(fctx)
		}
	})

	b.Run("dispatch+connstate", func(b *testing.B) {
		app.handle(fctx)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			app.connState(nil, fasthttp.StateActive)
			app.handle(fctx)
			app.connState(nil, fasthttp.StateIdle)
		}
	})
}
```

- [ ] **Step 2: Write the shutdown-latency benchmark**

Create `bench/lifecycle_bench_test.go`:

```go
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
			<-inFlight

			done := make(chan time.Time, 1)
			go func() {
				_ = app.Shutdown(context.Background())
				done <- time.Now()
			}()
			time.Sleep(5 * time.Millisecond) // let Shutdown enter its poll loop

			start := time.Now()
			close(release)
			total += (<-done).Sub(start)
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
```

Before creating it, check `bench/helpers_test.go` for an existing helper that does what `serveForBench` does; if one exists, use it and drop `serveForBench`.

- [ ] **Step 3: Smoke-run both**

Run: `go test . ./bench/... -run '^$' -bench 'ConnStateHook|ShutdownLatency' -benchmem -count=1`
Expected: both run. `dispatch+connstate` reports `0 allocs/op`. `ms/shutdown` is printed for both sub-benchmarks.

- [ ] **Step 4: Record**

Run: `make bench-record LABEL=M7-lifecycle`
Expected: writes `bench/results/M7-lifecycle.txt`. Then run `benchstat` on the two `BenchmarkDispatchConnStateHook` arms (extract each arm's ten lines into two files in the scratchpad and compare), and record the delta and p-value in the task report. If `dispatch+connstate` shows any allocation, stop and report: spec D2 is reopened.

Record the `ms/shutdown` medians for both sub-benchmarks in the task report. The spec expected `idle-keepalive-only` to be near 0 because fasthttp closes idle connections before its first poll; fasthttp decrements its open count only when the serving goroutine exits, which may be after that first check. Whatever the number is, it goes in the retrospective as measured, with the explanation that fits it.

- [ ] **Step 5: Run the full gate**

Run: `make lint && make test && make test-debug && make bench`
Expected: all exit 0.

- [ ] **Step 6: Commit**

```bash
git add conns_bench_test.go bench/lifecycle_bench_test.go bench/results/M7-lifecycle.txt
git commit -m "bench: record the ConnState hook's cost and Shutdown's drain latency

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Documentation, ADR-0009, and the retrospective

Spec "Documentation". Read `docs/milestones/M6-context-pooling.md`, `docs/milestones/TEMPLATE.md`, `docs/adr/0008-rice-recovers-panics-in-core.md` and the top entry of `docs/progress.md` first: match their structure and voice.

**Files:**
- Create: `docs/adr/0009-shutdown-force-closes-at-deadline.md`
- Create: `docs/milestones/M7-lifecycle.md`
- Modify: `docs/adr/README.md`, `docs/03-core-concepts.md` (§4, App), `docs/02-architecture.md` (file map), `docs/05-performance-model.md`, `docs/04-roadmap.md`, `README.md`, `doc.go`, `docs/progress.md`

**Interfaces:**
- Consumes: the task reports of Tasks 1–6 (fault-injection texts, benchmark numbers).
- Produces: documentation only.

- [ ] **Step 1: ADR-0009**

Create `docs/adr/0009-shutdown-force-closes-at-deadline.md` in the shape of ADR-0008 (Status, Context, Decision, Consequences). Context: finding 2 of the spec, with the two fasthttp lines quoted (`s.stop.Store(1)` / `defer s.stop.Store(0)`) and the version (v1.73.0). Decision: spec D2, including the `forceClosed` late-connection rule. Rejected alternatives: keeping fasthttp's behaviour, wrapping the listener, re-implementing the drain — one paragraph each, reasons from the spec. Consequences: handlers are not stopped, their responses are lost; the 100 ms poll; the measured per-request cost from Task 6; the ADR must be revisited if fasthttp changes `ShutdownWithContext`. Add its line to `docs/adr/README.md`.

- [ ] **Step 2: Core concepts, architecture, performance model**

- `docs/03-core-concepts.md`, the App section: add `OnStart`, `OnShutdown`, `RunContext` and the three options to the API block; rewrite the `Shutdown` paragraph to state the guarantee (nothing served after it returns), the hook order, and point at ADR-0009.
- `docs/02-architecture.md`: add `conns.go` and `lifecycle.go` to the file map, with one-line responsibilities.
- `docs/05-performance-model.md`: a row for the `ConnState` hook with its measured cost, and the two shutdown-latency numbers with the 100 ms poll as the explanation.

- [ ] **Step 3: README and `doc.go`**

- `README.md`: a "Graceful shutdown" example using `signal.NotifyContext` and `RunContext`, a sentence recommending all three timeouts in production, and the status line moved to M7.
- `doc.go`: a short lifecycle paragraph — `RunContext`, hook order, the shutdown guarantee.

- [ ] **Step 4: Roadmap**

In `docs/04-roadmap.md`, mark M7 `☑`. Leave its text as is.

- [ ] **Step 5: Retrospective and journal**

Create `docs/milestones/M7-lifecycle.md` from `TEMPLATE.md`. It must answer the roadmap question — what fasthttp gives for free and what had to be built — with the four findings, and it must include: the measured `ConnState` cost with its p-value, both shutdown-latency numbers with the explanation that fits them, every fault injection with its recorded failure text, and anything the implementation found that the spec got wrong (as a correction, the way M6's retrospective does).

Add a new top entry to `docs/progress.md` in the journal's shape (`Did`, `Learned`, `Measured`, `Next`). `Next:` is M8 — the benchmark suite against Gin, Echo and Fiber, and the retrospective.

- [ ] **Step 6: Check every claim against the code**

For each number and behaviour stated in the files changed by this task, find the test, benchmark line or source line that supports it. Fix or remove any claim that has none. In particular, grep the repo for stale text: `grep -rn "no deadline of its own\|M7 adds\|blunt" --include=*.go --include=*.md .` should return only history (progress journal, old milestone files).

- [ ] **Step 7: Run the full gate**

Run: `make lint && make test && make test-debug && make cover && make bench`
Expected: all exit 0; coverage ≥ 98.8%.

- [ ] **Step 8: Commit**

```bash
git add docs README.md doc.go
git commit -m "docs: close M7 with ADR-0009, the retrospective, and measured shutdown costs

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```
