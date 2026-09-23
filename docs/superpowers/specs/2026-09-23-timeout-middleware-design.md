# Timeout middleware — Design

**Status:** approved, not yet implemented
**Date:** 2026-09-23
**Milestone:** none. Work after the roadmap — see [04-roadmap.md](../../04-roadmap.md), *Done after M8*
**Decision record:** ADR-0014 (new), "Timeout is cooperative"
**Depends on:** `c.Context` and `c.SetContext` ([ADR-0010](../../adr/0010-request-context-cancels-at-force-close.md)),
`c.HandleError` and `middleware.Logger` ([ADR-0013](../../adr/0013-middleware-can-settle-a-request.md))

## Goal

Give a request a deadline that the work it triggers — a database query, an outbound HTTP call —
honours, and answer 503 when that deadline is what made the request fail.

## Question this design answers

Can rice stop a handler that runs too long, and if not, what can a timeout honestly promise?

## What probing found

Run against rice at `7084231` and fasthttp v1.73.0.

### 1. A pre-emptive timeout is possible — at the transport, not as a middleware

fasthttp ships `TimeoutWithCodeHandler` (`server.go:485`). It runs the handler on a goroutine and,
when the timer fires first, calls `ctx.TimeoutErrorWithCode`, which records a response and marks
the `RequestCtx` as timed out (`TimeoutErrorWithResponse`, `server.go:1705`). The server then
abandons that `RequestCtx` rather than reusing it — "Acquire a new ctx because the old one will
still be in use by the timeout out handler" (`server.go:2624-2628`) — and `releaseCtx` refuses to
pool a timed-out one. Abandoning the context is what makes it safe.

So `fasthttp.TimeoutHandler(app.FasthttpHandler(), d, msg)` is a safe pre-emptive timeout for a
rice App **today**: rice's whole `Ctx` lifecycle, including its release, happens inside the
goroutine fasthttp spawned.

### 2. As a rice middleware, the same idea races

A pre-emptive timeout written as a rice middleware runs the rest of the chain on a goroutine and
returns when the timer fires. But every middleware in a rice chain shares one `*Ctx`. A probe with
such a middleware inside a Logger-like middleware, under `-race`: the client receives 503, and the
race detector reports `WARNING: DATA RACE` — the abandoned handler goroutine writes the response
the outer middleware is reading and settling. Worse, when the middleware returns, `handle`
releases the `Ctx` to the pool while the goroutine still holds it: the use-after-release class
[ADR-0005](../../adr/0005-context-pooling-and-borrow-contract.md) exists to prevent. fasthttp's
own handler escapes this only because it sits outside everything, where nothing else shares the
context.

### 3. This corrects an earlier claim

The observability branch's documents say a pre-emptive timeout "would release the `Ctx` while the
handler's goroutine still holds it", and present that as the reason a timeout must be cooperative
— `docs/04-roadmap.md:224` and `docs/progress.md:203`. That is true of a middleware and false of
the transport. The roadmap sentence is corrected in place; the progress entry is merged, so the
correction is a new entry.

## Non-goals

- **A pre-emptive middleware.** Finding 2.
- **A configurable status.** The user chose a fixed 503; a custom `ErrorHandler` can answer
  differently, since the returned error wraps the cause.
- **`Retry-After`.** No service has asked for it.
- **Stopping code that ignores the context.** Nothing in a Go process can stop a goroutine from
  outside; see D6.

## Decisions

### D1 — `Timeout(d time.Duration) rice.Middleware`, cooperative

For each request:

```go
prev := c.Context()
ctx, cancel := context.WithTimeout(prev, d)
defer cancel()
c.SetContext(ctx)
defer c.SetContext(prev)
err := next(c)
```

The deadline is attached to the context the handler passes to its database and HTTP clients,
which already honour it. `d <= 0` panics at construction with a `rice: ` prefix.

Deriving from `c.Context()` rather than `context.Background()` keeps ADR-0010's force-close signal:
a request still running when shutdown gives up sees its context cancelled whether or not the
timeout has fired.

### D2 — the error becomes 503 when three things are all true

1. The chain returned an error.
2. **This middleware's own deadline has passed** — `ctx.Err() == context.DeadlineExceeded`, asked
   of the context this middleware created.
3. The error is not already an `*rice.HTTPError`.

Condition 2 asks the context rather than the error. Many database drivers wrap a timeout in their
own error type without unwrapping to `context.DeadlineExceeded`, so `errors.Is` on the returned
error would miss exactly the case a timeout exists for. Condition 3 follows the rule `binding`
set for `Validate`: an author who chose a status keeps it.

The 503 is `&rice.HTTPError{Code: fasthttp.StatusServiceUnavailable, Err: err}` with no message,
so the client receives the status text "Service Unavailable" and the driver's error text never
reaches it — the rule M5 established. The cause stays in `Err` for a custom `ErrorHandler` or
logger.

