# Timeout Middleware Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `middleware.Timeout(d)`, a cooperative per-request deadline that answers 503 when that deadline is what made the request fail.

**Architecture:** `Timeout` derives a context with a deadline from `c.Context()`, installs it with `c.SetContext` for the rest of the chain, and restores the previous context when the chain returns. It maps the chain's error to a 503 `*rice.HTTPError` only when its own deadline has passed and the error is not already an `*rice.HTTPError`. No change to package `rice`.

**Tech Stack:** Go 1.25, `context`, fasthttp v1.73.0 status constants. No new dependency.

**Spec:** [docs/superpowers/specs/2026-09-23-timeout-middleware-design.md](../specs/2026-09-23-timeout-middleware-design.md)

## Global Constraints

- **Branch:** `timeout-middleware`, already created, already holding the spec commit. Do not commit to `main`.
- **No change to package `rice`.** Everything lives in `middleware/`.
- **Cooperative only.** `Timeout` never runs the chain on another goroutine. A probe proved a pre-emptive middleware races with every middleware outside it, which share one `*Ctx`.
- **The 503 is `&rice.HTTPError{Code: fasthttp.StatusServiceUnavailable, Err: err}` with no `Message`**, so the client receives the status text "Service Unavailable" and the cause never reaches the body.
- **The decision to answer 503 asks this middleware's own context** — `ctx.Err() == context.DeadlineExceeded` — never `errors.Is(err, context.DeadlineExceeded)` on the returned error.
- **Configuration mistakes panic at construction** with a `rice: ` prefix.
- **Budgets:** the constant is declared typed — `const want float64 = 0` — and pinned exactly in both directions without `-race`. **No claim that a figure is exact under `-race` may be written until it has been measured on both darwin and Linux, with and without `-race`.**
- **Run `make test` before each commit.** Commit messages: subject, blank line, `Co-Authored-By` trailer on its own line.

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `middleware/timeout.go` (new) | `Timeout` | 1 |
| `middleware/timeout_test.go` (new) | behaviour | 1 |
| `middleware/alloc_test.go` | the budget | 1 |
| `docs/adr/0014-timeout-is-cooperative.md` (new), `docs/adr/README.md`, `docs/04-roadmap.md`, `docs/07-retrospective.md`, `README.md`, `docs/02-architecture.md`, `docs/05-performance-model.md`, `middleware/doc.go`, `docs/progress.md` | documentation | 2 |

**Helper names in package `middleware_test` already taken:** `measure`, `newRequest`, `assertAllocBudget`, `logRequest`, `loggedApp`, `one`, `ipApp`, `dispatchFrom`, `serveApp`, `idApp`, `dispatchWithID`, `dispatch`, `panickingHandler`. Task 1's new helpers are `timedApp`, `send` and `waitDone`.

---

### Task 1: `middleware.Timeout`

**Files:**
- Create: `middleware/timeout.go`, `middleware/timeout_test.go`
- Modify: `middleware/alloc_test.go`

**Interfaces:**
- Consumes: `rice.Ctx.Context`, `rice.Ctx.SetContext`, `rice.HTTPError`, `middleware.Logger`, and the test helpers `newRequest`, `measure`, `assertAllocBudget`, `logRequest`, `one`.
- Produces: `func Timeout(d time.Duration) rice.Middleware`.

- [ ] **Step 1: Write the failing tests**

Create `middleware/timeout_test.go`:

