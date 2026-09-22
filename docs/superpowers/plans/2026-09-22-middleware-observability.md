# Middleware Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make rice's access log tell the truth: two core changes (application middleware runs on route misses; `c.HandleError` settles a request's outcome), then three middleware — `RealIP`, `RequestID`, `Logger`.

**Architecture:** `App` gains a precompiled miss chain — the application's middleware wrapped around a handler returning `ErrNotFound` — that `handle` runs whenever the lookup fails. `Ctx` gains a `handled` flag and `HandleError`, which runs the `ErrorHandler` immediately so a middleware can read the final status. The three middleware live in the existing opt-in `middleware/` package.

**Tech Stack:** Go 1.25, fasthttp v1.73.0, `log/slog` and `crypto/rand` from the standard library. No new module dependency.

**Spec:** [docs/superpowers/specs/2026-09-22-middleware-observability-design.md](../specs/2026-09-22-middleware-observability-design.md)

## Global Constraints

- **Branch:** `middleware-observability`, already created, already holding the spec commit. Do not commit to `main`.
- **TDD.** Every test is written and seen failing before the code that satisfies it, except where a step says the test is a guard expected to pass already.
- **`poison.check()` is the first statement of every exported `Ctx` method**, before any argument handling. `ricedebug_test.go` calls every exported method with zero-valued arguments on a released `Ctx` and expects the use-after-release panic.
- **`TestAllocBudget404` must stay at 0.** It pins "a miss costs what a hit costs, and both cost nothing".
- **Middleware budgets are pinned exactly, in both directions**, the way `binding/alloc_test.go` pins `binding.JSON`: the measured `N` passes, `N-1` fails, `N+1` fails. **Declare the constant typed from the start — `const want float64 = 0` — never untyped.** An untyped constant passed to `%.0f` fails `go vet`'s printf check at build time, so the deliberately-red first run never happens; the previous branch's plan made exactly this mistake.
- **Configuration mistakes panic at construction** with a message prefixed `rice: `, as `WithMaxBodySize`, `WithErrorHandler` and the hook registrars already do.
- **Status constants and header names:** use fasthttp's (`fasthttp.StatusNotFound` etc.), matching `errors.go`.
- **Run `make test` before each commit**, plus `go test -tags ricedebug ./...` for tasks 1 and 2.
- **Commit messages:** subject line, blank line, optional body, blank line, `Co-Authored-By` trailer on its own line. A commit on the previous branch glued the trailer onto the subject.

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `app.go` | `miss` field; `New` sets it; `handle` runs it on a miss and respects `c.handled` | 1, 2 |
| `build.go` | compiles the miss chain from the application's middleware | 1 |
| `miss_test.go` (`package rice`, **new**) | the miss chain's behaviour | 1 |
| `ctx.go` | `handled` field; `reset` clears it; `HandleError` | 2 |
| `handle_error_test.go` (`package rice`, **new**) | `HandleError`'s behaviour, and the status table | 2 |
| `alloc_test.go` (`package rice`) | the miss-chain and `HandleError` budgets | 1, 2 |
| `middleware/realip.go` (**new**) | `RealIP` | 3 |
| `middleware/realip_test.go` (**new**) | `RealIP`, including the keep-alive test | 3 |
| `middleware/requestid.go` (**new**) | `RequestID`, `RequestIDFrom` | 4 |
| `middleware/requestid_test.go` (**new**) | `RequestID` | 4 |
| `middleware/logger.go` (**new**) | `Logger` | 5 |
| `middleware/logger_test.go` (**new**) | `Logger`, including the end-to-end 404 test | 5 |
| `middleware/alloc_test.go` (**new**) | the three middleware budgets | 3, 4, 5 |
| `docs/…`, `README.md`, `middleware/doc.go` | ADR-0012, ADR-0013 and every document the change touches | 6 |

Task 2 depends on Task 1 (its status table includes the 404 row). Task 5 depends on Tasks 1–4. Task 6 depends on all.

**Helper names across `middleware_test` files must not collide** — they share one package. `recover_test.go` already defines `dispatch(t, app, path)`. The new files use `dispatchFrom` (Task 3), `serveApp` (Task 3), `dispatchWithID` (Task 4) and `logRequest` (Task 5).

---

### Task 1: application middleware runs on route misses

**Files:**
- Modify: `app.go` (the `App` struct, `New`, `handle`)
- Modify: `build.go` (`build`)
- Create: `miss_test.go` (`package rice`)
- Modify: `alloc_test.go`

**Interfaces:**
- Consumes: `chain.Compile`, `ErrNotFound`, the existing `dispatchCtx(app, method, path)` helper in `panic_test.go`.
- Produces: `App.miss Handler` (unexported). `handle` dispatches through it on every lookup failure. Task 2 edits the same `handle` line this task writes.

- [ ] **Step 1: Write the failing tests**

Create `miss_test.go`:

```go
package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// A route miss used to go straight to the error funnel, so no middleware ever
// saw it. That made a request logger blind to 404s and made CORS preflight
// impossible. See ADR-0012.

func TestAppMiddlewareRunsOnAMiss(t *testing.T) {
	ran := false
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			ran = true
			return next(c)
		}
	})
	app.GET("/ok", func(c *Ctx) error { return nil })

	fctx := dispatchCtx(app, "GET", "/nope")

	if !ran {
		t.Error("application middleware did not run on a route miss")
	}
	if code := fctx.Response.StatusCode(); code != 404 {
		t.Errorf("status = %d, want 404", code)
	}
}

// TestGroupMiddlewareDoesNotRunOnAMiss pins that only the application's own
// middleware wraps the miss chain: no group matched, so no group's middleware
// has any claim on the request.
func TestGroupMiddlewareDoesNotRunOnAMiss(t *testing.T) {
	ran := false
	app := New()
	api := app.Group("/api", func(next Handler) Handler {
		return func(c *Ctx) error {
			ran = true
			return next(c)
		}
	})
	api.GET("/users", func(c *Ctx) error { return nil })

	dispatchCtx(app, "GET", "/api/nope")

	if ran {
		t.Error("group middleware ran on a miss; only application middleware may")
	}
}

func TestAWrongMethodTakesTheMissPath(t *testing.T) {
	ran := false
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			ran = true
			return next(c)
		}
	})
	app.GET("/users", func(c *Ctx) error { return nil })

	fctx := dispatchCtx(app, "DELETE", "/users")

	if !ran {
		t.Error("application middleware did not run for a wrong method on an existing path")
	}
	if code := fctx.Response.StatusCode(); code != 404 {
		t.Errorf("status = %d, want 404", code)
	}
}

// TestAnUnbuiltAppAnswersAMiss pins why the miss chain is initialised in New
// and not only in Build: the dispatch path is reachable before Build, and this
// package's own tests call handle on unbuilt Apps throughout. A miss chain that
// existed only after Build would be nil here.
func TestAnUnbuiltAppAnswersAMiss(t *testing.T) {
	app := New()
	app.GET("/ok", func(c *Ctx) error { return nil })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/nope")
	app.handle(fctx) // deliberately no Build

	if code := fctx.Response.StatusCode(); code != 404 {
		t.Errorf("status = %d, want 404", code)
	}
}

func TestAMissWithNoMiddlewareIsUnchanged(t *testing.T) {
	app := New()
	app.GET("/ok", func(c *Ctx) error { return nil })

	fctx := dispatchCtx(app, "GET", "/nope")

	if code := fctx.Response.StatusCode(); code != 404 {
		t.Errorf("status = %d, want 404", code)
	}
	if body := string(fctx.Response.Body()); body != "Not Found" {
		t.Errorf("body = %q, want %q", body, "Not Found")
	}
}

// TestParamIsEmptyOnAMiss rests on the router's documented contract: "params
// is filled only on a match. On a miss it is left as the caller passed it", and
// the caller passes a Ctx that reset has just cleared.
func TestParamIsEmptyOnAMiss(t *testing.T) {
	var got []byte
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			got = c.Param("id")
			return next(c)
		}
	})
	app.GET("/users/:id/posts", func(c *Ctx) error { return nil })

	dispatchCtx(app, "GET", "/users/42/nope") // matches :id, then misses

	if len(got) != 0 {
		t.Errorf("Param(\"id\") = %q on a miss, want empty", got)
	}
}
```