### D3 — two outcomes that are not rewritten

- **The deadline passed but the chain succeeded.** The work finished; a late answer is still an
  answer. It is returned as it is.
- **The context was cancelled, not timed out** — `context.Canceled`, from ADR-0010's force-close
  or from an outer middleware's own cancellation. That is not this middleware's timeout, so the
  error passes through untouched.

### D4 — the previous context is restored after the chain returns

Without the restore, a middleware outside `Timeout` — `Logger`, which hands `c.Context()` to
slog — would receive a context that has already expired. The deadline belongs to the part of the
chain inside `Timeout` only.

### D5 — placement, and nesting only shortens

`Timeout` goes inside `Logger`, so the 503 it returns is the status `Logger` records:

```go
app.Use(Logger(l), Recover(), RealIP(1), RequestID(), Timeout(5*time.Second))
app.GET("/report", h, middleware.Timeout(30*time.Second))
```

A route's `Timeout` inside an application-wide one can only shorten the deadline. A context's
deadline is never later than its parent's, so `Timeout(30s)` inside `Timeout(5s)` still expires at
five seconds. The documentation states this where the route-level example appears.

### D6 — the honest limitation

A handler that ignores its context is not stopped. It runs to completion, and its late result is
returned — a 200 an hour later is still a 200. `Timeout` is a deadline the work is asked to
honour, not a switch that cuts it off. The documentation says so in those words, and a test pins
it, so a change in behaviour forces a change in the documentation. Anyone who must cut the client
off regardless can wrap the whole App in `fasthttp.TimeoutHandler` at the transport — at the cost
of a goroutine per request, and of the access log recording the handler's eventual status rather
than the timeout the client received.

## Components

| File | Change |
| --- | --- |
| `middleware/timeout.go` (new) | `Timeout` |
| `middleware/timeout_test.go` (new) | behaviour |
| `middleware/alloc_test.go` | the budget |

No change to package `rice`.

## Allocation budget

`context.WithTimeout` allocates. The figure is measured and pinned exactly, in both directions,
without the race detector. **Before the documentation claims the figure is exact under `-race`,
it is measured on both darwin and Linux, with and without `-race`.** `RequestID`'s figure turned
out to differ between the two under `-race`, and a claim measured on one platform is a claim about
that platform. If the figures differ, the budget takes a race slack derived from the mechanism, as
`assertAllocBudget` requires.

## Testing

Timing tests block on `<-c.Context().Done()`, so they return the moment the deadline passes rather
than guessing with a sleep. Only the success-after-deadline case sleeps, with a wide margin.

- a handler that honours its context and returns `ctx.Err()` → **503**, body "Service Unavailable"
- **a driver-style error that does not wrap `context.DeadlineExceeded`**, returned after the
  deadline → still 503 — the test that proves D2 asks the context, not the error
- an `*rice.HTTPError` of 400 returned after the deadline → **400**
- the deadline passes but the handler returns success → **200**
- the parent context is cancelled rather than expiring → not 503
- a middleware outside `Timeout` sees the **previous** context after the chain returns
- the context a handler captured is **done** once the request finishes — proof `cancel` ran and
  the timer was released
- a route-level `Timeout` inside an application-wide one: the earlier deadline wins
- `Logger` outside `Timeout` logs **503**
- **the documented limitation, pinned**: a handler that ignores its context finishes late and
  returns 200
- `d <= 0` panics with a `rice: ` prefix

## Documentation

- **ADR-0014 (new)** — "Timeout is cooperative". Findings 1 and 2 with the probe as evidence; the
  three alternatives (a pre-emptive middleware; documenting `fasthttp.TimeoutHandler` only; the
  cooperative middleware) and why each lost or won; the correction in finding 3.
- **`docs/04-roadmap.md`** — remove "Route-level and global timeout middleware" from *Explicitly
  deferred*; add a *Done after M8* entry; correct line 224's claim in place.
- **`docs/07-retrospective.md`** — replace its enumeration of the *Explicitly deferred* list with a
  link to the list. Three consecutive branches changed that list and left the copy stale; this
  branch changes it again. Pointing at the list removes the copy that goes stale.
- **`README.md`**, **`docs/02-architecture.md`**, **`docs/05-performance-model.md`**,
  **`middleware/doc.go`** (the recommended order gains `Timeout`).
- **`docs/progress.md`** — one entry, carrying finding 3's correction of the entry at line 203.

## Exit criteria

1. `make test` green under `-race` and `-tags ricedebug`, on darwin, and in a Linux container.
2. Every D2, D3 and D6 case is pinned by a test that fails when its rule is broken.
3. The budget is measured on both platforms before any `-race` claim is written.
4. ADR-0014 written and indexed; no document still presents a pre-emptive timeout as impossible
   rather than impossible as a middleware.

## Open questions

None.
