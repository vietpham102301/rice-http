# ADR-0014 — Timeout is cooperative

Status: Accepted
Date: 2026-09-23

## Context

A service wants a request to have a deadline: a report query that runs for a minute should be
abandoned, and the client told so. The obvious timeout is pre-emptive — run the handler, and when
the timer fires first, answer the client and stop waiting. Two probes, against rice at `7084231`
and fasthttp v1.73.0, found that whether that is safe depends on where it is installed.

**At the transport it is safe.** fasthttp ships one. `TimeoutWithCodeHandler` (`server.go:485`)
runs the handler on a goroutine and, when the timer fires first, calls `ctx.TimeoutErrorWithCode`,
which records a response and marks the `RequestCtx` timed out (`TimeoutErrorWithResponse`,
`server.go:1705`). The server then abandons that `RequestCtx` rather than reuse it — "Acquire a new
ctx because the old one will still be in use by the timeout out handler" (`server.go:2624-2628`) —
and `releaseCtx` panics rather than pool a timed-out one (`server.go:3068`). Abandoning the context
is what makes it safe. So `fasthttp.TimeoutHandler(app.FasthttpHandler(), d, msg)` is a safe
pre-emptive timeout for a rice App today: rice's whole `Ctx` lifecycle, its release included,
happens inside the goroutine fasthttp spawned.

**As a rice middleware it races.** A pre-emptive middleware runs the rest of the chain on a
goroutine and returns when the timer fires. But every middleware in a rice chain shares one
`*Ctx`. The probe installed such a middleware inside a Logger-like middleware and ran it under
`-race`: the client received 503, and the race detector reported `WARNING: DATA RACE` — the
abandoned handler goroutine writing the response the outer middleware was reading and settling.
Worse, when the middleware returns, `handle` releases the `Ctx` to the pool while the goroutine
still holds it: the use-after-release class
[ADR-0005](0005-context-pooling-and-borrow-contract.md) exists to prevent. fasthttp's own handler
escapes this only because it sits outside everything, where nothing else shares the context.

**This corrects an earlier claim.** The observability branch's documents said a pre-emptive
timeout "would release the `Ctx` while the handler's goroutine still holds it", and presented that
as the reason a timeout must be cooperative (`docs/04-roadmap.md`, and the entry in
`docs/progress.md` that closed that branch). It is true of a middleware and false of the transport.
A pre-emptive timeout is not unsafe outright; it is unsafe as a middleware, and safe at the
transport, where fasthttp abandons the context it timed out.

## Decision

`middleware.Timeout(d time.Duration) rice.Middleware` is cooperative. For each request it derives
`context.WithTimeout(c.Context(), d)`, installs it with `c.SetContext`, runs the rest of the chain,
then cancels it and restores the previous context. It never runs the chain on another goroutine.

- **The deadline is on the context.** The handler passes `c.Context()` to its database and HTTP
  clients, which already honour a deadline. Deriving from `c.Context()` rather than
  `context.Background()` keeps [ADR-0010](0010-request-context-cancels-at-force-close.md)'s
  force-close signal. `d <= 0` panics at construction with a `rice: ` prefix.
- **The error becomes 503 when three things all hold:** the chain returned an error; the deadline
  on the context this middleware created has passed — `ctx.Err() == context.DeadlineExceeded`,
  asked of that context, so under an outer `Timeout` it may be that outer, shorter deadline rather
  than `d`; and the error is not already an `*rice.HTTPError`. It asks the context rather than the
  error because many database drivers wrap a timeout in their own error type without unwrapping to
  `context.DeadlineExceeded`, so `errors.Is` on the returned error would miss exactly the case a
  timeout exists for. An author who chose a status keeps it, the rule `binding`
  set for `Validate`. The 503 is `&rice.HTTPError{Code: 503, Err: err}` with no message: the client
  receives "Service Unavailable", and the cause stays in `Err` for a custom `ErrorHandler`.
- **Two outcomes are not rewritten.** A chain that succeeds after the deadline is returned as it
  is: a late answer is still an answer. A context that was cancelled rather than timed out —
  ADR-0010's force-close, or an outer middleware's own cancellation — is not this middleware's
  timeout, and its error passes through untouched.
- **The previous context is restored** when the chain returns, so a middleware outside `Timeout` —
  `Logger`, which hands `c.Context()` to slog — does not receive one that has already expired.
- **Placement and nesting.** `Timeout` goes inside `Logger`, so the 503 it returns is the status
  `Logger` records. A route's `Timeout` inside an application-wide one can only shorten the
  deadline: a context's deadline is never later than its parent's.
