# M2 — Static Router: Design

**Status:** approved, not yet implemented
**Date:** 2026-09-06
**Milestone:** M2 in [docs/04-roadmap.md](../../04-roadmap.md)
**Depends on:** M1 (`Handler`, `Ctx`, `App`, the error funnel, the server)

## Goal

Replace `App.SetHandler` with per-method route registration backed by a
`map[string]Handler` per HTTP verb. The map is deliberately the naive
implementation. Its purpose is to produce lookup numbers at 10, 100 and 1000
registered routes that M3's radix tree must beat, so that the tree is built on
evidence rather than on faith.

## Question this milestone answers

How bad is a map, really?

The honest expectation is that it will not be bad at all. Hashing a short string
is close to flat in the number of routes, so the map may well beat a radix tree
on purely static paths. If that is what the numbers say, it is a finding rather
than a failure: it would mean the radix tree earns its keep on parameters and
shared prefixes, not on static lookup, and M3 should be judged on those cases.
M2 exists to turn that sentence from a guess into a measurement.

## Non-goals

Explicitly out of scope, each with the milestone that owns it.

- Path parameters (`:id`) and wildcards (`*path`) — M3
- Trailing-slash handling and redirects — M3
- Middleware, groups, and the `build()` phase — M4
- `HTTPError`, a configurable `ErrorHandler`, panic recovery — M5
- Context pooling — M6
- `405 Method Not Allowed` and the `Allow` header — deferred, see below
- Path normalisation. `/a//b`, `/a/./b` and percent-encoded paths all register
  successfully today but can never match a real request, because
  `fctx.Path()` returns the path already collapsed and decoded by fasthttp
  before it reaches lookup. Registration does not run it through the same
  normalisation, so the two disagree. Known gap, not yet guarded against — M3
  owns it.

## Decisions

### D1: `internal/router` is generic over the handler type

`docs/02-architecture.md` forbids anything under `internal/` from importing
package `rice`, so the router cannot name `rice.Handler`. It is generic instead.

```go
package router

// Tree maps request paths to handlers.
//
// M2 backs it with a map, which is exact-match only. M3 replaces the internals
// with a radix tree without changing this API.
type Tree[H any] struct {
	routes map[string]H
}

func (t *Tree[H]) Insert(path string, h H) error
func (t *Tree[H]) Lookup(path []byte) (H, bool)
func (t *Tree[H]) Len() int
```

Rejected alternatives: storing `any` and type-asserting in `rice` adds an
assertion to the hot path and loses registration-time type safety; having the
router return an index into a `[]Handler` owned by `rice` adds a second
indirection and a second memory access for no benefit.

`Insert` returns an error rather than panicking. The router stays pure and
independently testable; `App` decides that a bad registration is fatal.

### D2: `Lookup` takes `[]byte` and must not allocate

`Ctx.Path()` returns a borrowed `[]byte`. The lookup is written as:

```go
h, ok := t.routes[string(path)]
```

The Go compiler special-cases `m[string(b)]` and does not copy the bytes. Writing
it as `key := string(path)` followed by `t.routes[key]` loses the optimisation and
costs one allocation per request.

This is the single most fragile line in the milestone. It is a correct-looking
refactor away from silently costing an allocation on every request, and the cost
would not show up as a test failure — only as a worse number in a benchmark
nobody reads that week. It is therefore pinned by an `AllocsPerRun` test, not
merely observed in a benchmark.

