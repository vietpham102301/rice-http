# M6 — Context Pooling and the Borrow Contract: Design

**Status:** approved, not yet implemented
**Date:** 2026-09-18
**Milestone:** M6 in [docs/04-roadmap.md](../../04-roadmap.md)
**Decision record:** [ADR-0005](../../adr/0005-context-pooling-and-borrow-contract.md); this
milestone corrects its poisoning mechanism
**Depends on:** M3 (`Params` held by value on `Ctx`), M5 (the deferred recovery in `handle`)

## Goal

Remove the last allocation on rice's dispatch path, and pay for it honestly.

Every dispatch benchmark since M1 reports one allocation per request: the `Ctx` that
`App.handle` builds with `&Ctx{}`. M6 pools it, publishes `acquire`/`release`, removes the
fixed parameter limit that only existed because nothing owned parameter storage, adds the
per-request `Set`/`Get` store that `docs/03-core-concepts.md` has promised since M0, and
ships the `ricedebug` build that turns a use-after-release from silent corruption into a
panic.

## Question this milestone answers

What does pooling actually save versus M1's baseline, and what class of bug does it
introduce in exchange?

The first half is a number. The second half is the reason ADR-0005 exists, and this
milestone answers it by building the detector and showing it fire.

## What reading the design found

ADR-0005 decides that `ricedebug` "poisons a released `Ctx` — clearing its pointers and
setting a generation counter — so any later method call panics." That mechanism does not
work if the poisoned `Ctx` goes back into the pool:

1. A handler retains `c` — `go audit(c)` — and returns. `release` poisons `c` and `Put`s it.
2. The next request `Get`s the same `c`. `reset` clears the poison and binds it to the new
   request.
3. `audit` calls `c.Param("id")`. It does not panic. It returns the *other* request's
   parameter.

That is exactly the bug `ricedebug` exists to catch, and it catches it only when the stale
call lands before the object is reused — which is to say, nondeterministically, and least
often under the load where the bug actually appears. A generation counter cannot rescue it:
the retaining code holds a bare `*Ctx`, with no generation of its own to compare against,
and the stale pointer and the reused object are the same pointer.

D5 replaces the mechanism. ADR-0005 is corrected in place, with a "Corrected in M6" note in
the style of the "Recorded in M3" note it already carries.

## Non-goals

- **`c.Query`, `c.Header`, `c.Body`.** Still owned by no milestone; still listed under
  "Explicitly deferred" in the roadmap.
- **Typed store keys** (`rice.Key[T]` with a `Get` that returns `T`). Safer than
  `string`/`any` and immune to key collisions between middleware, but the documented API is
  `Set(key string, v any)`. Added to "Explicitly deferred".
- **Detecting retained `[]byte`.** A slice from `Param` or `Path`, or the `*fasthttp.RequestCtx`
  from `RequestCtx()`, points into fasthttp's memory, not into the `Ctx`. Nothing rice owns
  can be poisoned to catch it. Documented as a limit, not solved (D6).
- **Shrinking grown storage.** A `Ctx` whose store or params grew past their initial
  capacity keeps the larger slice for its life in the pool.
- **Lifecycle, timeouts, graceful draining.** M7.

## Decisions

### D1: One `sync.Pool` per `App`, created in `New`

The pool lives on `App`, not at package level, because what it builds depends on the App:
parameter capacity is the maximum over *this* App's routes (D3).

It is created in `New`, not in `build`, because the dispatch path is reachable before
`Build`. `app_test.go` and `errors_test.go` call `app.handle(fctx)` directly on unbuilt Apps
throughout, dispatching through the registration-time trees. A pool created in `build`
would be nil there.

```go
a.pool.New = func() any { return a.newCtx() }
```

`newCtx` reads `a.maxParams` at the moment it runs, so a `Ctx` built after more routes are
registered is sized for them.

### D2: `acquire` and `release` bracket dispatch; release rides the existing defer

```go
func (a *App) handle(fctx *fasthttp.RequestCtx) {
	c := a.acquire(fctx)
	defer func() {
		if r := recover(); r != nil {
			a.callErrorHandler(c, &PanicError{Value: r, Stack: debug.Stack()})
		}
		a.release(c)
	}()
	...
}
```

