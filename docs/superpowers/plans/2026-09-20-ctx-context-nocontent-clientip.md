# Ctx — Context, NoContent and ClientIP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `c.Context()`, `c.SetContext()`, `c.ClientIP()` and `c.NoContent()` to rice's core `Ctx`, with a base context the `App` owns and cancels when a shutdown force-closes.

**Architecture:** `App` holds a `context.Context` built in `New` from `context.WithCancel(context.Background())`. `Ctx` holds one `context.Context` field that `reset` clears; `Context()` returns it, or falls back to the App's when it is nil. `closeConns` — the single force-close point — cancels the base context after releasing `connMu` and before closing sockets. `NoContent` and `ClientIP` are independent accessors with no new state.

**Tech Stack:** Go 1.25, fasthttp v1.73.0 (pinned), standard library only. No new dependency.

**Spec:** [docs/superpowers/specs/2026-09-20-ctx-context-nocontent-clientip-design.md](../specs/2026-09-20-ctx-context-nocontent-clientip-design.md)

## Global Constraints

- **Branch:** `ctx-context-nocontent-clientip`, already created, already holding the spec commit. Do not commit to `main`.
- **TDD is not optional here.** Every budget test must be seen failing before the code exists, and the failure text goes in the commit message or the progress entry. This is principle 9 of `docs/01-design-principles.md`, and the repo's reviewers check for it.
- **No new `Option`.** If a task seems to need one, stop and re-read the spec's D9.
- **`poison.check()` is the first statement of every exported `Ctx` method.** `ricedebug_test.go` walks the exported method set by reflection and calls every method with zero-valued arguments, expecting `errUseAfterRelease`. A method that validates its arguments before checking the poison fails that test.
- **No reflection and no `encoding/json` in the code added by this plan.** ADR-0006. `ricedebug_test.go` may use reflection; production code may not.
- **Budgets:** every new method is 0 allocations per call, enforced with the existing `budget` helper in `budget_test.go`.
- **Run `make test` (not just `go test`) before each commit**, and `go test -race ./...` for tasks 3 and 4.

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `ctx_response.go` | `NoContent` beside `Status`, `String`, `Bytes`, `JSON` | 1 |
| `ctx_response_test.go` (`package rice`) | `NoContent` unit behaviour | 1 |
| `ctx_context_test.go` (`package rice_test`, **new**) | `NoContent` and `ClientIP` over a real connection | 1, 2 |
| `ctx.go` | `ClientIP`; the `ctx` field; `reset`; `Context`; `SetContext` | 2, 3 |
| `ctx_test.go` (`package rice`) | `ClientIP`, `Context`, `SetContext` unit behaviour | 2, 3 |
| `app.go` | `baseCtx` and `cancelBase` fields, built in `New` | 3 |
| `conns.go` | `closeConns` cancels the base context | 4 |
| `lifecycle_test.go` (`package rice_test`) | the four lifecycle tests, beside the existing `slowApp` harness | 4 |
| `alloc_test.go` (`package rice`, `//go:build !ricedebug`) | the four budget tests | 1, 2, 3 |
| `docs/adr/0010-*.md` (**new**), `doc.go`, `docs/0{2,3,4,5}-*.md`, `docs/progress.md` | documentation | 5 |

Tasks 1 and 2 are independent of each other and of 3; task 4 depends on 3; task 5 depends on all.

---

### Task 1: `c.NoContent(code int)`

**Files:**
- Modify: `ctx_response.go` (append after `Bytes`, before `JSON`)
- Test: `ctx_response_test.go` (`package rice`), `alloc_test.go`, `ctx_context_test.go` (create, `package rice_test`)

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `func (c *Ctx) NoContent(code int) error` — always returns nil; sets the status, clears the body, and leaves no `Content-Type` on the response.

- [ ] **Step 1: Write the failing unit tests**

Append to `ctx_response_test.go`:

```go
func TestNoContentSetsTheStatusAndNoBody(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	if err := c.NoContent(204); err != nil {
		t.Fatalf("NoContent(204) = %v, want nil", err)
	}
	if got := fctx.Response.StatusCode(); got != 204 {
		t.Errorf("status = %d, want 204", got)
	}
	if got := fctx.Response.Body(); len(got) != 0 {
		t.Errorf("body = %q, want empty", got)
	}
}

// TestNoContentDiscardsABodyAlreadyWritten is why NoContent calls ResetBody
// rather than only setting the status: a 204 carrying a body is malformed, and
// a handler that wrote before changing its mind would produce one.
func TestNoContentDiscardsABodyAlreadyWritten(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	if err := c.String(200, "half an answer"); err != nil {
		t.Fatalf("String: %v", err)
	}
	if err := c.NoContent(204); err != nil {
		t.Fatalf("NoContent(204) = %v, want nil", err)
	}
	if got := fctx.Response.Body(); len(got) != 0 {
		t.Errorf("body = %q, want empty: NoContent must discard what was written", got)
	}
}
```

Create `ctx_context_test.go` with the wire-level test. This one matters most: fasthttp sets a default `Content-Type`, and a getter would not reveal whether it reaches the client.

```go
package rice_test

import (
	"net/http"
	"testing"

	rice "github.com/vietpham102301/rice-http"
)

// TestNoContentSendsNoContentTypeOnTheWire reads the response a real client
// gets. fasthttp fills in a default Content-Type, so asserting on the response
// object would not show whether one is sent; RFC 9110 has no body to describe
// for a 204, and a proxy that sees a Content-Type on one may expect a body.
func TestNoContentSendsNoContentTypeOnTheWire(t *testing.T) {
	app := rice.New()
	app.DELETE("/items/:id", func(c *rice.Ctx) error { return c.NoContent(204) })
	addr, _ := serve(t, app)

	req, err := http.NewRequest(http.MethodDelete, "http://"+addr+"/items/42", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 204 {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		t.Errorf("Content-Type = %q, want none on a 204", ct)
	}
	if resp.ContentLength > 0 {
		t.Errorf("Content-Length = %d, want 0 or absent", resp.ContentLength)
	}
}
```

Add to `alloc_test.go`:

```go
func TestAllocBudgetNoContent(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.NoContent", 0, func() { _ = c.NoContent(204) })
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'NoContent' 2>&1 | head -20`
Expected: compile failure, `c.NoContent undefined (type *Ctx has no field or method NoContent)`.

- [ ] **Step 3: Implement**

Append to `ctx_response.go`, after `Bytes`:

```go
// NoContent sends the given status code with no body: 204 after a successful
// DELETE, 205, 304.
//
// It discards anything already written to the body, and sends no Content-Type:
// a status that carries no body must not describe one.
func (c *Ctx) NoContent(code int) error {
	c.poison.check()
	c.fctx.SetStatusCode(code)
	c.fctx.Response.ResetBody()
	c.fctx.Response.Header.SetContentType("")
	return nil
}
```

If `SetContentType("")` turns out not to suppress the header — fasthttp may substitute its default when the value is empty — use `c.fctx.Response.Header.SetNoDefaultContentType(true)` instead, and say in the commit message which one was needed and why. Do not leave both in.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test`
Expected: all green, including the three new tests.

- [ ] **Step 5: Break the budget on purpose, then restore it**

Temporarily add `_ = make([]byte, 8)` as the first line after `poison.check()` in `NoContent`, run `go test -run TestAllocBudgetNoContent ./...`, and record the failure text — it reads like `Ctx.NoContent allocated 1.0 objects per call, budget is 0`. Remove the line and re-run to green. The recorded text goes into the task 5 progress entry.

- [ ] **Step 6: Commit**

```bash
git add ctx_response.go ctx_response_test.go ctx_context_test.go alloc_test.go
git commit -m "ctx: add NoContent, a status with no body and no content type

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `c.ClientIP() net.IP`