> **Corrected during final review (2026-09-06).** As designed, this section
> claimed that hoisting the conversion into a local — `key := string(path)` —
> "loses the optimisation and costs one allocation on every request." Applying
> that exact edit and measuring it does not confirm that: all three lookup
> budget tests still pass, and a standalone reproduction with `//go:noinline`
> and a 51-byte path (past the runtime's 32-byte stack buffer) still measures
> zero allocations, on go1.25.6. The map-index conversion optimisation
> survives a single-use local; only a conversion that genuinely escapes costs
> an allocation.
>
> The decision recorded above is unchanged: the map probe should still be
> written as `t.routes[string(path)]` on one line, because that is the exact
> form the optimisation is documented for and it cannot quietly become an
> escaping conversion later. What was wrong is the claim that today's compiler
> charges for the alternative, and the claim that a named test would catch the
> regression if it ever did — `alloc_test.go`'s lookup budget tests still pin
> the outcome (zero allocations) and still guard against a real regression,
> just not against this specific rewrite.

### D3: Method dispatch is a fixed array with a map fallback

Per `docs/02-architecture.md`.

```go
type method uint8

const (
	mGET method = iota
	mPOST
	mPUT
	mPATCH
	mDELETE
	mHEAD
	mOPTIONS
	methodCount
)

type App struct {
	trees [methodCount]router.Tree[Handler]
	rare  map[string]*router.Tree[Handler] // nil until an uncommon verb is registered
	// ... M1 fields
}
```

`trees` is an array of values, not pointers, so every element starts as a zero
`Tree` whose inner map is nil. `Insert` must create the map on first use; a nil map
reads fine but panics on write, so this is a real crash rather than a style point.

`methodIndex(m []byte) (method, bool)` switches on length and then on bytes. No
string comparison, no map lookup, no allocation on the common path. Uncommon
verbs fall through to `rare`, which stays nil for applications that never
register one.

### D4: Wrong method returns 404, not 405

A request whose path is registered under a different verb is a miss, and a miss
is 404.

Correct 405 handling requires scanning every other method's tree on the miss path
and building an `Allow` header. That makes the miss path cost N lookups instead
of one — and M1's retrospective already identified the miss path as the one that
mistaken and hostile traffic hits hardest. Paying that cost in the milestone whose
entire output is a lookup measurement would contaminate the measurement.

Deferred rather than rejected. When it returns it should be opt-in, and it needs
its own decision about whether the `Allow` scan happens at registration time
(cheap at request time, more state) or at request time (no state, slow miss path).

### D5: 404 travels through the error funnel as a sentinel

The roadmap requires 404 to be routed through the error funnel rather than
written directly. The mechanism is a package-level sentinel:

```go
// ErrNotFound is returned into the error funnel when no route matches.
var ErrNotFound = errors.New("rice: not found")
```

`handleError` gains exactly one branch:

```go
status := fasthttp.StatusInternalServerError
body := "Internal Server Error"
if errors.Is(err, ErrNotFound) {
	status = fasthttp.StatusNotFound
	body = "Not Found"
}
```

A package-level sentinel is created once at init, so returning it costs nothing.
Returning a struct by value into an `error` interface would box it and cost one
allocation per 404 — on precisely the path that should be cheapest.

M5 generalises this to `HTTPError` without breaking anything: `errors.Is` keeps
working against a sentinel, so M5 can reclassify `ErrNotFound` as an `*HTTPError`
value and every existing check still holds.

### D6: The lookup happens before the `Ctx` is allocated

M1's retrospective recorded that `handle` allocated a `Ctx` before checking
whether a handler existed, so the miss path paid for a context nobody read. M1
deliberately left it alone, because moving that line would have optimised the
exact thing M1 existed to measure. M2 is where it gets fixed.

```go
func (a *App) handle(fctx *fasthttp.RequestCtx) {
	h, ok := a.lookup(fctx.Method(), fctx.Path())
	if !ok {
		c := &Ctx{}
		c.reset(a, fctx)
		a.handleError(c, ErrNotFound)
		return
	}

	c := &Ctx{}
	c.reset(a, fctx)

	if err := h(c); err != nil {
		a.handleError(c, err)
	}
}
```

The miss path still constructs a `Ctx`, because the funnel takes a `*Ctx` and
M5 will want a real context there to build a custom 404. What changes is that the
construction now happens after a decision instead of before one, and it can be
removed wholesale by M6's pool rather than needing a special case.

> **Corrected after M2's measurements (2026-09-06).** As designed, this section
> claimed the miss path "still allocates one `Ctx`". It does not. The
> construction is real, but the allocation is not: `BenchmarkRiceDispatchNotFound`
> and `BenchmarkStaticRouterMiss` both record 0 B/op and 0 allocs/op, and
> `go build -gcflags=-m` explains why — `&Ctx{} does not escape` on the miss
> path against `&Ctx{} escapes to heap` on the hit
> path. The miss-path `Ctx` reaches only `handleError`, a concrete method the
> compiler can see through, so it lands on the stack; the hit-path `Ctx` goes
> through `h(c)`, an indirect call through a `Handler` function value, which the
> compiler must assume escapes.
>
> This zero is a compiler artifact, not a design guarantee. When D5's successor
> in M5 replaces `handleError` with a configurable `ErrorHandler` — a function
> value rather than a concrete method — the miss-path `Ctx` will escape too and
> the 404 path returns to one allocation. That is expected behaviour, not a
> regression. The decision recorded above is unchanged; only the claim about its
> cost was wrong.

### D7: Registration ships without the middleware parameter

`docs/03-core-concepts.md` commits to
`func (a *App) GET(path string, h Handler, mw ...Middleware)`. `Middleware` does
not exist until M4. M2 ships the signature without it:

```go
func (a *App) Handle(method, path string, h Handler)
func (a *App) GET(path string, h Handler)
// POST, PUT, PATCH, DELETE, HEAD, OPTIONS
```

Appending a variadic parameter in M4 does not break any call site:
`app.GET("/x", h)` compiles against both signatures. This is what makes deferring
safe here, and it is the difference from M1's `Ctx.app` field, where the deferred
type was load-bearing at compile time and forced three plan tasks to merge.

Declaring `type Middleware func(Handler) Handler` now and accepting arguments
nothing consumes would be worse than either: the parameter would either be stored
where nothing reads it, which is dead code, or silently discarded, which is a trap
for anyone who passes one.

All seven common verbs ship in M2 rather than a subset, because D3's fixed array
has to enumerate them anyway, and because benchmarks and tests written against
`Handle("GET", ...)` would not resemble how the API is actually used.

### D8: Bad registrations panic

`App` panics on: an empty path, a path not beginning with `/`, a nil handler, and
a duplicate route for the same method and path.

These are programmer errors discovered at startup, not runtime conditions. A
duplicate registration in particular means one of the two handlers can never run,
which is silent and expensive to debug. Panicking at the registration call site
puts the failure in the stack trace where the mistake is.

`SetHandler` is removed. Its doc comment already names M2 as the milestone that
does so.

## Components

| Unit | Responsibility | Depends on |
| --- | --- | --- |
| `internal/router.Tree[H]` | Exact-match path→handler storage, insert with duplicate detection, non-allocating lookup | nothing |
| `method` + `methodIndex` | HTTP verb to array index, no allocation | nothing |
| `App` registration | `Handle` plus seven verb helpers, validation, panic on programmer error | `router`, `method` |
| `App.lookup` | Verb to tree, then path to handler | `router`, `method` |
| `App.handle` | Dispatch, miss to funnel, error to funnel | all of the above |
| `App.handleError` | One new branch for `ErrNotFound` | `ErrNotFound` |

`internal/router` is testable with no reference to `rice` at all, which is the
point of the layering rule and worth verifying by writing its tests first.

## Testing

**Router, in `internal/router`:** insert and look up; lookup of an absent path;
duplicate insert returns an error; `Len` reflects insert count; lookup on an empty
tree; and a lookup whose `[]byte` argument is overwritten in place before the next
lookup still returns the right handler both times, proving `Lookup` does not retain
the caller's slice. That last one matters because fasthttp reuses the buffer the
path points into for the next request on the same connection.

**Registration, in `rice`:** each of the seven verb helpers reaches its own tree
and no other; `Handle` with an uncommon verb (`"PROPFIND"`) routes through `rare`;
panics for empty path, missing leading slash, nil handler, duplicate route.

**Dispatch, in `rice`:** hit returns the handler's response; miss returns 404;
right path with wrong verb returns 404; a handler returning an error still yields
500, proving the new branch did not break the existing funnel; the 404 body is
`Not Found` and never contains the sentinel's message.

**Allocation budgets, in `alloc_test.go`:** `App.lookup` at 0 allocations for a
hit and for a miss. This is the test that catches a refactor of D2.

**Integration, in `server_test.go`:** a real request over a socket to a registered
route and to an unregistered one, asserting 200 and 404 respectively.

## Benchmarks

The output of the milestone.

| Benchmark | Measures |
| --- | --- |
| `BenchmarkStaticRouterLookup10` | Lookup with 10 routes registered |
| `BenchmarkStaticRouterLookup100` | Lookup with 100 routes registered |
| `BenchmarkStaticRouterLookup1000` | Lookup with 1000 routes registered |
| `BenchmarkStaticRouterMiss` | The 404 path, 1000 routes registered |
| `BenchmarkRiceDispatchRouted` | Full dispatch through the router, for comparison with M1's `BenchmarkRiceDispatch` |

Recorded with `make bench-record LABEL=M2-static-router`. The M1 numbers are
re-run in the same recording so the comparison is same-machine, same-session,
per the rule in `bench/results/README.md`.

## Exit criteria

- Routing tests cover hit, miss and wrong-method
- Benchmarks record lookup cost at 10, 100 and 1000 routes
- `AllocsPerRun` asserts 0 allocations for lookup, hit and miss
- `docs/05-performance-model.md`: the row `| Router lookup, static route | 0 |`
  moves from `TARGET (M3)` to `MEASURED M2`. It describes routing generally, not the
  radix tree specifically, and M2 is what first makes it true. Every other row keeps
  its label.
- Retrospective in `docs/milestones/M2-static-router.md`
- Journal entry in `docs/progress.md`, carrying the map-versus-tree prediction so
  that M3 can be honest about whether it came true

## Open questions

None blocking. Two carried forward deliberately:

- Whether the map beats a radix tree on static routes. That is M3's question; M2
  produces the number it will be answered with.
- Whether 405 should be opt-in and whether its `Allow` set is computed at
  registration or request time. Deferred with D4.
