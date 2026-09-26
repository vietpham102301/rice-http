# Streaming and SSE Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `c.Stream(fn)` and `c.SSE(heartbeat, fn)` send a response in pieces after the handler returns, on a context of their own that Shutdown cancels when it begins, with rice recovering the writer goroutine.

**Architecture:** `Stream` and `SSE` record `fn` in three `Ctx` fields that `reset` clears. `handle` reads them, releases the `Ctx`, and only then — and only when the funnel did not answer the request — calls fasthttp's `SetBodyStreamWriter`, whose goroutine creates the stream's context from the App's `streamCtx`, runs an optional heartbeat, recovers `fn`, and flushes. `Shutdown` cancels `streamCtx` before it drains. `SSE` wraps a `Stream` and encodes events at zero allocations.

**Tech Stack:** Go 1.25, fasthttp v1.73.0 (`RequestCtx.SetBodyStreamWriter`), `bufio`, `context`, `sync`. No new dependency.

**Spec:** [docs/superpowers/specs/2026-09-26-streaming-sse-design.md](../specs/2026-09-26-streaming-sse-design.md) — read D2 as corrected (finding 5).

## Global Constraints

- **Branch:** `streaming-sse`, already created, holding the spec commits. Do not commit to `main`.
- **Exported surface added:** exactly `Stream` with `Write`, `WriteString`, `Flush`, `Context`; `(*Ctx).Stream`; `SSE` with `Send`, `Context`; `Event` with fields `ID`, `Event`, `Data`, `Retry`; `(*Ctx).SSE`. Nothing else exported changes.
- **`Stream` and `SSE` on `Ctx` call `c.poison.check()` first.**
- **Panics carry the prefix `rice: `**: nil callback, a second `Stream`/`SSE` in one request, a negative SSE heartbeat.
- **`fn` starts only after the `Ctx` is released, and never when the funnel answered the request** (error returned, `HandleError`, panic) **or the request is HEAD** (spec D2, D5).
- **A stream's context is `context.WithCancel(a.streamCtx)`, never derived from `c.Context()`**; `streamCtx` is `context.WithCancel(a.baseCtx)`; `Shutdown` calls `a.cancelStreams()` before draining (spec D3).
- **The writer waits for the heartbeat goroutine to exit before it returns** (spec D7).
- **An error from `fn` is logged as `rice: stream: <err>` only when the stream's context is not cancelled; a panic is logged as `rice: stream panicked: <value>` followed by the stack** (spec D4).
- **SSE wire format** exactly as spec D6; `Send` returns an error and writes nothing for an `ID` containing `\r`, `\n` or NUL, or an `Event` containing `\r` or `\n`.
- **`SSE.Send` allocates nothing**, with and without `-race`.
- **Run `make test`, `make test-debug` and `make lint` before each commit; never commit with a failing suite.** Commit messages: subject, blank line, then `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` on its own line.

## Review Focus

Inputs the spec implies but does not enumerate. Each has a test in the task that owns the code.

1. **A handler that opens a stream, then returns an error** — the client must get the error response, and `fn` must never run. (Task 1)
2. **A request that opens a stream after `Shutdown` has begun** — its context is already cancelled; `fn` sees `Done()` at once. (Task 1)
3. **`fn` panicking after it has written part of the body** — the process survives, the panic is logged, and the connection's response ends. (Task 1)
4. **A client that disappears from an idle SSE stream** — the heartbeat must notice and cancel the context. (Task 2)
5. **Heartbeat goroutines outliving their stream** — none may remain once a stream has ended. (Task 2)

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `ctx.go` (modify) | three stream fields on `Ctx`, cleared by `reset` | 1 |
| `app.go` (modify) | `streamCtx`/`cancelStreams`; `handle` starts a recorded stream after release | 1 |
| `server.go` (modify) | `Shutdown` cancels streams before draining | 1 |
| `stream.go` (create) | `Stream`, `(*Ctx).Stream`, `registerStream`, `startStream`, `heartbeat` | 1 |
| `stream_test.go` (create) | loopback helpers and stream behaviour tests | 1 |
| `ricedebug_test.go` (modify) | touching `c` inside `fn` panics | 1 |
| `sse.go` (create) | `SSE`, `Event`, `(*Ctx).SSE`, `Send` | 2 |
| `sse_test.go` (create) | SSE format, heartbeat, disconnect, leaks | 2 |
| `alloc_test.go` (modify) | `Send` budget, stream setup budget | 3 |
| `bench/rice_bench_test.go` (modify) | `BenchmarkSSESend` | 3 |
| `docs/adr/0019-…md` (create), `docs/adr/README.md`, `docs/03-core-concepts.md`, `docs/05-performance-model.md`, `docs/04-roadmap.md`, `docs/progress.md`, `README.md` | documentation | 4 |

---

### Task 1: Stream runs after release, on its own context

