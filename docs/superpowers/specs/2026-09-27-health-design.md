# Health probes and a drain before shutdown — Design

**Status:** approved in conversation, spec awaiting review
**Date:** 2026-09-27
**Milestone:** none. Work after the roadmap
**Decision record:** ADR-0023 (new), "Shutdown drains before it closes, and readiness reports it"
**Depends on:** the lifecycle (M7: `Serve`, `Shutdown`, `RunContext`, the hooks), force-close at
the deadline ([ADR-0009](../../adr/0009-shutdown-force-closes-at-deadline.md)), streams stopping at
Shutdown ([ADR-0019](../../adr/0019-streams-run-after-the-handler.md)), the error funnel
([ADR-0002](../../adr/0002-handler-returns-error.md))

## Goal

Let a rice service on Kubernetes answer liveness and readiness probes, and shut down without failing
the requests that arrive in the seconds between SIGTERM and the moment the cluster stops routing to it.

## Question this design answers

Kubernetes sends SIGTERM and removes the pod from its Service at the same time, and the endpoint
change takes seconds to reach kube-proxy and ingress controllers. Rice's `Shutdown` closes the
listener at once, so requests routed in that window are refused. How does rice keep serving through
that window, tell the cluster it is going, move keep-alive clients elsewhere, and still bound the
whole shutdown?

## What the design is for

The owner deploys on Kubernetes. The owner chose: rice drains inside the app, with an option, rather
than relying on a `preStop` sleep; readiness reflects the lifecycle by default and takes optional
dependency checks; liveness never checks dependencies.

## What exists

Read against rice at `72f4fac`.

- `Shutdown(ctx)` sets `closed`, cancels streams, takes its turn, calls fasthttp's
  `ShutdownWithContext` (which closes the listener first, then drains), force-closes at the deadline,
  then runs `OnShutdown` hooks. It is safe to call concurrently; calls take turns.
- `RunContext(ctx, addr, grace)` serves until `ctx` ends, then calls `Shutdown` with a context of
  `grace`.
- `Serve` runs `OnStart` hooks, then publishes the listener under `mu` and serves.
- `App.handle` is the dispatch path, pinned at its allocation budgets.
- `HTTPError{Code, Message, Err}`: `Message` is sent, `Err` is logged, never sent.
- Options are `With…` functions on `rice.New`.

## Non-goals

- A startup probe distinct from readiness: readiness is false until `OnStart` hooks have run, which
  is what a startup probe would check.
- Serving probes on a separate port inside one `App`: an `App` serves one listener; a second `App`
  can serve them (D5).
- Detailed per-check JSON output: it would publish infrastructure names on a public route.
- Running checks concurrently or caching their results.
- Handling signals: rice leaves that to `signal.NotifyContext`, as today.

## Decisions

### D1 — `WithDrainDelay` and the drain phase, inside `Shutdown`

```go
// WithDrainDelay makes Shutdown keep serving for d before it closes the
// listener, reporting not ready and closing keep-alive connections meanwhile.
func WithDrainDelay(d time.Duration) Option
```

A negative `d` panics. The default is 0: no drain, today's behaviour.

`Shutdown(ctx)` gains a first phase, before it takes its turn:

1. Readiness turns false (D2) and the App enters *draining*.
2. If the App was ready when this happened and `d` > 0, `Shutdown` waits until `d` has passed since
   draining began, or until `ctx` ends, whichever is first. The listener stays open and requests are
   served normally.
3. Then everything proceeds as today: take the turn, close the listener, drain, force-close at the
   deadline, run the hooks.

The drain begins once, in the first `Shutdown`; a concurrent `Shutdown` waits for the same deadline
(or its own `ctx`), not a second one. A `Shutdown` of an App that was never ready — never served, or
still in `OnStart` hooks — skips the wait.

Streams are still cancelled when `Shutdown` begins (ADR-0019), not after the delay: a stream keeps
its connection on this pod for as long as it runs, and ending it lets the client reconnect through the
load balancer.

### D2 — `(*App).Ready`

```go
// Ready reports whether the App is serving and not shutting down: its
// OnStart hooks have succeeded, its listener is published, and Shutdown has
// not begun.
func (a *App) Ready() bool
```

It is an atomic load, safe from any goroutine, including a different `App`'s handler. It turns true
in `Serve` when the listener is published, and false when `Shutdown` begins or `Serve` returns.

### D3 — `Connection: close` while draining

While the App is draining, `App.handle` marks every response `Connection: close` (fasthttp's
`SetConnectionClose`) before dispatch. A keep-alive client — kube-proxy and ingress controllers keep
connections for a long time — then opens its next connection afresh, and the load balancer, which has
by then dropped this pod, sends it elsewhere. The check is one atomic load per request; the dispatch
budgets do not move, and a new budget pins a draining request.

### D4 — `RunContext` counts the delay outside `grace`

`RunContext(ctx, addr, grace)` gives `Shutdown` a context of `drainDelay + grace`, so `grace` still
means the time in-flight requests get after the listener closes. The doc comment says the whole
shutdown lasts up to `drainDelay + grace` plus the hooks, and that Kubernetes'
`terminationGracePeriodSeconds` (30 s by default) must exceed it.

A caller of `Shutdown` directly passes one context for both phases; the doc comment says so.

### D5 — the `health` package

A new opt-in package, `github.com/vietpham102301/rice-http/health`, importing only rice.

