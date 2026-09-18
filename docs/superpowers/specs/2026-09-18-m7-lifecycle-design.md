# M7 — Lifecycle: Design

**Status:** approved, not yet implemented
**Date:** 2026-09-18
**Milestone:** M7 in [docs/04-roadmap.md](../../04-roadmap.md)
**Decision record:** ADR-0009 (new), "Shutdown force-closes connections at the deadline"
**Depends on:** M1 (`Run`, `Serve`, `Addr`, the blunt `Shutdown`), M6 (a pooled `Ctx` is
released only when its handler returns, which constrains what a force-close can promise)

## Goal

Make stopping a rice server something a program can rely on.

M1 shipped a `Shutdown` that races fasthttp's shutdown against the context in a goroutine and
gives up when the context ends. M7 replaces it with a shutdown that drains in-flight requests
up to a deadline, guarantees that nothing is served once it returns, and runs the user's own
teardown after the server's. It adds the three server timeouts as options, and a
context-driven `RunContext` that a program wires to `signal.NotifyContext`.

## Question this milestone answers

What does fasthttp give for free here, and what has to be built?

The short answer, found while reading fasthttp v1.73.0 for this design, is "more than M1
assumed, and one thing less than it appears". The long answer is the three findings below
plus the shutdown-latency measurement in the benchmarks section.

## What reading the design found

1. **The comment on `ErrShutdownTimeout` is false for the pinned version.** It says
   "fasthttp's Shutdown has no deadline of its own". fasthttp v1.73.0 has
   `Server.ShutdownWithContext(ctx)`: it closes the listeners, closes idle keep-alive
   connections every 100 ms, and returns `ctx.Err()` when the context ends. `Shutdown()` is
   `ShutdownWithContext(context.Background())`. M1's goroutine is redundant, and on a timeout
   it leaks: it keeps waiting on a drain nobody is watching.

2. **`ShutdownWithContext` stops being a shutdown when it times out.** It sets its internal
   `stop` flag with `s.stop.Store(1)` and then `defer s.stop.Store(0)`. `serveConn` checks
   that flag to leave its keep-alive loop. When the deadline expires the flag is reset, and
   a keep-alive connection that was still busy goes on reading and serving *new* requests
   after `Shutdown` has returned. fasthttp does not close it. A caller reading "Shutdown
   returned an error" as "the server has stopped" is wrong, and would go on to close a
   database that live requests are still using.

3. **The three timeouts are already fields.** `ReadTimeout`, `WriteTimeout` and
   `IdleTimeout` on `fasthttp.Server`, all zero (unlimited) by default. Exposing them is
   API design, not engineering.

4. **A cancelled-before-serving race.** If `Shutdown` runs before fasthttp's `Serve` has
   recorded its listener, `ShutdownWithContext` sees no listener, returns `nil`, and the
   `Serve` that follows blocks forever. `RunContext` with an already-cancelled context hits
   this every time. fasthttp's `Serve` returns `nil` when handed a closed listener (it maps
   "use of closed network connection" to `io.EOF`), which is what D5 uses to close the hole.

## Non-goals

- **Restarting an App.** An App serves once. `Serve` after `Shutdown` returns without serving.
- **Stopping handler goroutines.** Go cannot kill a goroutine. A force-close ends the
  connection, so the handler's response write fails; the handler itself runs to completion
  and its `Ctx` is released then, as M6 requires.
- **Pre-drain hooks** (for example deregistering from a load balancer before draining).
  Considered and rejected in brainstorming; a program can do that before calling `Shutdown`.
- **Catching signals inside rice.** Core does not import `os/signal`; see D6.
- **Rice-chosen default timeouts.** Zero stays unlimited, so upgrading changes nothing.
- **Per-route timeouts.** Already listed under "Explicitly deferred" in the roadmap.

## Decisions

### D1: `Shutdown` delegates the drain to `ShutdownWithContext`

`Shutdown(ctx)` calls `a.srv.ShutdownWithContext(ctx)` directly. The M1 goroutine and its
`select` are deleted. The drain logic — close listeners, close idle connections, wait for the
open-connection count to reach zero — is fasthttp's, and rice does not duplicate it.

The cost of delegating is fasthttp's 100 ms poll: `Shutdown` can return up to ~100 ms after
the last in-flight request finishes. That is measured, not assumed (see Benchmarks), and
accepted: shutdown is not a hot path.

### D2: At the deadline, rice closes every connection it still tracks

Rice tracks open connections through `fasthttp.Server.ConnState`, installed in `New`:

- `StateNew` — add the `net.Conn` to `a.conns` (a `map[net.Conn]struct{}` under `a.connMu`).
- `StateClosed`, `StateHijacked` — remove it.
- `StateActive`, `StateIdle` — return immediately, before touching the lock.

