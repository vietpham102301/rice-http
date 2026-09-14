# ADR-0008 — rice recovers panics in core

Status: Accepted
Date: 2026-09-14

## Context

`docs/03-core-concepts.md` §7 has said since M1 that "the core installs a panic hook on the
fasthttp server so a panicking handler produces a 500 and does not take the connection
down." Both halves are false, and M5's probe is why this ADR exists.

fasthttp v1.73.0 has no `PanicHandler` and no equivalent:

```
$ grep -rn "PanicHandler" $(go env GOMODCACHE)/github.com/valyala/fasthttp@v1.73.0/*.go
$
```

No matches. The module's only `recover()` on the request path is in
`Response.writeBodyStream` (`http.go:2285`), which guards body-stream writes and runs nowhere
near a handler panic; a second, in `fasthttpadaptor/adaptor.go:66`, is outside the request
path entirely and unused by rice. `server.go:2621` calls the handler bare:

```go
if continueReadingRequest {
    s.Handler(ctx)
}
```

from inside `workerPool.workerFunc`, one goroutine per connection, with nothing above it on
the call stack that recovers.

A probe against `main` before M5 began confirmed what that implies, rather than assuming it:

```
GET /ok   -> 200 <nil>
panic: handler exploded
...
github.com/vietpham102301/rice-http.(*App).handle(...)  app.go:113
github.com/valyala/fasthttp.(*Server).serveConnCounted(...)  server.go:2621
github.com/valyala/fasthttp.(*workerPool).workerFunc(...)  workerpool.go:225
exit status 2
```

An unrecovered panic in one handler kills the whole process — every other in-flight
connection with it — not just the connection that triggered it. A documented safety
guarantee had been untrue for four milestones, because nothing had tested it.

## Decision

`App.handle` wraps the entire dispatch — lookup, chain call, error funnel — in one deferred
`recover()`, converting a recovered panic into a `*PanicError` and routing it through the
same `ErrorHandler` every other failure uses. `callErrorHandler` carries a second, identical
recover beneath it, so a panic inside the `ErrorHandler` itself does not resurrect the
process-killing failure this ADR removes.

This is unconditional core behaviour, not something a user opts into. It cannot be, because
of the Context finding above: fasthttp gives no per-connection safety net for an application
built directly on it to fall back on if the application declines to add its own.

**Measured cost**, two arms in one benchmarking session so they are directly comparable (ten
samples each, `bench/results/M5-error-handling.txt`):

| | median | range | allocs |
| --- | ---: | --- | ---: |
| `BenchmarkNoRecoverBaseline` | 0.91 ns | 0.91 – 0.92 | 0 |
| `BenchmarkDeferRecoverOverhead` | 3.02 ns | 3.01 – 3.03 | 0 |

About **2.1 ns**, zero additional allocations, ranges non-overlapping. `TestAllocBudgetDispatchWithRecover`
in `alloc_test.go` pins the zero-allocation half as a test, not just a benchmark reading: a
dispatch with recovery installed and nothing panicking costs exactly the one allocation the
`Ctx` itself costs, the same as it would with no recovery at all.

This is a **deliberate exception to design principle 7**, "the hot path branches as little as
possible, and pays no cost for unused features." Every request pays for a `defer` whether or
not anything panics. The exception is accepted because the alternative is not "no cost for a
feature nobody used" — it is a framework that can be brought down entirely by a single
handler bug in production, in exchange for roughly two nanoseconds. Principle 7's own
rule — "when two principles conflict, the lower number wins, and the conflict gets an
ADR" — is what this document is.

## Alternatives

**Recovery as opt-in `middleware.Recover` only, and nothing in core.** This is what Gin and
Fiber ship: a `Recover()`/`Recovery()` middleware the application must install itself.
Checked rather than assumed, the two frameworks are not equivalent here:

- Gin sits on `net/http`. Go's standard library recovers panics per connection on its own —
  `net/http/server.go`'s `(*conn).serve` (`recover()` guarding the whole per-connection loop)
  logs the panic and closes that one connection, whatever the application does. Gin's
  `Recovery()` middleware (`recovery.go` in `github.com/gin-gonic/gin@v1.11.0`, the only
  non-test `recover()` in that module) changes *what response the client sees*; it does not
  change whether the process survives, because `net/http` already guarantees that.
- Fiber sits on fasthttp, the same engine rice uses. `github.com/gofiber/fiber/v2@v2.52.5`
  has no `recover()` outside its test files — grepped the same way as the fasthttp check
  above. Fiber's base is `fasthttp`, and fasthttp has no per-connection recovery of its own,
  confirmed in the Context section. So an application built on Fiber that skips its
  `Recover()` middleware is exposed to exactly the failure this ADR's probe reproduced: one
  panicking handler takes the whole process down.

So "recovery as opt-in only" is safe for Gin because its transport already recovers
underneath it regardless of what the framework does, and is not safe for Fiber, which
inherits the same gap rice would if it made the same choice. Rejected for rice: fasthttp
gives no such backstop, the probe demonstrates the failure is real rather than theoretical,
and the measured cost of closing it — about 2 ns, zero allocations — is far cheaper than the
failure mode it prevents. Fiber shipping the same exposure is not a precedent to follow; it
is the same trap, undocumented on that side.

**Recover at the connection or server level, via a fasthttp configuration hook.** Rejected
because no such hook exists to configure — this alternative is the false claim §7 made, and
the reason this ADR exists is that it was never available to build.

**Recover only around the user's handler call, not the whole dispatch.** Considered: put the
`defer recover()` around `h(c)` in `handle` rather than around lookup and the error funnel
too. Rejected because the funnel itself — `callErrorHandler` calling a user-supplied
`ErrorHandler` — can panic just as easily as a handler can, and a narrower recover would
leave that path exposed again. `callErrorHandler`'s own last-resort recover exists for
exactly this case; see `TestAPanickingErrorHandlerRecoversFromAPanickingHandlerToo`.

## Consequences

**Makes easy:** a rice application cannot be taken down by a single panicking handler,
without the author having to know that fasthttp needs this and net/http-based frameworks do
not. `middleware.Recover` still exists and still does something core recovery cannot: it
converts a panic into an ordinary returned `error` *before* it unwinds past outer
middleware, so a logging or auditing middleware installed outside it still runs. Core
recovery, by contrast, sits outside the entire compiled chain, so every middleware frame has
already unwound by the time it fires.

**Makes hard:** nothing on the request path — the whole point of measuring the cost was to
confirm this before committing to it. It does mean the hot path is not literally
branch-free; principle 7 is now qualified rather than absolute, and any future request to
remove "the unconditional defer" as dead weight should be pointed at this document first.

**Forecloses:** describing rice's core dispatch as free of deferred function calls. It does
not foreclose swapping the recovery mechanism later if a cheaper one is found — the
allocation and time budgets in `alloc_test.go` and `05-performance-model.md` would simply be
re-measured against it.
