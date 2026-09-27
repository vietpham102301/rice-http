# RateLimit middleware — Design

**Status:** approved in conversation, spec awaiting review
**Date:** 2026-09-27
**Milestone:** none. Work after the roadmap; the second of two security middleware designs (BasicAuth
and KeyAuth were the first)
**Decision record:** ADR-0022 (new), "Rate limiting is GCRA behind a store that runs the algorithm"
**Depends on:** the error funnel ([ADR-0002](../../adr/0002-handler-returns-error.md)), misses running
application middleware ([ADR-0012](../../adr/0012-application-middleware-runs-on-route-misses.md)),
typed store keys ([ADR-0016](../../adr/0016-typed-store-keys.md)), authentication
([ADR-0021](../../adr/0021-auth-validates-through-a-callback.md))

## Goal

Let a rice service cap how fast one client — an address, a user, an API key — may call it, answer the
excess with 429 and a `Retry-After`, and keep the counting behind an interface so a shared store can
replace the in-memory one when the service runs on several instances.

## Question this design answers

Where does the counting algorithm live so that an in-memory store and a later Redis store are both
correct under concurrency, and how does an in-memory store bound its memory without a goroutine that
the middleware has no lifecycle to stop?

## What the design is for

The owner chose: a store interface with an in-memory implementation in core (a Redis store later, in
its own module as `otelrice` is); GCRA as the algorithm; the client address as the default key with a
`KeyFunc` to count by anything else; and `Retry-After` on a 429 as the only rate-limit header.

## What exists

Read against rice at `6c61107`.

- `middleware/` holds Logger, RequestID, RealIP, CORS, Timeout, Recover, BasicAuth and KeyAuth. None
  keeps state across requests.