**Files:**
- Create: `stream.go`, `stream_test.go`
- Modify: `ctx.go` (the `Ctx` struct after `handled`; `reset`; imports), `app.go` (App struct after `cancelBase`; `New` after `baseCtx` is created; `handle`'s deferred function), `server.go` (`Shutdown`, after `a.mu.Unlock()` that follows `a.closed = true`), `ricedebug_test.go`

**Interfaces:**
- Consumes: `c.poison.check()`, `a.release(c)`, `a.baseCtx`, `serve(t, app)` and `within(t, d, what, ch)` from lifecycle_test.go (package `rice_test`), `dispatchCtx(app, method, path)` from panic_test.go (package `rice`), `errUseAfterRelease`.
- Produces: `type Stream struct{ mu sync.Mutex; w *bufio.Writer; ctx context.Context; cancel context.CancelFunc }` with `Write`, `WriteString`, `Flush`, `Context`, `fail(error) error`; `func (c *Ctx) Stream(fn func(s *Stream) error) error`; `func (c *Ctx) registerStream(heartbeat time.Duration, fn func(*Stream) error)`; `func (a *App) startStream(fctx *fasthttp.RequestCtx, fn func(*Stream) error, heartbeat time.Duration)`; `func (s *Stream) heartbeat(every time.Duration, done chan struct{})`. Test helpers (package `rice_test`): `rawRequest(t, addr, method, path string) (net.Conn, *bufio.Reader)`, `readUntil(t, r *bufio.Reader, want string, d time.Duration) string`, `type lockedBuffer`, `captureStreamLog(t) *lockedBuffer`, `waitFor(t, d time.Duration, what string, cond func() bool)`, `shutdownOnCleanup(t, app)`.

- [ ] **Step 1: Confirm the branch**

Run: `git branch --show-current`
Expected: `streaming-sse`

- [ ] **Step 2: Write the failing tests**

Create `stream_test.go`:

```go
package rice_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// rawRequest sends one HTTP/1.1 request on a fresh connection and returns the
// connection and a reader over the raw response, so a test can read a streamed
// body piece by piece.
func rawRequest(t *testing.T, addr, method, path string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.Write([]byte(method + " " + path + " HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return conn, bufio.NewReader(conn)
}

// readUntil reads lines until what has been read contains want, and returns it.
// It fails the test if want has not arrived within d.
func readUntil(t *testing.T, r *bufio.Reader, want string, d time.Duration) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		var b strings.Builder
		for !strings.Contains(b.String(), want) {
			line, err := r.ReadString('\n')
			b.WriteString(line)
			if err != nil {
				break
			}
		}
		ch <- b.String()
	}()
	got := within(t, d, "reading "+strconvQuote(want), ch)
	if !strings.Contains(got, want) {
		t.Fatalf("connection ended before %q arrived; read %q", want, got)
	}
	return got
}

func strconvQuote(s string) string { return `"` + s + `"` }

// lockedBuffer is a log destination the stream's goroutine and the test can
// share without a data race.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// captureStreamLog sends the standard logger to a buffer for this test and back
// to io.Discard, where the package's TestMain keeps it, afterwards.
func captureStreamLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	return buf
}

// waitFor polls cond until it holds, failing the test after d.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %v", what, d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// shutdownOnCleanup shuts app down when the test ends, so no test leaves a
// stream or a server running into the next.
func shutdownOnCleanup(t *testing.T, app *rice.App) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
	})
}

func TestStreamSendsChunksBeforeTheCallbackReturns(t *testing.T) {
	release := make(chan struct{})
	app := rice.New()
	app.GET("/s", func(c *rice.Ctx) error {
		c.SetHeader("X-Before", "yes")
		return c.Stream(func(s *rice.Stream) error {
			if _, err := s.WriteString("first\n"); err != nil {
				return err
			}
			if err := s.Flush(); err != nil {
				return err
			}
			<-release
			_, err := s.Write([]byte("second\n"))
			return err
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/s")
	got := readUntil(t, r, "first", 2*time.Second)
	if !strings.Contains(got, "200 OK") || !strings.Contains(got, "X-Before: yes") {
		t.Errorf("status line or header missing before the first chunk: %q", got)
	}
	close(release)
	readUntil(t, r, "second", 2*time.Second)
	readUntil(t, r, "0\r\n", 2*time.Second) // the chunked body ends
}

func TestStreamNeverStartsWhenTheHandlerReturnsAnError(t *testing.T) {
	ran := make(chan struct{}, 1)
	app := rice.New()
	app.GET("/err", func(c *rice.Ctx) error {
		_ = c.Stream(func(s *rice.Stream) error { ran <- struct{}{}; return nil })
		return rice.NewHTTPError(418, "")
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/err")
	got := readUntil(t, r, "I'm a teapot", 2*time.Second)
	if !strings.Contains(got, "418") {
		t.Errorf("response %q, want the 418 the handler returned", got)
	}
	select {
	case <-ran:
		t.Error("the stream ran although the handler returned an error")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStreamNeverStartsForHEAD(t *testing.T) {
	ran := make(chan struct{}, 1)
	app := rice.New()
	app.HEAD("/s", func(c *rice.Ctx) error {
		c.SetHeader("X-Kind", "stream")
		return c.Stream(func(s *rice.Stream) error { ran <- struct{}{}; return nil })
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "HEAD", "/s")
	got := readUntil(t, r, "\r\n\r\n", 2*time.Second)
	if !strings.Contains(got, "X-Kind: stream") {
		t.Errorf("HEAD response %q is missing the handler's header", got)
	}
	select {
	case <-ran:
		t.Error("the stream ran for a HEAD request")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStreamPanicIsRecoveredAndLogged(t *testing.T) {
	logs := captureStreamLog(t)
	app := rice.New()
	app.GET("/p", func(c *rice.Ctx) error {
		return c.Stream(func(s *rice.Stream) error {
			_, _ = s.WriteString("partial\n")
			_ = s.Flush()
			panic("stream exploded")
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/p")
	readUntil(t, r, "partial", 2*time.Second)
	readUntil(t, r, "0\r\n", 2*time.Second)
	waitFor(t, 2*time.Second, "the panic being logged", func() bool {
		return strings.Contains(logs.String(), "rice: stream panicked: stream exploded")
	})
	if !strings.Contains(logs.String(), "goroutine ") {
		t.Errorf("the panic log has no stack: %q", logs.String())
	}
}

func TestStreamErrorIsLogged(t *testing.T) {
	logs := captureStreamLog(t)
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.Stream(func(s *rice.Stream) error { return errors.New("upstream went away") })
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/e")
	readUntil(t, r, "0\r\n", 2*time.Second)
	waitFor(t, 2*time.Second, "the error being logged", func() bool {
		return strings.Contains(logs.String(), "rice: stream: upstream went away")
	})
}

func TestShutdownStopsAStreamThatHonoursItsContext(t *testing.T) {
	logs := captureStreamLog(t)
	app := rice.New()
	app.GET("/s", func(c *rice.Ctx) error {
		return c.Stream(func(s *rice.Stream) error {
			_, _ = s.WriteString("open\n")
			_ = s.Flush()
			<-s.Context().Done()
			return s.Context().Err()
		})
	})
	addr, _ := serve(t, app)

	_, r := rawRequest(t, addr, "GET", "/s")
	readUntil(t, r, "open", 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v, want a clean drain", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Shutdown took %v; the stream should have stopped as it began", d)
	}
	if strings.Contains(logs.String(), "rice: stream:") {
		t.Errorf("a stream ended by Shutdown was logged as an error: %q", logs.String())
	}
}

func TestShutdownForceClosesAStreamThatIgnoresItsContext(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	app := rice.New()
	app.GET("/s", func(c *rice.Ctx) error {
		return c.Stream(func(s *rice.Stream) error {
			_, _ = s.WriteString("open\n")
			_ = s.Flush()
			<-block
			return nil
		})
	})
	addr, _ := serve(t, app)

	_, r := rawRequest(t, addr, "GET", "/s")
	readUntil(t, r, "open", 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := app.Shutdown(ctx); !errors.Is(err, rice.ErrShutdownTimeout) {
		t.Errorf("Shutdown: %v, want ErrShutdownTimeout", err)
	}
}

func TestAStreamOpenedDuringShutdownStartsCancelled(t *testing.T) {
	inHandler := make(chan struct{})
	proceed := make(chan struct{})
	sawDone := make(chan bool, 1)
	app := rice.New()
	app.GET("/s", func(c *rice.Ctx) error {
		close(inHandler)
		<-proceed
		return c.Stream(func(s *rice.Stream) error {
			select {
			case <-s.Context().Done():
				sawDone <- true
			case <-time.After(time.Second):
				sawDone <- false
			}
			return nil
		})
	})
	addr, _ := serve(t, app)

	rawRequest(t, addr, "GET", "/s")
	within(t, 2*time.Second, "the handler starting", inHandler)

	shutdownErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		shutdownErr <- app.Shutdown(ctx)
	}()
	time.Sleep(50 * time.Millisecond) // let Shutdown begin and cancel the streams
	close(proceed)

	if !within(t, 2*time.Second, "the stream running", sawDone) {
		t.Error("a stream opened after Shutdown began did not see Done at once")
	}
	if err := within(t, 3*time.Second, "Shutdown returning", shutdownErr); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestStreamPanicsOnMisuse(t *testing.T) {
	cases := map[string]func(c *rice.Ctx){
		"nil callback": func(c *rice.Ctx) { _ = c.Stream(nil) },
		"called twice": func(c *rice.Ctx) {
			_ = c.Stream(func(*rice.Stream) error { return nil })
			_ = c.Stream(func(*rice.Stream) error { return nil })
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			recovered := make(chan any, 1)
			app := rice.New()
			app.GET("/s", func(c *rice.Ctx) error {
				defer func() { recovered <- recover() }()
				call(c)
				return nil
			})
			addr, _ := serve(t, app)
			shutdownOnCleanup(t, app)
			rawRequest(t, addr, "GET", "/s")
			r := within(t, 2*time.Second, "the handler running", recovered)
			if msg, _ := r.(string); !strings.HasPrefix(msg, "rice: ") {
				t.Errorf("recovered %v, want a rice: panic", r)
			}
		})
	}
}
```

In `ricedebug_test.go`, append:

```go
// TestAStreamCallbackTouchingItsCtxPanics pins spec D2: the callback starts
// only after the Ctx is released and poisoned, so using c inside it panics
// every time rather than racing the release.
func TestAStreamCallbackTouchingItsCtxPanics(t *testing.T) {
	recovered := make(chan any, 1)
	app := New()
	app.GET("/s", func(c *Ctx) error {
		return c.Stream(func(s *Stream) error {
			defer func() { recovered <- recover() }()
			_ = c.Path()
			return nil
		})
	})
	dispatchCtx(app, "GET", "/s")

	select {
	case r := <-recovered:
		if r != errUseAfterRelease {
			t.Errorf("recovered %v, want the use-after-release panic", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stream callback never ran")
	}
}
```

(Add `"time"` to `ricedebug_test.go`'s imports if it is not already there.)

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test -run 'Stream|Shutdown' -count=1 .`
Expected: FAIL to compile, `c.Stream undefined (type *rice.Ctx has no field or method Stream)`.

- [ ] **Step 4: Write the implementation**

In `ctx.go`, add `"time"` to the imports, and after the `handled bool` field of `Ctx`:

```go
	// streamFn is the callback Stream or SSE recorded, and streamHeartbeat
	// its heartbeat. streamSet records that either was called, including on a
	// HEAD request, where no callback is kept. handle starts the stream only
	// after this Ctx is released; see startStream and ADR-0019.
	streamFn        func(*Stream) error
	streamHeartbeat time.Duration
	streamSet       bool
```

In `reset`, after `c.handled = false`:

```go
	c.streamFn = nil
	c.streamHeartbeat = 0
	c.streamSet = false
```

In `app.go`, after the `cancelBase context.CancelFunc` field of `App`:

```go

	// streamCtx is the parent of every stream's context. Shutdown cancels it
	// when it begins, so open streams stop and the drain can finish; it is a
	// child of baseCtx, so force-close cancels it too. See ADR-0019.
	streamCtx     context.Context
	cancelStreams context.CancelFunc
```

In `New`, directly after `a.baseCtx, a.cancelBase = context.WithCancel(context.Background())`:

```go
	a.streamCtx, a.cancelStreams = context.WithCancel(a.baseCtx)
```

In `handle`'s deferred function, replace `a.release(c)` with:

```go
		// A stream is started only now, after release: fasthttp starts the
		// writer goroutine the moment SetBodyStreamWriter is called, and the
		// callback must not run beside the handler, the middleware or the
		// funnel, nor before the Ctx is poisoned. A request the funnel answered
		// keeps its error response. See ADR-0019.
		fn, hb, answered := c.streamFn, c.streamHeartbeat, c.handled
		a.release(c)
		if fn != nil && !answered {
			a.startStream(fctx, fn, hb)
		}
```

Extend the comment above that deferred function where it says release "runs whether the handler returned, errored or panicked" with one sentence: `A stream the handler recorded starts after release, and only when the funnel did not answer the request.`

In `server.go`, in `Shutdown`, directly after the `a.mu.Unlock()` that follows `a.closed = true`:

```go

	// Open streams never end by themselves; tell them to stop now so the drain
	// below can finish. Handlers keep ADR-0010's rule: their context is not
	// cancelled until force-close. See ADR-0019.
	a.cancelStreams()
```

Create `stream.go`:

```go
package rice

import (
	"bufio"
	"context"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

// Stream is a response body written in pieces, after the handler has returned.
// Stream hands one to the callback; nothing in it belongs to the request's Ctx.
//
// Write and WriteString buffer; Flush sends what is buffered. Once the client
// has gone, each of them returns an error and the stream's context is
// cancelled. A Stream is safe for use by one goroutine at a time, plus rice's
// own heartbeat.
type Stream struct {
	mu     sync.Mutex
	w      *bufio.Writer
	ctx    context.Context
	cancel context.CancelFunc
}

// Write buffers p.
func (s *Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	n, err := s.w.Write(p)
	s.mu.Unlock()
	return n, s.fail(err)
}

// WriteString buffers v.
func (s *Stream) WriteString(v string) (int, error) {
	s.mu.Lock()
	n, err := s.w.WriteString(v)
	s.mu.Unlock()
	return n, s.fail(err)
}

// Flush sends what is buffered to the client.
func (s *Stream) Flush() error {
	s.mu.Lock()
	err := s.w.Flush()
	s.mu.Unlock()
	return s.fail(err)
}

// Context is cancelled when the stream must stop: Shutdown has begun, Shutdown
// force-closed the connections, the client has gone, or the callback returned.
//
// It is not c.Context(). That may be a middleware's, cancelled as soon as the
// handler returns, which is before the stream starts; so values and deadlines a
// middleware put there do not reach the stream. See ADR-0019.
func (s *Stream) Context() context.Context { return s.ctx }

// fail cancels the stream's context when a write has failed, which is how a
// departed client is noticed: fasthttp reports it only to the next write.
func (s *Stream) fail(err error) error {
	if err != nil {
		s.cancel()
	}
	return err
}

// Stream sends the response body in pieces: fn writes to s after the handler
// has returned. Set the status and headers before calling it; with no status set
// it is 200. Return its result from the handler:
//
//	return c.Stream(func(s *rice.Stream) error { ... })
//
// fn runs on its own goroutine, after this Ctx has been released, so it must not
// use c: read and copy what it needs from the request first. It does not run at
// all when the handler returns an error, a middleware settles the request with
// HandleError, the handler panics, or the request is HEAD.
//
// rice recovers a panic in fn and logs it with its stack. An error fn returns is
// logged unless the stream's context was already cancelled, which is how a
// stream normally ends. Neither reaches the ErrorHandler: the status has already
// been sent.
//
// Middleware sees the handler return before the stream is written: Logger's
// duration ends there, Timeout's deadline does not cover the stream, and
// middleware.Recover does not cover fn. See ADR-0019.
//
// A nil fn, or a second Stream or SSE in the same request, panics.
func (c *Ctx) Stream(fn func(s *Stream) error) error {
	c.poison.check()
	c.registerStream(0, fn)
	return nil
}

// registerStream records fn for handle to start after release, or records only
// that a stream was asked for when the request is HEAD.
func (c *Ctx) registerStream(heartbeat time.Duration, fn func(*Stream) error) {
	if fn == nil {
		panic("rice: Stream or SSE callback is nil")
	}
	if c.streamSet {
		panic("rice: Stream or SSE called twice for one request")
	}
	c.streamSet = true
	if c.fctx.IsHead() {
		// fasthttp would run the writer for HEAD with nobody reading, and an
		// event stream would never end. The headers are the whole answer.
		return
	}
	c.streamFn, c.streamHeartbeat = fn, heartbeat
}

// startStream hands fn to fasthttp as the body writer. handle calls it after
// releasing the Ctx, so fn can never run beside the handler.
func (a *App) startStream(fctx *fasthttp.RequestCtx, fn func(*Stream) error, heartbeat time.Duration) {
	parent := a.streamCtx
	fctx.SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithCancel(parent)
		s := &Stream{w: w, ctx: ctx, cancel: cancel}

		var hbDone chan struct{}
		if heartbeat > 0 {
			hbDone = make(chan struct{})
			go s.heartbeat(heartbeat, hbDone)
		}

		// Runs last. The heartbeat must have stopped before this function
		// returns: fasthttp then puts w back in a pool, and a heartbeat still
		// writing would write into another response.
		defer func() {
			cancel()
			if hbDone != nil {
				<-hbDone
			}
			s.mu.Lock()
			_ = w.Flush()
			s.mu.Unlock()
		}()

		// fasthttp runs this function on a bare goroutine; a panic here would
		// end the process. See ADR-0019.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("rice: stream panicked: %v\n%s", r, debug.Stack())
			}
		}()

		if err := fn(s); err != nil && ctx.Err() == nil {
			log.Printf("rice: stream: %v", err)
		}
	})
}