```go
package middleware_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// waitDone blocks until the request's context is done and returns its error.
// It gives up after two seconds so that a deadline which never fires fails the
// test instead of hanging it.
func waitDone(c *rice.Ctx) error {
	select {
	case <-c.Context().Done():
		return c.Context().Err()
	case <-time.After(2 * time.Second):
		return errors.New("test bug: the deadline never fired")
	}
}

// timedApp returns an App with Timeout(d) installed and h on GET /t.
func timedApp(d time.Duration, h rice.Handler) *rice.App {
	app := rice.New()
	app.Use(middleware.Timeout(d))
	app.GET("/t", h)
	return app
}

// send dispatches GET /t and returns the status and body the client received.
func send(app *rice.App) (int, string) {
	fctx := newRequest("GET", "/t", nil)
	app.FasthttpHandler()(fctx)
	return fctx.Response.StatusCode(), string(fctx.Response.Body())
}

func TestTimeoutAnswers503WhenTheHandlerHonoursTheDeadline(t *testing.T) {
	app := timedApp(10*time.Millisecond, func(c *rice.Ctx) error { return waitDone(c) })

	status, body := send(app)

	if status != 503 {
		t.Errorf("status = %d, want 503", status)
	}
	if body != "Service Unavailable" {
		t.Errorf("body = %q, want the status text alone", body)
	}
}

// driverError stands in for a database driver that reports a timeout in its
// own error type and does not unwrap to context.DeadlineExceeded.
type driverError struct{}

func (driverError) Error() string { return "driver: query interrupted" }

// TestTimeoutAsksItsContextNotTheError is why Timeout checks its own
// context's Err instead of calling errors.Is on the returned error: many
// drivers wrap a timeout without unwrapping to context.DeadlineExceeded, and
// errors.Is would miss exactly the case a timeout exists for.
func TestTimeoutAsksItsContextNotTheError(t *testing.T) {
	app := timedApp(10*time.Millisecond, func(c *rice.Ctx) error {
		_ = waitDone(c)
		return driverError{}
	})

	status, body := send(app)

	if status != 503 {
		t.Errorf("status = %d, want 503 for a driver error returned after the deadline", status)
	}
	if strings.Contains(body, "driver") {
		t.Errorf("body = %q, want the driver's error kept out of the response", body)
	}
}

// TestTimeoutKeepsAnHTTPErrorTheHandlerChose follows the rule binding set for
// Validate: an author who chose a status keeps it.
func TestTimeoutKeepsAnHTTPErrorTheHandlerChose(t *testing.T) {
	app := timedApp(10*time.Millisecond, func(c *rice.Ctx) error {
		_ = waitDone(c)
		return rice.NewHTTPError(400, "bad input")
	})

	if status, _ := send(app); status != 400 {
		t.Errorf("status = %d, want the handler's own 400", status)
	}
}

// TestTimeoutDoesNotStopAHandlerThatIgnoresTheContext pins two statements at
// once. D3: when the deadline passes but the handler still succeeds, its
// answer is returned. D6, the documented limitation: Timeout is a deadline the
// work is asked to honour, not a switch that cuts it off, so a handler that
// ignores its context runs to completion. If this ever starts failing because
// Timeout begins to cut handlers off, the documentation must change with it.
func TestTimeoutDoesNotStopAHandlerThatIgnoresTheContext(t *testing.T) {
	app := timedApp(10*time.Millisecond, func(c *rice.Ctx) error {
		time.Sleep(60 * time.Millisecond) // ignores the deadline entirely
		return c.String(200, "late but done")
	})

	status, body := send(app)

	if status != 200 || body != "late but done" {
		t.Errorf("got %d %q, want the handler's own late 200", status, body)
	}
}

// TestTimeoutLeavesACancellationAlone covers a context cancelled rather than
// timed out — by ADR-0010's force-close, or by a middleware outside this one.
// That is not this middleware's timeout, so the error is not rewritten.
func TestTimeoutLeavesACancellationAlone(t *testing.T) {
	app := rice.New()
	app.Use(
		func(next rice.Handler) rice.Handler {
			return func(c *rice.Ctx) error {
				ctx, cancel := context.WithCancel(c.Context())
				cancel() // cancelled before Timeout ever runs
				c.SetContext(ctx)
				return next(c)
			}
		},
		middleware.Timeout(time.Second),
	)
	app.GET("/t", func(c *rice.Ctx) error { return waitDone(c) })

	if status, _ := send(app); status == 503 {
		t.Error("status = 503 for a cancelled context, want the cancellation left alone")
	}
}

// TestTimeoutRestoresThePreviousContext keeps an outer middleware — Logger,
// which hands c.Context() to slog — from receiving a context that has already
// expired.
func TestTimeoutRestoresThePreviousContext(t *testing.T) {
	var before, after context.Context
	app := rice.New()
	app.Use(
		func(next rice.Handler) rice.Handler {
			return func(c *rice.Ctx) error {
				before = c.Context()
				err := next(c)
				after = c.Context()
				return err
			}
		},
		middleware.Timeout(10*time.Millisecond),
	)
	app.GET("/t", func(c *rice.Ctx) error { return waitDone(c) })

	send(app)

	if after != before {
		t.Error("the context outside Timeout changed; want the previous one restored")
	}
}

// TestTimeoutReleasesItsTimer pins that cancel runs when the chain returns,
// not when the deadline eventually passes: a context the handler captured is
// already done the moment the request finishes, an hour before its deadline.
func TestTimeoutReleasesItsTimer(t *testing.T) {
	var captured context.Context
	app := timedApp(time.Hour, func(c *rice.Ctx) error {
		captured = c.Context()
		return c.String(200, "quick")
	})

	send(app)

	if captured.Err() == nil {
		t.Error("the handler's context is still live after the request; cancel did not run")
	}
}

// TestANestedTimeoutOnlyShortens pins that a route's Timeout inside an
// application-wide one cannot extend it: a context's deadline is never later
// than its parent's.
func TestANestedTimeoutOnlyShortens(t *testing.T) {
	app := rice.New()
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.GET("/t", func(c *rice.Ctx) error { return waitDone(c) }, middleware.Timeout(time.Hour))

	start := time.Now()
	status, _ := send(app)

	if status != 503 {
		t.Errorf("status = %d, want 503", status)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("the request took %v, want the application-wide 20ms deadline to win", elapsed)
	}
}

func TestLoggerOutsideTimeoutRecords503(t *testing.T) {
	var buf bytes.Buffer
	app := rice.New()
	app.Use(
		middleware.Logger(slog.New(slog.NewJSONHandler(&buf, nil))),
		middleware.Timeout(10*time.Millisecond),
	)
	app.GET("/t", func(c *rice.Ctx) error { return waitDone(c) })

	line := one(t, logRequest(t, &buf, app, "GET", "/t", "", ""))

	if line["status"] != float64(503) {
		t.Errorf("logged status = %v, want 503", line["status"])
	}
}

func TestTimeoutPanicsOnANonPositiveDuration(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		func() {
			defer func() {
				r := recover()
				s, ok := r.(string)
				if !ok || !strings.HasPrefix(s, "rice: ") {
					t.Errorf("Timeout(%v) panicked with %v, want a string starting %q", d, r, "rice: ")
				}
			}()
			middleware.Timeout(d)
		}()
	}
}
```