- **The limitation, stated plainly.** A handler that ignores its context is not stopped. It runs to
  completion and its late result is returned — a 200 an hour later is still a 200. `Timeout` is a
  deadline the work is asked to honour, not a switch that cuts it off.
  `TestTimeoutDoesNotStopAHandlerThatIgnoresTheContext` pins this, so a change in behaviour forces
  a change in the documentation.

`Timeout` costs 4 allocations per request around a handler that returns at once —
`context.WithTimeout` allocates. `TestAllocBudgetTimeout` pins it exactly, measured at 4 on darwin
(go1.25.6, arm64) and in a Linux container (golang:1.25.14, amd64), with and without `-race`.

## Alternatives

**A pre-emptive middleware.** The one that actually stops waiting: run the chain on a goroutine,
answer 503 when the timer fires. Rejected on the probe's evidence. Every middleware in the chain
shares one `*Ctx`, so the abandoned goroutine races with every middleware outside it — the probe's
`WARNING: DATA RACE`. And on top of the race, the `Ctx` is released to the pool while the goroutine
still holds it, the use-after-release ADR-0005 exists to prevent. fasthttp can do this safely only
because it can abandon its context; a middleware cannot abandon a `Ctx` the rest of the chain and
`handle` still own.

**Documenting `fasthttp.TimeoutHandler` only, and building nothing.** It does what the cooperative
middleware cannot: it cuts the client off whatever the handler does. Rejected as the only answer
for three costs. It runs every request on a goroutine of its own. The timeout response is written
by fasthttp, bypassing rice's error funnel and every middleware, so a custom `ErrorHandler` never
sees it. And the access log records the handler's eventual status, not the timeout the client
received: `Logger` runs inside the abandoned goroutine and logs whatever the handler finally
wrote — the gap between what is logged and what the client received that
[ADR-0013](0013-middleware-can-settle-a-request.md) was written to close. It remains available, as
the Consequences say, for the service that needs it.

**The cooperative middleware.** Chosen. It stops nothing, but every context-aware driver stops
itself when the deadline passes, and the answer goes through the funnel, where the `ErrorHandler`
writes it and `Logger` records it.

## Consequences

**Makes easy.** A deadline that every context-aware database driver and HTTP client honours, set
once for the App or per route, answered as 503 through the funnel, and logged as the 503 the
client received. It adds no goroutine, and it keeps ADR-0010's force-close signal, because the
deadline is derived from the request's own context.

**Makes hard: code that ignores its context is not stopped.** A handler in a CPU loop, or a driver
call made with `context.Background()`, runs to completion, and its answer — however late — is what
the client receives. Nothing in a Go process can stop a goroutine from outside; the only thing that
can stop waiting for one is whatever stands outside it. The documentation says this where
`Timeout` is described, in `Timeout`'s doc comment, `middleware/doc.go` and the README.

**Makes hard: a failure after the deadline stops being logged.** `DefaultErrorHandler`
(`errors.go:107`) logs only an error it cannot match as an `*HTTPError`; `middleware.Logger`
records the method, path, status, latency, address and request id, and never the error. So
wrapping a plain error in a 503 `*HTTPError` turns a failure that was logged into one that is
not — the client is told, and the operator is not. And because the condition is the context
rather than the error, every error returned after the deadline is answered 503, a genuine bug in a
handler that ignored its context included, so the bug is hidden behind a status that reads like
load. The cause is not lost — it stays in `Err` — but reading it takes an `ErrorHandler` that logs
`he.Err`, which the default does not. A service that installs `Timeout` should install one.

**The escape hatch is the transport.** A service that must cut the client off regardless mounts
the App's handler on a `fasthttp.Server` of its own, wrapped in
`fasthttp.TimeoutWithCodeHandler(app.FasthttpHandler(), d, msg, fasthttp.StatusServiceUnavailable)`
(`fasthttp.TimeoutHandler` answers 408 instead). It pays for that with a goroutine per request, a
timeout response written by fasthttp outside rice's funnel and middleware, an access log that
records the handler's eventual status rather than the timeout the client received, and — because
rice's `App` does not wrap its own handler — its own server in place of `Run`, `RunContext` and
`Shutdown`. The handler goroutine it abandons still runs to completion; the transport stops waiting
for it, not the work.

**Forecloses nothing.** If rice's `App` ever wraps its own server's handler, a transport-level
option can sit beside this middleware; nothing here depends on it being absent.