**Files:**
- Modify: `ctx.go` (append after `Body`; add `net` to the import block)
- Test: `ctx_test.go`, `ctx_context_test.go`, `alloc_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `func (c *Ctx) ClientIP() net.IP` — the connection's address, never a header.

- [ ] **Step 1: Write the failing tests**

Append to `ctx_test.go` (`package rice`):

```go
// TestClientIPIgnoresXForwardedFor is a security claim, not an implementation
// detail: a ClientIP that trusted this header by default would let any client
// declare its own address, and the access log and every rate limiter built on
// it would be reporting attacker-supplied data. The trust policy lives in
// middleware.RealIP, which rewrites the remote address instead.
func TestClientIPIgnoresXForwardedFor(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.Set("X-Forwarded-For", "203.0.113.9")
	fctx.Request.Header.Set("X-Real-Ip", "203.0.113.9")
	c := &Ctx{}
	c.reset(nil, fctx)

	if got := c.ClientIP(); got.String() == "203.0.113.9" {
		t.Errorf("ClientIP() = %v, want the connection address: headers must not be trusted", got)
	}
}

// TestClientIPFollowsSetRemoteAddr pins the mechanism middleware.RealIP will
// use: it resolves the header against its trusted-hop count and rewrites
// fasthttp's remote address, and ClientIP reports the result without knowing
// that happened.
func TestClientIPFollowsSetRemoteAddr(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	fctx.SetRemoteAddr(&net.TCPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 1234})
	c := &Ctx{}
	c.reset(nil, fctx)

	if got := c.ClientIP(); got.String() != "203.0.113.9" {
		t.Errorf("ClientIP() = %v, want 203.0.113.9", got)
	}
}
```

`ctx_test.go` needs `net` in its import block.

Append to `ctx_context_test.go` (`package rice_test`):

```go
// TestClientIPOverARealConnection checks the accessor against a real socket,
// where the address comes from the connection rather than from a test fixture.
func TestClientIPOverARealConnection(t *testing.T) {
	app := rice.New()
	app.GET("/ip", func(c *rice.Ctx) error { return c.String(200, c.ClientIP().String()) })
	addr, _ := serve(t, app)

	resp, err := http.Get("http://" + addr + "/ip")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := string(body); got != "127.0.0.1" {
		t.Errorf("ClientIP() = %q, want 127.0.0.1", got)
	}
}
```

This adds `io` to `ctx_context_test.go`'s imports.

Add to `alloc_test.go`:

```go
func TestAllocBudgetClientIP(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	fctx.SetRemoteAddr(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5000})
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.ClientIP", 0, func() { _ = c.ClientIP() })
}
```

`alloc_test.go` needs `net` in its import block if it is not there already.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'ClientIP' 2>&1 | head -20`
Expected: compile failure, `c.ClientIP undefined`.

- [ ] **Step 3: Implement**

Append to `ctx.go`, after `Body`, and add `"net"` to its imports:

```go
// ClientIP returns the address the request came from: the connection's peer,
// never a header.
//
// Behind a reverse proxy that is the proxy's address. X-Forwarded-For is not
// consulted, because any client can set it; a middleware that resolves it
// against a known number of trusted hops rewrites the connection address with
// fasthttp's SetRemoteAddr, and this accessor then reports the result.
//
// Borrowed: net.IP is a []byte, and it is valid only until the handler returns.
func (c *Ctx) ClientIP() net.IP {
	c.poison.check()
	return c.fctx.RemoteIP()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test`
Expected: green.

- [ ] **Step 5: Break the budget on purpose, then restore it**

Same procedure as task 1, step 5, on `TestAllocBudgetClientIP`. Record the failure text.

- [ ] **Step 6: Commit**

