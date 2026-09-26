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

- Streaming and server-sent events

## Done after M8

Outside any milestone, because the API was already written down in
[03-core-concepts.md](03-core-concepts.md) and needed building, not designing:

- `c.Query`, `c.Header` and `c.Body`, the borrowed read accessors. `Query` and `Header` had sat
  on the list above since M3; `Body` was committed in the same table and missing from this
  list too. Each is held at zero allocations by a budget test.
- `c.JSON`, the JSON response helper, as ADR-0006's one `encoding/json` exception. Its
  allocations are pinned in [05-performance-model.md](05-performance-model.md).
- `WithMaxBodySize`, and the transport error handler behind it — the one item here that the
  design docs had not already written down. The option sets fasthttp's `MaxRequestBodySize`; rice
  installs its own `fasthttp.Server.ErrorHandler` so a body over the limit is answered **413**
  where fasthttp's default answers 400, which the 4 MiB default limit needed too. Described in
  [03-core-concepts.md](03-core-concepts.md#4-app).
- `c.Context`, `c.SetContext`, `c.ClientIP` and `c.NoContent`, the four methods a JSON API
  behind a reverse proxy needs from `Ctx` and core did not have. The context is the `App`'s, not
  fasthttp's, and is cancelled only when a `Shutdown` force-closes — never when a client
  disconnects, which fasthttp does not report.
  [ADR-0010](adr/0010-request-context-cancels-at-force-close.md) records why, and names the
  passthrough and the two other alternatives that lost. `ClientIP` reads no header;
  `middleware.RealIP` resolves `X-Forwarded-For`, below. Cookies, form/multipart and `Redirect` were on the same
  list and were cut: nothing in the first service that will use rice needs them, and they stay
  unpromised until a real use case arrives with its own brainstorm.
- The `binding/` package, which came off the deferred list above with a design of its own. One
  exported function, `binding.JSON[T any](c *rice.Ctx) (T, error)`: a strict decode —
  `DisallowUnknownFields` plus a trailing-data check — and, if `T` or `*T` has one, a
  `Validate() error` method that runs after it. A decode failure is 400 with a fixed message, a
  failed `Validate` is 422 carrying the author's own message, and an `*rice.HTTPError` returned
  from `Validate` passes through with its own status.
  [ADR-0011](adr/0011-binding-is-generic-and-validation-is-a-method.md) records the shape and
  names the two alternatives that lost — struct tags with a third-party validator, and a
  reflection-free decoder written per type. It was the first place in rice to buy ergonomics with
  allocations, and it is pinned at an exact measured figure in
  [05-performance-model.md](05-performance-model.md) rather than bounded.
  Deliberately left out: no `Content-Type` check, because real clients omit the header and a 415
  nobody predicts is worse than a body that parses; no body-size limit of its own, because
  `WithMaxBodySize` already sets one at the transport and a second would be two sources of truth;
  and no `binding.Query` or `binding.Header`, because `c.Query` and `c.Header` exist and binding
  them without struct tags would be contrived.
- `middleware.RealIP`, `middleware.RequestID` and `middleware.Logger`, the access log, and two
  changes to core without which the log could not tell the truth. Probing before the design found
  that application middleware never ran on a route miss, so a logger would never have recorded a
  404; and that a middleware reading the status after `next` read it before the funnel wrote it, so
  a logger would have recorded every 500 as a 200. Core now compiles a miss chain from the
  application's middleware ([ADR-0012](adr/0012-application-middleware-runs-on-route-misses.md)) and
  gains `c.HandleError`, which runs the `ErrorHandler` at once and marks the request settled
  ([ADR-0013](adr/0013-middleware-can-settle-a-request.md)); both hold their paths at zero
  allocations. `RealIP(trustedHops)` counts `X-Forwarded-For` from the trusted end and sets the
  address on every request, because fasthttp keeps a rewritten address for the life of a keep-alive
  connection. It did not then parse an entry with a port or a bracketed IPv6 entry; the last entry
  below records that it now does. `RequestID` keeps an incoming id only if it is safe to log.
  `Logger` records the status the client receives and is not told about a panic unless `Recover`
  sits inside it. The three allocate, and each figure is pinned with its fixture in
  [05-performance-model.md](05-performance-model.md#opt-in-packages). Timeout stayed on the list
  above, needing a design of its own: a pre-emptive timeout written as a middleware would release
  the `Ctx` while the handler's goroutine still holds it. That is true of a middleware and not of
  the transport, where fasthttp abandons the context it timed out rather than reuse it; the entry
  below and ADR-0014 record the correction. CORS, which the miss chain makes possible, is built
  below.
- `middleware.Timeout(d)`, a deadline the work is asked to honour, not a switch that cuts it off.
  It attaches `d` to `c.Context()`, which the database and HTTP clients a handler calls already
  honour, and answers 503 when the chain returns an error after its own deadline has passed —
  asking its context rather than the error, because drivers do not reliably unwrap to
  `context.DeadlineExceeded` — unless the error is already an `*rice.HTTPError`. A handler that
  ignores its context is not stopped, and its late answer is returned; a test pins that. A route's
  `Timeout` inside an application-wide one only shortens the deadline.
  [ADR-0014](adr/0014-timeout-is-cooperative.md) records why it is cooperative: a pre-emptive
  timeout is safe at the transport, where fasthttp abandons the timed-out context, and races as a
  middleware, where every middleware shares one `*Ctx`. It names `fasthttp.TimeoutHandler` as the
  escape hatch and its costs. Its 4 allocations are pinned in
  [05-performance-model.md](05-performance-model.md#opt-in-packages).
- `middleware.CORS(cfg)`, which lets a browser application on one of a fixed list of origins call
  the service: it answers the preflight with 204 and puts the CORS headers on every other
  response. It states a policy and the browser enforces it — the configured methods and headers
  are sent as they are, and `Access-Control-Request-Method` and `-Headers` are never parsed; the
  one check it makes is the origin, compared exactly, and an origin not in the list receives no
  CORS headers. It must be installed with `app.Use`: a preflight is a route miss, and only
  application middleware runs on a miss, so a `CORS` on a group never answers one. It writes its
  headers before `next`, and the funnel does not reset headers, so a 401, a 404 and a 500 arrive
  carrying them and the browser reports the status rather than a CORS failure. A configuration
  that cannot work — no origins, `"*"`, an origin with a trailing slash or an upper-case letter —
  panics at construction. [ADR-0015](adr/0015-cors-states-a-policy.md) records the three findings
  behind it, and names server-side validation of the requested method and headers, a wildcard
  origin and a per-group preflight as the alternatives that lost. It costs 0 allocations on all
  three branches, pinned in [05-performance-model.md](05-performance-model.md#opt-in-packages).
- `middleware.RealIP` reading an `X-Forwarded-For` entry with a port, which came off the deferred
  list above. Some load balancers append the address they saw with its port, and behind them
  `RealIP` had fallen back to the connection's address and reported the proxy on every request. It
  now reads `203.0.113.9:4711`, `[2001:db8::1]` and `[2001:db8::1]:4711` as well as a bare address,
  and drops the port. An unbracketed IPv6 address is read whole and never split at its last colon,
  since `2001:db8::1:4711` is itself a valid address. Anything else — brackets around IPv4, a port
  outside 1–65535 — still falls back to the connection's address. An entry with a port costs what a
  bare one does, 3 allocations, pinned in
  [05-performance-model.md](05-performance-model.md#opt-in-packages).
- `rice.Key[T]`, the typed per-request store key, which came off the deferred list above and
  replaced `c.Set(string, any)` and `c.Get(string)`. `NewKey[T](name)` makes a key whose identity is
  a pointer, not its name, so two middleware choosing the same name never share a slot, and whose
  type is fixed, so `k.Get(c)` returns `T` with no assertion. It is a breaking change, chosen over
  keeping the string API beside it; [ADR-0016](adr/0016-typed-store-keys.md) records why, and names
  package-level `rice.Set`/`rice.Get` and `context.WithValue`-style key types as the alternatives
  that lost. The store's shape and every figure are unchanged, pinned in
  [05-performance-model.md](05-performance-model.md): a pointer value costs nothing and a
  non-pointer value still costs the caller one boxing allocation.
- `App.Static` and `Group.Static`, serving an `fs.FS` — a directory through `os.DirFS` or files
  compiled in through `embed.FS` — for GET and HEAD. Probing found that fasthttp's file server
  answers its own errors, loses a prefix in its directory redirect and runs a cache goroutine
  until told to stop, so rice wraps it and owns each edge: every failure reaches the ErrorHandler,
  the redirect is rice's 301, and Shutdown stops the goroutine
  ([ADR-0017](adr/0017-static-files-wrap-fasthttp-fs.md)).
- `c.Accepts(offers ...string)` and `ErrNotAcceptable`: a handler answers the same resource in the
  format the request's `Accept` header prefers, by RFC 9110's rules, and `Vary: Accept` is added for
  it. Probing found that fasthttp's `Peek` reads only the first of several `Accept` lines and that the
  ricedebug walker could not call a variadic method; the matching reads every line, and the walker
  uses `CallSlice`. Zero allocations, and nothing stored on `Ctx`
  ([ADR-0018](adr/0018-accepts-negotiates-by-q-and-adds-vary.md)).