- `acquire` is `pool.Get` followed by the existing `reset(app, fctx)`.
- `release` runs **after** the ErrorHandler, so an ErrorHandler may still use `c`. It runs
  whether the handler returned, errored, or panicked — the property
  `docs/02-architecture.md` names as what makes the pool safe.
- It shares M5's deferred closure rather than adding a second `defer`, so the recovery's
  measured cost stays the only defer cost on the path.
- `release` drops every reference the `Ctx` holds before it goes back: `fctx` and `app` to
  nil, parameter values zeroed, store entries zeroed. A pooled object that kept them would
  keep a request's memory reachable for as long as it sat in the pool.

**Accepted hole.** If `respond` itself panicked inside `callErrorHandler`'s last-resort
recover, nothing catches it and `release` never runs. That is unreachable today (M5's
analysis in `app.go` still holds), and the consequence if it became reachable is that the
pool loses one object, not that anything is corrupted. Stated in a comment beside the call.

### D3: `Params` becomes an append-grown slice pre-sized from the maximum seen

`Params.slots` changes from `[MaxParams]Param` to `[]Param`. `add` appends and can no longer
fail, so it loses its `bool` result, and the lookup's `if params.add(...)` guards in
`tree.go` become unconditional calls. `MaxParams` and `parsePattern`'s rejection of routes
with more than eight parameters are removed.

Sizing:

- `router.Tree` records the largest parameter count (named parameters plus wildcard) of any
  pattern it accepted, exposed as `func (t *Tree[H]) MaxParams() int`.
- Registration updates `a.maxParams` from the tree it inserted into.
- `newCtx` allocates `make([]Param, 0, a.maxParams)`.

**Why append rather than a hard-sized slice.** A hard-sized slice with an `add` that reports
"full" is what the roadmap's wording suggests, but its failure mode is a lookup that
*silently fails to match* whenever the sizing is wrong — and sizing is wrong exactly when a
`Ctx` was built before a route with more parameters was registered, which the direct
`handle` tests do. It would also cost a capacity check per request. With `append`, a
too-small `Ctx` grows once and keeps the larger slice in the pool: a one-time allocation
instead of a wrong answer. After `Build`, registration is rejected, so in a served App the
pre-sizing is always sufficient and the grow path is never taken.

**Why not a package-level pool.** `maxParams` belongs to one App; two Apps in one process
would size each other's contexts.

A zero `Params` remains usable — a nil slice appends — so the router's own tests keep
constructing `var p Params` unchanged.

### D4: The per-request store is a pre-sized slice of key/value entries

```go
type entry struct {
	key string
	val any
}

func (c *Ctx) Set(key string, v any)     // replaces an existing key
func (c *Ctx) Get(key string) (any, bool)
```

- `newCtx` allocates `make([]entry, 0, storeCapacity)` with `storeCapacity = 4`.
- Both operations scan linearly. At four entries that beats a map and allocates nothing.
- A fifth distinct key grows the slice. Pooling makes that cliff a once-per-`Ctx` cost rather
  than a per-request one, since the grown slice is kept. The cliff is still documented.
- `reset` zeroes each used entry, then truncates to `[:0]`. The zeroing is required: a
  truncated slice still holds its `any` values in the backing array, and the pool would keep
  them alive.

**Why a slice and not an inline `[4]entry` array.** They differ only on a pool miss, by one
allocation when a `Ctx` is created. A slice matches D3, and avoids a slice that points back
into its own struct. `docs/03-core-concepts.md` and `docs/05-performance-model.md` currently
say "inline array"; both are corrected.

**Boxing is the caller's cost.** `Set` itself allocates nothing, but converting a
non-pointer value to `any` at the call site can: `c.Set("id", someString)` allocates the
interface's data word. Pointers and constants do not. The budget tests pin all three
(`Set` with a pointer: 0; `Get`: 0; `Set` with a non-constant string: 1) and the documentation
says whose allocation the last one is.

### D5: `ricedebug` never returns a `Ctx` to the pool

Under `-tags ricedebug`, `release` poisons the `Ctx` and drops it; `acquire` always builds a
fresh one. A poisoned `Ctx` therefore stays poisoned for as long as anything references it,
and every use after release panics — deterministically, regardless of load. The price is a
fresh `Ctx` per request in debug builds only — up to three objects through `newCtx`, not one.