- `c.ClientIP()` returns a `net.IP`; RealIP, installed before, makes it the proxied client's.
- A middleware has no hook into `App.Shutdown`; only the application registers `OnShutdown` hooks.
- The funnel does not reset response headers, so a header set before returning an `*HTTPError`
  reaches the client (BasicAuth's challenge relies on it).
- `c.SetHeader(key, value string)` sets a response header.
- BasicAuth and KeyAuth store an identity under a `rice.Key[T]` that a later middleware can read.

## Non-goals

- A Redis or other shared store: the interface admits one; it is a separate module and design.
- `RateLimit`, `RateLimit-Policy` (an IETF draft) or `X-RateLimit-*` headers.
- Several limits on one middleware (100 a minute and 1000 an hour): install two middleware.
- A skipper, or an empty key meaning "not limited": groups scope middleware, as with auth.
- A cap on how many keys the in-memory store holds; its memory is bounded by expiry (D5).
- Charging by cost (a request that counts as 5).
- Fail-open on a store error: the application wraps its store to get it (D4).

## Decisions

### D1 — the API

```go
// Limit allows Rate requests every Per, with bursts of up to Burst requests.
type Limit struct {
	Rate  int
	Per   time.Duration
	Burst int // 0 means Rate
}

// RateLimitStore charges one request to key under GCRA and reports how long
// the caller must wait before it would be allowed; 0 means allowed now.
type RateLimitStore interface {
	Take(ctx context.Context, key string, l Limit) (wait time.Duration, err error)
}

type RateLimitConfig struct {
	Limit   Limit
	KeyFunc func(c *rice.Ctx) (string, error) // nil: the client address (D3)
	Store   RateLimitStore                   // nil: a new MemoryStore for this middleware
}

func RateLimit(cfg RateLimitConfig) rice.Middleware

type MemoryStore struct{ /* unexported */ }

func NewMemoryStore() *MemoryStore
func (s *MemoryStore) Take(ctx context.Context, key string, l Limit) (time.Duration, error)
```

```go
api := app.Group("/api",
	middleware.RateLimit(middleware.RateLimitConfig{
		Limit: middleware.Limit{Rate: 100, Per: time.Minute, Burst: 20},
	}),
)
```

Construction panics, with a `rice: middleware.RateLimit:` prefix, on `Rate` ≤ 0, `Per` ≤ 0,
`Burst` < 0, and a `Per / Rate` below one nanosecond. A `Limit` handed to `MemoryStore.Take` that
would panic at construction returns an error instead: the store is an exported type and may be called
directly.

### D2 — the algorithm: GCRA, and the store runs it

The emission interval is `T = Per / Rate`; the burst is `B = Burst`, or `Rate` when `Burst` is 0. Each
key keeps one value, its theoretical arrival time `tat`. For a request at `now`:

```
tat     = max(stored tat, now)          // absent key: now
newTat  = tat + T
allowAt = newTat - B*T
if now < allowAt: deny, wait = allowAt - now, stored tat unchanged
else:             allow, wait = 0, store newTat
```

A fresh key admits `B` requests at once, then one every `T`. A denied request does not move `tat`, so
a client hammering while limited is not pushed further back. The read, the decision and the write are
one step under the store's own lock (MemoryStore) or one atomic script (a Redis store): the store
runs the algorithm so that neither needs a compare-and-set loop. The owner chose this over a store
that only reads and writes `tat` (every Redis call would become a retry loop under contention) and
over a counter with a TTL (fixed windows, which admit twice the limit across a boundary).

The store receives the `Limit` on every call, so one store can serve limiters with different limits;
keys are the caller's namespace, and two limiters sharing a store must not share keys (D3).

### D3 — the key

`KeyFunc` nil means the client address from `c.ClientIP()`, masked so that one client is one key: an
IPv4 address (or an IPv4-mapped IPv6 address) whole, an IPv6 address to its /64, since a single IPv6
client is routinely given a whole /64 and would otherwise have 2^64 keys. The key is the masked
address's 16 bytes as a string: a key, not a display form. Install RealIP before RateLimit, or behind a
proxy every request is one client.

A `KeyFunc` returns any string, including "" (a key like any other, shared by every request that
returns it). A limit per user reads the identity an auth middleware stored:

```go
KeyFunc: func(c *rice.Ctx) (string, error) {
	u, _ := userKey.Get(c)
	return u.ID, nil
}
```

The doc comment tells an application that shares one `RateLimitStore` between two middleware to prefix
its keys; a nil `Store` gives each middleware a store of its own, so the default never collides.

### D4 — the three outcomes

`Take` receives `c.Context()`, so a Timeout deadline reaches a store that makes network calls.

- `wait` 0, `err` nil: the chain runs. No header is written.
- `wait` > 0, `err` nil: 429. `Retry-After` is set to `wait` in whole seconds rounded up, at least 1,
  and a shared `*rice.HTTPError{Code: 429, Message: "Too Many Requests"}` is returned to the funnel.
  The chain does not run.
- `err` non-nil, from `KeyFunc` or from `Take`: returned as it is. The funnel answers 500 and
  DefaultErrorHandler logs it. A store outage is not a client exceeding its limit. An application that
  would rather serve than refuse while its store is down wraps the store and returns 0 on error; the
  doc comment shows it in five lines.

### D5 — MemoryStore

- 64 shards, each a mutex and a `map[string]int64`; a key's shard is chosen by `hash/maphash` with a
  per-store seed. `tat` is stored as nanoseconds since the store's creation, measured on the monotonic
  clock (`now.Sub(base)`), so a wall-clock step does not release or lock out every client.
- **No goroutine.** A key whose `tat` is at or before `now` is indistinguishable from an absent one,
  so it may be deleted at any time. Each shard remembers when it last swept; a `Take` that finds a
  shard not swept for a minute deletes that shard's expired keys before answering. Memory is thereby
  bounded by the keys active in the last `B*T` plus one sweep interval, with nothing to start or stop,
  and no `Close`.
- The zero `MemoryStore` is not usable; `NewMemoryStore` builds it. The clock and the sweep interval
  are unexported fields, which the package's tests set.

### D6 — the `Retry-After` value

Whole seconds, rounded up, so a client that waits exactly as told is allowed. Written with
`strconv.Itoa`, which does not allocate for values below 100.

### D7 — placement

```go
app.Use(
	otelrice.Middleware(),
	middleware.Logger(l),
	middleware.Recover(),
	middleware.RealIP(1),
	middleware.CORS(cfg),
	middleware.RateLimit(ipLimit), // by address: throttles guessing before BasicAuth
)
api := app.Group("/api",
	middleware.KeyAuth(keyCfg),
	middleware.RateLimit(userLimit), // by user: a quota per identity
)
```

After RealIP, so the address is the client's. Inside CORS, so a 429 carries the CORS headers and a
preflight is not counted. Inside Logger and otelrice, so 429s are logged and traced. Before BasicAuth
or KeyAuth to throttle credential guessing by address; after them, with a `KeyFunc`, for a quota per
identity. Installed with `app.Use`, it also counts route misses (ADR-0012), so scanning for paths is
throttled too.

## Consequences to record

- ADR-0022: GCRA; the store runs the algorithm; a store error is a 500; the IPv6 /64 default; no
  goroutine; `Retry-After` only.
- `05-performance-model.md` (*Opt-in packages*): the measured budgets.
- ADR-0021's "Brute force is the next design" is answered; the roadmap's security bullet says so.

## Components

| File | Contents |
|---|---|
| `middleware/ratelimit.go` | `Limit`, `RateLimitStore`, `RateLimitConfig`, `RateLimit`, the default key, the 429 |
| `middleware/memorystore.go` | `MemoryStore`, `NewMemoryStore`, GCRA, shards, sweeping |
| `middleware/ratelimit_test.go` | behaviour tests through the middleware |
| `middleware/memorystore_test.go` | GCRA and sweeping against a controlled clock; concurrency |
| `middleware/alloc_test.go` | the budgets |

## Allocation budget

Measured with and without `-race` and pinned, each after the key exists in the store: RateLimit
allowing a request with the default key; RateLimit answering 429 with the default key;
`MemoryStore.Take` on an existing key (expected 0). Inserting a new key allocates the map entry and
is not pinned.

## Testing

- **GCRA (store, controlled clock):** a fresh key admits `B` requests then denies; the wait of the
  first denied request is exactly `T`; after waiting it, one more is admitted; after `B*T` of idleness
  the full burst is back; `Burst` 0 behaves as `Rate`; `Burst` 1 admits one at a time; a denied
  request does not move `tat` (hammering while limited does not lengthen the wait); two keys are
  independent; two `Limit`s on two keys of one store are independent.
- **Clock:** a `now` earlier than a key's stored `tat` (a request racing another's clock reading)
  neither panics nor admits more than `B`; `tat` is kept relative to the store's creation.
