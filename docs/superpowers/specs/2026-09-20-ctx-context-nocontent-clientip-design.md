# Ctx — Context, NoContent and ClientIP: Design

**Status:** approved, not yet implemented
**Date:** 2026-09-20
**Milestone:** none. Work after the roadmap, like the read accessors — see
[04-roadmap.md](../../04-roadmap.md), *Done after M8*
**Decision record:** ADR-0010 (new), "The request context cancels at force-close, not at
disconnect"
**Depends on:** M6 (the pooled `Ctx`, `reset` and the borrow contract), M7 (`Shutdown`, the
force-close path, and [ADR-0009](../../adr/0009-shutdown-force-closes-at-deadline.md))

## Goal

Give a service the three things it needs from `Ctx` that core does not have: a
`context.Context` to pass to a database or an HTTP client, a status-only response, and the
client's IP address.

The scope came from the first service that will use rice: a JSON API with token auth, behind a
reverse proxy. Cookies, form/multipart and `Redirect` were on the list and were cut. Nothing in
that service needs them, and designing a multipart API without a use case is how a framework
grows a shape nobody wanted.

## Question this design answers

What does fasthttp's `context.Context` implementation actually promise, and what can rice
honestly promise on top of it?

## What reading fasthttp found

Everything below is fasthttp v1.73.0, the pinned version.

1. **`RequestCtx` implements `context.Context`, but at the scope of the server, not the
   request.** `Deadline()` returns `(time.Time{}, false)` always — its own doc says it "is only
   present to make RequestCtx implement the context interface". `Done()` returns `ctx.s.done`,
   the *server's* channel. `Value(key)` is `UserValue(key)`, fasthttp's per-request store, which
   is not rice's `Set`/`Get` store.

2. **That channel closes when shutdown begins, not when the grace period ends.**
   `ShutdownWithContext` closes the listeners and then `close(s.done)` (`server.go:2072`), before
   any draining. Handing it to a handler would cancel every in-flight request the moment
   `Shutdown` is called — cutting off exactly the requests M7's drain exists to let finish. A
   passthrough is not a cheap version of this feature; it is a regression against M7.

3. **Per-request cancellation is not available at a price rice will pay.** fasthttp's own
   comment explains why `Done()` is server-wide: "creating a new channel for every request is
   just too expensive". Detecting a client disconnect would need that channel, or a goroutine
   per request. Principle 1 refuses both. rice therefore cannot offer cancel-on-disconnect, and
   says so rather than implying it.

4. **`SetRemoteAddr` exists.** A proxy-trust policy can rewrite what fasthttp reports as the
   remote address, so `c.ClientIP()` needs no new state on `Ctx` and no knowledge of headers.
   The policy lives in a middleware, which is where principle 4 puts it.

5. **A failed start does not force-close.** `Serve`'s `runStart` error path closes the listener
   and returns (`server.go:77-80`); it never reaches `closeConns`. A retried `Serve` therefore
   still has a live base context, and keeping that true is a constraint on where the cancel goes.

## Non-goals

- **Cancel on client disconnect.** Not implementable on fasthttp at an acceptable cost. See
  finding 3 and ADR-0010.
- **Cookies, form/multipart, `Redirect`.** Cut from scope; each gets its own brainstorm when a
  real use case arrives.
- **Parsing `X-Forwarded-For`.** That is `middleware.RealIP`, a separate spec.
- **Any new `Option`.** This design adds none, and that is a check on it, not a coincidence.
- **Values in the base context.** See D8.

## Decisions

### D1 — rice owns the context; the field is on `Ctx`, the value is on `App`

`App` gains `baseCtx context.Context` and `cancelBase context.CancelFunc`, built once in `New`
from `context.WithCancel(context.Background())`. `Ctx` gains one field, `ctx context.Context`.

`reset` sets `c.ctx = nil` — it must drop the reference anyway — and `Context()` falls back to
`c.app.baseCtx` when the field is nil:

```go
func (c *Ctx) Context() context.Context {
	c.poison.check()
	if c.ctx != nil {
		return c.ctx
	}
	return c.app.baseCtx
}
```

No per-request assignment beyond the nil store `reset` already owes, so the hot path gains
nothing measurable and the budget stays 0.