```bash
git add ctx.go ctx_test.go ctx_context_test.go alloc_test.go
git commit -m "ctx: add ClientIP, the connection address and never a header

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: the base context, `c.Context()` and `c.SetContext()`

**Files:**
- Modify: `app.go` (`App` struct, `New`), `ctx.go` (`Ctx` struct, `reset`, two new methods)
- Test: `ctx_test.go`, `alloc_test.go`

**Interfaces:**
- Consumes: nothing from tasks 1 and 2.
- Produces:
  - `App.baseCtx context.Context` and `App.cancelBase context.CancelFunc` — unexported; task 4 calls `cancelBase`.
  - `func (c *Ctx) Context() context.Context`
  - `func (c *Ctx) SetContext(ctx context.Context)` — panics on nil.

- [ ] **Step 1: Write the failing tests**

Append to `ctx_test.go`:

```go
// TestContextIsTheAppsBaseContextByDefault also pins that the budget tests'
// fixture is not enough for Context: it reads c.app, so a Ctx reset with a nil
// App has no context to return.
func TestContextIsTheAppsBaseContextByDefault(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)

	got := c.Context()
	if got == nil {
		t.Fatal("Context() = nil, want the App's base context")
	}
	if err := got.Err(); err != nil {
		t.Errorf("Context().Err() = %v, want nil: nothing has force-closed", err)
	}
	select {
	case <-got.Done():
		t.Error("Context() is already done, want live")
	default:
	}
}

func TestSetContextIsWhatContextReturns(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)

	type key struct{}
	want := context.WithValue(c.Context(), key{}, "v")
	c.SetContext(want)

	if got := c.Context(); got != want {
		t.Errorf("Context() = %v, want the context SetContext was given", got)
	}
	if got := c.Context().Value(key{}); got != "v" {
		t.Errorf("Context().Value(key) = %v, want %q", got, "v")
	}
}

// TestResetClearsTheContext keeps a released Ctx from handing a finished
// request's context to the next one.
func TestResetClearsTheContext(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)
	c.SetContext(context.WithValue(c.Context(), struct{}{}, "v"))

	c.reset(app, fctx)

	if got := c.Context(); got != app.baseCtx {
		t.Errorf("Context() after reset = %v, want the App's base context", got)
	}
}

// TestAContextOutlivesItsHandler pins the D4 exception: reset clears the field,
// which must not disturb a context a handler already took. Every other accessor
// on Ctx hands out something that dies here; this one does not.
func TestAContextOutlivesItsHandler(t *testing.T) {
	app := New()
	var kept context.Context
	app.GET("/keep", func(c *Ctx) error {
		kept = c.Context()
		return nil
	})
	dispatchCtx(app, "GET", "/keep")

	if kept == nil {
		t.Fatal("handler did not run")
	}
	if err := kept.Err(); err != nil {
		t.Errorf("Err() = %v after the handler returned, want nil: the context is owned, not borrowed", err)
	}
}

func TestSetContextPanicsOnNil(t *testing.T) {
	app := New()
	c := &Ctx{}
	c.reset(app, &fasthttp.RequestCtx{})

	v := mustPanic(t, "SetContext(nil)", func() { c.SetContext(nil) })
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, "rice: ") {
		t.Errorf("SetContext(nil) panicked with %v, want a string starting %q", v, "rice: ")
	}
}
```

`ctx_test.go` needs `context` and `strings` in its imports.

Add to `alloc_test.go`:

```go
// TestAllocBudgetContext uses a real App, unlike the other budgets in this
// file: Context falls back to the App's base context, so a Ctx reset with a nil
// App would panic rather than measure anything.
func TestAllocBudgetContext(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)

	budget(t, "Ctx.Context", 0, func() { _ = c.Context() })

	ctx := context.Background()
	budget(t, "Ctx.SetContext", 0, func() { c.SetContext(ctx) })
}
```

`alloc_test.go` needs `context` in its imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'Context' 2>&1 | head -20`
Expected: compile failure, `c.Context undefined` and `app.baseCtx undefined`.

- [ ] **Step 3: Implement the App fields**

In `app.go`, add to the `App` struct, directly after the `hooksStarted` field:

```go
	// baseCtx is what Ctx.Context returns when no middleware has replaced it.
	// It is cancelled by closeConns, so a handler still running when Shutdown
	// gives up learns that its response is about to be discarded. It is never
	// cancelled by a clean shutdown, which has no handler left to tell, nor by a
	// failed start, which leaves Serve retryable. See ADR-0010.
	//
	// It carries no values. rice's per-request store is Set and Get; a second
	// store with a different lifetime would be one too many.
	baseCtx    context.Context
	cancelBase context.CancelFunc
```

In `New`, before the `a.pool.New` line:

```go
	a.baseCtx, a.cancelBase = context.WithCancel(context.Background())
```

- [ ] **Step 4: Implement the Ctx field and methods**

In `ctx.go`, add to the `Ctx` struct after `app`:

```go
	// ctx is nil unless a middleware called SetContext. Context falls back to
	// the App's base context, so binding a request costs no assignment here
	// beyond the clearing reset already owes.
	ctx context.Context
```

In `reset`, after `c.fctx = fctx`:

```go
	c.ctx = nil
```

Append the two methods after `ClientIP` (or after `Body` if task 2 has not landed):

```go
// Context returns the context to pass to work the request triggers: a database
// query, an outbound HTTP call.
//
// Unlike everything else reachable from a Ctx, the returned context is owned,
// not borrowed. It belongs to the App and stays valid after the handler
// returns.
//
// It is cancelled when a Shutdown gives up waiting and force-closes, meaning
// the response is about to be discarded. It is not cancelled when the client
// disconnects: fasthttp does not report that, and detecting it would cost a
// channel per request. It carries no deadline and no values of its own.
//
// See docs/adr/0010-request-context-cancels-at-force-close.md.
func (c *Ctx) Context() context.Context {
	c.poison.check()
	if c.ctx != nil {
		return c.ctx
	}
	return c.app.baseCtx
}

// SetContext replaces what Context returns for the rest of this request.
//
// It is how a middleware adds a deadline or a value. Derive from c.Context(),
// not from context.Background(): a context derived from Background silently
// drops the force-close signal.
//
// A nil context panics, as every other configuration mistake in rice does.
func (c *Ctx) SetContext(ctx context.Context) {
	c.poison.check()
	if ctx == nil {
		panic("rice: SetContext: context is nil")
	}
	c.ctx = ctx
}
```

`ctx.go` needs `context` in its imports.

Note the order inside `SetContext`: `poison.check()` comes before the nil check, because `TestEveryCtxMethodPanicsAfterRelease` calls every exported method with zero-valued arguments on a released `Ctx` and expects `errUseAfterRelease`, not the nil panic.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `make test`
Then: `go test -race ./...`
Then: `go test -tags ricedebug ./...` — this is what proves `poison.check()` is ordered correctly in `SetContext`.
Expected: all three green.

- [ ] **Step 6: Confirm the pooled Ctx did not get more expensive**

`Ctx` grew by one interface field, two words. Run:

```bash
go test -run 'TestNewCtxStaysWithinThreeAllocations|TestAllocBudget' ./... -v 2>&1 | tail -30
```

Expected: unchanged and green. If `TestNewCtxStaysWithinThreeAllocations` fails, stop and report — the spec assumed this would not happen and being wrong about it is a design finding, not something to fix by raising the ceiling.

- [ ] **Step 7: Break both budgets on purpose, then restore them**

Same procedure as task 1, step 5, for `Ctx.Context` and `Ctx.SetContext`. Record both failure texts.

- [ ] **Step 8: Commit**

