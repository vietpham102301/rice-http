# ADR-0022 — Rate limiting is GCRA behind a store that runs the algorithm

Status: Accepted
Date: 2026-09-27

## Context

BasicAuth and KeyAuth (ADR-0021) left guessing unthrottled, and a service needs a quota per client
anyway. Rate limiting is the first middleware that keeps state across requests. That state is per
process when the service runs alone and must be shared when it runs behind a load balancer, and rice's
core takes no dependency for the shared case. A middleware also has no hook into `App.Shutdown`, so a
store that needed a background goroutine would need the application to stop it.

## Decision

`middleware.RateLimit(RateLimitConfig{Limit, KeyFunc, Store})`, with
`Limit{Rate, Per, Burst}` and a `RateLimitStore` interface of one method,
`Take(ctx, key, limit) (wait time.Duration, err error)`.

- **GCRA.** A key keeps one value, its theoretical arrival time. A fresh key admits `Burst` requests
  at once (`Rate` when `Burst` is 0), then one every `Per/Rate`. A refused request is not charged, so
  hammering while limited does not lengthen the wait. No window boundary lets a client through at
  twice the rate.
- **The store runs the algorithm.** `Take` reads, decides and writes in one step: under a shard's lock
  in `MemoryStore`, in one script in a shared store. A store that only read and wrote the value would
  make every shared-store call a compare-and-set loop.
- **`MemoryStore` has no goroutine.** 64 shards, each a mutex and a map. A key whose time has passed
  is the same as an absent one, so each shard deletes those keys at most once a minute, during a
  `Take`. Times are kept relative to the store's creation on the monotonic clock.
- **The key.** By default the client address after RealIP: IPv4 whole, IPv6 to its /64, since one
  IPv6 client is routinely given a /64. A `KeyFunc` counts by anything else, such as the identity an
  auth middleware stored. `""` is a key, not an exemption; groups decide which routes are limited.
- **Three outcomes.** Allowed: the chain runs, no header. Refused: 429 through the funnel with a shared
  `*rice.HTTPError` and `Retry-After` in whole seconds rounded up, at least 1. An error from `KeyFunc`
  or the store: returned as it is, a 500 by default, because a store that is down is not a client over
  its limit. An application that prefers to serve while its store is down wraps the store.
- **Placement.** After RealIP; inside CORS, Logger and otelrice; by address before BasicAuth or KeyAuth
  to throttle guessing, by identity after them for a quota. With `app.Use` it counts misses (ADR-0012).

A `Limit` whose `Rate` or `Per` is not positive, whose `Burst` is negative, whose `Per/Rate` is below a
nanosecond or whose `Burst*Per/Rate` overflows a `time.Duration` panics at construction; `MemoryStore.Take`
returns an error for it.

## Alternatives

**Fixed window.** A counter per key per minute: the simplest, and a client can send twice the limit
across a boundary.

**Sliding window.** Two counters per key, smoother; still an approximation, and twice the state of GCRA.

**A store that reads and writes the value, the algorithm in the middleware.** A simpler interface, and
under contention a shared store would need a compare-and-set retry loop per request.

**A background sweeper with `Close`.** Exact expiry, and a lifecycle the middleware does not have: the
application would have to register the store's `Close` with `OnShutdown` or leak a goroutine per store.

**`RateLimit` or `X-RateLimit-*` headers.** The IETF headers are a draft whose syntax has changed; the
`X-` headers are not a standard. `Retry-After` is RFC 9110's and every retrying client reads it.

**Fail-open by default on a store error.** Keeps serving through an outage, and silently removes the
limit exactly when a shared store is under stress. The application chooses it by wrapping its store.

## Consequences

**Costs, pinned exactly with and without `-race`:** RateLimit allowing a request with the default key
and store, 1 allocation (the key string); a 429, 1 (the key; `Retry-After` below 100 seconds is one of
strconv's preformatted numbers); `MemoryStore.Take` on an existing key, 0.

**Each instance counts alone with `MemoryStore`.** Behind a load balancer of N instances a client gets
up to N times the limit until a shared store is installed. A Redis store is a separate module.

**Memory is bounded by expiry, not by a cap.** A flood from many addresses holds one entry per address
for `Burst*Per/Rate` plus up to a minute.
