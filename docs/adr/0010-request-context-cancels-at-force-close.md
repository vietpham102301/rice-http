# ADR-0010 — The request context cancels at force-close, not at disconnect

Status: Accepted
Date: 2026-09-20

## Context

A handler that calls a database or an outbound HTTP client needs a `context.Context` to pass
it. rice had none to give. The first service that will use rice is a JSON API that does both,
so `c.Context()` had to exist; the only question was what it promises.

fasthttp appears to answer it already. `*fasthttp.RequestCtx` implements `context.Context`, and
`c.RequestCtx()` has returned one since M1, so a handler could pass that. Reading the
implementation (fasthttp v1.73.0, the pinned version) shows it is a `context.Context` at the
scope of the server, not of the request:

- `Deadline()` returns `(time.Time{}, false)`, always. Its own comment: it "is only present to
  make RequestCtx implement the context interface" (`server.go:2985–2989`).
- `Done()` returns `ctx.s.done` — the *server's* channel, shared by every request
  (`server.go:2997`).
- `Value(key)` is `UserValue(key)`, fasthttp's per-request store. That is not rice's `Set`/`Get`
  store, so a value put in with `c.Set` is not readable through it.

The channel closes at the wrong moment. `ShutdownWithContext` closes the listeners and then
runs `close(s.done)` (`server.go:2072`), before it polls a single connection. Handing that
channel to handlers would cancel every in-flight request the instant `Shutdown` is called —
cutting off exactly the requests M7's grace period exists to let finish. A passthrough is not a
cheap version of this feature. It is a silent regression against
[ADR-0009](0009-shutdown-force-closes-at-deadline.md)'s drain, and one that only shows itself
during a deploy.

Per-request cancellation is not available at a price rice will pay either, and fasthttp says
why in the same file: "Because creating a new channel for every request is just too expensive,
so RequestCtx.s.done is only closed when the server is shutting down" (`server.go:2995–2996`).
Detecting a client disconnect needs that channel, or a goroutine per request to watch the
socket. Design principle 1 refuses both.

So rice cannot forward fasthttp's context, and cannot build the one users coming from
`net/http` expect. What it can build is a context whose one cancellation is a moment rice
itself owns.

## Decision

`c.Context()` returns a context rice owns, and it is cancelled at exactly one moment: when a
`Shutdown` gives up waiting and force-closes the connections.

`App` holds `baseCtx` and `cancelBase`, built once in `New` from
`context.WithCancel(context.Background())`. `Ctx` gains one field, `ctx context.Context`, which
`reset` clears; `Context()` returns that field when a middleware has set one with
`SetContext`, and `a.baseCtx` otherwise. No per-request context is allocated, and both
accessors hold a budget of 0.

The cancel lives in `closeConns`, the function ADR-0009 already made the one-way latch for
force-close. It runs after the connection snapshot is taken and `connMu` is released, and
before the sockets are closed: a handler blocked on a database call learns to abandon its work
from its context rather than from a write to a socket that is already gone. `cancelBase` runs
outside the lock because calling a callback under a lock is how deadlocks are built. Putting it
in `closeConns` rather than at `Shutdown`'s two call sites means a third call site cannot
force-close without cancelling.

Three moments deliberately do *not* cancel:

| Moment | Cancels? | Why |
| --- | --- | --- |
| `Shutdown` begins | No | This is fasthttp's mistake. Cancelling here cuts off requests the grace period was about to let finish |
| A clean drain finishes | No | Every handler has already returned. The signal would reach nobody |
| An `OnStart` hook fails | No | `Serve`'s start-error path closes the listener and returns (`server.go:77–80`); it never reaches `closeConns`, and a retried `Serve` must still have a live context |

The base context carries no values and no deadline. `Value` returns nil for every key: rice
already has a per-request store behind `Set` and `Get`, and a second one with a different
lifetime is two stores and two answers to "where does this belong".

`SetContext` is how a middleware installs a deadline or a value, and the contract on it is
*derive from `c.Context()`*, not from `context.Background()`. A context derived from
`Background` silently drops the force-close signal. `SetContext(nil)` panics.

## Alternatives

