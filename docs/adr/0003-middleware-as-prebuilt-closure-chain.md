# ADR-0003 — Middleware as a pre-built closure chain

Status: Accepted
Date: 2026-09-02

## Context

Middleware has to run in order, be able to short-circuit, and be able to do work after the
handler returns. Two shapes dominate Go: the decorator, `func(Handler) Handler`, and the
index-walk, where the context holds a handler slice and a cursor advanced by `c.Next()`.

A second, separate question rides along: *when* is the chain assembled? Assembling per
request costs allocations; assembling at route registration makes `app.Use` order-dependent.

## Decision

Two decisions, taken together because they only make sense together.

1. `type Middleware func(next Handler) Handler` — the decorator shape.
2. Chains are compiled during a one-time **build phase**, triggered by `Run` or by the test
   helper, not at registration and not per request. Registering a route after build panics.

## Alternatives

**Index-walk with `c.Next()` (Gin, Fiber).** Very familiar, and it makes "run the rest of
the chain" explicit at the call site. Rejected on two counts. It requires per-request state
(the cursor) on the context, which the pooled `Ctx` must reset, and it introduces the rule
"if you do not call `Next`, the chain stops" — a rule that has to be memorised because it
is not visible in the code's shape. With the decorator, "stop" is `return`, which needs no
rule.

**Compile chains at registration time.** Simpler: no build phase, no `sync.Once`, no
"cannot register after start" restriction. Rejected because `app.Use(mw)` written after
`app.GET(...)` would silently not apply to that route. That is a genuine footgun present in
several popular frameworks, and it produces security bugs specifically — an auth middleware
added at the bottom of a setup function protecting nothing.

**Compile chains lazily per route on first request.** Preserves order-independence without a
build phase. Rejected: it needs synchronisation on the hot path, and it moves a
configuration error from startup to whenever that route is first hit in production.

## Consequences

**Makes easy:** zero per-request chain cost. `Use` works in any order. Configuration errors
surface at startup, before the socket opens. Middleware composes as plain Go functions and
can be tested without a server.

**Makes hard:** the type `func(Handler) Handler` reads badly to newcomers, and writing one
means two nested closures. Routes cannot be added dynamically at run time.

**Requires:** a `build()` step under `sync.Once`, called by `Run` and by the test helper, and
a clear panic message when a route is registered afterwards.