// heartbeat writes an SSE comment every interval until the stream's context is
// cancelled. A failed write means the client has gone, and cancels the stream.
func (s *Stream) heartbeat(every time.Duration, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			_, _ = s.w.WriteString(":\n\n")
			err := s.w.Flush()
			s.mu.Unlock()
			if err != nil {
				s.cancel()
				return
			}
		}
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -run 'Stream|Shutdown' -count=1 -race .`
Expected: PASS.

Run: `go test -run 'AStreamCallbackTouchingItsCtxPanics|EveryCtxMethodPanicsAfterRelease' -count=1 -race -tags ricedebug .`
Expected: PASS, including `TestEveryCtxMethodPanicsAfterRelease/Stream`.

Run: `for i in 1 2 3 4 5; do go test -run 'Stream|Shutdown' -count=1 -race . || break; done`
Expected: five passes; these tests use real sockets and time.

- [ ] **Step 6: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS. The existing dispatch budgets in alloc_test.go still pass at their figures: the three new fields are read, not allocated.

- [ ] **Step 7: Commit**

```bash
git add ctx.go app.go server.go stream.go stream_test.go ricedebug_test.go
git commit -m "rice: Stream writes a body after release, on a context Shutdown cancels

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: SSE on top of Stream

**Files:**
- Create: `sse.go`, `sse_test.go`

