# M4 — Middleware and Groups: Design

**Status:** approved, not yet implemented
**Date:** 2026-09-11
**Milestone:** M4 in [docs/04-roadmap.md](../../04-roadmap.md)
**Decision record:** [ADR-0003](../../adr/0003-middleware-as-prebuilt-closure-chain.md)
**Depends on:** M3 (`internal/router`, per-verb registration, `Ctx.Param`)

## Goal

Add `Middleware`, groups, and the one-time build phase that compiles them. After
this milestone a route's middleware is folded into a single closure before the
socket opens, so a request costs exactly what it cost in M3: one tree lookup and
one handler call.

ADR-0003 already fixed the shape: `type Middleware func(next Handler) Handler`,
chains compiled in a `build()` step under `sync.Once` rather than at registration
or per request, and registering after build is a panic.

## Question this milestone answers

Does compiling chains at build time actually remove the per-request cost, or does
the closure indirection eat the gain?

The design says the answer is yes and that M4 adds **zero** per-request cost,
because the compiled chain is a `Handler` like any other and dispatch cannot tell
it apart from a bare one. That is a prediction, and it is cheap to be wrong about
in a way nobody notices — a five-middleware chain that costs 40 ns more than a
bare handler would still pass every behavioural test in this plan. So the
benchmark carries the milestone: five middleware must cost no additional
allocations and a time within noise of zero middleware.

M3 is the reason this matters now rather than later. It made dispatch 43 ns more
expensive, all of it `Ctx` growth. A milestone that adds nothing on top is worth
proving rather than asserting.

## Non-goals

Out of scope, each with the milestone that owns it or the decision that excluded it.

- `HTTPError`, a configurable `ErrorHandler`, panic recovery, `middleware.Recover` — M5
- Context pooling and parameter slot sizing — M6
- Lifecycle hooks, server timeouts, `Option` values — M7
- Route-level and global timeout middleware — explicitly deferred in the roadmap
- Dynamic route registration after start — foreclosed by ADR-0003
- `c.Query` and `c.Header` — still owned by no milestone; recorded in M3, unchanged here

## Decisions

### D1: Registration keeps inserting eagerly; `build` constructs fresh trees

`docs/02-architecture.md` describes `build()` as the step that inserts handlers
into the trees. M2 and M3 insert at registration instead, and they derive
something valuable from it: a duplicate route or a parameter-name conflict panics
from inside the `app.GET` call that caused it, so the stack trace names the
offending line. M2's D8 chose that deliberately.

M4 keeps both properties by doing the work twice:

```
Registration   validate the pattern
               insert into the tree      <- rejects bad configuration at the call site
               append to a.routes        <- the record build compiles from

build()        trees = fresh, empty
               for each recorded route: fold its chain, insert the compiled handler
```

The registration-time tree exists to reject a bad configuration where the mistake
was made. The build-time tree is the one that serves. The cost is inserting every
route twice, which is startup time and nothing else.

A recorded route holds what `build` needs and nothing more:

```go
type route struct {
	method string
	path   string      // already joined with the group prefix
	h      Handler
	group  *Group      // nil when registered directly on the App
	mws    []Middleware // this route's own middleware only
}
```

`group` is a pointer rather than a resolved middleware list for the reason D3
gives: the group's chain is not known until build, because a parent may still
gain middleware after the route is registered.

Rejected: deferring insertion to `build()` entirely. It matches the architecture
doc more literally and inserts once, but a parameter-name conflict would then
panic from inside `build()` — called from `Run` — and the stack would show
`Run → build → insert` rather than the `app.GET` line that is actually wrong.
Every existing test that calls `app.lookup` directly after registering would also
have to change.

Rejected: storing `*route` in the tree so `build` can assign a compiled handler
through the pointer. One insertion, call-site diagnostics preserved, but it adds
a pointer dereference to the hot path and a place to hang per-route configuration
that no scheduled milestone needs. YAGNI.

### D2: `Build` is exported, returns nothing, and panics like every other configuration error

```go
func (a *App) Build()
```

`Run`, `Serve` and `FasthttpHandler` all call it internally under `sync.Once`.
`FasthttpHandler` must, because every benchmark in `bench/` drives dispatch
through it and never opens a socket; without that trigger the benchmarks would
measure empty trees.

`handle` does **not** build. ADR-0003 rejected lazy per-request compilation
precisely because it would put synchronisation on the hot path. A test that calls
`handle` directly must call `Build` first.

`Build` returns nothing rather than an error. With D1, registration has already
rejected everything that could be wrong — every pattern is validated and every
conflict detected at the call site — so by the time `build` runs there is nothing
left for it to report. A `Build() error` that can only ever return `nil` invites
`if err != nil` around a branch that never runs, and this project has spent six
review rounds removing claims with nothing behind them. Configuration errors
panic, consistently with `Handle`.

If M5 or M7 gives `build` something real to validate, adding a return value then
is a breaking change — and rice is not 1.0, which is exactly when that trade is
cheap. Recorded so the decision is visible rather than rediscovered.

### D3: A `Group` holds a pointer to its parent and resolves middleware at build