Add to `alloc_test.go`, directly after `TestAllocBudget404`:

```go
// TestAllocBudget404WithAppMiddleware pins that the miss chain costs nothing:
// it is compiled once at Build, and ErrNotFound is a package variable.
func TestAllocBudget404WithAppMiddleware(t *testing.T) {
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error { return next(c) }
	})
	app.GET("/ok", func(c *Ctx) error { return nil })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/missing")

	budget(t, "404 through a miss chain with application middleware", 0, func() {
		app.handle(fctx)
	})
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -run 'Miss|UnbuiltApp|WrongMethod|ParamIsEmpty|404WithAppMiddleware' -v 2>&1 | grep -E '^(--- |FAIL|ok)'`

Expected: `TestAppMiddlewareRunsOnAMiss` and `TestAWrongMethodTakesTheMissPath` FAIL — application middleware did not run. The other four are guards that pass already: `TestGroupMiddlewareDoesNotRunOnAMiss`, `TestAnUnbuiltAppAnswersAMiss`, `TestAMissWithNoMiddlewareIsUnchanged` and `TestParamIsEmptyOnAMiss` pin behaviour that must survive the change, and `TestAllocBudget404WithAppMiddleware` passes today only because the middleware never runs. Say so in your report.

- [ ] **Step 3: Implement**

In `app.go`, add to the `App` struct, directly after `errorHandler`:

```go
	// miss answers every request the lookup did not match. New sets it to
	// notFound so that an unbuilt App, which the package's tests dispatch to
	// throughout, still has one; Build replaces it with notFound wrapped in the
	// application's middleware. Group and route middleware never wrap it: no
	// group matched, so none has a claim on the request. See ADR-0012.
	miss Handler
```

Add this package-level function to `app.go`, beside `transportError`:

```go
// notFound is the handler a route miss runs. Returning the package-level
// ErrNotFound rather than constructing an error keeps the miss path at zero
// allocations.
func notFound(*Ctx) error { return ErrNotFound }
```

In `New`, add to the composite literal:

```go
		miss:         notFound,
```

In `handle`, replace:

```go
	h, ok := a.lookup(fctx.Method(), fctx.Path(), &c.params)
	if !ok {
		a.callErrorHandler(c, ErrNotFound)
		return
	}

	if err := h(c); err != nil {
		a.callErrorHandler(c, err)
	}
```

with:

```go
	h, ok := a.lookup(fctx.Method(), fctx.Path(), &c.params)
	if !ok {
		// A miss runs the application's middleware like any route, so a
		// logger sees 404s and a CORS middleware can answer a preflight for a
		// path that has no OPTIONS route. See ADR-0012.
		h = a.miss
	}

	if err := h(c); err != nil {
		a.callErrorHandler(c, err)
	}
```

In `build.go`, at the end of `build`, before `a.built = true`:

```go
	// The miss chain gets the application's middleware and nothing else. With
	// none, chain.Compile returns notFound itself and a miss behaves exactly as
	// it did before this chain existed.
	a.miss = chain.Compile(Handler(notFound), a.mws)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test`, then `go test -tags ricedebug ./...`
Expected: green, including all six new tests and the new budget.

- [ ] **Step 5: Confirm the existing budgets did not move**

Run: `go test . -run 'TestAllocBudget|TestNewCtxStaysWithinThreeAllocations' -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: every existing budget still passes, `TestAllocBudget404` at 0 included. **Do not raise a ceiling to make one pass** — if a budget fails, stop and report it.

- [ ] **Step 6: Commit**

```bash
git add app.go build.go miss_test.go alloc_test.go
git commit -F - <<'EOF'
app: run application middleware on route misses

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 2: `c.HandleError` settles a request's outcome

**Files:**
- Modify: `ctx.go` (the `Ctx` struct, `reset`, a new method)
- Modify: `app.go` (`handle`)
- Create: `handle_error_test.go` (`package rice`)
- Modify: `alloc_test.go`

**Interfaces:**
- Consumes: the miss chain from Task 1, `callErrorHandler`, `dispatchCtx`, `mustPanic`.
- Produces: `func (c *Ctx) HandleError(err error)`. Task 5's `Logger` calls it.

- [ ] **Step 1: Write the failing tests**

Create `handle_error_test.go`:

```go
package rice

import (
	"errors"
	"testing"

	"github.com/valyala/fasthttp"
)

// statusRecorder is what a request logger does: it settles the error with
// HandleError, then reads the status the client will receive.
func statusRecorder(seen *int) Middleware {
	return func(next Handler) Handler {
		return func(c *Ctx) error {
			if err := next(c); err != nil {
				c.HandleError(err)
			}
			*seen = c.fctx.Response.StatusCode()
			return nil
		}
	}
}

// TestMiddlewareSeesTheFinalStatus is the regression test for the whole
// branch. Before HandleError and the miss chain existed, a middleware reading
// the status after next saw 200, 200, 200 and nothing, while the client
// received 200, 418, 500 and 404: the funnel writes the status only after the
// whole chain has returned. See ADR-0013.
func TestMiddlewareSeesTheFinalStatus(t *testing.T) {
	var seen int
	app := New()
	app.Use(statusRecorder(&seen))
	app.GET("/ok", func(c *Ctx) error { return c.String(200, "ok") })
	app.GET("/teapot", func(c *Ctx) error { return NewHTTPError(418, "teapot") })
	app.GET("/boom", func(c *Ctx) error { return errors.New("db down") })

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/ok", 200},
		{"/teapot", 418},
		{"/boom", 500},
		{"/nope", 404},
	} {
		seen = -1
		fctx := dispatchCtx(app, "GET", tc.path)
		if seen != tc.want {
			t.Errorf("%s: middleware saw %d, want %d", tc.path, seen, tc.want)
		}
		if got := fctx.Response.StatusCode(); got != tc.want {
			t.Errorf("%s: client received %d, want %d", tc.path, got, tc.want)
		}
	}
}

// countingApp returns an App whose ErrorHandler counts its calls.
func countingApp(calls *int) *App {
	return New(WithErrorHandler(func(c *Ctx, err error) {
		*calls++
		DefaultErrorHandler(c, err)
	}))
}

// TestHandleErrorRunsTheErrorHandlerExactlyOnce covers the case the handled
// flag exists for: a middleware that settles the error and then still returns
// it. Without the flag, handle would run the funnel a second time and write
// the response twice.
func TestHandleErrorRunsTheErrorHandlerExactlyOnce(t *testing.T) {
	var calls int
	app := countingApp(&calls)
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			err := next(c)
			c.HandleError(err)
			return err // returned anyway, on purpose
		}
	})
	app.GET("/teapot", func(c *Ctx) error { return NewHTTPError(418, "teapot") })

	fctx := dispatchCtx(app, "GET", "/teapot")

	if calls != 1 {
		t.Errorf("ErrorHandler ran %d times, want exactly 1", calls)
	}
	if code := fctx.Response.StatusCode(); code != 418 {
		t.Errorf("status = %d, want 418", code)
	}
}

func TestHandleErrorWritesTheResponseNow(t *testing.T) {
	var during int
	app := New()
	app.GET("/teapot", func(c *Ctx) error {
		c.HandleError(NewHTTPError(418, "teapot"))
		during = c.fctx.Response.StatusCode()
		return nil
	})

	dispatchCtx(app, "GET", "/teapot")

	if during != 418 {
		t.Errorf("status read straight after HandleError = %d, want 418", during)
	}
}

func TestHandleErrorNilIsANoOp(t *testing.T) {
	var calls int
	app := countingApp(&calls)
	app.GET("/ok", func(c *Ctx) error {
		c.HandleError(nil)
		return c.String(200, "ok")
	})

	fctx := dispatchCtx(app, "GET", "/ok")

	if calls != 0 {
		t.Errorf("ErrorHandler ran %d times for HandleError(nil), want 0", calls)
	}
	if code := fctx.Response.StatusCode(); code != 200 {
		t.Errorf("status = %d, want 200", code)
	}
}

func TestHandleErrorTwiceKeepsTheFirst(t *testing.T) {
	var calls int
	app := countingApp(&calls)
	app.GET("/x", func(c *Ctx) error {
		c.HandleError(NewHTTPError(418, "first"))
		c.HandleError(NewHTTPError(409, "second"))
		return nil
	})

	fctx := dispatchCtx(app, "GET", "/x")

	if calls != 1 {
		t.Errorf("ErrorHandler ran %d times, want 1", calls)
	}
	if code := fctx.Response.StatusCode(); code != 418 {
		t.Errorf("status = %d, want 418: the first HandleError settles the request", code)
	}
}

// TestAPanicAfterHandleErrorIsStill500 pins that the panic path ignores the
// handled flag. M5 established that a recovered panic anywhere in the chain is
// always a 500, and the response has not been sent yet, so overwriting it is
// correct.
func TestAPanicAfterHandleErrorIsStill500(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error {
		c.HandleError(NewHTTPError(418, "teapot"))
		panic("after settling")
	})

	fctx := dispatchCtx(app, "GET", "/x")

	if code := fctx.Response.StatusCode(); code != 500 {
		t.Errorf("status = %d, want 500", code)
	}
}

// TestResetClearsHandled keeps a pooled Ctx from carrying one request's flag
// into the next, which would silently suppress that request's error response.
func TestResetClearsHandled(t *testing.T) {
	app := New()
	c := &Ctx{}
	c.reset(app, &fasthttp.RequestCtx{})
	c.HandleError(NewHTTPError(418, "teapot"))

	c.reset(app, &fasthttp.RequestCtx{})

	if c.handled {
		t.Error("handled survived reset; the next request's error would never be answered")
	}
}
```

Add to `alloc_test.go`:

```go
// TestAllocBudgetHandleError pins HandleError at zero allocations of its own,
// measured with ErrNotFound through the default funnel, which is itself free.
// The flag is cleared inside the measured closure: HandleError is a no-op once
// a request is settled, so without clearing it every iteration after the
// first would measure the no-op instead of the work.
func TestAllocBudgetHandleError(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)

	budget(t, "Ctx.HandleError", 0, func() {
		c.handled = false
		c.HandleError(ErrNotFound)
	})
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -run 'HandleError|FinalStatus|ResetClearsHandled|AfterHandleError' 2>&1 | head -20`
Expected: compile failure — `c.HandleError undefined` and `c.handled undefined`.

- [ ] **Step 3: Implement**

In `ctx.go`, add to the `Ctx` struct, after `ctx`:

```go
	// handled is set by HandleError. handle reads it so that an error a
	// middleware has already settled is not answered a second time.
	handled bool
```

In `reset`, beside `c.ctx = nil`:

```go
	c.handled = false
```

Add the method after `SetContext`:

```go
// HandleError answers err through the App's ErrorHandler now, instead of after
// the chain has returned.
//
// The funnel normally runs only once every middleware has returned, so a
// middleware that reads the response status after next(c) reads it too early:
// a request that ends in a 500 still shows 200 there. A request logger calls
// HandleError first and then reads the status the client will receive. See
// docs/adr/0013-middleware-can-settle-a-request.md.
//
// A request is settled once. A nil error does nothing, and a second call does
// nothing: the first settles the request. An error returned after it has been
// handled is not answered again. A panic is still a 500 even after a request
// was settled.
func (c *Ctx) HandleError(err error) {
	c.poison.check()
	if err == nil || c.handled {
		return
	}
	c.handled = true
	c.app.callErrorHandler(c, err)
}
```

In `app.go`'s `handle`, change the final dispatch so a settled error is not answered twice:

```go
	if err := h(c); err != nil && !c.handled {
		a.callErrorHandler(c, err)
	}
```

The deferred panic path in `handle` stays exactly as it is — it must not consult `c.handled`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test`, then `go test -race ./...`, then `go test -tags ricedebug ./...`
Expected: green. The `ricedebug` run is what proves `poison.check()` precedes the `nil` check: `TestEveryCtxMethodPanicsAfterRelease` calls `HandleError(nil)` on a released `Ctx`.

- [ ] **Step 5: Confirm the pooled Ctx did not get more expensive, and break the new budget once**

Run: `go test . -run 'TestAllocBudget|TestNewCtxStaysWithinThreeAllocations' -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: every budget passes. `Ctx` grew by one `bool`.

Then break `TestAllocBudgetHandleError` on purpose to prove it can fail: temporarily add a package-level `var sink string` and the line `sink = fmt.Sprint(err)` inside `HandleError` after the `nil` check, run the test, record the failure line verbatim, and remove both. (`_ = make([]byte, 8)` will not work: escape analysis keeps it on the stack.)

- [ ] **Step 6: Commit**

