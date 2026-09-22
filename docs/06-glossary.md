# 06 — Glossary

Terms used with a specific, narrow meaning in these documents.

**Allocation budget** — the maximum number of heap allocations a given operation may make
per request, asserted by a test, not merely observed by a benchmark. See
[05-performance-model.md](05-performance-model.md).

**Borrowed** — a value whose backing memory is owned by fasthttp or by the context pool,
valid only until the current handler returns. The opposite of owned. Every byte slice rice
hands out is borrowed.

**Build phase** — the one-time step between route registration and serving, where
middleware chains are compiled and routes are inserted into the trees. Triggered by `Run`
or by the test helper, run under `sync.Once`.

**Chain** — the single closure produced by wrapping a handler in its middleware, computed
during the build phase and stored on the route. One chain belongs to no route: the miss chain,
the application's middleware around a handler that returns `ErrNotFound`, stored on the `App`
and run for every request no route matches. See
[ADR-0012](adr/0012-application-middleware-runs-on-route-misses.md).

**Ctx** — rice's pooled per-request handle. Distinct from `context.Context` (Go's
cancellation type) and from `fasthttp.RequestCtx` (the transport's request object). When
these docs mean one of the latter two, they spell it out.

**Error funnel** — the single `ErrorHandler` through which every error from every layer
passes on its way to becoming a response.

**Hot path** — the code executed on every request: pool acquire, route lookup, chain call,
pool release. Code outside it is cold and may allocate freely.

**Owned** — a value the caller may keep past the end of the handler. Usually produced by
methods that copy, which are named to say so (`ParamString`, not `Param`). `Context()` is the
one exception: it copies nothing, but returns a `context.Context` owned by the `App` rather
than the request, so it outlives the handler regardless of its name. See
[ADR-0010](adr/0010-request-context-cancels-at-force-close.md).

**Poisoning** — deliberately invalidating a released `Ctx` under the `ricedebug` build tag
so that use-after-release panics loudly instead of returning plausible garbage.

**Registration phase** — everything before build: `New`, `Use`, `GET`, `Group`. Allocations
here are unmeasured and unimportant, because they happen once.

**Superseded** — an ADR replaced by a later decision. The old file stays, with a header
pointing forward. ADRs are never deleted or rewritten.