**Interfaces:**
- Consumes: `Stream` (fields `mu`, `w`, `ctx`; method `fail`), `(*Ctx).registerStream`, test helpers `rawRequest`, `readUntil`, `waitFor`, `shutdownOnCleanup`, `serve`, `within` (Task 1 and lifecycle_test.go).
- Produces: `type SSE struct{ s *Stream }`; `type Event struct{ ID, Event, Data string; Retry time.Duration }`; `func (c *Ctx) SSE(heartbeat time.Duration, fn func(s *SSE) error) error`; `func (x *SSE) Send(ev Event) error`; `func (x *SSE) Context() context.Context`; `var errInvalidEvent`. Test helper `heartbeatGoroutines() int`.

- [ ] **Step 1: Write the failing tests**

Create `sse_test.go`:

```go
package rice_test

import (
	"runtime"
	"strings"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

func TestSSEWritesTheEventStreamFormat(t *testing.T) {
	sendErr := make(chan error, 1)
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(0, func(s *rice.SSE) error {
			if err := s.Send(rice.Event{ID: "7", Event: "tick", Data: "a\nb\r\nc\rd", Retry: 1500 * time.Millisecond}); err != nil {
				return err
			}
			if err := s.Send(rice.Event{}); err != nil {
				return err
			}
			sendErr <- s.Send(rice.Event{ID: "x\ny", Data: "never"})
			_ = s.Send(rice.Event{Event: "done"})
			<-s.Context().Done()
			return nil
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/e")
	got := readUntil(t, r, "event: done", 2*time.Second)
	for _, want := range []string{
		"Content-Type: text/event-stream",
		"Cache-Control: no-cache",
		"id: 7\nevent: tick\nretry: 1500\ndata: a\ndata: b\ndata: c\ndata: d\n\n",
		"\ndata: \n\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("response is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "never") {
		t.Error("an event with a line break in its ID was written")
	}
	if err := within(t, time.Second, "the invalid Send", sendErr); err == nil {
		t.Error("Send accepted an ID containing a line break")
	}
}

func TestSSERejectsFramingCharacters(t *testing.T) {
	results := make(chan []bool, 1)
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(0, func(s *rice.SSE) error {
			var errs []bool
			for _, ev := range []rice.Event{
				{ID: "a\rb"}, {ID: "a\nb"}, {ID: "a\x00b"},
				{Event: "a\rb"}, {Event: "a\nb"},
				{ID: "ok", Event: "ok", Data: "line\nline"},
			} {
				errs = append(errs, s.Send(ev) != nil)
			}
			results <- errs
			return nil
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	rawRequest(t, addr, "GET", "/e")
	got := within(t, 2*time.Second, "the sends", results)
	want := []bool{true, true, true, true, true, false}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d: error %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSSEHeartbeatArrives(t *testing.T) {
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(50*time.Millisecond, func(s *rice.SSE) error {
			<-s.Context().Done()
			return nil
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/e")
	readUntil(t, r, ":\n", time.Second)
}

func TestSSEPanicsOnANegativeHeartbeat(t *testing.T) {
	recovered := make(chan any, 1)
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		defer func() { recovered <- recover() }()
		return c.SSE(-time.Second, func(*rice.SSE) error { return nil })
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	rawRequest(t, addr, "GET", "/e")
	r := within(t, 2*time.Second, "the handler running", recovered)
	if msg, _ := r.(string); !strings.HasPrefix(msg, "rice: ") {
		t.Errorf("recovered %v, want a rice: panic", r)
	}
}

func TestSSEHeartbeatNoticesAClientThatLeft(t *testing.T) {
	const every = 50 * time.Millisecond
	cancelled := make(chan struct{})
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(every, func(s *rice.SSE) error {
			if err := s.Send(rice.Event{Data: "hello"}); err != nil {
				return err
			}
			<-s.Context().Done()
			close(cancelled)
			return nil
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	conn, r := rawRequest(t, addr, "GET", "/e")
	readUntil(t, r, "data: hello", 2*time.Second)
	conn.Close()
	// fasthttp learns of the close only from a write that fails; the first
	// heartbeat after the close may still fit in the kernel's buffers.
	within(t, 10*every+time.Second, "the stream noticing the client left", cancelled)
}

// heartbeatGoroutines counts running heartbeat goroutines by their frame.
func heartbeatGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "(*Stream).heartbeat(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

func TestSSEHeartbeatStopsWithTheStream(t *testing.T) {
	finished := make(chan struct{})
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(20*time.Millisecond, func(s *rice.SSE) error {
			defer close(finished)
			return s.Send(rice.Event{Data: "one"})
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/e")
	readUntil(t, r, "data: one", 2*time.Second)
	within(t, 2*time.Second, "the callback returning", finished)
	waitFor(t, 2*time.Second, "no heartbeat goroutine remaining", func() bool {
		return heartbeatGoroutines() == 0
	})
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -run SSE -count=1 .`
Expected: FAIL to compile, `c.SSE undefined`.