```bash
git add ctx.go app.go handle_error_test.go alloc_test.go
git commit -F - <<'EOF'
ctx: add HandleError, so middleware can read the status a request ends with

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 3: `middleware.RealIP`

**Files:**
- Create: `middleware/realip.go`, `middleware/realip_test.go`, `middleware/alloc_test.go`

**Interfaces:**
- Consumes: `rice.Ctx.RequestCtx`, `rice.Ctx.ClientIP`, fasthttp's `RequestHeader.PeekAll` and `RequestCtx.SetRemoteAddr`.
- Produces: `func RealIP(trustedHops int) rice.Middleware`. Task 5's tests install it inside `Logger`.

- [ ] **Step 1: Write the failing tests**

Create `middleware/realip_test.go`:

```go
package middleware_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// ipApp answers GET /ip with the address rice attributes the request to.
func ipApp(trustedHops int) *rice.App {
	app := rice.New()
	app.Use(middleware.RealIP(trustedHops))
	app.GET("/ip", func(c *rice.Ctx) error { return c.String(200, c.ClientIP().String()) })
	return app
}

// dispatchFrom sends GET /ip as if from peer, the address of the proxy this
// service is connected to, with one X-Forwarded-For header line per xff value.
// It returns the address rice attributed the request to.
func dispatchFrom(t *testing.T, app *rice.App, peer string, xff ...string) string {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod("GET")
	req.SetRequestURI("/ip")
	for _, v := range xff {
		req.Header.Add("X-Forwarded-For", v)
	}
	fctx := &fasthttp.RequestCtx{}
	fctx.Init(&req, &net.TCPAddr{IP: net.ParseIP(peer), Port: 4000}, nil)
	app.FasthttpHandler()(fctx)
	return string(fctx.Response.Body())
}

func TestRealIPTakesTheEntryWrittenByTheTrustedProxy(t *testing.T) {
	// The client sent its own X-Forwarded-For claiming 198.51.100.7; the
	// trusted proxy appended the address it actually saw.
	got := dispatchFrom(t, ipApp(1), "10.0.0.2", "198.51.100.7, 203.0.113.9")
	if got != "203.0.113.9" {
		t.Errorf("address = %s, want 203.0.113.9", got)
	}
}

func TestRealIPCountsTwoTrustedHops(t *testing.T) {
	// CDN appended the client; the load balancer appended the CDN.
	got := dispatchFrom(t, ipApp(2), "10.0.0.2", "198.51.100.7, 203.0.113.9, 192.0.2.44")
	if got != "203.0.113.9" {
		t.Errorf("address = %s, want 203.0.113.9", got)
	}
}

// TestRealIPFallsBackWhenThereAreTooFewEntries pins that the middleware never
// reaches for the leftmost entry: that is the part the client wrote.
func TestRealIPFallsBackWhenThereAreTooFewEntries(t *testing.T) {
	got := dispatchFrom(t, ipApp(2), "10.0.0.2", "203.0.113.9")
	if got != "10.0.0.2" {
		t.Errorf("address = %s, want the connection's 10.0.0.2", got)
	}
}

func TestRealIPFallsBackOnAnEntryThatIsNotAnIP(t *testing.T) {
	got := dispatchFrom(t, ipApp(1), "10.0.0.2", "not-an-ip")
	if got != "10.0.0.2" {
		t.Errorf("address = %s, want the connection's 10.0.0.2", got)
	}
}

func TestRealIPFallsBackWithNoHeader(t *testing.T) {
	got := dispatchFrom(t, ipApp(1), "10.0.0.2")
	if got != "10.0.0.2" {
		t.Errorf("address = %s, want the connection's 10.0.0.2", got)
	}
}

// TestRealIPReadsEveryHeaderLine covers a request carrying the header twice.
// fasthttp's Peek returns only the first line; the two lines are one list, and
// counting two hops from the right must cross from the second into the first.
func TestRealIPReadsEveryHeaderLine(t *testing.T) {
	got := dispatchFrom(t, ipApp(2), "10.0.0.2", "203.0.113.9", "192.0.2.44")
	if got != "203.0.113.9" {
		t.Errorf("address = %s, want 203.0.113.9", got)
	}
}

func TestRealIPPanicsOnFewerThanOneHop(t *testing.T) {
	for _, n := range []int{0, -1} {
		func() {
			defer func() {
				r := recover()
				s, ok := r.(string)
				if !ok || !strings.HasPrefix(s, "rice: ") {
					t.Errorf("RealIP(%d) panicked with %v, want a string starting %q", n, r, "rice: ")
				}
			}()
			middleware.RealIP(n)
		}()
	}
}

// serveApp starts app on an ephemeral port and returns its address.
func serveApp(t *testing.T, app *rice.App) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()
	// Not t.Context(): it is cancelled just before Cleanup functions run, which
	// would hand Shutdown a context that has already ended and force-close.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
	})
	deadline := time.Now().Add(2 * time.Second)
	for app.Addr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("app did not start serving")
		}
		time.Sleep(time.Millisecond)
	}
	return ln.Addr().String()
}

// TestRealIPDoesNotLeakAnAddressAcrossKeepAliveRequests is the reason RealIP
// sets the address on every request. fasthttp serves every request on a
// connection from one RequestCtx and clears a rewritten address only when the
// connection closes, so a middleware that skipped the rewrite would attribute
// the second request below to the first request's client. Behind a reverse
// proxy, those are different clients sharing one upstream connection.
func TestRealIPDoesNotLeakAnAddressAcrossKeepAliveRequests(t *testing.T) {
	addr := serveApp(t, ipApp(1))

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)

	get := func(extra string) string {
		t.Helper()
		fmt.Fprintf(conn, "GET /ip HTTP/1.1\r\nHost: rice\r\n%s\r\n", extra)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	if got := get("X-Forwarded-For: 203.0.113.9\r\n"); got != "203.0.113.9" {
		t.Fatalf("first request: address = %s, want 203.0.113.9", got)
	}
	if got := get(""); got != "127.0.0.1" {
		t.Errorf("second request on the same connection: address = %s, want 127.0.0.1 — the first request's address leaked", got)
	}
}
```

Create `middleware/alloc_test.go`:

```go
package middleware_test