**Corrected during M6's documentation task.** This paragraph originally read "one allocation per
request in debug builds only". That is wrong by this milestone's own counting: with nothing ever
`Put` back, every debug-build request goes through `pool.New` → `newCtx`, which allocates the
`Ctx`, its parameter slice and its store slice. The debug build *is* the unpooled arm of
`BenchmarkDispatchPooledVsUnpooled`, which the same document records at three allocations.

The mechanism compiles out of release builds by construction, not by hoping the inliner
cooperates:

```go
// poison_debug.go
//go:build ricedebug

const poolReuse = false

type poison struct{ released bool }

func (p *poison) mark()  { p.released = true }
func (p *poison) check() {
	if p.released {
		panic(errUseAfterRelease)
	}
}
```

```go
// poison_release.go
//go:build !ricedebug

const poolReuse = true

type poison struct{}

func (*poison) mark()  {}
func (*poison) check() {}
```

- `Ctx` embeds a `poison`. In release builds it is zero-sized and its methods are empty.
- Every exported `Ctx` method calls `c.poison.check()` first: `RequestCtx`, `Method`, `Path`,
  `Param`, `ParamString`, `Status`, `SetHeader`, `SetContentType`, `String`, `Bytes`, `Set`,
  `Get`.
- `release` calls `c.poison.mark()`, then `if poolReuse { a.pool.Put(c) }`. `poolReuse` is a
  constant, so the dead branch is eliminated at compile time.
- `acquire` under `ricedebug` still calls `pool.Get`; with nothing ever `Put`, that is always
  `New`. No separate code path is needed.
- The panic message is `rice: *Ctx used after its handler returned; see the borrow contract
  in the package documentation`.

A benchmark of dispatch before and after the `check()` calls are added confirms the release
build pays nothing for them.

### D6: The borrow contract states what `ricedebug` cannot catch

`doc.go` and `docs/03-core-concepts.md` §2 "Lifetime" currently imply the debug build
catches every violation of the contract. It catches misuse of the `*Ctx`. It cannot catch a
retained `[]byte` from `Param` or `Path`, or a retained `*fasthttp.RequestCtx` from
`RequestCtx()`, because those alias fasthttp's memory, which rice does not own and cannot
poison. Both documents say so, with the `go audit(c.Param("id"))` example as the case the
debug build will *not* flag.

## Components

| Unit | Responsibility | Depends on |
| --- | --- | --- |
| `pool.go` | `newCtx`, `acquire`, `release`, `storeCapacity` | `ctx.go`, `poison_*.go` |
| `poison_debug.go` / `poison_release.go` | `poison`, `poolReuse`, `errUseAfterRelease` | — |
| `ctx.go` | `Ctx` fields (`poison`, `store`), `reset`, `check()` in every accessor | `internal/router` |
| `ctx_store.go` | `Set`, `Get` | `ctx.go` |
| `app.go` | `pool`, `maxParams`, `acquire`/`release` in `handle` | `pool.go` |
| `route.go` | update `maxParams` on registration | `internal/router` |
| `internal/router/params.go` | slice-backed `Params`, infallible `add`, no `MaxParams` | — |
| `internal/router/router.go`, `pattern.go`, `tree.go` | `Tree.MaxParams`, drop the eight-parameter limit, unconditional `add` | `params.go` |

`pool.go` is the file `docs/02-architecture.md` has reserved for this since M0.

## Public API added

```go
func (c *Ctx) Set(key string, v any)
func (c *Ctx) Get(key string) (any, bool)
```

Changed, internal only: `router.MaxParams` removed; `router.Params.add` returns nothing;
`router.Tree.MaxParams() int` added.

Changed, behaviour: a route may declare any number of parameters.

New build tag: `ricedebug`.

## Allocation budgets