```go
// Live answers 200 "ok". It checks nothing: a process that can answer is alive.
func Live() rice.Handler

// Check is one dependency readiness waits on. Fn gets a context bounded by
// Timeout (0 means one second).
type Check struct {
	Name    string
	Timeout time.Duration
	Fn      func(ctx context.Context) error
}

// Ready answers 200 "ok" when app.Ready() and every check passes, and 503
// "not ready" otherwise.
func Ready(app *rice.App, checks ...Check) rice.Handler
```

- `Ready` returns 503 through the funnel with a shared `*rice.HTTPError{Code: 503, Message: "not
  ready"}` while `app.Ready()` is false, without running any check.
- Checks run in order, each with `context.WithTimeout(c.Context(), timeout)`. The first failure — an
  error, or the timeout — returns `&rice.HTTPError{Code: 503, Message: "not ready", Err: <cause naming
  the check>}`: the client sees "not ready", the log sees which check and why. The rest do not run.
- A `Check` with an empty `Name` or a nil `Fn` panics when `Ready` is called; a negative `Timeout`
  panics.
- `Live` never checks anything, so a dependency outage never makes Kubernetes restart healthy pods.
  The doc comment says why, and warns that a readiness check on a dependency every pod shares turns
  that dependency's outage into every pod leaving the Service at once — including routes that did not
  need it.

```go
app := rice.New(rice.WithDrainDelay(5 * time.Second))
app.GET("/livez", health.Live())
app.GET("/readyz", health.Ready(app, health.Check{Name: "db", Fn: db.PingContext}))
api := app.Group("/api", middleware.KeyAuth(keyCfg), middleware.RateLimit(limitCfg))
```

### D6 — where probes sit

Probes are ordinary routes, so middleware installed with `app.Use` runs on them. The README and the
package doc say: install authentication on groups, not with `app.Use`, or the kubelet's probe gets a
401; `Logger` with `app.Use` logs every probe. The alternative is a second `App` on a management
port — `health.Ready(mainApp)` reads the main App's state from there — shown in the package doc.

## Consequences to record

- ADR-0023: the drain in `Shutdown`, `Ready`, `Connection: close`, the `RunContext` arithmetic, the
  `health` package, `preStop` sleep as the rejected alternative.
- `05-performance-model.md`: the dispatch budgets unchanged; a draining request's budget; `Ready` at 0.

## Components

| File | Contents |
|---|---|
| `app.go` | `WithDrainDelay`; the `drainDelay`, `ready` and `draining` fields; `Ready` |
| `server.go` | `Serve` sets `ready`; `Shutdown`'s drain phase; `RunContext`'s `drainDelay + grace` |
| `app.go` (`handle`) | `SetConnectionClose` while draining |
| `drain_test.go` | the drain, readiness, `Connection: close`, `RunContext` timing |
| `health/health.go`, `health/doc.go` | `Live`, `Check`, `Ready` |
| `health/health_test.go` | the package's behaviour |
| `alloc_test.go` | a draining request; `Ready` |

## Allocation budget

Every existing dispatch budget unchanged. A request served while draining: the same as the non-draining
fixture (expected: equal). `Ready`: 0. `health.Live` and `health.Ready` with no checks: measured and
pinned (expected 0 when ready; 0 for the shared 503).

## Testing

- **Drain:** with `WithDrainDelay(300ms)`, after `Shutdown` begins a new connection is still accepted
  and served for the delay, then refused; `Ready` is false from the first instant; responses during the
  delay carry `Connection: close`; `Shutdown` returns after at least the delay.
- **Bounded by ctx:** a `Shutdown` whose ctx ends before the delay stops waiting and proceeds (and,
  its ctx being done, force-closes as today).
- **No delay by default:** without the option, `Shutdown` closes the listener at once (the existing
  lifecycle tests keep passing).
- **Never ready:** `Shutdown` of an App that never served returns without waiting the delay.
- **Concurrent Shutdowns:** two calls wait for one deadline, not two.
- **Ready:** false before `Serve`, false during a slow `OnStart` hook, true once serving, false after
  `Shutdown` begins, false after `Serve` returns with an `OnStart` error.
- **RunContext:** with a delay and a grace, a request in flight when the listener closes gets the full
  grace after the delay.
- **Streams:** a stream open when `Shutdown` begins is cancelled at once, not after the delay.
- **health.Live:** 200 "ok" always, including while draining.
- **health.Ready:** 503 before serving and while draining, without running checks; 200 when ready with
  passing checks; 503 "not ready" when a check errors, and the ErrorHandler's error names the check and
  wraps its cause; a check that exceeds its Timeout is a 503 whose cause is `context.DeadlineExceeded`;
  a later check does not run after a failure; Timeout 0 means one second; the panics.
- **Budgets:** as above.

## Documentation

ADR-0023; `03-core-concepts.md` (lifecycle: the drain and `Ready`); `05-performance-model.md`;
`04-roadmap.md` (*Done after M8*); `docs/progress.md`; README (the Graceful shutdown section gains the
drain and a Kubernetes example with probes and `terminationGracePeriodSeconds`); `health/doc.go`.

## Exit criteria

`make test`, `make test-debug` and `make lint` pass; every D1–D5 behaviour has a test; no existing
budget moves; core's `go.mod` is unchanged.

## Open questions

None.