import (
	"testing"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// measure reports what one call of mw costs, wrapped around a handler that does
// nothing. It runs inside a handler because a *rice.Ctx is valid only there,
// and warms once first so one-time setup is not counted as steady state.
func measure(t *testing.T, mw rice.Middleware, headers map[string]string) float64 {
	t.Helper()
	wrapped := mw(func(c *rice.Ctx) error { return nil })
	var got float64
	app := rice.New()
	app.GET("/m", func(c *rice.Ctx) error {
		_ = wrapped(c) // warm
		got = testing.AllocsPerRun(1000, func() { _ = wrapped(c) })
		return nil
	})
	fctx := newRequest("GET", "/m", headers)
	app.FasthttpHandler()(fctx)
	return got
}

// TestAllocBudgetRealIP pins RealIP's cost for one trusted hop and a
// two-entry header. Parsing the address allocates.
func TestAllocBudgetRealIP(t *testing.T) {
	const want float64 = 0 // replaced in step 5 with the measured figure

	got := measure(t, middleware.RealIP(1), map[string]string{
		"X-Forwarded-For": "198.51.100.7, 203.0.113.9",
	})
	if got != want {
		t.Errorf("RealIP allocated %.1f objects per call, want exactly %.0f", got, want)
	}
}
```

and the request builder it uses, in the same file:

```go
// newRequest builds a request to dispatch through FasthttpHandler.
func newRequest(method, uri string, headers map[string]string) *fasthttp.RequestCtx {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(method)
	fctx.Request.SetRequestURI(uri)
	for k, v := range headers {
		fctx.Request.Header.Set(k, v)
	}
	return fctx
}
```

`middleware/alloc_test.go` then needs `"github.com/valyala/fasthttp"` in its imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./middleware/ 2>&1 | head -20`
Expected: compile failure — `undefined: middleware.RealIP`.

- [ ] **Step 3: Implement**

Create `middleware/realip.go`:

```go
package middleware

import (
	"bytes"
	"net"

	rice "github.com/vietpham102301/rice-http"
)

// RealIP makes c.ClientIP report the client's address when the service sits
// behind trustedHops proxies that each append to X-Forwarded-For.
//
// It counts from the right. Each proxy appends the address it received a
// connection from, so only the right-hand entries — written by proxies this
// service trusts — cannot be forged; the left-hand ones are whatever the client
// sent. The client is the entry trustedHops from the right. With fewer entries
// than that, or an entry that is not an IP address, c.ClientIP reports the
// connection's own address. It never falls back to the leftmost entry.
//
// It sets the address on every request, resolved or not. fasthttp serves every
// request on a connection from one context and clears a rewritten address only
// when the connection closes, so a middleware that sometimes skipped the rewrite
// would attribute a request to whichever client came before it on the same
// keep-alive connection — behind a reverse proxy, a different client.
//
// Install it before anything that reads c.ClientIP. A trustedHops below 1
// panics.
func RealIP(trustedHops int) rice.Middleware {
	if trustedHops < 1 {
		panic("rice: middleware.RealIP: trustedHops must be at least 1")
	}
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			fctx := c.RequestCtx()
			// PeekAll's result is overwritten by the next Peek, so it is
			// consumed here before any other header is read.
			fctx.SetRemoteAddr(resolve(fctx.Request.Header.PeekAll("X-Forwarded-For"), trustedHops))
			return next(c)
		}
	}
}

// resolve returns the entry trustedHops from the right across every header
// line, or nil when there is none that parses as an IP address.
//
// A nil return is an untyped nil net.Addr, which SetRemoteAddr documents as
// restoring the connection's address. Returning a typed nil *net.TCPAddr
// instead would make a non-nil interface and break that.
func resolve(lines [][]byte, trustedHops int) net.Addr {
	seen := 0
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		for len(line) > 0 {
			var entry []byte
			if j := bytes.LastIndexByte(line, ','); j >= 0 {
				entry, line = line[j+1:], line[:j]
			} else {
				entry, line = line, nil
			}
			seen++
			if seen < trustedHops {
				continue
			}
			ip := net.ParseIP(string(bytes.TrimSpace(entry)))
			if ip == nil {
				return nil
			}
			return &net.TCPAddr{IP: ip}
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test`
Expected: green. `TestAllocBudgetRealIP` still fails, because its `want` is 0 — that is the next step.

- [ ] **Step 5: Measure, then pin in both directions**

Run: `go test ./middleware/ -run TestAllocBudgetRealIP -v`
Expected: FAIL, printing `RealIP allocated N.0 objects per call, want exactly 0`. **Do not guess N before running.** Set `want` to N, re-run, confirm PASS. Then set `want` to N-1 and confirm FAIL, to N+1 and confirm FAIL, and restore N. Record all four outputs in your report, and drop the `// replaced in step 5` comment once the real figure is in.

- [ ] **Step 6: Commit**

```bash
git add middleware/realip.go middleware/realip_test.go middleware/alloc_test.go
git commit -F - <<'EOF'
middleware: add RealIP, counting X-Forwarded-For from the trusted end

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 4: `middleware.RequestID`

**Files:**
- Create: `middleware/requestid.go`, `middleware/requestid_test.go`
- Modify: `middleware/alloc_test.go`

**Interfaces:**
- Consumes: `rice.Ctx.Header`, `SetHeader`, `Set`, `Get`; the `measure` helper from Task 3's `alloc_test.go`.
- Produces: `func RequestID() rice.Middleware` and `func RequestIDFrom(c *rice.Ctx) string`. Task 5's `Logger` calls `RequestIDFrom`.

- [ ] **Step 1: Write the failing tests**

Create `middleware/requestid_test.go`:

```go
package middleware_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

var generatedID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// idApp answers GET /id with the id RequestIDFrom returns.
func idApp() *rice.App {
	app := rice.New()
	app.Use(middleware.RequestID())
	app.GET("/id", func(c *rice.Ctx) error { return c.String(200, middleware.RequestIDFrom(c)) })
	return app
}

// dispatchWithID sends GET /id, with an X-Request-Id header when incoming is
// not empty, and returns the id the handler saw and the response header.
func dispatchWithID(t *testing.T, app *rice.App, incoming string) (seen, header string) {
	t.Helper()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/id")
	if incoming != "" {
		fctx.Request.Header.Set("X-Request-Id", incoming)
	}
	app.FasthttpHandler()(fctx)
	return string(fctx.Response.Body()), string(fctx.Response.Header.Peek("X-Request-Id"))
}

func TestRequestIDGeneratesAnIDWhenNoneArrives(t *testing.T) {
	seen, header := dispatchWithID(t, idApp(), "")

	if !generatedID.MatchString(seen) {
		t.Errorf("generated id = %q, want 32 lowercase hex characters", seen)
	}
	if header != seen {
		t.Errorf("response header = %q, want the same id the handler saw, %q", header, seen)
	}
}

func TestRequestIDKeepsAValidIncomingID(t *testing.T) {
	seen, header := dispatchWithID(t, idApp(), "gw-7f3a_b2")

	if seen != "gw-7f3a_b2" || header != "gw-7f3a_b2" {
		t.Errorf("handler saw %q and header is %q, want the incoming id kept", seen, header)
	}
}

// TestRequestIDReplacesAnUnsafeIncomingID covers the security control. A
// client-supplied id goes into every log line for its request, so one carrying
// a newline would let a client forge log entries.
func TestRequestIDReplacesAnUnsafeIncomingID(t *testing.T) {
	for _, tc := range []struct {
		name, incoming string
	}{
		{"newline", "abc\nFORGED log line"},
		{"too long", strings.Repeat("a", 65)},
		{"space", "has space"},
		{"punctuation", "id;drop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen, header := dispatchWithID(t, idApp(), tc.incoming)

			if !generatedID.MatchString(seen) {
				t.Errorf("id = %q, want a freshly generated one", seen)
			}
			if strings.Contains(header, "FORGED") || strings.Contains(seen, "FORGED") {
				t.Errorf("the unsafe incoming id reached the response: header %q, handler saw %q", header, seen)
			}
		})
	}
}

func TestRequestIDAcceptsExactlySixtyFourCharacters(t *testing.T) {
	id := strings.Repeat("a", 64)
	if seen, _ := dispatchWithID(t, idApp(), id); seen != id {
		t.Errorf("a 64-character id was replaced; want it kept")
	}
}