Add to `middleware/alloc_test.go`:

```go
// TestAllocBudgetTimeout pins Timeout's cost around a handler that returns at
// once. context.WithTimeout allocates.
func TestAllocBudgetTimeout(t *testing.T) {
	const want float64 = 0 // replaced in step 5 with the measured figure

	got := measure(t, middleware.Timeout(time.Second), nil)
	assertAllocBudget(t, "Timeout", got, want, 0) // race slack decided in step 5
}
```

`middleware/alloc_test.go` then needs `"time"` in its imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./middleware/ 2>&1 | head -10`
Expected: compile failure — `undefined: middleware.Timeout`.

- [ ] **Step 3: Implement**

Create `middleware/timeout.go`:

```go
package middleware

import (
	"context"
	"errors"
	"time"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
)

// Timeout gives the rest of the chain a deadline d, on the context it passes
// to its database and HTTP clients, and answers 503 when that deadline is what
// made the request fail.
//
// It is cooperative. The deadline is attached to c.Context(), and work that
// honours its context — which database drivers and HTTP clients do — stops
// when it passes. A handler that ignores its context is not stopped: it runs
// to completion and its answer, however late, is returned. Timeout is a
// deadline the work is asked to honour, not a switch that cuts it off. See
// docs/adr/0014-timeout-is-cooperative.md for why it cannot be the latter as a
// middleware, and for the transport-level alternative.
//
// The chain's error becomes 503 Service Unavailable when all three hold: the
// chain returned an error; this middleware's own deadline has passed; and the
// error is not already an *rice.HTTPError, since an author who chose a status
// keeps it. It asks its own context rather than the error because many
// database drivers report a timeout in their own error type without unwrapping
// to context.DeadlineExceeded. The cause stays in HTTPError.Err; the client
// receives only the status text. A context cancelled rather than timed out is
// left alone.
//
// Install it inside Logger, so the 503 is what Logger records. A route's own
// Timeout inside an application-wide one can only shorten the deadline, never
// lengthen it: a context's deadline is never later than its parent's.
//
// A d of zero or less panics.
func Timeout(d time.Duration) rice.Middleware {
	if d <= 0 {
		panic("rice: middleware.Timeout: duration must be positive")
	}
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			// Derived from c.Context(), not context.Background(), so the
			// force-close signal of ADR-0010 still reaches the handler.
			prev := c.Context()
			ctx, cancel := context.WithTimeout(prev, d)
			defer cancel() // release the timer now, not when the deadline passes
			c.SetContext(ctx)
			// The deadline belongs to the chain inside Timeout only. Restoring
			// the previous context keeps an outer middleware — Logger, which
			// hands c.Context() to slog — from receiving one that has expired.
			defer c.SetContext(prev)

			err := next(c)
			if err == nil || ctx.Err() != context.DeadlineExceeded {
				return err
			}
			var he *rice.HTTPError
			if errors.As(err, &he) {
				return err
			}
			return &rice.HTTPError{Code: fasthttp.StatusServiceUnavailable, Err: err}
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test`, then `go test -tags ricedebug ./...`
Expected: green apart from `TestAllocBudgetTimeout`, whose `want` is still 0.

- [ ] **Step 5: Measure on both platforms, then pin**

Run all four, and record each output verbatim:

```bash
go test ./middleware/ -run TestAllocBudgetTimeout -count=1 -v
go test -race ./middleware/ -run TestAllocBudgetTimeout -count=1 -v
docker run --rm --user 1000:1000 -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath \
  -e GOFLAGS=-buildvcs=false -v "$PWD":/src -w /src golang:1.25.14 \
  go test ./middleware/ -run TestAllocBudgetTimeout -count=1 -v
docker run --rm --user 1000:1000 -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath \
  -e GOFLAGS=-buildvcs=false -v "$PWD":/src -w /src golang:1.25.14 \
  go test -race ./middleware/ -run TestAllocBudgetTimeout -count=1 -v
```

Each fails, printing its figure. **Do not guess them.** Set `want` to the figure measured without `-race`; darwin and Linux must agree on it — if they do not, stop and report, because the off-race pin would then be platform-dependent too.

Then decide the race slack from the evidence, and record the reasoning in the test's doc comment:
- If both `-race` figures equal the off-race figure, the slack is 0. Say so, and name the platforms it was measured on.
- If a `-race` figure is higher, find the allocation that differs before choosing a slack: `go test -race -run TestAllocBudgetTimeout -memprofile mem.out -memprofilerate=1 ./middleware/` followed by `go tool pprof -sample_index=alloc_objects -top mem.out`, on the platform that differs. The slack is the number of allocations that mechanism adds, and the doc comment names the mechanism, as `assertAllocBudget`'s own comment requires. Never raise it until the test goes green.

Finally confirm the pin: off-race, `want` = N-1 must FAIL and N+1 must FAIL; restore N. Record all outputs, and drop the `// replaced in step 5` comments.

- [ ] **Step 6: Commit**

```bash
git add middleware/timeout.go middleware/timeout_test.go middleware/alloc_test.go
git commit -F - <<'EOF'
middleware: add Timeout, a deadline the work is asked to honour

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 2: ADR-0014 and the documentation

**Files:**
- Create: `docs/adr/0014-timeout-is-cooperative.md`
- Modify: `docs/adr/README.md`, `docs/04-roadmap.md`, `docs/07-retrospective.md`, `README.md`, `docs/02-architecture.md`, `docs/05-performance-model.md`, `middleware/doc.go`, `docs/progress.md`

**Interfaces:**
- Consumes: the four measured figures and the race-slack reasoning from Task 1.
- Produces: nothing code depends on.

- [ ] **Step 1: Write ADR-0014, "Timeout is cooperative"**

Follow `docs/adr/README.md`'s format — `Context`, `Decision`, `Alternatives`, `Consequences` — `Status: Accepted`, `Date` the day the work lands. It must contain:

- **Context:** the spec's findings 1 and 2. fasthttp's `TimeoutWithCodeHandler` (`server.go:485`) runs the handler on a goroutine and, on timeout, marks the `RequestCtx` timed out (`TimeoutErrorWithResponse`, `server.go:1705`); the server abandons it rather than reuse it (`server.go:2624-2628`), and `releaseCtx` panics on a timed-out one (`server.go:3068`). A pre-emptive rice middleware, probed under `-race`, raced with the middleware outside it and returned a pooled `Ctx` the handler still held.
- **Decision:** the cooperative middleware, as the spec's D1–D6 describe.
- **Alternatives**, each with why it lost: **a pre-emptive middleware** (a data race with every outer middleware, which share one `*Ctx`, and a use-after-release on top); **documenting `fasthttp.TimeoutHandler` only** (it does cut the client off whatever the handler does, but costs a goroutine per request, bypasses rice's funnel and middleware for the timeout response, and makes the access log record the handler's eventual status instead of the 503 the client received — the problem ADR-0013 was written to fix).
- **Consequences:** makes easy — a deadline every context-aware driver honours, answered through the funnel and logged correctly; makes hard — code that ignores its context is not stopped, and the ADR says so plainly. Name the transport-level escape hatch and its costs.
- **The correction:** earlier documents presented a pre-emptive timeout as unsafe outright. It is unsafe as a middleware and safe at the transport.

Add its row to `docs/adr/README.md`'s index.

- [ ] **Step 2: Update the roadmap and correct its claim**

In `docs/04-roadmap.md`: remove "Route-level and global timeout middleware" from *Explicitly deferred*; add a *Done after M8* entry naming `middleware.Timeout` and ADR-0014; and correct line 224, which says a pre-emptive timeout "would release the `Ctx` while the handler's goroutine still holds it", so it says that is true of a middleware and not of the transport.

- [ ] **Step 3: Stop the retrospective copying the deferred list**

`docs/07-retrospective.md` line 159 onward enumerates the roadmap's *Explicitly deferred* items. Three consecutive branches changed that list and left this copy stale, and this branch changes it again. Replace the enumeration with a pointer, so there is no copy to go stale:

> Nothing is scheduled. The roadmap's [Explicitly deferred](04-roadmap.md#explicitly-deferred) list names what is not promised; each item on it would need its own design.

Keep every sentence that follows it — the ones recording which items have left the list — unchanged. Then search the rest of `docs/` for any other copy of the list's items and report what you find.

- [ ] **Step 4: Update the remaining documents**

- `middleware/doc.go`: the recommended order gains `Timeout`, inside `RequestID`; state that it is cooperative, in one sentence.
- `README.md`: `Timeout` in the middleware section, with the recommended order, the route-level example, the fact that nesting only shortens, and the cooperative limitation in one plain sentence.
- `docs/02-architecture.md`: `timeout.go` in `middleware/`'s layout; the sentence listing what is still unbuilt loses Timeout.
- `docs/05-performance-model.md`: a row for `middleware.Timeout` in *Opt-in packages*, with its fixture, its figure, and a race note stating exactly what Task 1 measured on each platform.

- [ ] **Step 5: Write the progress entry**

Prepend to `docs/progress.md`, milestone `post-M8`. **Learned** must carry: that a pre-emptive timeout is possible at the transport — fasthttp abandons the timed-out context — and impossible as a middleware, which the probe showed; that this corrects the entry at `docs/progress.md:203` and the roadmap sentence, which presented it as impossible outright; and that `Timeout` asks its own context rather than the error, because drivers do not reliably unwrap. **Measured:** the four figures from Task 1, and the coverage from `make cover`. **Next:** CORS.

- [ ] **Step 6: Verify and commit**

Run: `make test && go test -race ./... && go test -tags ricedebug ./... && make lint && make cover`

```bash
git add docs/ README.md middleware/doc.go
git commit -F - <<'EOF'
docs: ADR-0014 and the docs for Timeout

Co-Authored-By: <your session's trailer>
EOF
```

---

## Self-Review

**Spec coverage.** D1 → Task 1 step 3. D2's three conditions → `TestTimeoutAnswers503…`, `TestTimeoutAsksItsContextNotTheError`, `TestTimeoutKeepsAnHTTPErrorTheHandlerChose`. D3 → `TestTimeoutDoesNotStopAHandlerThatIgnoresTheContext` (success after the deadline) and `TestTimeoutLeavesACancellationAlone`. D4 → `TestTimeoutRestoresThePreviousContext`. D5 → `TestANestedTimeoutOnlyShortens` and `TestLoggerOutsideTimeoutRecords503`. D6 → the same test as D3's success case: the spec lists "success after the deadline" and "a handler that ignores its context" separately, but they are one observable behaviour, so one test pins both and its comment names both rather than duplicating it. The timer release → `TestTimeoutReleasesItsTimer`. The construction panic → `TestTimeoutPanicsOnANonPositiveDuration`. The two-platform budget rule → Task 1 step 5. Documentation, including the correction and the retrospective pointer → Task 2. Exit criteria → Task 2 step 6, with the Linux run in Task 1 step 5.

**Placeholder scan.** The budget figure and race slack are deliberately absent: they are measured in step 5, and inventing them is what that step exists to prevent. Commit trailers read `<your session's trailer>`. Every other step carries its content.

**Type consistency.** `Timeout(d time.Duration) rice.Middleware` is the same in both tasks. The new helpers `timedApp`, `send` and `waitDone` collide with none of the names listed under File Structure. `assertAllocBudget` is called with its current five-argument signature.
