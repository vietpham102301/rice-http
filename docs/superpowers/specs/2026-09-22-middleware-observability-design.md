# Middleware observability — Design

**Status:** approved, not yet implemented
**Date:** 2026-09-22
**Milestone:** none. Work after the roadmap — see [04-roadmap.md](../../04-roadmap.md), *Done after M8*
**Decision records:** ADR-0012 (new), "Application middleware runs on route misses"; ADR-0013
(new), "Middleware can settle a request's outcome with `c.HandleError`"
**Depends on:** M4 (the compiled chain), M5 (the error funnel and `middleware.Recover`), and
`c.ClientIP`, whose proxy-trust policy was deferred to the `RealIP` middleware this design adds

## Goal

Give a service an access log that tells the truth: every request, with the status the client
actually received, the client's real address, and an id to correlate it by.

Three middleware — `RealIP`, `RequestID`, `Logger` — plus two changes to core without which
the log cannot tell the truth at all. Timeout and CORS are separate designs.

## Question this design answers

What does a middleware in rice actually see about a request, and what does it need to see to
record that request correctly?

## What probing found

Everything below was run against rice at `33e5d71` and fasthttp v1.73.0, not inferred from
reading.

### 1. Application middleware never runs on a route miss

`App.handle` calls the error funnel directly when the lookup fails:

```go
h, ok := a.lookup(fctx.Method(), fctx.Path(), &c.params)
if !ok {
    a.callErrorHandler(c, ErrNotFound)
    return
}
```

Middleware registered with `app.Use` is compiled into each route's chain at build time, so it
exists only on routes that matched. No document states this; it is an unexamined consequence
of M4's compiled-chain design, not a decision. It breaks three of the five planned middleware
before they are written:

- **Logger** would never record a 404 — the requests an operator most wants to see: scanners,
  broken clients, mistyped URLs.
- **RequestID** would give a 404 response no id.
- **CORS** would be impossible. A browser's preflight `OPTIONS /users` against a route with only
  `GET` and `POST` registered is a miss, so the CORS middleware never runs and every non-simple
  cross-origin request fails.

### 2. Middleware sees the status before the funnel has written it

The funnel runs after the entire chain has returned: `if err := h(c); err != nil {
a.callErrorHandler(c, err) }`. A middleware reading the response status after `next(c)`
therefore reads it too early. A probe installing exactly that middleware:

| Request | Middleware read | Client received |
| --- | --- | --- |
| `/ok` | 200 | 200 |
| `/teapot` (returns an `HTTPError` 418) | **200** | 418 |
| `/boom` (returns a plain error) | **200** | **500** |
| `/nope` (no route) | *never ran* | 404 |

A Logger written the obvious way would record every production 500 as a 200.

### 3. A rewritten remote address outlives its request on a keep-alive connection

fasthttp acquires one `RequestCtx` per connection and serves every request on that connection
from it. `ctx.reset()`, which clears a remote address set by `SetRemoteAddr`, runs only in
`releaseCtx`, when the connection closes. A probe with a middleware that rewrites the address
only when it can resolve a header:

| Request | Header | `c.ClientIP()` |
| --- | --- | --- |
| 1, client A | `203.0.113.9` | 203.0.113.9 |
| 2, **a different client**, same connection | none | **203.0.113.9** |
| 3, new connection | none | 127.0.0.1 |

Behind a reverse proxy this is not an edge case. The proxy multiplexes many clients' requests
onto a few keep-alive connections to the backend, so a proxy-trust middleware that ever skips
the rewrite attributes one client's request to another — rate limits charge the wrong client,
and audit logs name the wrong one.

### 4. The 404 path is pinned at zero allocations

`TestAllocBudget404` pins "a miss costs what a hit costs, and both cost nothing". Any change to
the miss path must keep it at 0.

## Non-goals

- **Timeout and CORS.** Separate designs. Timeout has its own finding: a pre-emptive timeout —
  running the handler on another goroutine and answering 503 when time runs out — cannot be
  made safe here, because the middleware's return releases the `Ctx` to the pool while that
  goroutine still holds it. That is ADR-0005's use-after-release class, and it is the next
  design's problem.
- **`app.Pre`, or any second registration verb.** See D1.
- **Putting the request id in the context.** See D6.
- **Logging the query string.** See D8.