- [ ] **Step 3: Write the implementation**

Create `sse.go`:

```go
package rice

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SSE is a server-sent event stream. SSE hands one to the callback.
type SSE struct{ s *Stream }

// Event is one server-sent event. Empty fields are left out, except Data: an
// event always carries at least one data line, so a browser dispatches it.
type Event struct {
	ID    string
	Event string
	Data  string
	Retry time.Duration
}

// errInvalidEvent is Send's answer to an ID or event name that would break the
// stream's framing.
var errInvalidEvent = errors.New("rice: SSE event ID or name contains a line break or NUL")

// SSE streams server-sent events. It sets Content-Type: text/event-stream and
// Cache-Control: no-cache, then behaves as Stream: fn runs after the handler has
// returned, must not use c, and does not run for HEAD or when the request was
// answered with an error.
//
// While fn runs, rice writes a comment line every heartbeat, whether or not
// events are flowing. That keeps proxies from closing an idle connection and is
// how a client that left is noticed: fasthttp reports a closed connection only
// to a write. Zero disables it; a negative heartbeat panics.
//
// Read Last-Event-ID, and anything else fn needs, before calling SSE:
//
//	last := string(c.Header("Last-Event-ID"))
//	return c.SSE(15*time.Second, func(s *rice.SSE) error { ... })
func (c *Ctx) SSE(heartbeat time.Duration, fn func(s *SSE) error) error {
	c.poison.check()
	if heartbeat < 0 {
		panic("rice: SSE heartbeat is negative; use 0 to disable it")
	}
	if fn == nil {
		panic("rice: Stream or SSE callback is nil")
	}
	c.fctx.Response.Header.SetContentType("text/event-stream")
	c.fctx.Response.Header.Set("Cache-Control", "no-cache")
	c.registerStream(heartbeat, func(s *Stream) error { return fn(&SSE{s: s}) })
	return nil
}

// Context is the stream's context; see Stream.Context.
func (x *SSE) Context() context.Context { return x.s.ctx }

// Send writes one event and flushes it. It allocates nothing.
//
// Data is split into one data line per line; \r\n, \r and \n all end a line. An
// ID containing \r, \n or NUL, or an event name containing \r or \n, would change
// the stream's framing: Send writes nothing and returns an error. It returns an
// error, too, once the client has gone.
func (x *SSE) Send(ev Event) error {
	if strings.ContainsAny(ev.ID, "\r\n\x00") || strings.ContainsAny(ev.Event, "\r\n") {
		return errInvalidEvent
	}
	s := x.s
	s.mu.Lock()
	w := s.w
	if ev.ID != "" {
		w.WriteString("id: ")
		w.WriteString(ev.ID)
		w.WriteByte('\n')
	}
	if ev.Event != "" {
		w.WriteString("event: ")
		w.WriteString(ev.Event)
		w.WriteByte('\n')
	}
	if ev.Retry > 0 {
		var b [20]byte
		w.WriteString("retry: ")
		// WriteByte, not Write: Write can hand its slice to the underlying
		// io.Writer, which would move b to the heap.
		for _, d := range strconv.AppendInt(b[:0], ev.Retry.Milliseconds(), 10) {
			w.WriteByte(d)
		}
		w.WriteByte('\n')
	}
	data := ev.Data
	for {
		i := strings.IndexAny(data, "\r\n")
		line := data
		if i >= 0 {
			line = data[:i]
		}
		w.WriteString("data: ")
		w.WriteString(line)
		w.WriteByte('\n')
		if i < 0 {
			break
		}
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			i++
		}
		data = data[i+1:]
	}
	w.WriteByte('\n')
	// bufio.Writer's errors are sticky, so Flush reports any earlier failure too.
	err := w.Flush()
	s.mu.Unlock()
	return s.fail(err)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -run 'SSE|Stream|Shutdown' -count=1 -race .`