The fallback reads `c.app`, which `release` sets to nil. In the debug build the poison check
catches a use-after-release first; in a release build `Context()` on a released `Ctx` panics on
a nil dereference instead of returning something plausible. That is the better of the two
failures available and is documented, not relied upon.

Rejected: a passthrough to `c.fctx` (finding 2, and it would foreclose `middleware.Timeout`),
and making `*Ctx` itself a `context.Context` as gin does (it fuses the borrow contract with
context lifetime — a driver that retains the context retains a pooled `*Ctx`, the exact
use-after-release class M6's poisoning exists to catch).

### D2 — the cancel lives inside `closeConns`, and runs before the sockets close

`closeConns` is already the one-way latch for force-close: it sets `forceClosed` under
`connMu`, so a connection reported afterwards is closed on arrival. Cancelling the base context
is the same event, so it belongs in the same function rather than at the two `Shutdown` call
sites, where it could be forgotten by a third one.

Order inside the function: take the connection snapshot under `connMu`, release the lock, call
`cancelBase()`, then close the sockets. A handler blocked on a database call learns to abandon
its work from the context, rather than from a write to a socket that is already gone.
`cancelBase` runs outside `connMu` because calling a callback under a lock is how deadlocks are
built.

### D3 — three moments that deliberately do not cancel

| Moment | Cancels? | Why |
| --- | --- | --- |
| `Shutdown` begins | No | This is fasthttp's mistake (finding 2). Cancelling here cuts off requests that would have finished inside the grace period |
| A clean drain finishes | No | Every handler has already returned. The signal would reach nobody |
| An `OnStart` hook fails | No | That path never force-closes (finding 5), and `Serve` stays retryable |

### D4 — `Context()` is the first accessor that returns an owned value

The borrow contract in `doc.go` says everything reachable from a `*Ctx` is borrowed and dies
when the handler returns. After this change that sentence is false: the context belongs to the
`App` and stays valid afterwards. The contract gets a named exception rather than a quiet one,
and a test pins it — `reset` clears the field, which must not affect a context already handed
out.

### D5 — `SetContext(nil)` panics

`SetContext` exists so middleware can install a derived context — a deadline for
`middleware.Timeout`, a value for a tracing library. A nil context is a programmer error, and
rice panics on those with a `rice: ` prefix, as `WithMaxBodySize` and the hook registrars do.

The documentation must say: derive from `c.Context()`, not from `context.Background()`.
A context derived from `Background` silently drops the force-close signal.

### D6 — `NoContent(code int)`, not `NoContent()`

It takes a code so 205 and 304 work, and so it matches the shape of `String(code, s)` and
`Bytes(code, b)`. It calls `ResetBody()` as well as setting the status: a 204 with a body is
malformed, and a handler that wrote before calling it would produce one.

It must also leave no `Content-Type` on the response. fasthttp adds a default one, so this is
asserted against the bytes on the wire, not against a getter.

### D7 — `ClientIP() net.IP`, and it ignores every header

`net.IP` is a `[]byte` taken from the connection's address: free, and borrowed like every other
`[]byte` from `Ctx`. There is no `ClientIPString`; a caller who wants one calls `.String()` and
pays the allocation where it can be seen.

The accessor never reads `X-Forwarded-For`. Trusting that header by default lets any client
declare its own address, which would corrupt the access log in spec 3 and any rate limiter
after it. `middleware.RealIP` will resolve the header against a declared number of trusted
proxy hops and call `SetRemoteAddr`; this accessor then reports the result without knowing it
happened.

### D8 — the base context carries no values

`Value` returns nil for every key. rice already has a per-request store behind `Set` and `Get`;
a second one with a different lifetime is two stores and two answers to "where does this
belong". Whether `middleware.RequestID` puts its id in the store, in the context, or both, is a
spec 3 decision and is deliberately not made here.

### D9 — no new `Option`

The whole design is methods on `Ctx` plus internal state. If a later spec in this group finds
itself adding `Option`s to express behaviour, that is a signal to re-read principle 4 before
writing the code.

## Components

| File | Change |
| --- | --- |
| `app.go` | `baseCtx`, `cancelBase` fields; built in `New` |
| `conns.go` | `closeConns` cancels the base context after releasing `connMu`, before closing sockets |
| `ctx.go` | `ctx` field; `reset` clears it; `Context`, `SetContext`, `ClientIP` |
| `ctx_response.go` | `NoContent` |

## Public API added

```go
func (c *Ctx) Context() context.Context
func (c *Ctx) SetContext(ctx context.Context)   // nil panics
func (c *Ctx) ClientIP() net.IP
func (c *Ctx) NoContent(code int) error
```

## Allocation budgets

Four new rows in [05-performance-model.md](../../05-performance-model.md), each 0, each broken
once on purpose and shown to fail before the code is written:

| Operation | Budget | Enforced by |
| --- | --- | --- |
| `c.Context` | 0 | `TestAllocBudgetContext` |
| `c.SetContext` | 0 | `TestAllocBudgetSetContext` |
| `c.ClientIP` | 0 | `TestAllocBudgetClientIP` |
| `c.NoContent` | 0 | `TestAllocBudgetNoContent` |

`Ctx` grows by one interface field, two words. `TestNewCtxStaysWithinThreeAllocations` and
every dispatch budget are expected to be unaffected and are re-run to confirm it rather than
assumed.

## Testing

**Lifecycle** — reusing the `slowApp` harness in `lifecycle_test.go`:

- a clean shutdown does not cancel an in-flight request's context (pins D3, row 1)
- a drain that times out cancels it, with `context.Canceled` (pins D2)
- a `Serve` retried after an `OnStart` failure still has a live context (pins D3, row 3)
- two `Shutdown` calls do not panic; the cancel is idempotent
- a context obtained in a handler is still usable after the handler returns (pins D4 —
  `reset` clears the field without disturbing the value already handed out)

**Behaviour**

- `SetContext` is what `Context` returns, seen from a later middleware and from the handler
- `SetContext(nil)` panics with a `rice: ` prefix, via the existing `mustPanic` helper
- `Context` and `SetContext` join the `ricedebug_test.go` table: use after release panics

**Response and IP**

- `NoContent(204)` on the wire: status 204, no body, **no `Content-Type`**
- `NoContent` after a body was written leaves no body
- `ClientIP` over a real loopback request is `127.0.0.1`
- `ClientIP` follows `SetRemoteAddr` (pins the mechanism `middleware.RealIP` will use)
- `ClientIP` ignores `X-Forwarded-For` — written as a test because it is a security claim

**Not tested, on purpose.** D2 orders the cancel before the socket closes. A test asserting
that order against a dying socket would race, so the tests assert only that the signal arrives.
The gap between the intent and what is measured is stated in the documentation rather than
hidden behind a flaky test.

## Benchmarks

None recorded. This follows the precedent set by the read accessors after M8: budget tests
cover the new methods, nothing on the dispatch path changes, and `bench/results/` gains no file.

## Documentation

- **ADR-0010 (new)** — "The request context cancels at force-close, not at disconnect".
  Records findings 1–3, and names the alternatives: the passthrough, `*Ctx` as a
  `context.Context`, and a per-request cancellation channel. Added to the ADR index.
- **`doc.go`** — the borrow contract gains its first named exception; the lifecycle section
  gains the force-close signal and the "derive from `c.Context()`" rule.
- **`03-core-concepts.md` §2** — the four methods, the ownership exception, the
  no-disconnect-detection warning.
- **`05-performance-model.md`** — the four budget rows, marked `MEASURED after M8`.
- **`02-architecture.md`** — the `ctx.go` and `ctx_response.go` layout lines.
- **`04-roadmap.md`** — an entry under *Done after M8*.
- **`progress.md`** — one entry when the work lands.

## Exit criteria

1. `make test` green, including the new lifecycle tests under `-race`.
2. Four budget tests at 0, each shown failing first.
3. `TestNewCtxStaysWithinThreeAllocations` and the dispatch budgets re-run and unchanged.
4. ADR-0010 written and indexed; the borrow contract in `doc.go` no longer contains a false
   sentence.

## Open questions

**Should the cancel carry a cause?** Go 1.25 has `context.WithCancelCause`, which would let
`context.Cause(c.Context())` report something more specific than `context.Canceled` — that the
response is about to be discarded, rather than a bare cancellation. It would need an exported
error for the cause to be inspectable, which is API surface this design does not otherwise add.
Recommendation: defer it. `context.Canceled` is correct, and the cause can be added later
without breaking anyone, which is the definition of a decision that does not need making now.