fasthttp calls `ConnState` on every request (`Active`, then `Idle`), so the per-request cost
is one indirect call and a `switch` that returns. The map and mutex are touched only when a
connection opens or closes. `BenchmarkDispatchConnStateHook` has to show this is 0
allocations and within noise of dispatch without the hook; if it is not, this
decision is reopened.

When `ShutdownWithContext` returns a context error, `Shutdown` takes `a.connMu`, sets
`a.forceClosed = true`, closes every tracked connection, and releases the lock. A connection
that reaches `StateNew` after that point — accepted before the listener closed but reported
after the sweep — is closed on arrival because `forceClosed` is set. That closes the window
finding 2 opens without depending on timing.

Closing the connection makes the blocked `serveConn` read or write fail, the goroutine
exits, and fasthttp's `StateClosed` removes it from the map.

**Rejected: wrapping the listener.** A wrapper `net.Conn` allocates per connection and hides
the concrete type from fasthttp's `keepAliveConn` check in `acceptConn`, silently changing
TCP keep-alive behaviour.

**Rejected: re-implementing the drain.** Rice would need fasthttp's unexported `stop` flag
to make `serveConn` leave its loop.

### D3: `ErrShutdownTimeout` wraps the context's error

On a timeout `Shutdown` returns `fmt.Errorf("%w: %w", ErrShutdownTimeout, ctxErr)`, so both
`errors.Is(err, rice.ErrShutdownTimeout)` and `errors.Is(err, context.DeadlineExceeded)` (or
`context.Canceled`) hold. The variable's comment is rewritten to state D2's guarantee and to
drop finding 1's false claim.

A non-context error from `ShutdownWithContext` (a listener close error) is returned as is.

### D4: Hooks — `OnStart` FIFO before accepting, `OnShutdown` LIFO after draining

```go
func (a *App) OnStart(fn func() error)
func (a *App) OnShutdown(fn func(context.Context) error)
```

- **`OnStart`** hooks run in registration order inside `Serve`, after `Build` and after the
  listener exists, before any connection is accepted. The first error stops the sequence:
  `Serve` closes the listener and returns that error. Later hooks do not run, and no
  `OnShutdown` hook runs on account of it — the program never started.
- **`OnShutdown`** hooks run in reverse registration order, after the drain and after D2's
  force-close, each with the `ctx` passed to `Shutdown`. Reverse order matches the order of
  dependency: a resource opened first is torn down last, as `defer` does. Every hook runs even
  when an earlier one fails, and even when the drain timed out: a timed-out drain is exactly
  when teardown still has to happen.
- **At most once.** `OnShutdown` hooks run on the first `Shutdown` call only, guarded by a
  `sync.Once`. A second `Shutdown` still drains and force-closes, but runs no hooks.
- **They run even if the App never served.** A program that registers a hook, fails before
  `Run`, and calls `Shutdown` still gets its teardown.
- **Errors.** `Shutdown` returns `errors.Join(drainErr, hookErr1, hookErr2, ...)`, which is
  `nil` when all are `nil`. A panicking hook is not recovered: hooks are the program's own
  code at a point where rice has no response to fall back on, and swallowing a panic in
  teardown hides the bug.
- **Registration rules**, matching the rest of rice: a nil hook panics; registering after
  `Build` panics, since `Serve` may already have run the hooks.

### D5: `Serve` and `Shutdown` agree on a `closed` flag

`App` gains `closed bool`, guarded by the existing `a.mu`.

`Serve(ln)`:

1. `Build()`.
2. Under `a.mu`: if `closed`, close `ln` and return `nil`.
3. Run `OnStart` hooks (D4). On error, close `ln` and return the error.
4. Under `a.mu`: if `closed` (Shutdown ran during the hooks), close `ln` and return `nil`.
   Otherwise publish `a.ln = ln`.
5. `return a.srv.Serve(ln)`.

`Shutdown(ctx)`:

1. Under `a.mu`: set `closed = true`, read `ln := a.ln`.
2. D1 and D2: drain, and force-close on a timeout.
3. If `ln != nil`, close it and ignore the error. When fasthttp's `Serve` had recorded the
   listener, fasthttp already closed it and this close is a harmless no-op. When it had not
   (finding 4), this close is what makes that `Serve` return `nil` instead of blocking.
   The close comes *after* `ShutdownWithContext`, not before: closing first would make
   fasthttp's own listener close fail with "use of closed network connection", and
   `ShutdownWithContext` returns that error, so every clean shutdown would report one.
4. D4: `OnShutdown` hooks.

`Addr` keeps its meaning — the bound address once serving has been reached — and now becomes
non-empty only after `OnStart` succeeded, which is what the tests' `waitForAddr` wants.