Expected: PASS.

Run: `for i in 1 2 3 4 5; do go test -run 'SSE|Stream|Shutdown' -count=1 -race . || break; done`
Expected: five passes.

- [ ] **Step 5: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS, including `TestEveryCtxMethodPanicsAfterRelease/SSE` under ricedebug.

- [ ] **Step 6: Commit**

```bash
git add sse.go sse_test.go
git commit -m "rice: SSE sends server-sent events with a heartbeat on top of Stream

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Budgets and a benchmark

**Files:**
- Modify: `alloc_test.go`, `bench/rice_bench_test.go`

**Interfaces:**
- Consumes: `SSE`, `Stream`, `Event` (Tasks 1–2), `budget` (budget_test.go), `newRequestCtx` (bench/helpers_test.go).
- Produces: `TestAllocBudgetSSESend`, `TestAllocBudgetStreamSetup`, `BenchmarkSSESend`; two measured figures for Task 4.

- [ ] **Step 1: Write the Send budget**

Append to `alloc_test.go` (add `"bufio"`, `"bytes"` and `"time"` to its imports if absent):

```go
// TestAllocBudgetSSESend pins Send with every field set, on a warm writer, at
// 0: it writes strings and single bytes into the bufio.Writer, and formats
// Retry into a stack buffer written byte by byte.
func TestAllocBudgetSSESend(t *testing.T) {
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x := &SSE{s: &Stream{w: bufio.NewWriterSize(&out, 4096), ctx: ctx, cancel: cancel}}
	ev := Event{ID: "42", Event: "tick", Data: "first line\nsecond line", Retry: 1500 * time.Millisecond}

	budget(t, "SSE.Send", 0, func() {
		out.Reset()
		_ = x.Send(ev)
	})
}
```

- [ ] **Step 2: Measure the stream setup**

Create a temporary file `stream_measure_test.go`:

```go
//go:build !ricedebug

package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