func TestRequestIDGeneratesADifferentIDEachTime(t *testing.T) {
	app := idApp()
	a, _ := dispatchWithID(t, app, "")
	b, _ := dispatchWithID(t, app, "")
	if a == b {
		t.Errorf("two requests got the same generated id %q", a)
	}
}

func TestRequestIDFromIsEmptyWithoutTheMiddleware(t *testing.T) {
	app := rice.New()
	app.GET("/id", func(c *rice.Ctx) error { return c.String(200, middleware.RequestIDFrom(c)) })

	if seen, _ := dispatchWithID(t, app, ""); seen != "" {
		t.Errorf("RequestIDFrom = %q with no RequestID installed, want empty", seen)
	}
}
```

Add to `middleware/alloc_test.go`:

```go
// TestAllocBudgetRequestIDGenerated pins RequestID's cost when it generates an
// id, the common case. Encoding the id and storing it both allocate.
func TestAllocBudgetRequestIDGenerated(t *testing.T) {
	const want float64 = 0 // replaced in step 5 with the measured figure

	got := measure(t, middleware.RequestID(), nil)
	if got != want {
		t.Errorf("RequestID (generating) allocated %.1f objects per call, want exactly %.0f", got, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./middleware/ 2>&1 | head -20`
Expected: compile failure — `undefined: middleware.RequestID` and `undefined: middleware.RequestIDFrom`.

- [ ] **Step 3: Implement**

Create `middleware/requestid.go`:

```go
package middleware

import (
	"crypto/rand"
	"encoding/hex"

	rice "github.com/vietpham102301/rice-http"
)

const (
	requestIDHeader = "X-Request-Id"

	// requestIDKey is the store key. It is unexported and namespaced so that no
	// handler's own c.Set can collide with it; read the id with RequestIDFrom.
	requestIDKey = "rice/middleware.request-id"
)

// RequestID gives every request an id, stores it for RequestIDFrom, and writes
// it to the X-Request-Id response header so a client can quote it.
//
// An incoming X-Request-Id is kept when it is 1 to 64 characters from
// [A-Za-z0-9_-]. That is how a gateway's id survives into this service's logs
// for cross-service tracing. Anything else is discarded and a new id is
// generated: 16 random bytes, hex-encoded. The character set is the security
// control — an id goes into every log line for its request, so one containing
// a newline would let a client forge log entries.
//
// The id is not put into c.Context: that costs an allocation on every request
// for a use most handlers never have. To propagate it to an outbound call,
// write
//
//	c.SetContext(context.WithValue(c.Context(), requestIDKey{}, middleware.RequestIDFrom(c)))
//
// with a key type of your own.
func RequestID() rice.Middleware {
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			var id string
			if in := c.Header(requestIDHeader); validRequestID(in) {
				id = string(in) // copied: the header's bytes are borrowed
			} else {
				id = newRequestID()
			}
			c.Set(requestIDKey, id)
			c.SetHeader(requestIDHeader, id)
			return next(c)
		}
	}
}

// RequestIDFrom returns the id RequestID assigned, or "" when RequestID is not
// installed.
func RequestIDFrom(c *rice.Ctx) string {
	v, ok := c.Get(requestIDKey)
	if !ok {
		return ""
	}
	id, _ := v.(string)
	return id
}

// validRequestID reports whether an incoming id is safe to put in a log line.
func validRequestID(id []byte) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, b := range id {
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '-', b == '_':
		default:
			return false
		}
	}
	return true
}

// newRequestID returns 16 random bytes, hex-encoded. crypto/rand.Read cannot
// fail as of Go 1.24: it crashes the program rather than return an error.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test`
Expected: green apart from `TestAllocBudgetRequestIDGenerated`, whose `want` is still 0.

- [ ] **Step 5: Measure, then pin in both directions**

Same procedure as Task 3, step 5, on `TestAllocBudgetRequestIDGenerated`. Record the four outputs.

- [ ] **Step 6: Commit**

```bash
git add middleware/requestid.go middleware/requestid_test.go middleware/alloc_test.go
git commit -F - <<'EOF'
middleware: add RequestID, keeping an incoming id only if it is safe to log

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 5: `middleware.Logger`

**Files:**
- Create: `middleware/logger.go`, `middleware/logger_test.go`
- Modify: `middleware/alloc_test.go`

**Interfaces:**
- Consumes: `rice.Ctx.HandleError` (Task 2), the miss chain (Task 1), `RealIP` (Task 3), `RequestIDFrom` (Task 4), `middleware.Recover`, the `measure` helper.
- Produces: `func Logger(l *slog.Logger) rice.Middleware`.

- [ ] **Step 1: Write the failing tests**

Create `middleware/logger_test.go`:

```go
package middleware_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// logRequest dispatches one request through app and returns every log line
// the Logger wrote, each decoded from JSON. peer, when not empty, is the
// connection's address; xff, when not empty, is an X-Forwarded-For value.
func logRequest(t *testing.T, buf *bytes.Buffer, app *rice.App, method, uri, peer, xff string) []map[string]any {
	t.Helper()
	buf.Reset()
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	fctx := &fasthttp.RequestCtx{}
	if peer != "" {
		fctx.Init(&req, &net.TCPAddr{IP: net.ParseIP(peer), Port: 4000}, nil)
	} else {
		req.CopyTo(&fctx.Request)
	}
	app.FasthttpHandler()(fctx)

	var lines []map[string]any
	for _, l := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(l) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatalf("log line is not JSON: %v: %s", err, l)
		}
		lines = append(lines, m)
	}
	return lines
}

// loggedApp returns an App with Logger outermost and Recover inside it — the
// recommended order — and routes that end in each kind of outcome.
func loggedApp(buf *bytes.Buffer, opts ...rice.Option) *rice.App {
	app := rice.New(opts...)
	app.Use(
		middleware.Logger(slog.New(slog.NewJSONHandler(buf, nil))),
		middleware.Recover(),
		middleware.RealIP(1),
		middleware.RequestID(),
	)
	app.GET("/ok", func(c *rice.Ctx) error { return c.String(200, "ok") })
	app.GET("/teapot", func(c *rice.Ctx) error { return rice.NewHTTPError(418, "teapot") })
	app.GET("/boom", func(c *rice.Ctx) error { return errors.New("db down") })
	app.GET("/panic", func(c *rice.Ctx) error { panic("handler exploded") })
	return app
}

// one returns the single line a request produced, failing if there is not
// exactly one.
func one(t *testing.T, lines []map[string]any) map[string]any {
	t.Helper()
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want exactly 1: %v", len(lines), lines)
	}
	return lines[0]
}

func TestLoggerRecordsTheRequest(t *testing.T) {
	var buf bytes.Buffer
	line := one(t, logRequest(t, &buf, loggedApp(&buf), "GET", "/ok", "10.0.0.2", "203.0.113.9"))

	for key, want := range map[string]any{
		"method": "GET",
		"path":   "/ok",
		"status": float64(200),
		"ip":     "203.0.113.9",
		"level":  "INFO",
	} {
		if line[key] != want {
			t.Errorf("%s = %v, want %v", key, line[key], want)
		}
	}
	if _, ok := line["latency"]; !ok {
		t.Error("latency is missing")
	}
	if id, _ := line["request_id"].(string); !generatedID.MatchString(id) {
		t.Errorf("request_id = %v, want a generated id", line["request_id"])
	}
}

