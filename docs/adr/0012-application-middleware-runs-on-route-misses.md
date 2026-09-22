# ADR-0012 — Application middleware runs on route misses

Status: Accepted
Date: 2026-09-22

## Context

Until this decision, `App.handle` answered a route miss by calling the error funnel directly:

```go
h, ok := a.lookup(fctx.Method(), fctx.Path(), &c.params)
if !ok {
    a.callErrorHandler(c, ErrNotFound)
    return
}
```

Middleware registered with `app.Use` is compiled into each route's chain at build time
([ADR-0003](0003-middleware-as-prebuilt-closure-chain.md)), so it existed only on routes that
matched. A request that matched nothing ran no middleware at all. No document said so. It was a
consequence of M4's compiled-chain design that nobody had examined, not a decision anybody had
made, and it was found by a probe run against rice at `33e5d71` while designing the access-log
middleware.

It broke three of the five middleware planned after M8 before any of them was written:

- **Logger** would never record a 404 — the requests an operator most wants to see: scanners,
  broken clients, mistyped URLs.
- **RequestID** would give a 404 response no id, so a client reporting one would have nothing to
  quote.
- **CORS** would be impossible. A browser's preflight `OPTIONS /users` against a route with only
  `GET` and `POST` registered is a miss, so a CORS middleware would never run and every non-simple
  cross-origin request would fail.

The miss path was also pinned: `TestAllocBudget404` holds a 404 at zero allocations, "a miss costs
what a hit costs, and both cost nothing". Whatever changed had to keep that at 0.

## Decision

A route miss runs the application's middleware, through a chain compiled once like any route's.

`App` gains one field, `miss Handler`. `New` sets it to `notFound`, a function that returns the
package-level `ErrNotFound`. `build` replaces it with `chain.Compile(notFound, a.mws)`: the
application's middleware and nothing else. `handle`'s miss branch sets `h = a.miss` and falls
through to the same call a matched route takes, so a miss reaches the funnel by the same path as
any returned error.

**Only the application's middleware.** Group middleware and route middleware never wrap the miss
chain. No group matched, so no group has a claim on the request — `/api/nope` does not run the
`/api` group's middleware, even though its path begins with the group's prefix.

**Initialised in `New`, not only in `build`.** The dispatch path is reachable before `Build`: the
package's tests call `handle` on unbuilt Apps throughout, and the `Ctx` pool is created in `New`
for the same reason. A miss chain created only at build time would be nil there. Setting it in
`New` also means `handle` needs no nil check.

**The cost is nothing per request.** The chain is compiled once, and `ErrNotFound` is a package
variable. With no application middleware, `chain.Compile(notFound, nil)` returns `notFound`
itself, and a miss behaves byte for byte as it did before. `TestAllocBudget404` is still 0, and a
new budget, `TestAllocBudget404WithAppMiddleware`, holds a miss through one application
middleware at 0 as well.

## Alternatives

**A separate `app.Pre` for middleware that runs before routing, as Echo has.** It changes nothing
already shipped: `app.Use` keeps its meaning and a user opts in to the new verb. Rejected because
the trap survives. A Logger installed with `app.Use` — the verb every example and every user
reaches for first — would still silently miss every 404. The gap would now have a name and a
paragraph of documentation, and it would still be the default behaviour. Two registration verbs
whose difference matters only on the requests nobody tests is a second rule to learn in exchange
for not fixing the first.

**Wrapping at the fasthttp layer, around `FasthttpHandler()`.** It needs no change to core at all:
a user wraps the `fasthttp.RequestHandler` rice returns, and the wrapper sees every request.
Rejected because such a middleware has no `*rice.Ctx` — no `ClientIP`, no `Set`/`Get` store, no
`Context`, and no error funnel to settle a request through. It would be a second middleware system
beside the first, with a different signature, different ordering rules and different access to the
request, and the shipped middleware would have to be written twice or choose one.

## Consequences

**Makes easy.** A Logger installed with `app.Use` records 404s. `RequestID` puts an id on a 404
response. A CORS middleware can answer a preflight for a path that has no `OPTIONS` route. None of
them needs to know misses exist.

**Behaviour changes, and they are the price.**

- *Application middleware now sees misses.* Middleware written before this decision assumed that
  every request it saw had matched a route. An application-level authentication middleware that
  answers 401 without calling `next` now answers 401 on a path that does not exist, where the
  client used to receive 404. That is arguably better — it stops an unauthenticated client
  enumerating routes by status — but it is a change, and a test asserting a 404 on an
  unauthenticated miss would now fail.
- *`c.Param` returns empty on a miss*, because nothing was captured. A middleware that reads a
  parameter must already handle absence, since a route without that parameter returns empty too;
  on a miss, every parameter is absent.
- *A request for an existing path with the wrong method is a miss.* rice has no 405, so
  `DELETE /users` against a path registered only for `GET` takes the miss chain and answers 404,
  as it did before — now through the application's middleware.

**A middleware cannot tell a miss from a route that returned `ErrNotFound`.** The only signal is
the error `next` returns, and a handler may return `ErrNotFound` itself; there is no `c.Route()`.
Nothing planned needs the distinction, so none is added.

**Group-level behaviour on a miss is unchanged.** Group middleware still never runs on a miss,
including under the group's own prefix. A group's authentication therefore still does not stand
between a client and a 404 inside that group's prefix. That was true before this decision and it
is true after; it is stated here because the change to application middleware makes it easy to
assume the change reached groups too.

**A panic in application middleware on a miss is answered 500.** The deferred recovery in `handle`
wraps the whole dispatch, whether `h` is a route's chain or the miss chain, so this is the same
guarantee [ADR-0008](0008-rice-recovers-panics-in-core.md) gives every other request.

**Forecloses little.** A per-group miss handler — a `/api` 404 answered by the `/api` group's
middleware — is not provided and not prevented. It would need a lookup that reports the longest
matching group prefix, which the router does not track today. Revisit if a service needs a group's
middleware, typically its authentication, to run on that group's misses.