func TestMeasureStreamSetup(t *testing.T) {
	app := New()
	app.GET("/s", func(c *Ctx) error {
		return c.Stream(func(s *Stream) error { return nil })
	})
	app.Build()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/s")
	fn := func() {
		fctx.Response.Reset() // closes the previous stream's reader, as the server does
		app.handle(fctx)
	}
	fn()
	t.Logf("stream setup: %.2f allocs/op", testing.AllocsPerRun(1000, fn))
}
```

Run: `go test -run TestMeasureStreamSetup -count=1 -v .` and `go test -run TestMeasureStreamSetup -count=1 -v -race .`, three times each. Record every figure. The budget is the largest, rounded up. Then `rm stream_measure_test.go`.

- [ ] **Step 3: Write the setup budget**

Append to `alloc_test.go`, writing the measured integer where the comment says:

```go
// TestAllocBudgetStreamSetup pins what opening one stream costs through the
// dispatch path: the recorded closure, the writer closure, fasthttp's pipe and
// goroutine, the Stream and its context. Streams are outside the zero-allocation
// claim; this is a regression guard. Measured on darwin arm64, with and without
// -race. The response is reset each call, as fasthttp's server does, which closes
// the previous stream.
func TestAllocBudgetStreamSetup(t *testing.T) {
	app := New()
	app.GET("/s", func(c *Ctx) error {
		return c.Stream(func(s *Stream) error { return nil })
	})
	app.Build()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/s")

	const want float64 = /* measured figure */
	budget(t, "opening a stream", want, func() {
		fctx.Response.Reset()
		app.handle(fctx)
	})
}
```

Replace `/* measured figure */` with the integer from Step 2 before running anything.

- [ ] **Step 4: Run the budgets**

Run: `go test -run 'AllocBudgetSSESend|AllocBudgetStreamSetup' -count=3 . && go test -run 'AllocBudgetSSESend|AllocBudgetStreamSetup' -count=3 -race .`
Expected: PASS on every run. If `SSE.Send` is not 0, do not raise it: profile with `go test -run AllocBudgetSSESend -memprofile mem.out . && go tool pprof -list 'Send' mem.out`, fix the allocation in sse.go, say what it was in the report, and delete `mem.out`.

- [ ] **Step 5: Write the benchmark**

Append to `bench/rice_bench_test.go` (add `"bufio"`, `"io"`, `"context"` and `"time"` to its imports as needed):

```go
// BenchmarkSSESend measures encoding and flushing one event with every field
// set, through a real SSE stream to a discarding reader. It reports the cost
// of Send, not of the network.
func BenchmarkSSESend(b *testing.B) {
	app := rice.New()
	ready := make(chan *rice.SSE, 1)
	done := make(chan struct{})
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(0, func(s *rice.SSE) error {
			ready <- s
			<-done
			return nil
		})
	})
	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/e")
	h(fctx)
	go func() { _, _ = io.Copy(io.Discard, fctx.Response.BodyStream()) }()
	s := <-ready
	ev := rice.Event{ID: "42", Event: "tick", Data: "first line\nsecond line", Retry: 1500 * time.Millisecond}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Send(ev); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	close(done)
}
```

If `fctx.Response.BodyStream()` does not exist in fasthttp v1.73.0, read the body stream the way fasthttp exposes it (search `func (resp *Response) BodyStream` / `bodyStream` in its source) and say what you used in the report; do not change the benchmark's purpose.

- [ ] **Step 6: Run the benchmark**

Run: `go test ./bench -run '^$' -bench BenchmarkSSESend -benchmem -count=10 | tee /tmp/sse-bench.txt`
Expected: ten lines, each `0 allocs/op`. Record the median ns/op, B/op, the machine and the Go version for Task 4. Do not commit `/tmp/sse-bench.txt`.

- [ ] **Step 7: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add alloc_test.go bench/rice_bench_test.go
git commit -m "rice: pin SSE.Send at zero allocations and measure opening a stream

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Documentation

**Files:**
- Create: `docs/adr/0019-streams-run-after-the-handler.md`
- Modify: `docs/adr/README.md`, `docs/03-core-concepts.md` (a new *Streaming* subsection at the end of §2, before §3), `docs/05-performance-model.md` (the exclusions list; the `Ctx` methods table), `docs/04-roadmap.md` (*Explicitly deferred*, *Done after M8*), `docs/progress.md` (new entry at the top), `README.md`

**Interfaces:**
- Consumes: every behaviour from Tasks 1–3, and Task 3's figures.
- Produces: nothing code depends on.

- [ ] **Step 1: Write ADR-0019**

Create `docs/adr/0019-streams-run-after-the-handler.md`, in the shape of the existing ADRs (read ADR-0017 and ADR-0018 first):

```markdown
# ADR-0019 — Streams run after the handler, on their own context, and stop when Shutdown begins

Status: Accepted
Date: 2026-09-26

## Context

Streaming and server-sent events were the last item on the roadmap's *Explicitly deferred* list.
fasthttp streams a body through `SetBodyStreamWriter`, which runs a writer function on its own
goroutine, writing into a pipe the server copies to the connection. Probing it found five things:
a client that leaves is seen only by a later write; a panic in the writer ends the process, because
nothing recovers that goroutine; a HEAD request still runs the writer with nobody reading; a drain
waits for a stream that ends and returns cleanly; and the writer goroutine starts the moment
`SetBodyStreamWriter` is called, not when the handler returns.

Three of rice's rules meet here. The borrow contract (ADR-0005) says a `Ctx` is dead once the
handler returns. The request context (ADR-0010) is deliberately not cancelled when Shutdown begins,
because a handler finishes on its own and the grace period exists to let it. And an event stream
never finishes on its own.

## Decision

- **`c.Stream(fn)` and `c.SSE(heartbeat, fn)` record `fn`; `handle` starts it after the `Ctx` is
  released.** fasthttp's goroutine is started only then, so `fn` never runs beside the handler, the
  middleware or the error funnel, and under `ricedebug` a callback that uses `c` panics every time.
  When the funnel answered the request — an error returned, `HandleError`, a panic — the stream is
  never started and the error response stands. For HEAD nothing is recorded; the headers are the
  answer.
