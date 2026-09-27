# ADR-0023 — Shutdown drains before it closes, and readiness reports it

Status: Accepted
Date: 2026-09-27

## Context

On Kubernetes a pod being deleted receives SIGTERM at the same moment it is removed from its Service,
and kube-proxy and ingress controllers learn of the removal seconds later. Rice's `Shutdown` closed
its listener as soon as it began, so every request routed to the pod in those seconds was refused.
Kube-proxy and ingress controllers also hold keep-alive connections for a long time, and a connection
that stays open keeps delivering requests to the pod whatever its readiness says. Rice had no way to
report readiness at all.

## Decision

- **`rice.WithDrainDelay(d)`.** `Shutdown` gains a first phase: readiness turns off, the App starts
  *draining*, and if it was ready and `d` is positive `Shutdown` waits until `d` has passed since that
  moment, or until its own ctx ends, with the listener open and requests served normally. Then it
  proceeds as before. The drain starts once, in the first `Shutdown`; a concurrent call waits for the
  same deadline. An App that was never ready skips the wait. The default, 0, is the old behaviour.
- **`Connection: close` while draining.** `handle` marks every response so, with one atomic load per
  request, so keep-alive clients reconnect — through a load balancer that has by then dropped the pod.
- **`(*App).Ready`.** True from the moment `Serve` publishes its listener, after the `OnStart` hooks,
  until `Shutdown` begins or `Serve` returns. An atomic load, readable from any goroutine, including a
  second App serving probes on its own port.
- **`RunContext` counts the delay outside `grace`.** It gives `Shutdown` `drainDelay + grace`, so
  `grace` keeps meaning the time in-flight requests get after the listener closes. A direct caller of
  `Shutdown` passes one ctx for both phases.
- **Streams still stop when `Shutdown` begins** (ADR-0019): a stream holds its connection for as long
  as it runs, and ending it lets the client reconnect elsewhere.
- **A `health` package**, importing only rice: `Live` answers 200 and checks nothing; `Ready(app,
  checks...)` answers 503 while `app.Ready()` is false, then runs optional checks in order, each under
  its own timeout, and answers 503 "not ready" on the first failure with the cause in the logged error,
  never in the body.

## Alternatives

**A `preStop` sleep in the pod spec.** The usual answer, and it works — for every deployment that
remembers it, on images that have a `sleep` binary (distroless images do not, before Kubernetes 1.30's
built-in sleep action). It does not move keep-alive connections, and it leaves a rice service shut down
wrongly by default on any platform with the same race.

**The delay only in `RunContext`.** Simpler, and a program that handles signals itself and calls
`Shutdown` would get none of it.

**Serving probes before the middleware chain** (`app.EnableHealth("/livez", "/readyz")`). Keeps
authentication and logging off probes, at the price of a path comparison on every request and routes
that do not appear where routes are registered. A second App on a management port gets the same
isolation with no hidden routes.

**Readiness checking dependencies by default, or reporting each check's result.** A shared dependency's
outage would take every pod out of the Service at once, and a per-check report would publish
infrastructure names on a public route.

## Consequences

**Costs:** a request while draining, 0 allocations, as any other; `Ready`, 0; `health.Live` and
`health.Ready` (passing with no checks, or not ready), 0. Every existing budget is unchanged.

**A shutdown lasts up to the delay plus the grace plus the hooks.** The pod's
`terminationGracePeriodSeconds` must exceed it, or Kubernetes kills the process mid-drain.

**A probe is an ordinary route.** Middleware installed with `app.Use` runs on it; authentication
belongs on groups, or probes go to a second App.
