# ADR-0013 — Middleware can settle a request with `c.HandleError`

Status: Accepted
Date: 2026-09-22

## Context

The error funnel runs after the entire chain has returned. `handle` calls the compiled chain and
only then, if it returned an error, calls the `ErrorHandler`:

```go
if err := h(c); err != nil {
    a.callErrorHandler(c, err)
}
```

That is M5's design ([ADR-0002](0002-handler-returns-error.md) for the returned error,
[ADR-0008](0008-rice-recovers-panics-in-core.md) for the panic path), and it has a consequence
nobody had written down: a middleware that reads the response status after `next(c)` returns reads
it before the funnel has written it. A probe against rice at `33e5d71` installed exactly that
middleware — the one every access logger is — and compared what it read with what the client
received:

| Request | Middleware read | Client received |
| --- | --- | --- |
| `/ok` | 200 | 200 |
| `/teapot` (returns an `HTTPError` 418) | **200** | 418 |
| `/boom` (returns a plain error) | **200** | **500** |
| `/nope` (no route) | *never ran* | 404 |

The last row is [ADR-0012](0012-application-middleware-runs-on-route-misses.md)'s problem. The
middle two are this one's. A Logger written the obvious way would have recorded every production
500 as a 200: an access log that is wrong precisely on the requests it exists to show.

The middleware does receive the error — it is `next`'s return value. What it cannot know is what
the error becomes. The mapping from error to status belongs to the `ErrorHandler`, and an App may
replace that with `WithErrorHandler`, so no middleware can compute it without running it.

## Decision

`Ctx` gains one method:

```go
func (c *Ctx) HandleError(err error)
```

It runs the App's `ErrorHandler` on `err` immediately, through the same `callErrorHandler` the
funnel uses, and sets an unexported `handled` flag on the `Ctx`. `reset` clears the flag, so a
pooled `Ctx` does not carry it into the next request. `handle` becomes:

```go
if err := h(c); err != nil && !c.handled {
    a.callErrorHandler(c, err)
}
```

so a response cannot be written twice — including when a middleware calls `HandleError(err)` and
then still returns `err`.

- **A nil error is a no-op.**
- **A second call is a no-op.** The first settles the request; `handled` is set before the
  `ErrorHandler` runs, so an `ErrorHandler` that itself calls `HandleError` does not recurse.
- **The panic path deliberately ignores `handled`.** A panic after `HandleError` still becomes a
  500. M5 established that a recovered panic anywhere in the chain is always a 500, and the
  response has not been sent yet — fasthttp writes it after `handle` returns — so overwriting it is
  correct.
- **`poison.check()` comes first**, before the nil check, so under `-tags ricedebug` a call on a
  released `Ctx` panics even with a nil argument; the debug-build test calls every exported method
  with zero-valued arguments and expects exactly that.

A request logger calls it on whatever `next` returned and then reads the status the client will
receive. `middleware.Logger` does exactly that, then returns nil.

`HandleError` allocates nothing of its own: `TestAllocBudgetHandleError` holds it at 0, measured
with `ErrNotFound` through the default funnel, which is itself free. Whatever a custom
`ErrorHandler` allocates is that handler's cost. The budget was broken once on purpose and failed
with `Ctx.HandleError allocated 3.0 objects per call, budget is 0`.

## Alternatives

**`rice.StatusOf(err) int`, a pure function mapping an error to the status the default funnel
would write.** No change to core, no flag, no second moment at which a response can be written.
Rejected because it is wrong for every App with a custom `ErrorHandler` — which is exactly the
App that has thought about its error responses. An `ErrorHandler` that answers a validation error
with 422, or a timeout with 503, would be logged as whatever `DefaultErrorHandler` would have
said. A log line recording what a different handler would have answered is a documented lie, and
the documentation would be the only thing standing between an operator and a wrong conclusion
during an incident.

**Running the funnel inside the chain, at its innermost point.** Compile every chain as
`mw1(mw2(funnel(handler)))`, where `funnel` calls the `ErrorHandler` on the handler's error and
returns nil. Every middleware would always see the real status, with no new method and nothing to
remember. Rejected because middleware would never see an error again. That reverses M5's design:
`middleware.Recover` exists precisely so that a panic becomes an error an *outer* middleware can
see, inspect and act on, and with the funnel innermost there would be nothing left for it to see.
It would also change what every existing middleware receives from `next` — from the handler's
error to nil — without any of them being told.

## Consequences

**Makes easy.** A middleware can record the status a request really ends with, from the default
`ErrorHandler` or a custom one, with one call. The funnel is still one function: `HandleError` runs
the same `ErrorHandler` through the same `callErrorHandler`, including its last-resort recovery
around a panicking custom handler.

**A middleware that calls it consumes the error, so it must be outermost.** Once a request is
settled, the error has done its work: `middleware.Logger` returns nil afterwards, so nothing
outside it sees the error at all. A middleware that calls `HandleError` and then returns the error
anyway lets outer middleware see it, but the response is already written, and an outer middleware
that rewrites that error changes nothing the client receives. Middleware that inspects or
translates errors belongs inside any middleware that settles them. The documentation says to
install `Logger` outermost for this reason.

**The funnel is one function, but no longer one moment.** Before this decision, a response to an
error was written after the whole chain returned. Now it may be written in the middle of the
chain, by whichever middleware calls `HandleError` first. A reader asking "when is the error
response written" has two answers where there was one.

**`HandleError` cannot see a panic that unwinds past it.** A panic is not a returned error, so a
middleware settling errors never receives one unless `middleware.Recover` sits inside it and turns
the panic into an error first. For `Logger` this means a panicking request is not logged at all
unless `Recover` is installed inside it. Letting `Logger` recover and re-panic was considered and
rejected: the request would be logged, but the re-panic moves the stack trace core records to
`Logger`'s frame, pointing whoever debugs the panic at the wrong code. The limitation is documented
in `middleware/doc.go` and in `Logger`'s doc comment, and `TestLoggerDoesNotLogAPanicWithoutRecoverInside`
pins it, so a change in behaviour forces a change in the documentation.

**`Ctx` grew by one `bool`.** `reset` clears it on every request, alongside the fields it already
clears. `TestNewCtxStaysWithinThreeAllocations` and every dispatch budget were re-run with the new
field and did not move.

**Forecloses nothing.** A future `StatusOf`, if a use case without a `Ctx` appears, can be added
beside this; nothing here depends on it being absent.