```bash
git add app.go ctx.go ctx_test.go alloc_test.go
git commit -m "ctx: add Context and SetContext, backed by a base context the App owns

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: cancel the base context when a shutdown force-closes

**Files:**
- Modify: `conns.go` (`closeConns`)
- Test: `lifecycle_test.go`

**Interfaces:**
- Consumes: `App.cancelBase` from task 3.
- Produces: the cancellation contract ADR-0010 records. Nothing later in this plan depends on a new symbol.

- [ ] **Step 1: Write the failing tests**

Append to `lifecycle_test.go`. The helper first:

```go
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
```

Then the four tests. Three use the `ctxApp`/`sendCtx` pair above; the second-shutdown test
reuses `slowApp`, `sendSlow` and `waitUntilRefused`, the M7 helpers already in
`lifecycle_test.go`, because its handler must stay in flight across both `Shutdown` calls
rather than return as soon as the first cancels it:

```go
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
// safe: context.CancelFunc is idempotent, and Shutdown may be called more
// than once, including concurrently.
//
// It uses slowApp rather than ctxApp: ctxApp's handler watches its own
// context and returns as soon as the first force-close cancels it, so a
// second call would find nothing left in flight and drain cleanly, never
// reaching closeConns a second time. slowApp's handler only watches a gate
// and never returns on its own, so the connection stays busy across both
// calls.
//
// The two calls are concurrent, not sequential, because a sequential second
// call cannot exercise this at all: fasthttp's own ShutdownWithContext
// returns nil at once once its listener list is nil (server.go, checked
// before it ever polls open connections), and the first call already made it
// nil. A sequential second call was tried while writing this test and
// confirmed to return nil immediately — it is exactly the "a later call
// finds nothing left to drain, returns nil, and runs no hooks" case Shutdown's
// own doc comment describes, and it never reaches closeConns. Concurrently,
// the second call instead loses the race for Shutdown's turn and force-closes
// on its own ctx while the first is still draining on its own — so each
// independently reaches closeConns, and therefore cancelBase, without either
// waiting on the other.
//
// The second call's grace is far shorter than the first's, so which call
// takes the turn is not left to scheduling luck: waitUntilRefused confirms
// the first call has already started draining — closing the listener is the
// first thing its own Shutdown call does — before the second call is even
// made, so the second is guaranteed to find the turn taken and fall back to
// timing out on its own ctx well before the first's ctx ends.
func TestASecondShutdownDoesNotPanicOnTheCancel(t *testing.T) {
	app, inFlight, _ := slowApp(t)
	addr, _ := serve(t, app)
	sendSlow(t, addr)
	within(t, 2*time.Second, "the handler receiving the request", inFlight)

	firstCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		firstCh <- app.Shutdown(ctx)
	}()
	waitUntilRefused(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := app.Shutdown(ctx); !errors.Is(err, rice.ErrShutdownTimeout) {
		t.Errorf("second Shutdown() = %v, want an error wrapping ErrShutdownTimeout", err)
	}

	if err := within(t, 2*time.Second, "the first Shutdown returning", firstCh); !errors.Is(err, rice.ErrShutdownTimeout) {
		t.Errorf("first Shutdown() = %v, want an error wrapping ErrShutdownTimeout", err)
	}
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -run 'AnInFlightContext|SecondShutdownDoesNotPanic|RetriedAfterAFailedStart' ./... 2>&1 | head -30`
Expected: `TestATimedOutDrainCancelsAnInFlightContext` fails — the handler reports `"released"` never, and `within` reports `the handler noticing did not happen within 2s`, because nothing cancels the base context yet. The other three pass already; they are the guards that the change does not overreach.

- [ ] **Step 3: Implement**

In `conns.go`, extend `closeConns`. The cancel goes after the lock is released and before the sockets close:

```go
func (a *App) closeConns() {
	a.connMu.Lock()
	a.forceClosed = true
	conns := make([]net.Conn, 0, len(a.conns))
	for c := range a.conns {
		conns = append(conns, c)
	}
	a.connMu.Unlock()

	// Cancel before closing, so a handler blocked on a database call learns to
	// abandon its work from its context rather than from a write to a socket
	// that is already gone. The order is the intent; no test asserts it,
	// because a test that raced the socket's death would be flaky.
	//
	// It happens here rather than at Shutdown's two call sites because
	// force-closing and cancelling are one event, and a third call site would
	// otherwise be able to forget one half of it. cancelBase runs outside
	// connMu: calling a callback under a lock is how deadlocks are built.
	a.cancelBase()

	for _, c := range conns {
		_ = c.Close()
	}
}
```

Extend the function's doc comment above it with one sentence: `It also cancels the App's base context, which is what tells a handler still running that its response is about to be discarded.`

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test`
Then: `go test -race -count=2 ./...` — `-count=2` because these tests coordinate goroutines and a flake would otherwise hide.
Expected: green both times.

- [ ] **Step 5: Commit**

```bash
git add conns.go lifecycle_test.go
git commit -m "app: cancel the request context when a shutdown force-closes

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: ADR-0010 and the documentation

**Files:**
- Create: `docs/adr/0010-request-context-cancels-at-force-close.md`
- Modify: `docs/adr/README.md`, `doc.go`, `docs/02-architecture.md`, `docs/03-core-concepts.md`, `docs/04-roadmap.md`, `docs/05-performance-model.md`, `docs/progress.md`

**Interfaces:**
- Consumes: the four budget failure texts recorded in tasks 1, 2 and 3; the behaviour implemented in tasks 1–4.
- Produces: nothing code depends on.

- [ ] **Step 1: Write ADR-0010**

Follow the format in `docs/adr/README.md` exactly: `Context`, `Decision`, `Alternatives`, `Consequences`. Status `Accepted`, date `2026-09-20`. It must contain, because these are the findings the decision rests on:

- fasthttp v1.73.0's `RequestCtx` implements `context.Context` at the scope of the server: `Deadline()` returns `(time.Time{}, false)` always, `Done()` returns `ctx.s.done`, and `Value(key)` is `UserValue(key)`.
- `ShutdownWithContext` calls `close(s.done)` at `server.go:2072`, right after closing the listeners and before any draining.
- fasthttp's own comment on why `Done()` is server-wide: "creating a new channel for every request is just too expensive".
- Alternatives, each with why it lost: **the passthrough** (cancels in-flight requests when `Shutdown` starts, a regression against M7's drain, and its `Value` reads fasthttp's user values rather than rice's store); **`*Ctx` implementing `context.Context`**, as gin does (fuses the borrow contract with context lifetime — a driver retaining the context retains a pooled `*Ctx`, the use-after-release class ADR-0005's poisoning exists to catch); **a per-request cancellation channel** giving cancel-on-disconnect (the cost fasthttp itself refuses, and principle 1 with it).
- Consequences: what it makes easy (`middleware.Timeout` has somewhere to attach a deadline; a cut-off handler can stop work), what it makes hard (no cancel-on-disconnect, ever, while rice is on fasthttp — a user migrating from `net/http` must be told), what it forecloses (nothing permanently: a cause can be added with `context.WithCancelCause` later).
- A "revisit if" line: if fasthttp gives `RequestCtx` a per-request `Done`, this decision is re-read.

Add the row to the index table in `docs/adr/README.md`:

```markdown
| [0010](0010-request-context-cancels-at-force-close.md) | The request context cancels at force-close, not at disconnect | Accepted |
```

- [ ] **Step 2: Correct the borrow contract in `doc.go`**

The sentence "Every value reachable from a *Ctx is borrowed, not owned" is now false. Add the named exception immediately after the existing borrowed/owned paragraph:

```go
// Context is the one exception. The context.Context it returns belongs to the
// App, not to the request, and stays valid after the handler returns. It is
// cancelled when a Shutdown gives up and force-closes; it is never cancelled by
// a client disconnecting, which fasthttp does not report. See
// docs/adr/0010-request-context-cancels-at-force-close.md.
```

In the `# Lifecycle` section, after the sentence about force-closing and lost responses, add:

```go
// A handler still running then sees its Context cancelled, which is the only
// signal rice can give it that its response is about to be discarded.
```

- [ ] **Step 3: Update the numbered docs**

- `docs/03-core-concepts.md` §2 (`Ctx`): add `Context`, `SetContext`, `ClientIP` and `NoContent` to the method listing, then prose covering: the ownership exception, that cancellation means force-close and never disconnect, the "derive from `c.Context()`" rule, and that `ClientIP` reads no headers and `middleware.RealIP` is what will.
- `docs/05-performance-model.md`: four rows in the `Ctx` table, all budget 0, status `MEASURED after M8`, enforced by `TestAllocBudgetContext`, `TestAllocBudgetClientIP` and `TestAllocBudgetNoContent`. Below the table, a paragraph in the style of the one the accessors added: name the four failure texts recorded when each budget was broken on purpose.
- `docs/02-architecture.md`: the layout lines for `ctx.go` (`Query/Header/Body` → add `Context/ClientIP`) and `ctx_response.go` (add `NoContent`).
- `docs/04-roadmap.md`, *Done after M8*: one entry for this group, naming ADR-0010 and noting the cut scope (cookies, multipart, `Redirect`) is not promised.

- [ ] **Step 4: Write the progress entry**

Prepend to `docs/progress.md`, above the newest entry, using the file's shape (`Did` / `Learned` / `Measured` / `Next`), dated the day the work lands, milestone `post-M8`. It must say:

- **Did:** the four methods, the base context, the cancel in `closeConns`, ADR-0010, and the scope that was cut and why.
- **Learned:** that fasthttp's `context.Context` is the server's, not the request's, and that a passthrough would have cancelled in-flight requests at the moment `Shutdown` was called — a feature that looks free and would have silently undone M7's drain. Also that `Context` is the first accessor in rice returning something the caller may keep, so a contract written as an absolute needed a named exception rather than a footnote.
- **Measured:** the four budgets at 0 and their four break-on-purpose failure texts; whether `TestNewCtxStaysWithinThreeAllocations` moved when `Ctx` grew by two words; the root package coverage from `make cover`.
- **Next:** spec 2, binding and validation.

- [ ] **Step 5: Verify the whole branch**

```bash
make test && go test -race ./... && go test -tags ricedebug ./... && make lint && make cover
```
Expected: all green. Record the coverage figure for the progress entry; if it dropped, say so there rather than rounding it away.

- [ ] **Step 6: Commit**

```bash
git add docs doc.go
git commit -m "docs: ADR-0010 and the docs for Context, ClientIP and NoContent

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage.** D1 → task 3 steps 3–4. D2 → task 4 step 3. D3 → task 4 tests 1, 3 and 4. D4 → task 3's `TestResetClearsTheContext` plus task 5 step 2. D5 → task 3's `TestSetContextPanicsOnNil`. D6 → task 1. D7 → task 2. D8 → the `baseCtx` field comment in task 3 step 3, and `docs/03-core-concepts.md` in task 5. D9 → no task adds an `Option`; the global constraints say to stop if one seems necessary. Budgets → tasks 1, 2, 3. Testing section → tasks 1–4. Benchmarks (none) → no task records one. Documentation → task 5. Exit criteria → task 5 step 5.

**Gap found and closed inline.** The spec's test list includes "a context obtained in a handler is still usable after the handler returns". `TestResetClearsTheContext` covers the field being cleared but not that the value already handed out survives, so `TestAContextOutlivesItsHandler` was added to task 3, step 1.

**Placeholder scan.** No "TBD", no "add error handling", no "similar to task N". Task 5 describes documents rather than showing every line, which is the one place prose is the deliverable; the required content is enumerated so nothing is left to taste.

**Type consistency.** `baseCtx`/`cancelBase` are named identically in task 3 (definition), task 4 (use) and task 5 (documentation). `Context`, `SetContext`, `ClientIP`, `NoContent` keep their signatures across tasks. The test helper is `ctxApp` in both its definition and its four uses; `sendCtx` matches `sendSlow`'s existing shape. `within` and `mustPanic` are used with their real signatures from `lifecycle_test.go:33` and `route_test.go:99`.