// TestLoggerRecordsAMissAs404 is the end-to-end test of both core changes. Before
// them a Logger did not run on a miss at all, and would have recorded 200 if it
// had.
func TestLoggerRecordsAMissAs404(t *testing.T) {
	var buf bytes.Buffer
	line := one(t, logRequest(t, &buf, loggedApp(&buf), "GET", "/nope", "", ""))

	if line["status"] != float64(404) {
		t.Errorf("status = %v, want 404", line["status"])
	}
}

func TestLoggerRecordsTheStatusTheFunnelWrote(t *testing.T) {
	var buf bytes.Buffer
	app := loggedApp(&buf)
	for path, want := range map[string]float64{"/teapot": 418, "/boom": 500} {
		if got := one(t, logRequest(t, &buf, app, "GET", path, "", ""))["status"]; got != want {
			t.Errorf("%s: status = %v, want %v", path, got, want)
		}
	}
}

// TestLoggerRecordsACustomErrorHandlersStatus is what distinguishes
// HandleError from a function that maps errors to statuses the way the default
// handler would: the log records what the App's real ErrorHandler answered.
func TestLoggerRecordsACustomErrorHandlersStatus(t *testing.T) {
	var buf bytes.Buffer
	app := loggedApp(&buf, rice.WithErrorHandler(func(c *rice.Ctx, err error) {
		_ = c.String(503, "custom")
	}))

	if got := one(t, logRequest(t, &buf, app, "GET", "/teapot", "", ""))["status"]; got != float64(503) {
		t.Errorf("status = %v, want the custom handler's 503", got)
	}
}

func TestLoggerDoesNotRecordTheQueryString(t *testing.T) {
	var buf bytes.Buffer
	line := one(t, logRequest(t, &buf, loggedApp(&buf), "GET", "/ok?token=s3cret", "", ""))

	if line["path"] != "/ok" {
		t.Errorf("path = %v, want /ok", line["path"])
	}
	if strings.Contains(buf.String(), "s3cret") {
		t.Errorf("the query string reached the log: %s", buf.String())
	}
}

func TestLoggerLevelsByStatus(t *testing.T) {
	var buf bytes.Buffer
	app := loggedApp(&buf)
	for path, want := range map[string]string{"/ok": "INFO", "/teapot": "INFO", "/boom": "ERROR"} {
		if got := one(t, logRequest(t, &buf, app, "GET", path, "", ""))["level"]; got != want {
			t.Errorf("%s: level = %v, want %v", path, got, want)
		}
	}
}

func TestLoggerRecordsAPanicAs500WithRecoverInside(t *testing.T) {
	var buf bytes.Buffer
	if got := one(t, logRequest(t, &buf, loggedApp(&buf), "GET", "/panic", "", ""))["status"]; got != float64(500) {
		t.Errorf("status = %v, want 500", got)
	}
}

// TestLoggerDoesNotLogAPanicWithoutRecoverInside pins a documented limitation
// rather than a bug. Without Recover beneath it, a panic unwinds through
// Logger's frame before it records anything. If this ever starts passing for a
// different reason, the documentation in middleware/doc.go must change with it.
func TestLoggerDoesNotLogAPanicWithoutRecoverInside(t *testing.T) {
	var buf bytes.Buffer
	app := rice.New()
	app.Use(middleware.Logger(slog.New(slog.NewJSONHandler(&buf, nil))))
	app.GET("/panic", func(c *rice.Ctx) error { panic("handler exploded") })

	lines := logRequest(t, &buf, app, "GET", "/panic", "", "")

	if len(lines) != 0 {
		t.Errorf("got %d log lines, want 0 — the documented limitation no longer holds: %v", len(lines), lines)
	}
}

func TestLoggerPanicsOnANilLogger(t *testing.T) {
	defer func() {
		r := recover()
		s, ok := r.(string)
		if !ok || !strings.HasPrefix(s, "rice: ") {
			t.Errorf("Logger(nil) panicked with %v, want a string starting %q", r, "rice: ")
		}
	}()
	middleware.Logger(nil)
}
```

Add to `middleware/alloc_test.go`:

```go
// TestAllocBudgetLogger pins Logger's cost with slog's JSON handler writing to
// io.Discard. A different handler costs differently; the figure is for this one.
func TestAllocBudgetLogger(t *testing.T) {
	const want float64 = 0 // replaced in step 5 with the measured figure

	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	got := measure(t, middleware.Logger(l), nil)
	if got != want {
		t.Errorf("Logger allocated %.1f objects per call, want exactly %.0f", got, want)
	}
}
```

`middleware/alloc_test.go` then needs `"io"` and `"log/slog"` in its imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./middleware/ 2>&1 | head -20`
Expected: compile failure — `undefined: middleware.Logger`.

- [ ] **Step 3: Implement**

Create `middleware/logger.go`:

```go
package middleware

import (
	"log/slog"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// Logger writes one log line per request to l: method, path, status, latency,
// the client's address and, when RequestID is installed, the request id.
//
// Install it outermost. It settles any error the chain returns with
// c.HandleError before logging, so the status it records is the one the client
// receives — including from a custom ErrorHandler — and it returns nil, having
// consumed the error. Nothing outside it sees that error.
//
// Install Recover directly inside it. A panic unwinds through Logger's frame
// before it can record anything, so without Recover beneath it a panicking
// request is not logged at all:
//
//	app.Use(
//		middleware.Logger(l),
//		middleware.Recover(),
//		middleware.RealIP(1),
//		middleware.RequestID(),
//	)
//
// It reads the address and the id after the chain returns, so although it is
// outermost it sees what RealIP and RequestID did inside it.
//
// It records the path and never the query string, which carries tokens and
// personal data. Status 500 and above is logged at ERROR, everything else at
// INFO. A nil logger panics.
func Logger(l *slog.Logger) rice.Middleware {
	if l == nil {
		panic("rice: middleware.Logger: logger is nil")
	}
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			start := time.Now()
			if err := next(c); err != nil {
				c.HandleError(err)
			}

			status := c.RequestCtx().Response.StatusCode()
			level := slog.LevelInfo
			if status >= 500 {
				level = slog.LevelError
			}
			ctx := c.Context()
			if !l.Enabled(ctx, level) {
				return nil
			}

			attrs := [6]slog.Attr{
				slog.String("method", string(c.Method())),
				slog.String("path", string(c.Path())),
				slog.Int("status", status),
				slog.Duration("latency", time.Since(start)),
				slog.String("ip", c.ClientIP().String()),
			}
			n := 5
			if id := RequestIDFrom(c); id != "" {
				attrs[n] = slog.String("request_id", id)
				n++
			}
			l.LogAttrs(ctx, level, "request", attrs[:n]...)
			return nil
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test`, then `go test -race ./...`, then `go test -tags ricedebug ./...`
Expected: green apart from `TestAllocBudgetLogger`, whose `want` is still 0.