```go
type Group struct {
	app    *App
	parent *Group       // nil for a group created directly on the App
	prefix string       // full prefix, joined at creation
	mws    []Middleware // this group's own middleware only
}
```

The prefix is joined eagerly because nothing ever adds to a prefix after the fact.
Middleware is **not** snapshotted, and that is the subtle part.

If a child group copied its parent's middleware list at creation, then
`parent.Use(auth)` written after the child was created would silently not apply to
the child's routes. That is the same bug ADR-0003 exists to prevent, moved down
one level — and it is the same bug with the same consequence, since the middleware
people add late is usually authentication.

So a group records only its own middleware, and `build` walks from the route's
group up to the root collecting lists. Nothing is fixed until build, at every
level.

**A slice-aliasing hazard this creates, which must be tested rather than assumed.**
If a child group's middleware slice shares a backing array with its parent's, a
later `append` on one can overwrite what the other sees. Two sibling groups
created from the same parent are where it surfaces. Any slice a group keeps must
not alias one another group can grow.

### D4: Group prefixes are constrained so that joining always produces a valid pattern

`Group` panics unless the prefix is either empty or begins with `/` and does not
end with `/`. Route paths must begin with `/`, as they already must. Those two
rules make concatenation total: a valid prefix joined to a valid path is always a
valid pattern.

```go
app.Group("/api")     // fine
app.Group("")         // fine — a middleware-only group with no prefix
app.Group("/api/")    // panics: prefix must not end with /; write /api
app.Group("api")      // panics: prefix must begin with /
```

Without the constraint, `app.Group("/api/")` followed by `g.GET("/users")` would
join to `/api//users`, and M3's validation would reject it with a message naming a
pattern the author never wrote. M3's D8 rejected path normalisation for exactly
that reason — an error naming a string that appears in no line of the program is
worse than no error at all. The constraint moves the rejection to the `Group` call
that is actually wrong.

A consequence worth stating because it will surprise someone: with prefix `/api`,
`g.GET("/")` registers `/api/`, which is a different route from `/api`. ADR-0007
declined trailing-slash equivalence, so the two do not collide and neither implies
the other. There is no way to register the group's bare prefix from inside the
group; register it on the `App`.

### D5: The chain compiler lives in `internal/chain` and is generic

```go
package chain

// Compile folds mws around h so that mws[0] ends up outermost.
func Compile[H any, M ~func(H) H](h H, mws []M) H {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
```

Generic because `internal/` may not import `rice`, so it cannot name `Middleware`.
The `~func(H) H` constraint is what lets a `[]Middleware` be passed without
converting the slice, which would copy it.

Both halves of that were compiled and run before being written here, not assumed:
the constraint builds, a `[]Middleware` passes with no conversion, and the fold
produced `A-in B-in handler B-out A-out` for two middleware and left the handler
unwrapped for an empty slice. The throwaway module was deleted. This project has
shipped six claims that turned out to have nothing behind them, and a constraint
form is exactly the kind of detail that reads plausible and does not compile.

Six lines is a thin package, and it earns its own one by being the single place the
fold direction is written down. Folding from the wrong end reverses execution
order, produces a chain that still runs and still passes any test that does not
assert order, and is the most likely single defect in this milestone. In its own
package it gets exhaustive order tests with no HTTP anywhere near them.

### D6: Order is app, then outer group, then inner group, then route, then handler

Committed already in `docs/03-core-concepts.md`. For a route registered on a group
nested two deep:

```
chain = app.mws ++ outer.mws ++ inner.mws ++ routeMws
compiled = chain[0](chain[1](... chain[n-1](handler)))
```

`chain[0]` is outermost: it runs first on the way in and last on the way out. The
unwind order is what the roadmap's exit criteria single out, because it is the half
a naive test misses — a middleware that only logs before calling `next` cannot tell
a correct chain from a reversed one.

### D7: The hot path does not change at all

`handle` still performs one lookup and calls one `Handler`. A compiled chain is a
`Handler`; dispatch cannot distinguish it from a bare one. `Ctx` gains no field,
and no per-request state exists for middleware, which is the whole reason ADR-0003
chose the decorator over an index walk with a cursor.

This is the milestone's central claim and the benchmarks are what test it.

### D8: Registration and `Use` after `Build` panic

Both panic with a message naming what was attempted. ADR-0003 requires it: the
alternative is a data race, since the trees and middleware lists are read on every
request without synchronisation and would be written concurrently.

`Build` itself is idempotent through `sync.Once`; calling it twice is not an error,
because `Run` after an explicit `Build` is a reasonable thing to write.

## Components

| Unit | Responsibility | Depends on |
| --- | --- | --- |
| `internal/chain.Compile` | Fold a middleware slice around a handler, outermost first | nothing |
| `rice.Middleware` | The decorator type | `Handler` |
| `rice.Group` | Prefix plus its own middleware, and a parent pointer | `App` |
| `App.routes` | The record of every registration, in order, for `build` to compile from | `Handler`, `Middleware`, `Group` |
| `App.Build` | Fold every route's chain, rebuild the trees, once | `chain`, `router`, `App.routes` |
| `App.Use`, `Group.Use` | Append middleware; panic after build | — |
| Registration methods | Validate, insert eagerly, record | `router`, `Group` |