**The passthrough: return `c.fctx`.** Free, one line, and already implements the interface.
Rejected on two counts. Its `Done()` closes when `Shutdown` starts, so every in-flight request
would be cancelled at the moment the drain begins — the regression described above, invisible
until a deploy under load. And its `Value` reads fasthttp's user values rather than rice's
store, so a handler asking it for something a rice middleware set gets nil. It also forecloses
`middleware.Timeout`: there is no way to attach a deadline to a context rice does not own.

**Make `*Ctx` itself a `context.Context`, as gin does with `*gin.Context`.** It removes a
method call, and it is the shape a gin user expects. Rejected because it fuses the borrow
contract with context lifetime. A `*Ctx` is pooled and reused; a context is handed to a
database driver that may hold it past the call. A driver retaining the context retains a pooled
`*Ctx`, which is precisely the use-after-release class that
[ADR-0005](0005-context-pooling-and-borrow-contract.md)'s poisoning exists to catch. The two
lifetimes are different on purpose, and giving them one type asserts they are the same.

**A per-request cancellation channel, giving cancel-on-disconnect.** The feature users actually
want, and the only alternative that would deliver it: a channel per request, closed when the
connection dies, with a goroutine or a read deadline to notice. Rejected at the cost fasthttp
itself refuses in its own comment. A channel per request is an allocation on every request, and
the claim in [05-performance-model.md](../05-performance-model.md) is that a request allocates
zero heap objects in framework code. Principle 1 and that claim go together; this feature would
cost both.

## Consequences

**Makes easy.** `middleware.Timeout` has somewhere to attach a deadline, and a tracing
middleware somewhere to put a span, both through `SetContext` on a context derived from
`c.Context()`. A handler cut off by a timed-out drain can stop work instead of finishing a
database call whose result will be thrown away: ADR-0009 already says such a handler runs to
completion with its response lost, and until now it had no way to find that out. A context
obtained in a handler stays valid after the handler returns, so a background task started from
one is not reading freed memory.

**Makes hard: there is no cancel-on-disconnect, and there will not be while rice is on
fasthttp.** A client that hangs up mid-request leaves the handler running with a live context.
This is the one thing a user migrating from `net/http` must be told, because `*http.Request`'s
context does cancel on disconnect, and code written against that habit will wait for a timeout
that never comes. A handler that must bound its own work bounds it with a deadline it sets, not
with a disconnect it will not hear about. `Context()`'s doc comment,
[03-core-concepts.md](../03-core-concepts.md#2-ctx) and `doc.go` all say so.

**The cancel's ordering is the intent, not a measurement.** `closeConns` cancels before it
closes the sockets, and no test asserts that order: a test racing a dying socket against a
context notification would be flaky. The tests assert only that the signal arrives. The gap
between what is intended and what is pinned is stated here rather than hidden behind a test
that passes most of the time.

**`Context()` is the first accessor in rice that returns something the caller may keep.** The
borrow contract in `doc.go` was written as an absolute — "Every value reachable from a `*Ctx`
is borrowed, not owned" — and this decision makes that sentence false. The contract now carries
one named exception. An absolute with an exception is weaker than an absolute, and every future
accessor that wants to be owned will point at this one.

**A released `Ctx` fails differently here.** `Context()`'s fallback reads `c.app`, which
`release` sets to nil. Under `-tags ricedebug` the poison check catches the use-after-release
first; in a release build `Context()` panics on a nil dereference rather than returning a
plausible context. That is the better of the two failures available, and it is documented rather
than relied upon.

**Forecloses nothing permanently.** The cancellation is a bare `context.Canceled` today.
`context.WithCancelCause` would let `context.Cause()` report something more specific — that the
response is about to be discarded rather than a bare cancellation — and it needs an exported
error to be inspectable, which is API surface this decision does not otherwise add. It can be
added later without breaking anyone, which is the definition of a decision that does not need
making now.

**Revisit if fasthttp gives `RequestCtx` a per-request `Done`.** Everything above rests on
v1.73.0's implementation: `Done()` returning the server's channel, `close(s.done)` at
`server.go:2072` before the drain, and the cost fasthttp cites for the alternative. If fasthttp
starts closing a per-request channel on disconnect, this decision is re-read — cancel-on-
disconnect becomes available at fasthttp's price rather than rice's, and the non-goal above
stops being forced.