| Operation | Budget | Status |
| --- | ---: | --- |
| `App.handle`, static route, warm pool | 0 | TARGET M6 |
| `App.handle`, parameterised route read with `Param` | 0 | TARGET M6 |
| `App.handle`, 404 through the funnel | 0 | TARGET M6 |
| `Ctx.Set` with a pointer value | 0 | TARGET M6 |
| `Ctx.Get` | 0 | TARGET M6 |
| `Ctx.Set` with a non-constant string (caller's boxing) | 1 | TARGET M6 |
| Any dispatch under `-tags ricedebug` | `newCtx`'s objects more than release: 3 with a parameterised route, 2 static-only | documented, not asserted |

The existing `TestAllocBudgetHandleDispatch` and `TestAllocBudgetHandleDispatchParameterised`
drop from 1 to 0; `TestAllocBudget404` drops from 1 to 0.

**Budgets under `-race`.** `make test` runs with the race detector, and in race builds
`sync.Pool.Put` deliberately drops one object in four (`sync/pool.go`, Go 1.25). Each drop
sends the next `Get` to `newCtx`, which allocates the `Ctx`, its parameter slice and its
store slice — at most three objects — so a zero-allocation dispatch averages at most 0.75
allocations per call under `-race`. `testing.AllocsPerRun` divides integer counts, so this
reads as 0 and the budgets pass, while a genuine per-request allocation adds a full 1 and
fails. The margin is real but thin: a fourth allocation in `newCtx` erodes it, which is a reason
`newCtx` must stay at three. The comment on
the budget helper says so, because a reader who sees a pool-dependent zero pass under `-race`
will otherwise suspect the test is not measuring anything.

**Corrected after M6's whole-branch review.** Two claims in the paragraph above were wrong, and
both were measured on the finished branch at `GOMAXPROCS=1` over 200,000 iterations, reading
`runtime.MemStats` rather than `AllocsPerRun`:

- "at most three objects … averages at most 0.75" holds only for an App with a parameterised
  route. `MakeParams(0)` is `make([]Param, 0, 0)`, which Go serves from `runtime.zerobase` without
  a malloc, so a static-only App's `newCtx` allocates **two** objects and its dispatch averages
  **0.4982**; a parameterised App's allocates three and averages **0.7492**.
- "a fourth allocation … would break every zero budget under `-race`" is false. Injected, the
  static figure reaches 0.7535, which integer division still reports as 0, so
  `TestAllocBudgetHandleDispatch` passes; the parameterised figure reaches 1.0017, on the boundary,
  and three consecutive runs of `TestAllocBudgetHandleDispatchParameterised` gave fail, fail, pass.

The ceiling is therefore enforced by `TestNewCtxStaysWithinThreeAllocations`, which measures
`newCtx` directly and needs no race detector, rather than by the `-race` margin this paragraph
relied on.

`alloc_test.go` gains `//go:build !ricedebug`: the debug build allocates on purpose.

## Testing

**Pool mechanics (release build).**
- After a request, a `Ctx` retained by the handler holds no references: `fctx` and `app`
  are nil, `params.Len()` is 0, the store is empty, and the store's backing array holds no
  values. The last assertion is what fails if `reset` truncates without zeroing.
- A request after one that set store keys and captured parameters sees none of them.
- A handler that panics still releases its `Ctx` (observed through the retained pointer's
  cleared fields).
- An ErrorHandler receives a live `Ctx`: it can call `c.Path()` without seeing a released one.

**Params.**
- A route with more than eight parameters registers and captures every value.
- `Tree.MaxParams` reports the largest count across patterns, counting a wildcard.
- A `Ctx` built before a larger route was registered still captures all its parameters
  (the grow path of D3).
- Backtracking still truncates correctly with a slice: the existing backtracking tests pass
  unchanged, plus one asserting no stale parameter survives a failed branch.

**Store.**
- `Get` on an absent key returns `nil, false`.
- `Set` twice on one key replaces; `Get` returns the second value.
- Six distinct keys all round-trip (crosses the capacity).
- Middleware `Set`s, handler `Get`s — the use the store exists for.

**`ricedebug`** (files tagged `//go:build ricedebug`).
- **Exhaustive method test.** Using `reflect` over `*Ctx`'s exported method set, call every
  method on a released `Ctx` with zero-valued arguments and assert each panics with
  `errUseAfterRelease`. A method added later without a `check()` call fails this test
  without anyone remembering to extend it. Reflection is confined to the test; ADR-0006
  governs core.
- **The scenario test.** A handler retains `c`; a second request runs; the retained
  `c.Param` panics. This is the exact sequence D5's argument says the original mechanism
  misses, and it is the test that would fail against ADR-0005 as written.
- `poolReuse` is false.

**Concurrency.** N goroutines issue requests through `FasthttpHandler`, each with its own
`RequestCtx`, a distinct `:id`, and a middleware that `Set`s the id for the handler to `Get`
and echo alongside `Param("id")`. Every response must carry its own id, twice. Run under
`-race` in both builds.

**CI.** `Makefile` gains `test-debug: go test ./... -race -count=1 -tags ricedebug`; the CI
workflow runs it after `make test`.

**Fault injection.** Each guard is broken on purpose and watched to fail: remove a `check()`
from one accessor (exhaustive test fails); make `release` `Put` under `ricedebug` (scenario
test fails); truncate the store without zeroing (reference test fails); drop `release` from
the defer (reference test fails and the 0-alloc budget fails).

## Benchmarks

Recorded to `bench/results/M6-context-pooling.txt`.

- `BenchmarkRiceDispatch`, `BenchmarkRiceDispatchNotFound` and `BenchmarkDispatch404` —
  re-recorded; the headline comparison is against M1's baseline, with the between-session
  drift caveat stated beside the number.
- `BenchmarkDispatchParameterised` — new. Only tree-level parameter lookups
  (`BenchmarkTreeLookup1Param`, `BenchmarkTreeLookup5Params`) are benchmarked today; this is
  the full dispatch path for a route read with `Param`, the second zero the exit criteria
  claim.
- `BenchmarkDispatchPooledVsUnpooled` — both arms in one session: the pooled `handle`, and
  the same dispatch with a `&Ctx{}` per request. This is the honest answer to the milestone's
  question. The unpooled arm needs unexported access, so this benchmark lives in package
  `rice` (`pool_bench_test.go`) rather than `bench/`, and `scripts/bench.sh` and
  `make bench` gain the root package.
- `BenchmarkCtxSetGet` — one `Set` and one `Get` with a pointer value.
- `BenchmarkDispatchParallel` — `b.RunParallel` over the pooled path, since `sync.Pool`'s
  per-P caches are what a single-goroutine benchmark cannot show.
- The poison-check overhead: dispatch recorded before and after `check()` is added, same
  session, release build.

The benchmark comments in `bench/rice_bench_test.go` that say one allocation is expected
are updated.

## Documentation

- **`docs/adr/0005-context-pooling-and-borrow-contract.md`** — Decision corrected to D5's
  mechanism; "Corrected in M6" note explaining why a poisoned object that returns to the pool
  is not detected, and why the generation counter could not have helped.
- **`doc.go`** — borrow contract gains `Set`/`Get`, and D6's statement of what `ricedebug`
  cannot catch.
- **`docs/03-core-concepts.md` §2** — store described as a pre-sized slice, the boxing cost,
  D6's limit in "Lifetime".
- **`docs/05-performance-model.md`** — the unpooled M1 baseline row keeps its history; new
  rows for the budgets above with measured status; "Inline arrays before heap slices"
  corrected to what was built; the parameter-count cost line updated now that there is no
  limit.
- **`docs/02-architecture.md`** — `pool.go` and the `poison_*.go` pair move to what exists.
- **`docs/04-roadmap.md`** — M6 marked done; typed store keys added to "Explicitly deferred".
- **`README.md`** — status line.
- **Comments made stale by this milestone** — `ctx.go` (the `params` field and `reset`),
  `app.go` ("M3 allocates a Ctx per request on purpose… Do not optimise it here"),
  `internal/router/params.go` (`MaxParams`, "Until a pool exists").
- **`docs/milestones/M6-context-pooling.md`** — retrospective.
- **`docs/progress.md`** — journal entry.

## Exit criteria

1. `AllocsPerRun` asserts 0 allocations for dispatch to a static route and to a
   parameterised route read as bytes, and for a 404.
2. Under `-tags ricedebug`, a `Ctx` retained past its handler panics on use, including after
   another request has run — asserted by the scenario test.
3. The exhaustive reflection test covers every exported `Ctx` method.
4. The concurrency test passes under `-race` in both builds.
5. A route with more than eight parameters works.
6. `BenchmarkDispatchPooledVsUnpooled` is recorded, so the question "what does pooling save"
   has a same-session number.
7. `make lint`, `make test`, `make test-debug`, `make cover` and `make bench` all exit 0;
   coverage does not drop below 98.8% for the root package.

## Open questions

None. Scope (all four roadmap items), parameter storage (append-grown slice, limit removed),
and the store's shape were settled in brainstorming. The poisoning mechanism was found wrong
in ADR-0005 while designing and replaced by D5, approved in brainstorming.