## Decisions

### D1 — Application middleware runs on route misses

`New` sets `a.miss` to `notFound`, a handler that returns `ErrNotFound`. `build` replaces it with
`chain.Compile(notFound, a.mws)`: the application's middleware only — no group middleware, since
no group matched, and no route middleware. `handle`'s miss branch runs `a.miss(c)` exactly as it
would run a matched route's chain.

Initialising in `New` rather than only in `build` is deliberate. The dispatch path is reachable
before `Build` — the package's tests call `handle` on unbuilt Apps throughout — so a miss chain
created only at build time would be nil there. Initialising it in `New` also means `handle`
needs no nil check.

The cost: nothing per request. The chain is compiled once, and `ErrNotFound` is a package
variable. With no application middleware, `chain.Compile(notFound, nil)` returns `notFound`
itself and behaviour is byte-for-byte what it is today.

**Behaviour changes, and ADR-0012 records them:** application middleware now sees misses;
`c.Param` returns empty on a miss, because no parameters were captured; a request for an
existing path with the wrong method is a miss and takes the same path.

Rejected: **a separate `app.Pre` for middleware that runs before routing** (Echo's model). It
changes nothing already shipped, but a Logger installed with `app.Use` would still silently miss
every 404 — the trap survives, now with a name. Rejected: **wrapping at the fasthttp layer**
around `FasthttpHandler()`. It needs no core change, but such middleware has no `*rice.Ctx`, no
`ClientIP`, no store and no funnel: a second middleware system beside the first.

### D2 — `c.HandleError(err)` settles a request now

```go
func (c *Ctx) HandleError(err error)
```

It runs the App's `ErrorHandler` immediately and sets `c.handled`, which `reset` clears. `handle`
becomes `if err := h(c); err != nil && !c.handled { a.callErrorHandler(c, err) }`, so a response
cannot be written twice — even if a middleware calls `HandleError(err)` and then still returns
`err`.

- `nil` is a no-op.
- A second call is a no-op; the first settles the request.
- **The panic path deliberately ignores `c.handled`.** A panic after `HandleError` still becomes
  a 500. M5 established that "a recovered panic anywhere in the chain is always a 500", and the
  response has not been sent yet, so overwriting it is correct.
- `poison.check()` comes first, before the `nil` check, because `ricedebug_test.go` calls every
  exported method with zero-valued arguments on a released `Ctx` and expects the
  use-after-release panic.

Rejected: **`rice.StatusOf(err)`**, a pure function mapping an error to the status the default
funnel would write. It needs no core change and it is wrong for every custom `ErrorHandler` — a
log line would record what the default handler would have answered, which is a documented lie.
Rejected: **running the funnel inside the chain**, at its innermost point. Middleware would always
see the real status and would never see an error again — reversing M5's design, where
`middleware.Recover` exists precisely so that outer middleware sees the error.

### D3 — `RealIP(trustedHops int)` counts from the right, and always sets the address

The client is `XFF[len − trustedHops]`. Each proxy appends the address it received a connection
from, so only the right-hand entries — written by proxies this service trusts — cannot be
forged by the client. The left-hand entries are whatever the client sent.

- Every `X-Forwarded-For` header line is read, in order, and joined. fasthttp's `Peek` returns
  only the first line; `RequestHeader.PeekAll` returns them all. Its result is borrowed from a
  buffer that fasthttp documents as overwritten by "any future calls to the Peek\*", so the
  middleware must finish with it before calling any other header accessor.
- Fewer entries than `trustedHops`, or an entry that does not parse as an IP: the middleware
  calls `SetRemoteAddr(nil)`, which fasthttp documents as restoring the connection's address. It
  never falls back to the leftmost entry.
- **It calls `SetRemoteAddr` on every request, with no exception** — the resolved address, or
  `nil`. That is the fix for finding 3; a middleware that only sometimes rewrites leaks one
  client's address into another's request.
- `trustedHops < 1` panics at construction, as every configuration mistake in rice does.

### D4 — `RequestID()` accepts an incoming id only if it is safe to log

The incoming `X-Request-Id` is kept if it is 1 to 64 characters from `[A-Za-z0-9_-]`. Anything
else — too long, empty, or containing any other character — is discarded and a new id generated:
16 bytes from `crypto/rand`, hex-encoded to 32 characters.

The character set is the security control. A client-supplied id goes into every log line for its
request, so an id containing a newline is log injection: a forged log entry in the record an
incident responder reads. A gateway's legitimate ids pass the check, so cross-service tracing
still works, and the check needs no knowledge of what sits in front of the service.

### D5 — The id is stored in rice's store and echoed on the response

It is set with `c.Set` under an unexported key and read with `middleware.RequestIDFrom(c)
string`, which returns `""` when `RequestID` is not installed. It is also written to the
`X-Request-Id` response header, so a client can quote it in a bug report.

`docs/03-core-concepts.md` §3 teaches the middleware shape with an illustrative `RequestID` that
stores the id under the public key `"request_id"`. That example stays, because it is the clearest
illustration of the shape. It gains one sentence pointing at the real middleware and at
`RequestIDFrom`, so a reader does not call `c.Get("request_id")` and receive `nil` without
knowing why.

### D6 — The id is not put in the context

This is the question the `c.Context` design deferred to this one. `context.WithValue` costs an
allocation on every request, for a use — propagating the id to an outbound call — that most
handlers never have. The documentation shows the one line that does it:

```go
c.SetContext(context.WithValue(c.Context(), requestIDKey{}, middleware.RequestIDFrom(c)))
```

### D7 — `Logger(l *slog.Logger)` is outermost, and settles errors before it logs

```go
start := time.Now()
if err := next(c); err != nil {
    c.HandleError(err)
}
// log, then
return nil
```

It reads the address and the request id **after** `next` returns, so although it is outermost it
still sees what `RealIP` and `RequestID` did inside it. Status 500 and above logs at `ERROR`;
everything else at `INFO`, so alerting can key on level alone. A nil logger panics at
construction. It always returns `nil`: it consumes the error, which is why it must be outermost.

### D8 — The log records the path, not the query string

Query strings carry tokens, session identifiers and personal data. `c.Path()` excludes the query,
and that is what is logged.

### D9 — The recommended order, and the one trap in it

```go
app.Use(
    middleware.Logger(l),   // outermost: times everything, settles errors, logs last
    middleware.Recover(),   // inside Logger: a panic becomes an error Logger can see
    middleware.RealIP(1),
    middleware.RequestID(),
)
```

**A request that panics is not logged unless `Recover` is installed inside `Logger`.** The panic
unwinds through `Logger`'s frame before it can record anything.

Letting `Logger` recover and re-panic was considered and rejected. The request would be logged,
but the re-panic moves the stack trace core records to `Logger`'s own frame, pointing whoever
debugs the panic at the wrong code. `Logger` stays single-purpose; the order is documented in
three places, and the limitation itself is pinned by a test, so a change in behaviour forces a
change in the documentation.

## Components

| File | Change |
| --- | --- |
| `app.go` | `miss` field; set to `notFound` in `New`; `handle`'s miss branch runs it; `handle` respects `c.handled` |
| `build.go` | compiles the miss chain from the application's middleware |
| `ctx.go` | `handled` field; `reset` clears it; `HandleError` |
| `middleware/realip.go` | `RealIP` |
| `middleware/requestid.go` | `RequestID`, `RequestIDFrom` |
| `middleware/logger.go` | `Logger` |

## Public API added

```go
// package rice
func (c *Ctx) HandleError(err error)

// package middleware
func RealIP(trustedHops int) rice.Middleware
func RequestID() rice.Middleware
func RequestIDFrom(c *rice.Ctx) string
func Logger(l *slog.Logger) rice.Middleware
```

## Allocation budgets

**Core.** `c.HandleError`: 0, excluding whatever the `ErrorHandler` itself costs. Every existing
dispatch budget is re-run, not assumed: the miss path changed, and `TestAllocBudget404` must stay
at 0. `TestNewCtxStaysWithinThreeAllocations` must hold with the new `handled` field.

**The three middleware allocate** — `slog`, IP parsing, id generation. Each is measured and pinned
exactly, in both directions, the way `binding.JSON` is, with its fixture named. `Logger`'s figure
names the `slog` handler it was measured with, since a different handler costs differently. The
rows go in `05-performance-model.md`'s *Opt-in packages* section, not beside the zero-budget core
rows.

## Testing

**The regression test for the whole branch** is finding 2's table, turned into a test: a
middleware that records status using `HandleError` must see 200, 418, 500 and 404 — matching what
the client receives. Today it sees 200, 200, 200 and nothing.

**Core — the miss chain**
- application middleware runs on `/nope`, and the response is 404
- group middleware does not run on a miss
- a wrong method on an existing path takes the miss path
- an **unbuilt** App answers a miss with 404 and does not dereference nil
- an App with no middleware answers a miss exactly as it does today

**Core — `HandleError`**
- the response is written at the moment of the call
- with a counting custom `ErrorHandler`, the handler runs **exactly once** — including when a
  middleware calls `HandleError(err)` and then still returns `err`
- `nil` is a no-op; a second call is a no-op
- a panic after `HandleError` still produces 500
- **`reset` clears `handled`**, so a pooled `Ctx` does not carry it into the next request

**`RealIP`**
- one trusted hop; two trusted hops
- fewer entries than hops → the connection's address
- a non-IP entry → the connection's address
- several `X-Forwarded-For` lines, joined in order
- **two requests on one real keep-alive connection**, the second unresolvable → the connection's
  address, not the first request's — finding 3, as a test
- `trustedHops < 1` panics

**`RequestID`**
- no header → a generated 32-character hex id, on the response header and from `RequestIDFrom`
- a valid incoming id is kept
- invalid incoming ids are replaced — and one containing a newline appears **nowhere** in the
  response or the log
- two requests get two different ids
- `RequestIDFrom` without the middleware returns `""`

**`Logger`** — writing to a buffer and parsing each line as JSON
- method, path, status, latency, address and request id are recorded
- **`GET /nope` is logged as 404** — the end-to-end test of both core changes at once
- 418 and 500 are logged correctly with the default `ErrorHandler` **and with a custom one** — the
  property that distinguishes D2 from `StatusOf`
- the query string is not logged
- the logged address is the one `RealIP` resolved
- 5xx logs at `ERROR`, everything else at `INFO`
- a panic with `Recover` inside `Logger` is logged as 500
- **the documented limitation, pinned**: with no `Recover`, a panicking request is not logged
- a nil logger panics

## Documentation

- **ADR-0012 (new)** — application middleware runs on route misses: finding 1, the behaviour
  changes, and the two rejected alternatives.
- **ADR-0013 (new)** — `c.HandleError`: finding 2's table as the evidence, and the two rejected
  alternatives.
- **`middleware/doc.go`** — the recommended order and the panic trap.
- **`docs/03-core-concepts.md`** — `HandleError` in §2; in §3, that application middleware runs on
  misses, that `c.Param` is empty there, and the sentence pointing the illustrative `RequestID`
  at the real one.
- **`README.md`** — the middleware and their order. Updated in this branch, not left for review
  to catch: the last two branches each shipped a README or retrospective sentence their own
  change had made false.
- **Forward references become present tense.** `docs/03-core-concepts.md:71` and
  `docs/04-roadmap.md:186` both say `middleware.RealIP` "is what will" resolve the proxy headers.
  The implementation must also re-run the search for every mention of `RealIP`, `RequestID` and
  `Logger` across `docs/`, `README.md` and the Go doc comments, and fix any it finds that this
  change has made false. The references to `middleware.Timeout` in ADR-0010 stay as they are:
  Timeout is not part of this branch.
- **`docs/05-performance-model.md`** — the `HandleError` row, and the three middleware rows in
  *Opt-in packages*.
- **`docs/02-architecture.md`**, **`docs/04-roadmap.md`** *Done after M8*, and one
  **`docs/progress.md`** entry.

## Exit criteria

1. `make test` green under `-race` and `-tags ricedebug`.
2. The status-recording test sees 200, 418, 500 and 404.
3. `TestAllocBudget404` still 0; every other dispatch budget unchanged; the three middleware
   budgets pinned in both directions.
4. ADR-0012 and ADR-0013 written and indexed.
5. No document says `RealIP`, `RequestID` or `Logger` "will" do something they now do.

## Open questions

None. The one acknowledged limitation — a panicking request is unlogged unless `Recover` sits
inside `Logger` — is documented and pinned rather than solved, for the reason D9 gives.