- [ ] **Step 5: Measure, then pin in both directions**

Same procedure as Task 3, step 5, on `TestAllocBudgetLogger`. Record the four outputs.

- [ ] **Step 6: Commit**

```bash
git add middleware/logger.go middleware/logger_test.go middleware/alloc_test.go
git commit -F - <<'EOF'
middleware: add Logger, recording the status the client actually receives

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 6: ADR-0012, ADR-0013 and every document the change touches

**Files:**
- Create: `docs/adr/0012-application-middleware-runs-on-route-misses.md`, `docs/adr/0013-middleware-can-settle-a-request.md`
- Modify: `docs/adr/README.md`, `middleware/doc.go`, `README.md`, `docs/02-architecture.md`, `docs/03-core-concepts.md`, `docs/04-roadmap.md`, `docs/05-performance-model.md`, `docs/progress.md`

**Interfaces:**
- Consumes: the budget figures and break-on-purpose texts recorded in Tasks 2–5.
- Produces: nothing code depends on.

- [ ] **Step 1: Write the two ADRs**

Follow `docs/adr/README.md`'s format — `Context`, `Decision`, `Alternatives`, `Consequences` — `Status: Accepted`, `Date` the day the work lands. Read `docs/adr/0010-request-context-cancels-at-force-close.md` first for the house voice.

**ADR-0012 — "Application middleware runs on route misses."** Context: the spec's finding 1, including the three middleware it broke. Decision: the miss chain, compiled from the application's middleware only. Alternatives, each with why it lost: `app.Pre` (the trap survives with a name); wrapping at the fasthttp layer (no `*rice.Ctx`, a second middleware system). Consequences, including the behaviour changes: application middleware now sees misses, `c.Param` is empty there, and a wrong method on an existing path is a miss.

**ADR-0013 — "Middleware can settle a request with `c.HandleError`."** Context: the spec's finding 2, with its four-row table as the evidence. Decision: `HandleError`, the `handled` flag, and the panic path deliberately ignoring it. Alternatives: `rice.StatusOf` (wrong for every custom `ErrorHandler`); running the funnel inside the chain (reverses M5, makes `Recover` pointless). Consequences: a middleware that calls it consumes the error, so it must be outermost.

Add both rows to the index in `docs/adr/README.md`.

- [ ] **Step 2: Fix every forward reference this change made false**

Two are known. `docs/03-core-concepts.md:71` and `docs/04-roadmap.md:186` both say `middleware.RealIP` "is what will" resolve the proxy headers. Change both to the present tense.

Then run the search yourself rather than trusting that list:

```bash
grep -rn 'RealIP\|RequestID\|middleware\.Logger' --include='*.md' --include='*.go' . | grep -v 'docs/superpowers/' | grep -v '_test.go'
```

Fix every hit this branch has made false. The references to `middleware.Timeout` in ADR-0010 stay: Timeout is not part of this branch. The previous two branches each shipped a sentence their own change had made false — a retrospective line, then the README — so treat this step as the one most likely to be skipped.

- [ ] **Step 3: Point the illustrative `RequestID` at the real one**

`docs/03-core-concepts.md` §3 teaches the middleware shape with a `RequestID` that stores the id under the public key `"request_id"`. Keep the example. Add one sentence after it: rice ships a real one, `middleware.RequestID`, which stores the id under its own key — read it with `middleware.RequestIDFrom(c)`, not `c.Get("request_id")`.

- [ ] **Step 4: Update the remaining documents**

- `middleware/doc.go`: the recommended order, and that a panicking request is not logged unless `Recover` is inside `Logger`.
- `docs/03-core-concepts.md`: `HandleError` in §2's method listing; in §3, that application middleware runs on misses and that `c.Param` is empty there.
- `README.md`: the three middleware and their recommended order. It is the document a reader meets first, and the last branch's final review found it still teaching a superseded pattern.
- `docs/05-performance-model.md`: a `c.HandleError` row (0) in the `Ctx` table, a row for the miss chain in the dispatch table, and the three middleware rows in *Opt-in packages* with their fixtures — `Logger`'s naming slog's JSON handler on `io.Discard`.
- `docs/02-architecture.md`: the three new files in `middleware/`.
- `docs/04-roadmap.md`: an entry under *Done after M8*. Timeout and CORS stay on *Explicitly deferred*; nothing leaves that list in this branch, so `docs/07-retrospective.md` needs no change — confirm that rather than assume it.

- [ ] **Step 5: Write the progress entry**

Prepend to `docs/progress.md` in the file's `Did` / `Learned` / `Measured` / `Next` shape, milestone `post-M8`. Learned must carry the three probe findings: application middleware never ran on a miss; a middleware read the status before the funnel wrote it, so the obvious Logger would have recorded every 500 as a 200; and a rewritten remote address outlived its request on a keep-alive connection. Measured: the four budget figures with their fixtures and break-on-purpose texts, `TestAllocBudget404` still 0, and the coverage from `make cover` — say so if it dropped. Next: Timeout, then CORS.

- [ ] **Step 6: Verify and commit**

Run: `make test && go test -race ./... && go test -tags ricedebug ./... && make lint && make cover`

```bash
git add docs/ README.md middleware/doc.go
git commit -F - <<'EOF'
docs: ADR-0012, ADR-0013 and the docs for RealIP, RequestID and Logger

Co-Authored-By: <your session's trailer>
EOF
```

---

## Self-Review

**Spec coverage.** D1 → Task 1. D2 → Task 2. D3 → Task 3, including the always-set rule, `PeekAll`, the fallbacks and the construction panic. D4 and D5 → Task 4, and D5's documentation sentence → Task 6 step 3. D6 → Task 4's doc comment and Task 6. D7 → Task 5. D8 → `TestLoggerDoesNotRecordTheQueryString`. D9 → `middleware/doc.go` in Task 6 and the two panic tests in Task 5. Finding 4 → Task 1 step 5 and `TestAllocBudget404WithAppMiddleware`. The spec's testing section → Tasks 1–5, item for item. Documentation → Task 6, including the forward-reference search. Exit criteria → Task 6 step 6.

**Placeholder scan.** The budget figures are deliberately absent: each budget test starts at `const want float64 = 0` so that its first run fails and prints the real number, and each task's step 5 says to measure rather than guess. Commit trailers read `<your session's trailer>` because each implementer uses its own session's attribution. Every other step carries its content.

**Type consistency.** `HandleError(err error)` is spelled the same in Tasks 2 and 5. `RealIP(trustedHops int)`, `RequestID()`, `RequestIDFrom(c *rice.Ctx) string` and `Logger(l *slog.Logger)` match between their definitions and their uses. `notFound` and `a.miss` match between `app.go` and `build.go`. The `middleware_test` helpers — `dispatchFrom`, `serveApp`, `ipApp`, `dispatchWithID`, `idApp`, `logRequest`, `loggedApp`, `one`, `measure`, `newRequest` — are each defined once and do not collide with `recover_test.go`'s `dispatch`. `generatedID` is defined in `requestid_test.go` and used in `logger_test.go`, which is legal within one package and ordered correctly, since Task 4 lands before Task 5.