- **Sweep:** after the sweep interval, a `Take` on one key deletes the shard's expired keys and keeps
  unexpired ones (asserted through an unexported count).
- **Store validation:** `Take` with a `Limit` that would panic at construction returns an error.
- **Concurrency:** 64 goroutines taking the same key under `-race` are admitted exactly `B` times
  within one instant of the controlled clock.
- **Middleware:** allowed → the handler runs and no `Retry-After` is written; denied → 429, body
  "Too Many Requests", `Retry-After` exact (a wait of 1.2 s → "2", of 300 ms → "1"), the handler does
  not run; a `KeyFunc` error → 500 and `Take` is not called; a store error → 500; a custom ErrorHandler
  sees the store's error; `Take` receives the request's context (a Timeout's deadline).
- **Default key:** two IPv4 clients are two keys; two addresses in one IPv6 /64 are one key; two
  /64s are two keys; an IPv4-mapped IPv6 address is the IPv4 client's key; behind RealIP the forwarded
  address is the key.
- **Placement:** inside CORS a 429 carries `Access-Control-Allow-Origin`; with `app.Use` a miss is
  counted; on a group other routes are not.
- **Construction panics:** `Rate` 0, `Per` 0, `Burst` −1, `Per/Rate` under 1 ns.
- **Budgets:** as above.

## Documentation

ADR-0022; `03-core-concepts.md` (the middleware list); `05-performance-model.md`; `04-roadmap.md`
(*Done after M8*, and the security bullet); `docs/progress.md`; `middleware/doc.go` (the package list,
adding BasicAuth and KeyAuth which it lacks); README (a Rate limiting section: by address, by user,
fail-open wrapping).

## Exit criteria

`make test`, `make test-debug` and `make lint` pass; every D2, D3, D4 and D6 row has a test; the
existing-key `MemoryStore.Take` budget is 0; core's `go.mod` is unchanged.

## After the branch

Merged to `main`, then one tag for the release that carries BasicAuth, KeyAuth and RateLimit:
`v0.3.0` (new API, pre-1.0, so a minor version). `otelrice` does not change and is not re-tagged.

## Open questions

None.