- **A stream gets objects of its own, not the `Ctx`.** `Stream` wraps fasthttp's `bufio.Writer`
  under a mutex; `SSE` wraps a `Stream`.
- **A stream's context is its own.** It is `context.WithCancel(streamCtx)`, where the App's
  `streamCtx` is a child of the base context. It is cancelled when Shutdown begins, at force-close,
  when a write fails because the client left, and when `fn` returns. It is not derived from
  `c.Context()`, which may be a middleware's context that ends when the handler does.
- **Shutdown cancels streams when it begins.** Handlers keep ADR-0010's rule. A stream that honours
  its context ends at once, and the drain finishes cleanly; one that does not is force-closed at the
  deadline as before.
- **rice recovers the writer.** A panic in `fn` is logged with its stack; an error is logged unless
  the stream's context was already cancelled. The ErrorHandler is not involved: the status is sent.
- **SSE writes a heartbeat comment on an interval**, because a departed client is noticed only by a
  write. The writer waits for the heartbeat to stop before returning, because fasthttp pools the
  `bufio.Writer` afterwards.

## Alternatives

**Call `SetBodyStreamWriter` from inside `Stream`.** The obvious wrapper. Rejected because fasthttp
starts the goroutine immediately: `fn` would race the handler's remaining code, the middleware and the
funnel, run before the `Ctx` is released, and keep running beside an error response.

**Run the stream synchronously in the handler by hijacking the connection.** Keeps the `Ctx` valid
and lets middleware see the true duration. Rejected: rice would write HTTP framing itself, and a
hijacked connection leaves rice's connection tracking, so Shutdown could no longer force-close it
(ADR-0009).

**Let the handler return a channel of events.** Rice would own the loop and could interleave
heartbeats without a lock. Rejected: it fits SSE only, and a producer blocked on a channel nobody reads
leaks unless it watches a context anyway.

**Derive the stream's context from `c.Context()`.** Carries middleware values. Rejected: `Timeout`'s
context is cancelled when the handler returns, so the stream would start cancelled.

**Treat streams like handlers at Shutdown (ADR-0010).** Rejected by the owner: with any SSE client
connected, every Shutdown would wait out its full deadline and then cut the stream.

## Consequences

**Middleware sees less.** Logger's duration ends when the handler returns; Timeout does not cover the
stream; `middleware.Recover` does not cover `fn` (rice's own recovery does). Headers set before `next`
are sent.

**A stream opened during Shutdown starts cancelled**, and `fn` sees `Done()` at once.

**An App mounted with `FasthttpHandler()` and never shut down stops a stream only when its client
leaves.**

**Three fields on `Ctx`.** `reset` clears them; reading them costs the dispatch path nothing.

**Streams are outside the zero-allocation claim; `SSE.Send` is inside it.**
```

- [ ] **Step 2: List it**

In `docs/adr/README.md`, add a row for ADR-0019 after ADR-0018, in the existing row format.

- [ ] **Step 3: Core concepts**

In `docs/03-core-concepts.md`, add a `### Streaming` subsection at the end of §2 (`Ctx`), before §3, with: the signatures of `Stream`, `SSE`, `Event` and their methods; a short example (the one in the spec's D1); and one paragraph each on when `fn` runs (after release; never on an answered request or HEAD; must not use `c`), the stream's context (what cancels it; not `c.Context()`), and what middleware sees. Link ADR-0019. Every sentence must match stream.go and sse.go.

- [ ] **Step 4: Performance model**

In `docs/05-performance-model.md`: add to *What "zero allocations" excludes* a bullet that opening a stream allocates (the figure from `TestAllocBudgetStreamSetup`); add rows to the `Ctx` methods table in its existing format — `c.Stream` / `c.SSE` with the setup figure and `TestAllocBudgetStreamSetup`, `SSE.Send` with 0 and `TestAllocBudgetSSESend`, status "MEASURED after M8"; and a sentence naming `BenchmarkSSESend` with its median ns/op, 0 allocs/op, machine and Go version, darwin arm64 only, no committed results file.

- [ ] **Step 5: Roadmap**

In `docs/04-roadmap.md`, remove `- Streaming and server-sent events` from *Explicitly deferred*. If the list is then empty, replace it with one line: `None. Every item deferred at M8 has been built; see *Done after M8*.` Append to *Done after M8* a bullet in the voice of the existing ones, summarising: `c.Stream` and `c.SSE`; the five probe findings in one sentence; that `fn` starts after release and never on an answered request or HEAD; the stream's own context and Shutdown cancelling it at its start; the recovery; `SSE.Send` at zero allocations; ADR-0019.

- [ ] **Step 6: Progress entry**

Add at the top of `docs/progress.md`, below the `---` that follows the entry template, `## 2026-09-26 — post-M8 — Streaming and server-sent events`, with **Did**, **Learned** (at least: fasthttp starts the writer goroutine at `SetBodyStreamWriter`, found while prototyping after the design was approved; a disconnect is seen only by a write; a panic in the writer ends the process; `bufio.Writer.Write` moved the Retry buffer to the heap where `WriteByte` does not), **Measured** (both budgets with and without `-race`, the benchmark median, coverage from `make cover`), and **Next** (the roadmap's deferred list is empty; the next work needs a new brainstorm).

- [ ] **Step 7: README**

In `README.md`, beside the existing examples, add a short SSE example: a handler that reads `Last-Event-ID` before calling `c.SSE(15*time.Second, ...)` and a loop that selects on `s.Context().Done()` and a channel of events.

- [ ] **Step 8: Verify and commit**

Run: `make test && make test-debug && make lint && make cover`
Expected: PASS; root-package coverage not below 99.5%. If it is below, add the smallest tests that reach the uncovered lines in stream.go or sse.go, and say which in the report.

```bash
git add docs README.md
git commit -m "docs: ADR-0019 and the docs for streaming and SSE

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```
