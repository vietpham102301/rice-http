# 04 — Roadmap

Nine milestones. Each one is small enough to finish in a sitting or two, ends in something
runnable, and answers a question that the next milestone depends on.

**Status legend:** ☐ not started · ◐ in progress · ☑ done

Every milestone is done only when all three exist: passing tests, a recorded benchmark, and
a retrospective in `docs/milestones/`. That is principle 9, made operational.

---

### ☑ M0 — Scaffold

Repository skeleton, nothing that serves traffic.

- `go.mod` at `github.com/vietpham102301/rice-http`, fasthttp dependency pinned
- Package skeleton matching [02-architecture.md](02-architecture.md)
- `Makefile`: `test`, `bench`, `lint`, `cover`
- CI running tests and benchmarks on push
- `bench/` harness that writes results to a committed file

**Exit criteria:** `make test` and `make bench` both run green on an empty test suite.
**Question answered:** what does the measurement rig look like, before there is anything to measure?

---

### ☑ M1 — Minimal server

An `App` that serves one hardcoded handler. No router, no middleware, no pooling.

- `App`, `Handler`, a `Ctx` that is a thin wrapper over `*fasthttp.RequestCtx`
- `Run(addr)` and a blunt `Shutdown`
- `c.String` and `c.Bytes`

**Exit criteria:** an integration test starts the app on an ephemeral port, issues a real
request, asserts the body. Benchmark records allocations per request with a naively
allocated `Ctx`.
**Question answered:** what does the fasthttp handler boundary actually look like, and what
does one unpooled request cost? This number becomes the baseline everything else is
measured against.

---

### ☑ M2 — Static router

A `map[string]Handler` per method. Deliberately the naive implementation.

- Method dispatch via fixed array, map fallback for rare verbs
- 404 handling routed through the error funnel

**Exit criteria:** routing tests cover hit, miss, and wrong-method. Benchmark records
lookup cost for 10, 100 and 1000 registered routes.
**Question answered:** how bad is a map, really? Writing the radix tree in M3 is only
justified if there is a number here to beat. Skipping this milestone would mean building
the clever thing on faith.

---

### ☑ M3 — Radix tree router

Replace the map. This is the algorithmic core.

- Node types: static, parameter (`:name`), wildcard (`*path`)
- Insert with common-prefix splitting; conflict detection at registration
- `Lookup` filling a caller-supplied `Params`, zero allocations
- Fixed priority: static > param > wildcard

**Exit criteria:** a table-driven test suite covering prefix splits, priority ordering,
trailing slashes, conflicting registrations and deep nesting. `AllocsPerRun` asserts 0 for
lookup. Benchmark compared directly against M2's recorded numbers.
**Question answered:** where does the tree actually win, and where does it lose to the map?

---

### ☑ M4 — Middleware and groups

- `Middleware` as `func(Handler) Handler`
- `internal/chain` compiler and the `build()` phase
- `Group` with prefix and middleware inheritance
- `app.Use` working regardless of registration order

**Exit criteria:** tests asserting exact execution order for nested groups, including the
unwind order on the way out, and a test that `Use` after route registration still applies.
Benchmark shows a five-middleware chain costs 0 allocations per request.
**Question answered:** does compiling chains at build time actually remove the per-request
cost, or does the closure indirection eat the gain?

---

### ☑ M5 — Error handling

- `HTTPError`, `ErrorHandler`, default implementation
- The single error funnel wired through routing, middleware and handlers
- A deferred recovery in rice's own dispatch path, mapped into the funnel — fasthttp has no
  panic hook to map into one; see [ADR-0008](adr/0008-rice-recovers-panics-in-core.md)
- `middleware.Recover` as an opt-in package

**Exit criteria:** tests for wrapped errors via `errors.As`, a custom error handler, and a
panicking handler producing 500 without dropping the connection. A test asserts the cause
string never appears in the response body.
**Question answered:** can one funnel handle every failure mode without special cases?

---

### ☑ M6 — Context pooling and the borrow contract

The milestone the whole project builds toward.

- `sync.Pool` for `Ctx`, `reset` and `release`
- Parameter storage pre-sized from the largest parameter count seen during registration, with
  the fixed eight-parameter limit removed
- A pre-sized key/value store behind `Set` and `Get`
- `ricedebug` build tag: poisoning released contexts, keeping them out of the pool, and
  panicking on use-after-release

**Exit criteria:** `AllocsPerRun` asserts **0 allocations** for a static route and for a
parameterised route read as bytes. A `ricedebug` test proves use-after-release is caught. A
race-detector test hammers the pool concurrently.
**Question answered:** what does pooling actually save versus M1's baseline, and what class
of bug does it introduce in exchange?

---

### ☑ M7 — Lifecycle

- Graceful shutdown with deadline, draining in-flight requests
- `OnStart` and `OnShutdown` hooks
- Server timeouts surfaced as options: read, write, idle
- Signal handling helper

**Exit criteria:** a test that starts the app, holds a slow request open, calls `Shutdown`,
and asserts the in-flight request completed while a new connection was refused. A test that
the deadline is honoured when a request refuses to finish.
**Question answered:** what does fasthttp give for free here, and what has to be built?

---

### ☑ M8 — Benchmark suite and retrospective

- Comparison against Gin, Echo and Fiber on identical routes and payloads
- Allocation budget table for every public method, committed
- `docs/05-performance-model.md` updated with real numbers replacing targets
- A written retrospective: what was learned, what would be done differently

**Exit criteria:** committed benchmark results reproducible with one make target, and a
retrospective naming at least three things that surprised the author.
**Question answered:** where does rice stand, and — more importantly — why, in terms of the
design decisions recorded in the ADRs?

---

## Explicitly deferred

Not scheduled, not promised. Each would need its own brainstorm.

- Request binding and validation, as an opt-in side package
- Route-level and global timeout middleware
- Static file serving
- Content negotiation
- Streaming and server-sent events
- `c.Query` and `c.Header`, the remaining borrowed read accessors. Committed in
  [03-core-concepts.md](03-core-concepts.md) and listed as not implemented in
  [05-performance-model.md](05-performance-model.md), but owned by no milestone.
  Noticed while writing M3.
- `c.JSON`, a JSON response helper. Shown in [00-overview.md](00-overview.md)'s example and
  budgeted for M8, which settled that it measures rather than builds; a handler encodes with
  `encoding/json` and writes with `c.Bytes` today, as the comparison's `json` scenario does.
- Typed per-request store keys (`rice.Key[T]` with a `Get` returning `T`), safer than
  `Set(string, any)` and immune to key collisions between middleware. Rejected for M6 because
  the documented API was already `string`/`any`.