`internal/chain` imports nothing and is testable with no HTTP at all.

## Public API added

```go
type Middleware func(next Handler) Handler

func (a *App) Use(mw ...Middleware)
func (a *App) Group(prefix string, mw ...Middleware) *Group
func (a *App) Build()

type Group struct{ /* unexported */ }

func (g *Group) Use(mw ...Middleware)
func (g *Group) Group(prefix string, mw ...Middleware) *Group
func (g *Group) Handle(method, path string, h Handler, mw ...Middleware)
func (g *Group) GET(path string, h Handler, mw ...Middleware)  // and POST, PUT, PATCH, DELETE, HEAD, OPTIONS
```

Every existing registration method on `App` gains a trailing `mw ...Middleware`.
That is the variadic M2's D7 deliberately left room for: `app.GET("/x", h)`
compiles unchanged against both signatures, which is why deferring it was safe.

## Allocation budgets

Rows this milestone moves from TARGET to measured in
[`docs/05-performance-model.md`](../../05-performance-model.md):

| Operation | Budget | Enforced by |
| --- | --- | --- |
| Chain call, 0 middleware | 0 | `AllocsPerRun` in `alloc_test.go` |
| Chain call, 5 middleware | 0 | `AllocsPerRun` in `alloc_test.go` |

The end-to-end row stays at one allocation, the `Ctx`. Middleware adds nothing to
it, and a budget test asserting a five-middleware dispatch still costs exactly one
allocation is what proves that rather than asserts it.

**A hazard specific to these budgets, given this project's history.** A budget test
that measures a chain built from middleware that do nothing may be optimised into
nothing. Each budget test must assert its precondition — that every middleware in
the chain actually ran, observed through a side effect the compiler cannot discard
— before measuring. Six times across M2 and M3 something named as a guard turned
out not to guard what it claimed; the habit that closed the last three was breaking
the guarded property deliberately and confirming the guard went red. Apply it here.

## Testing

**Chain compiler, in `internal/chain`:** order for zero, one, two and five
middleware; the unwind order on the way out; that `Compile` with an empty slice
returns the handler unchanged and does not wrap it.

**Order through the App:** app-level only; group-level only; route-level only; all
three together in a three-deep nesting, asserting the full sequence in and out.

**Order independence:** `app.Use` written after `app.GET` still applies — the
roadmap names this explicitly. `parent.Use` written after a child group was created
still applies to the child's routes, which is D3's reason for existing.

**Slice aliasing:** two sibling groups created from one parent, each given its own
middleware, must not see each other's. A parent given more middleware after both
children exist must reach both without corrupting either.

**Prefixes:** joining at one, two and three levels; the empty prefix; `g.GET("/")`
registering the prefix with a trailing slash; `Group("/api/")` and `Group("api")`
panicking at the `Group` call with a message naming the offending prefix.

**Build:** `Build` is idempotent; `Run` after an explicit `Build` does not rebuild;
registration after `Build` panics; `Use` after `Build` panics; a route registered
before `Use` still gets that middleware.

**Short-circuiting:** a middleware that returns without calling `next` stops the
chain, and the handler does not run. A middleware that returns an error reaches the
error funnel.

**Integration:** a grouped, middlewared route answered over a real socket, with the
middleware observable in the response.

## Benchmarks

Recorded with `make bench-record LABEL=M4-middleware-and-groups`.

| Benchmark | Purpose |
| --- | --- |
| `BenchmarkChainDispatch0` | Dispatch with no middleware — the control |
| `BenchmarkChainDispatch1` | One middleware |
| `BenchmarkChainDispatch5` | Five middleware — the roadmap's stated case |
| `BenchmarkChainDispatch5Grouped` | The same five arriving through nested groups, proving groups cost nothing at request time |
| `BenchmarkBuild1000Routes` | The build phase itself, so the startup cost of inserting twice is a number rather than a shrug |

The comparison that answers the milestone's question is `ChainDispatch5` against
`ChainDispatch0`: same allocations, and a difference in time attributable to five
closure calls and nothing else. The M3 benchmarks are re-run in the same recording
so the whole table stays comparable on one machine.

## Exit criteria

- Tests asserting exact execution order for nested groups, including the unwind
  order on the way out
- A test that `Use` after route registration still applies
- `AllocsPerRun` asserts 0 additional allocations for a five-middleware chain, with
  each budget test asserting its middleware actually ran before measuring
- Benchmarks recorded, with `ChainDispatch5` against `ChainDispatch0` reported
- Both `Chain call` rows in the performance model move to measured
- Retrospective in `docs/milestones/M4-middleware-and-groups.md`
- Journal entry in `docs/progress.md`

## Open questions

- Whether five closure calls are measurable at all above the ~80 ns dispatch M3
  leaves. If they are lost in the noise, that is the answer to the milestone's
  question and should be stated as such rather than dressed up.
- Whether the build phase's double insertion is worth measuring beyond
  `BenchmarkBuild1000Routes` — for instance whether a very large route set makes
  startup noticeably slower. Deferred unless the number looks bad.