### D6: `RunContext` is the signal helper; core does not import `os/signal`

```go
func (a *App) RunContext(ctx context.Context, addr string, grace time.Duration) error
```

1. Bind `addr` synchronously. A bind error is returned at once.
2. Start `Serve` in a goroutine.
3. Wait for whichever comes first:
   - `Serve` returns (an `OnStart` error, or a serve error): return that error.
   - `ctx` is done: call `Shutdown` with a fresh `context.WithTimeout(context.Background(),
     grace)` — not derived from `ctx`, which is already cancelled — then wait for `Serve` to
     return, and return `errors.Join(shutdownErr, serveErr)`.

`grace == 0` means no grace: the drain times out at once and every connection is
force-closed. A negative `grace` panics.

A program gets signal handling from the standard library:

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
err := app.RunContext(ctx, ":8080", 10*time.Second)
```

The signal list stays the program's choice, and tests cancel a context instead of sending a
real signal to the test process.

**Rejected: `RunGraceful(addr, grace)` catching SIGINT/SIGTERM itself.** It fixes the signal
list, pulls `os/signal` into core, and can only be tested by signalling the test binary.

### D7: Timeouts are three options, zero means unlimited

```go
func WithReadTimeout(d time.Duration) Option
func WithWriteTimeout(d time.Duration) Option
func WithIdleTimeout(d time.Duration) Option
```

Each sets the matching `fasthttp.Server` field. A negative duration panics in the option
constructor, like `WithErrorHandler(nil)`. Zero keeps fasthttp's meaning, unlimited — and for
`IdleTimeout`, "fall back to `ReadTimeout`", which the option's comment states. The README
recommends setting all three in production.

Options run in `New` before the App can serve, so the fields are written once and never
raced, on the same argument as `errorHandler`.

## Components

| Unit | Responsibility | Depends on |
| --- | --- | --- |
| `server.go` | `Serve` (D5), `Shutdown` (D1–D5), `RunContext` (D6), `ErrShutdownTimeout` | `lifecycle.go`, `conns.go` |
| `lifecycle.go` (new) | `OnStart`, `OnShutdown`, `runStart`, `runShutdown` | `app.go` |
| `conns.go` (new) | `connState` callback, `closeConns` | `app.go` |
| `app.go` | fields `closed`, `conns`, `connMu`, `forceClosed`, `onStart`, `onShutdown`, `shutdownOnce`; `ConnState` installed in `New`; the three timeout options | fasthttp |

## Public API added

```go
func (a *App) OnStart(fn func() error)
func (a *App) OnShutdown(fn func(context.Context) error)
func (a *App) RunContext(ctx context.Context, addr string, grace time.Duration) error
func WithReadTimeout(d time.Duration) Option
func WithWriteTimeout(d time.Duration) Option
func WithIdleTimeout(d time.Duration) Option
```

Changed, behaviour:

- `Shutdown` closes connections still open at the deadline; nothing is served after it
  returns.
- `Shutdown` runs `OnShutdown` hooks and joins their errors into its result.
- `ErrShutdownTimeout` now wraps the context's error.
- `Serve` runs `OnStart` hooks, and returns `nil` without serving if the App is already shut
  down.

## Allocation budgets

Dispatch budgets from M6 are unchanged: 0 allocations for a static route, a parameterised
route read as bytes, and a 404 — now with the `ConnState` hook installed on the server. The
existing `AllocsPerRun` guards run through `handle` and do not go through `ConnState`, so the
new benchmark (below) is what pins D2's per-request cost.

## Testing

All in `server_test.go` and a new `lifecycle_test.go`, black-box through a real listener
unless noted. Every test runs under `make test` (`-race`) and `make test-debug`.

**Drain (roadmap exit criterion 1).** A handler blocks on a channel. Start a request, call
`Shutdown` with a generous deadline in a goroutine, confirm a new TCP connection is refused,
release the handler, assert the in-flight request got its full 200 response and `Shutdown`
returned `nil`.

**Deadline (roadmap exit criterion 2).** A handler blocks until the test ends. `Shutdown`
with a 100 ms deadline returns within a bound (deadline + 500 ms slack for CI), and
`errors.Is` holds for both `ErrShutdownTimeout` and `context.DeadlineExceeded`.

**Finding 2 pinned.** On a keep-alive connection: first request completes, a second request
blocks in its handler, `Shutdown` times out; then a third request written on the same raw
connection gets no response and the read sees EOF or a reset. This test must fail against
plain `ShutdownWithContext` — checked by fault injection (below).

**Idle keep-alive connections** are closed by the drain and `Shutdown` returns promptly with
`nil`.

**Hooks.**
- `OnStart` hooks run in registration order before the first request is served.
- An `OnStart` error is returned by `Run`, later hooks do not run, and the port is free
  afterwards (a second `net.Listen` on the same address succeeds).
- `OnShutdown` hooks run in reverse order, after the in-flight request finished (the hook
  observes a flag the handler set), with the `Shutdown` ctx.
- Every `OnShutdown` hook runs when one fails; the errors are all `errors.Is`-reachable in
  the result, joined with a drain timeout when there is one.
- Two `Shutdown` calls run the hooks once.
- `Shutdown` on an App that never served runs the hooks and returns `nil`.
- Nil hook and hook after `Build` both panic.

**`Serve`/`Shutdown` ordering (finding 4).** `Shutdown` before `Serve`: `Serve` returns `nil`
promptly and runs no `OnStart` hooks. `RunContext` with an already-cancelled context returns
promptly. Both run with a test timeout so a regression hangs visibly rather than forever.

**`RunContext`.** Serves until the context is cancelled, then shuts down and returns `nil`;
an in-flight request finishes within `grace`; a bind error returns at once; an `OnStart`
error returns at once; negative `grace` panics.

**Timeouts.** With `WithReadTimeout(100ms)`, a raw client that sends half a request line and
stalls is disconnected. `WithIdleTimeout` closes an idle keep-alive connection. The
`WriteTimeout` option is asserted to reach the `fasthttp.Server` field through an
export-test accessor rather than an end-to-end slow reader, which is flaky on loopback
buffers. Negative durations panic.

**Connection tracking (package-internal).** After the drain completes normally, `a.conns` is
empty — nothing leaks. Under `-race`, many clients connect and disconnect while `Shutdown`
force-closes.

**Fault injection.** Each guard is broken on purpose and watched to fail: remove the
force-close (finding 2 test fails), remove D5's listener close (the before-serve test hangs
into its timeout), move it before `ShutdownWithContext` (the clean-shutdown tests see a
spurious error), run hooks FIFO (order test fails), drop the `sync.Once` (the run-once test
fails).

## Benchmarks

Recorded to `bench/results/M7-lifecycle.txt`.

- `BenchmarkDispatchConnStateHook` — the per-request `ConnState` cost: dispatch with and
  without the hook's `Active`/`Idle` calls, both arms in one session, in package `rice`.
  Must read 0 allocs; the delta is reported with its p-value.
- `BenchmarkShutdownLatency` — time from the last in-flight request completing to `Shutdown`
  returning, over a real listener. Expected 0–100 ms from fasthttp's poll (D1); recorded as
  the answer to "what does fasthttp give for free" rather than as a target. Also recorded:
  `Shutdown` with only idle connections, which fasthttp closes before its first poll.
- `BenchmarkRiceDispatch` re-recorded as the regression check against M6.

## Documentation

- **`docs/adr/0009-shutdown-force-closes-at-deadline.md`** — new. Context is finding 2;
  decision is D2; consequences include "handlers are not stopped" and the 100 ms poll.
- **`docs/adr/README.md`** — index entry.
- **`server.go`** — the `ErrShutdownTimeout` and `Shutdown` comments rewritten (finding 1).
- **`docs/03-core-concepts.md` §4** — `Shutdown`'s guarantee, hook order, `RunContext`, the
  timeouts.
- **`docs/02-architecture.md`** — `lifecycle.go` and `conns.go` in the file map.
- **`docs/05-performance-model.md`** — the `ConnState` row and the shutdown latency.
- **`docs/04-roadmap.md`** — M7 marked done.
- **`README.md`** — the `signal.NotifyContext` + `RunContext` example, the production
  timeouts advice, the status line.
- **`doc.go`** — lifecycle paragraph.
- **`docs/milestones/M7-lifecycle.md`** — retrospective.
- **`docs/progress.md`** — journal entry.

## Exit criteria

1. The drain test: a slow in-flight request completes during `Shutdown` while a new
   connection is refused.
2. The deadline test: `Shutdown` returns at the deadline when a request never finishes.
3. The finding-2 test: nothing is served on a surviving keep-alive connection after a
   timed-out `Shutdown`, and it fails with the force-close removed.
4. The hook-order, run-once and before-serve tests pass.
5. `BenchmarkDispatchConnStateHook` shows 0 allocations, and `BenchmarkShutdownLatency` is
   recorded.
6. `make lint`, `make test`, `make test-debug`, `make cover` and `make bench` all exit 0;
   root package coverage does not drop below 98.8%.

## Open questions

None. The timeout behaviour (force-close), hook semantics (FIFO start, LIFO shutdown after
drain, errors joined) and the signal helper's shape (`RunContext`, no `os/signal` in core)
were settled in brainstorming. Findings 1–4 came from reading fasthttp v1.73.0's source for
this design.
