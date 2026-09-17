# Progress Journal

Append-only. Newest entry first. Every entry is dated, names its milestone, and says what
was learned — not just what was done. Entries are never edited after the fact; a correction
is a new entry that says what the old one got wrong.

Each entry uses this shape:

```
## <ISO date> — Mn — Short title
**Did:**       what changed
**Learned:**   the non-obvious thing
**Measured:**  numbers, if any
**Next:**      the immediate next step
```

---

## 2026-09-18 — M6 — Context pooling: zero allocations, and a poisoning mechanism that would not have worked

**Did:** Pooled the `Ctx`. One `sync.Pool` per `App`, created in `New` rather than `build`
because the dispatch path is reachable before `Build`; `acquire` binds a pooled `Ctx` and
`release` unbinds it from inside M5's existing deferred closure, after the `ErrorHandler`, on
every path including a panic. `Params` is now a slice pre-sized by `MakeParams` from
`Tree.MaxParams()`, so `add` cannot fail, the eight-parameter limit and `router.MaxParams` are
gone, and a route may declare any number of parameters. `Set` and `Get` landed at last, backed by
a four-entry slice of key/value pairs rather than a map. `-tags ricedebug` poisons a released
`Ctx` and **keeps it out of the pool**, with `check()` first in all twelve exported `Ctx` methods
and a reflection test that fails if a thirteenth is added without one; `make test-debug` runs the
whole suite that way and CI runs it after `make test`. A 16-goroutine × 500-request test hammers
the pool in both builds under `-race`. ADR-0005 is corrected in place. Documented in
[M6-context-pooling.md](milestones/M6-context-pooling.md) and
[ADR-0005](adr/0005-context-pooling-and-borrow-contract.md).

**Learned:** Four things, and the first two are about guards rather than about pooling.

1. *The safety mechanism in an accepted ADR would not have caught the bug it was written for.*
   ADR-0005 specified poisoning a released `Ctx` by "clearing its pointers and setting a
   generation counter," and never said whether the poisoned object went back into the pool. If it
   does, the next request `Get`s the same object, `reset` clears the poison, and the stale
   reference reads that request's data without panicking. A generation counter cannot help: the
   code holding the stale pointer has no generation of its own, because the stale pointer and the
   reused object are the same pointer. Found by reading the design, five milestones after the ADR
   was accepted, and not by anyone running the framework in anger. The debug build now drops
   poisoned contexts instead of pooling them, which costs a fresh `Ctx` per request there —
   up to three objects through `newCtx` — the same count the unpooled benchmark arm reports, and
   two rather than three for an App with no parameterised route.

2. *The plan's own test could not fail on its own fault.* `TestARetainedCtxPanicsEvenAfterAnotherRequest`
   was written from the argument in point 1, and Task 4's injection — return the poisoned `Ctx` to
   the pool — left it green. The test drove both requests to completion before checking the
   retained pointer, and `release` marks unconditionally, so request 2's own release had already
   re-poisoned the shared object; it passed whether or not the live window was protected. The
   implementer reported the mismatch instead of adjusting it away, a review confirmed the cause,
   and the test now makes the stale call from *inside* request 2's handler. Against ADR-0005
   modelled faithfully (`poolReuse = true` plus clearing the poison on acquire) it fails with
   `stale call on request 1's Ctx while request 2 held it: recovered <nil> (returned "2")` —
   nothing panicked, and request 1's reference was handed request 2's parameter. The blind spot is
   the window while a later request holds the reused `Ctx`, not any use after release, and a test
   written against the latter cannot see it. The plan's test was the defect; the guard count goes
   from twelve to thirteen.

3. *`testing.AllocsPerRun`'s warm-up call hides pre-sizing faults.* It runs `f` once before it
   starts counting, and that call grows an undersized slice by `append`; every measured call then
   reuses the grown backing array, because `Reset` truncates in place and keeps the capacity. Two
   of this milestone's planned assertions were blind for exactly that reason. Both were replaced
   or backed by a direct capacity assertion — which is why `Params.Cap()` exists — and the
   injections confirmed the replacements go red where the originals did not.

4. *A test-harness detail became a design constraint, and the constraint as first written was
   wrong.* `make test` runs with `-race`, where `sync.Pool.Put` deliberately drops one object in
   four; each drop sends the next `Get` to `newCtx`, so a zero-allocation dispatch really allocates
   a fraction of a `Ctx` there. Measured at `GOMAXPROCS=1` over 200,000 iterations: **0.7492 per
   call for a parameterised route, 0.4982 for a static one**, because `newCtx` allocates three
   objects in the first case and two in the second — `MakeParams(0)` is a zero-capacity slice and
   Go serves it from `runtime.zerobase` with no malloc. `AllocsPerRun` divides integer counts, so
   both read as 0 while a genuine per-request allocation adds a full 1 and fails. Every version of
   this milestone's docs and comments up to the final review then added that a *fourth* allocation
   in `newCtx` "would break every zero budget under `make test`". It would not: injected, the
   static figure reaches 0.7535 and still reads 0, and the parameterised one reaches 1.0017, close
   enough to the boundary that three consecutive runs gave fail, fail, pass. The margin is real and
   the ceiling matters, but no existing test enforced it —
   `TestNewCtxStaysWithinThreeAllocations` now measures `newCtx` directly and fails
   deterministically without the race detector. A comment is not a guard, which is the second time
   this milestone learned that.

**Measured:** What pooling saves, both arms in one session so no cross-session drift is in the
number (`BenchmarkDispatchPooledVsUnpooled`, `bench/results/M6-context-pooling.txt`):
**35.94 ns, 0 B, 0 allocs pooled against 82.11 ns, 240 B, 3 allocs unpooled.** Three, not one —
the unpooled arm builds its `Ctx` through `newCtx` and pays for the parameter and store slices
too, so this is not the same figure as M1's single-allocation baseline for a smaller, pre-pooling
`Ctx`. Against M5's file, `BenchmarkRiceDispatch` moved 105.05n → 33.27n with allocations 1 → 0;
the allocation half is exact, the time half directional only, because the host OS moved from
Darwin 25.6.0 to Darwin 27.0.0 between the two recordings and `BenchmarkFasthttpBaseline`, which
runs no rice code, moved −3.88% across the same pair. `DispatchHTTPError` went 2 → 1 allocations
and `DispatchPanic` 4 → 3 without either path being opened: the one they lost is the `Ctx`. The
`check()` calls cost nothing in the release build — `go build -gcflags=-m` reports
`inlining call to (*poison).check` at every call site and the release body is empty — and the
benchstat around them, two back-to-back runs on the same machine rather than one process, reads
`RiceDispatch` −0.27% (p=0.001) against `CtxSetHeader`
+1.19% (p=0.000), both with 0 allocs on both sides. Two significant deltas with opposite signs are
code layout and session drift, not a cost; a real per-call cost cannot make one benchmark faster.
Root package coverage 98.9%.

**Next:** M7 — graceful shutdown with a deadline, OnStart/OnShutdown hooks, server timeouts as
options.

---

## 2026-09-14 — M5 — Error handling: one funnel, a false safety claim corrected, and a regression caught before it shipped

**Did:** Added `HTTPError` and `PanicError`, one `ErrorHandler` per `App` set with
`WithErrorHandler` and defaulting to `DefaultErrorHandler`, and `respond` as the funnel's one
way to write an error body. Every failure — a route miss, a returned error, a wrapped error,
a recovered panic — now reaches `DefaultErrorHandler` through the same type switch in front
of the same `errors.As` fallback: `grep -n 'ErrNotFound' app.go` returns exactly one line,
the call into the funnel, with no special-cased branch left anywhere. `App.handle` wraps the
whole dispatch in a deferred `recover()`, and `callErrorHandler` carries a second one beneath
it so a panicking `ErrorHandler` cannot resurrect the failure the first one exists to
prevent. `middleware.Recover` ships as the project's first opt-in package. Documented in
[M5-error-handling.md](milestones/M5-error-handling.md) and
[ADR-0008](adr/0008-rice-recovers-panics-in-core.md).

**Learned:** Three things, and a correction to a figure this entry almost got wrong.

1. *A documented safety guarantee was never real.* `docs/03-core-concepts.md` §7 has said
   since M1 that "the core installs a panic hook on the fasthttp server." fasthttp v1.73.0
   has no `PanicHandler` and no equivalent — `grep -rn "PanicHandler"` against the vendored
   source returns nothing — and a probe against `main` before this milestone began showed
   the actual consequence: a panicking handler took the whole process down, exit status 2,
   not just its connection. Four milestones, untested, corrected now rather than left to be
   found by someone running the framework in anger.

2. *The generalisation the milestone was built to make nearly cost the thing it was meant to
   protect.* Replacing M2's special-cased `errors.Is(err, ErrNotFound)` with a uniform
   `errors.As` walk is exactly what "one funnel" means, and `errors.As`'s `any` parameter
   heap-allocates its target on every call, matched or not — confirmed with `-gcflags=-m`.
   Unfixed, the 404 path — the one mistaken and hostile traffic hits hardest, and the exact
   case D2's prebuilt `ErrNotFound` exists to protect — would have gotten *more* expensive
   under the generalisation than it was before it, not less. A type-switch fast path in front
   of the `errors.As` fallback closed it before `alloc_test.go` was committed, not after.

3. *A design rationale that sounds careful can be checked and found false.* D8 justified
   writing the last-resort response with raw fasthttp calls "so that a bug in rice's own
   response path cannot recurse." `respond` is three fasthttp calls with no path back into
   error handling; it cannot recurse into anything, and acting on the false rationale walked
   the implementer into reintroducing the exact `ResetBody` no-op a previous task had just
   removed from `respond` itself. Both spec sections are corrected in place.

**Measured:** The recovery mechanism's cost, both arms in one session so they are directly
comparable (`bench/results/M5-error-handling.txt`, ten samples each, sorted before reading
median and range):

| | median | range | allocs |
| --- | ---: | --- | ---: |
| `BenchmarkNoRecoverBaseline` | 0.91 ns | 0.91 – 0.92 | 0 |
| `BenchmarkDeferRecoverOverhead` | 3.02 ns | 3.01 – 3.03 | 0 |

About **2.1 ns and zero additional allocations**. A panic itself, including `debug.Stack()`,
costs roughly **8.3 µs and 4 allocations** (`BenchmarkDispatchPanic`, median 8347 ns, range
8290–8361). The allocation budgets in `alloc_test.go` pin the funnel's other costs as tests:
a 404 is 1 allocation, a handler-constructed `HTTPError` is 2.

**Correction:** an earlier reading of this entry's own draft compared `BenchmarkChainDispatch0`
across the M4 and M5 files directly — 84.01 ns to 96.96 ns, a tempting "+13 ns of recovery
overhead." That comparison does not hold: `BenchmarkCtxSetHeader` and
`BenchmarkTreeLookup1000`, in files M5 never touched, moved *more* between the same two
recordings than any dispatch benchmark M5's changes run through, and
`BenchmarkFasthttpBaseline` — no rice code on that path at all — barely moved. This is
session-to-session drift, the fifth entry on the project's unexplained-numbers list and the
first that is instability itself rather than a stable unexplained cost, and it is not
reported as M5's price anywhere in the retrospective.

**Next:** M6 — pool `*Ctx` in a `sync.Pool`, publish `reset`/`release`, size parameter slots
from the max seen at build time, and add the `ricedebug` poisoning build tag. This is the
milestone the project has been building toward: the one allocation every dispatch benchmark
in this file still reports.

---

## 2026-09-12 — M4 — Middleware and groups: the chain is free, and it took four readings to say what "free" costs

**Did:** Added `Middleware` as `func(next Handler) Handler`, the `internal/chain.Compile`
fold, the one-time build phase, and `Group`. `Build` runs once under `sync.Once`, discards the
trees registration built, folds each route's chain — application, then each group outermost
first, then the route's own — and inserts the compiled `Handler` into fresh trees; `Run`,
`Serve` and `FasthttpHandler` all trigger it, and registering or calling `Use` afterwards
panics. Registration still inserts eagerly, so a duplicate route or a parameter-name conflict
still panics from the `app.GET` line that wrote it. A `Group` holds a pointer to its parent
rather than a copy of the parent's middleware, so `parent.Use(auth)` written after a child was
created still reaches the child's routes — the same footgun ADR-0003 exists to prevent, moved
down one level. Group prefixes must be empty or begin with `/` and not end with one, which
makes joining total. Every registration method gained the trailing `mw ...Middleware` M2's D7
left room for, so `app.GET("/x", h)` compiles unchanged. 160 tests pass under `-race` — 105 in
`rice`, 47 in `internal/router`, 8 in `internal/chain`.

**Learned:** Four things, the last of them about how the third was nearly overcounted.

1. *Compiling at build time does remove the per-request cost, and the milestone's real
   difficulty was saying by how much.* Five middleware add zero allocations and about 4.6 ns
   to an ~86 ns dispatch — a slope of roughly 1.1 ns per middleware, roughly linear. That
   number is the fourth reading of the same data. The first was a flat per-call average
   (2.3 ns each, about double). The second was mine as controller: a step at the first
   middleware then flat, read off the recorded medians, reviewed and agreed with, and wrong in
   shape as well as size. The third came from actually re-running — eight counts of the three
   committed benchmarks plus a 0/1/2/10 sweep — which put 0 → 1 at −0.3 ns and 1 → 5 at
   +4.9 ns, and which also produced zero measuring *slower* than one, an impossibility that is
   what first showed the comparison was unstable. The fourth correction is to the explanation:
   both earlier accounts blamed `ChainDispatch1`'s single 120.10 ns outlier, and deleting that
   sample moves its median only 0.77 ns. Reading the ten samples in *recording order* instead
   of sorted shows the real cause — a contiguous slow window in the session that inflated the
   tail of `ChainDispatch1` and the head of `ChainDispatch5` together, which is also why the
   recorded 5 − 0 delta of +8.67 ns is nearly twice the re-run's +4.6. The recorded file is not
   being re-run; the drift is real data and naming it beats hiding it behind a cleaner
   recording.

2. *A mechanism refuted is worth recording; a mechanism guessed is not.* The step was blamed
   on inlining — `Compile` returns the handler unwrapped for an empty slice, so perhaps the
   dispatch site inlines at zero middleware and not at one. `go build -gcflags=-m` says
   `cannot inline (*App).handle: function too complex: cost 390 exceeds budget 80`, and `h(c)`
   is an indirect call identically at every middleware count, because `h` is a value read out
   of the tree at run time and inlining is decided once over source text. That is a measured
   negative result and it is in the retrospective as one. Why the ~1.1 ns slope exists is
   **left unexplained**: one more indirect call per middleware is the obvious account and
   roughly a nanosecond each is unremarkable on this hardware, but nobody took a counter
   reading. It joins M1's 43 ns header write, M2's ~9 ns and M3's 15.93 ns on the unexplained
   list rather than getting a plausible cause attached to it.

3. *Nine for nine, a guard nobody broke on purpose was not a guard.* M3 counted five across M2
   and M3; the M4 design found a sixth; M4 itself found three more. One was a shipped
   slice-aliasing test that mutated the caller's slice with an `append` — which writes past the
   length the alias's header froze at, so it cannot detect aliasing at all — and had been
   praised by a task reviewer as exercising exactly that trap. A second was the Task 3 brief,
   which specified the same append-based shape for `Group`'s guard and would have shipped a
   second blind test beside the first, but was caught before it shipped. The third was a
   comment claiming `sync.Once` gives `App.built`'s unlocked reader a happens-before guarantee; `Once` orders
   goroutines that both call `Do`, and `register` never does. What closed all three was not
   review — review had already passed one of them. It was breaking the guarded property and
   confirming the guard went red: the aliasing tests were rewritten to overwrite
   `callerSlice[0]` in place, and each of the four copy sites (`App.Use`, `register`,
   `App.Group`, `Group.Group`) was then broken to `mws: mw` and watched to fail. The
   five-middleware allocation budget got the same treatment and failed on its precondition
   ("3 middleware ran, want 5") rather than quietly measuring a shorter chain.

4. *An experiment that cannot fail proves nothing, and it looks exactly like an experiment
   that passed.* A tenth false guard was claimed while the retrospective was being written and
   it was a false alarm — worth recording for how it happened rather than as a tally.
   `TestCompileWithNoMiddlewareReturnsTheHandlerItself` did contain a dead branch,
   `if &got == &h`, comparing two locals' addresses; a review had flagged it Minor and it was
   deferred, and a dead branch beside a live one invites the reading that the live one does
   more than it does. Both the retrospective's author and the controller then concluded the
   test could not detect a wrapping `Compile`, and both "confirmed" it with the same injection:
   an identity middleware, which returns the handler unchanged and wraps nothing. A genuinely
   wrapping `Compile` is not expressible — inside `Compile[H any, M ~func(H) H]` the handler is
   an opaque `H` that cannot be called, so the body can return `h`, a zero value, or the result
   of applying an `M`, and nothing else. The property is enforced by the signature, not by the
   test. This is the guard discipline failing one level up, in the fault injection meant to
   validate the guard: "break it and watch it go red" is worth nothing unless the break is
   real. The rewrite landed anyway and the suite is better for it — the dead branch is gone,
   identity is compared through `reflect.Value.Pointer`, and the new
   `TestCompileWithMiddlewareWrapsTheHandler` guards the complement and was confirmed red when
   `Compile` drops its middleware. The count of genuine false guards stays at **nine**.

**Measured:** Medians of ten runs from one recording — Apple M2 Pro, go1.25.6 darwin/arm64,
[bench/results/M4-middleware-and-groups.txt](../bench/results/M4-middleware-and-groups.txt):

| Benchmark | ns/op | B/op | allocs/op | range |
| --- | --- | --- | --- | --- |
| `BenchmarkChainDispatch0` | 84.01 | 352 | 1 | 83.00–85.60 |
| `BenchmarkChainDispatch1` | 91.71 | 352 | 1 | 84.20–120.10 |
| `BenchmarkChainDispatch5` | 92.68 | 352 | 1 | 89.78–105.40 |
| `BenchmarkChainDispatch5Grouped` | 93.86 | 352 | 1 | 91.04–101.50 |
| `BenchmarkBuild1000Routes` | ~239000 | 265001 | 7503 | 232306–247313 |
| **Five middleware, recorded medians (`5` − `0`)** | **+8.67** | **0** | **0** | |
| **Five middleware, eight-count re-run (~91.2 − ~86.6)** | **+4.6** | **0** | **0** | |

The two five-middleware figures are both correct and are not the same quantity: +8.67 is what
the committed recording's medians say, inflated by the slow window that straddles
`ChainDispatch1` and `ChainDispatch5`; +4.6 is what a dedicated eight-count re-run of the same
three benchmarks on the same binary says, and it is the figure the 0/1/2/10 sweep's
~1.1 ns/middleware slope independently agrees with. **Groups cost nothing at request time:**
`ChainDispatch5Grouped` against `ChainDispatch5` is +1.18 ns by median and −0.18 ns by mean of
the same ten runs, and a difference whose sign flips between the two summaries of one
recording is zero. The design says a group exists only at registration time and the numbers
agree. **The per-request allocation is still one, and M4 is not why it is 352 bytes** — M3 grew
the `Ctx` to 344 (352 after the allocator's size class), M4 adds no field to it and no
per-request state, which is why ADR-0003 chose the decorator over an index walk with a cursor.
`AllocsPerRun` pins it: `App.handle` costs 1 allocation with 0 middleware and 1 with 5, and a
compiled five-middleware chain called directly costs 0. **The double insertion costs about
239 µs per 1000 routes** — 239 ns per route, once, before the socket opens, in exchange for
call-site panics. That benchmark times the build pass only, with registration's own insertion
outside the timer; it is still the right number, because every alternative D1 considered
inserts once somewhere and what D1 bought is exactly one extra pass over the route set. Its
7503 allocs/op decompose exactly, measured rather than attributed: 1.000 for
`middlewareFor`'s slice, 3.000 for `chain.Compile`'s closures and 3.503 for `tree.Insert`'s
nodes, totalling 7.503 per route. The M3 suite was re-run in the same recording and drifted up
a couple of percent, floor included — session drift, not a regression, and no M4 code runs in
those paths.

**Next:** M5 — `HTTPError`, a configurable `ErrorHandler`, the fasthttp panic hook mapped into
the funnel, and `middleware.Recover` as an opt-in package. What M5 has to prove is that one
error funnel can absorb every failure mode — a route miss, a middleware that returns early, a
handler that fails, a handler that panics — without any of them becoming a special case, and
that the cause string never reaches the response body.

---

## 2026-09-11 — M3 — Radix tree router: the tree loses on static routes, and the `Ctx` is what got expensive

**Did:** Replaced the map with a radix tree per verb. Named parameters (`/users/:id`), a
trailing catch-all (`/files/*path`), insertion that splits nodes on the longest common prefix,
and a lookup that tries static children, then the parameter child, then the wildcard child, and
unwinds to the next alternative when a branch strands part of the path — so `/users/newx`
matches `/users/:id` even though `/users/new` consumed three bytes of it first, the bug
httprouter carried and Gin inherited. Registration rejects everything ambiguous or unreachable
at startup: duplicates, two parameter names at one position, a wildcard that is not last, a
repeated parameter name, more than eight parameters, and any pattern written in a form
`fctx.Path()` never produces (`/a//b`, `/a/./b`, `/caf%C3%A9`). Captured parameters live in a
fixed inline array on the `Ctx`, read through `c.Param` (borrowed) and `c.ParamString`
(copies). ADR-0004's open question is closed by
[ADR-0007](adr/0007-no-trailing-slash-or-case-insensitive-matching.md): no trailing-slash
redirection, no case-insensitive fallback, both opt-in later, decided on scope rather than on
the measurement ADR-0004 asked for — because both sit on the miss path and neither would cost a
matching request anything. 119 tests pass under `-race`.

**Learned:** Three things.

1. *The tree loses on static routes, exactly as predicted, and the milestone's biggest number
   is not about the tree at all.* Dispatch roughly doubled — `RiceDispatch` 37.91 → 79.70,
   `RiceDispatchNotFound` 33.15 → 97.68, `CtxSetHeader` 80.59 → 127.85, against a baseline that
   barely moved (11.24 → 10.92). The obvious suspect was the tree. It is not the answer. The
   `Ctx` grew from 16 bytes to 344 because `Params` is eight inline slots, and a throwaway
   benchmark of the two struct shapes — 11.8 ns/op for the 16-byte one, 72.9 for the 352-byte
   one — puts **about +61 ns on the allocation alone**, which more than accounts for the whole
   slowdown. The probe was thrown away rather than committed; it measures a struct shape, not
   rice. This is the first milestone to account for its own cost instead of recording it
   unexplained, and the account is mundane: it is the size of the thing being allocated.

2. *A prediction from M1 is now wrong, and M3 is why.* M1 estimated the `Ctx` allocation at
   roughly 3 ns and concluded M6's pooling would be "1 alloc to 0 rather than a large latency
   win". M3 made that allocation twenty-two times bigger, so removing it is now worth about
   61 ns per request — roughly three quarters of everything `RiceDispatch` measures. M6's payoff
   grew because M3 spent, which is not a credit to M3. Separately, the same probe offers the
   first candidate for M2's unexplained ~9 ns: M1's 3 ns came from comparing a 404 path that
   M2 then proved does not allocate, so it was never a measurement of an allocation; a direct
   probe says a 16-byte allocation costs about 11.8 ns, and the ~9 ns gap in the estimate is the
   size of the gap in M2's remainder. A coincidence of magnitude, not a profile, and recorded
   as a candidate.

3. *A guard nobody has broken on purpose is not a guard.* Five times across M2 and M3,
   something named as a guard did not constrain what it claimed: D2's named allocation test
   stayed green when the forbidden form was applied; a doc comment cited a test that did not
   check it; `TestTreeLookupDoesNotRetainThePathSlice` could not fail, because the helper copied
   the path and both fixtures were static; `App.lookup`'s comment claimed parameter capture
   allocated nothing and cited static-only budgets; and `ParamString`'s budget measured zero
   because a discarded result let the compiler elide the copy, with an upper bound that accepted
   zero. Every one was caught by reading the guard afterwards, never by the guard failing. The
   habit that closed the last three: break the thing deliberately and confirm the guard goes red
   — making `ParamString` alias instead of copy, and watching the budget fail, is what turned it
   into a test.

**Measured:** Medians of ten runs from one recording, map and tree from the same binary on the
same machine — Apple M2 Pro, go1.25.6 darwin/arm64,
[bench/results/M3-radix-tree-router.txt](../bench/results/M3-radix-tree-router.txt):

| Benchmark | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| `BenchmarkFasthttpBaseline` | 10.92 | 0 | 0 |
| `BenchmarkRiceDispatch` | 79.70 | 352 | 1 |
| `BenchmarkMapLookup10` | 6.19 | 0 | 0 |
| `BenchmarkMapLookup100` | 5.96 | 0 | 0 |
| `BenchmarkMapLookup1000` | 6.23 | 0 | 0 |
| `BenchmarkTreeLookup10` | 93.47 | 352 | 1 |
| `BenchmarkTreeLookup100` | 98.87 | 352 | 1 |
| `BenchmarkTreeLookup1000` | 109.40 | 352 | 1 |
| `BenchmarkTreeLookup1Param` | 91.32 | 352 | 1 |
| `BenchmarkTreeLookup5Params` | 110.25 | 352 | 1 |
| `BenchmarkTreeLookupWildcard` | 89.45 | 352 | 1 |
| `BenchmarkTreeLookupBacktrack` | 97.09 | 352 | 1 |
| `BenchmarkTreeMiss` | 111.10 | 352 | 1 |
| `BenchmarkTreeWrongVerb` | 107.70 | 352 | 1 |
| **Map scaling, 10 → 1000 routes** | **+0.04** | **0** | **0** |
| **Tree scaling, 10 → 1000 routes** | **+15.93** | **0** | **0** |

The map and the tree figures must not be divided into each other. `MapLookup*` is a bare
`map[string]int` probe with no framework around it; `TreeLookup*` is a full dispatch, `Ctx`
allocation and response write included, because `internal/router` is not reachable from package
`bench`. Read each series against itself: the map is flat in route count (a 0.27 ns spread
across a hundredfold change, pointing both ways), the tree's cost rises by 15.93 ns, and since
everything else in that path is constant across `n` the rise belongs to the lookup. That is the
design doc's prediction holding in both halves — the tree does not beat the map on static
routes, so it earns its keep on parameters and shared prefixes instead, and a static-only route
set is a case where the naive structure was already right. Parameters cost about 4.7 ns each
(91.32 for one, 110.25 for five) and the backtracking worst case costs about 6 ns over a direct
parameter match (97.09 against 91.32) — measured, and unremarkable. `unsafe.Sizeof(Ctx{})` is
exactly 344, the figure the design predicted; the 352 B/op the benchmarks report is the
allocator rounding it to the next size class, and both numbers are right. The zero-allocation
404 path is gone: `TreeMiss` and `RiceDispatchNotFound` now allocate one `Ctx`, because the
lookup fills a `*Params` living on it. M2 predicted M5 would end that zero; M3 ended it first,
for a different reason, which is the clearest evidence yet for M2's rule — measure a zero,
document why it holds, and do not pin it with a test unless the design guarantees it. Recorded
in [ADR-0005](adr/0005-context-pooling-and-borrow-contract.md). One known limitation, recorded
and not fixed: nothing isolates tree lookup from dispatch, so the tree's absolute per-lookup
cost is unknown and the hybrid map-in-front-of-tree question M2 asked M3 to settle stays open.

**Next:** M4 — `Middleware` as `func(Handler) Handler`, the `internal/chain` compiler, the
`build()` phase under `sync.Once`, and `Group` with prefix and middleware inheritance. What M4
has to prove is that compiling chains at build time actually removes the per-request cost
rather than trading it for closure indirection that eats the gain — a five-middleware chain at
zero allocations per request, or an honest explanation of why not.

---

## 2026-09-06 — M2 — Static router: the naive map is flat, and hard to beat

**Did:** Replaced `App.SetHandler` with per-verb route registration. `internal/router.Tree[H]`
is generic — nothing under `internal/` may import package `rice`, so the router cannot name
`rice.Handler` — and is backed by a `map[string]H` on purpose. Common verbs reach their tree
through a fixed array indexed by a `method` constant, so dispatching `GET` is an array index
rather than a string comparison; uncommon verbs fall back to a lazily created map that stays
nil for applications that never register one. Registration panics on a programmer error
(empty path, missing leading slash, nil handler, duplicate route); a miss produces
`ErrNotFound` and travels through the same error funnel a failing handler uses, so 404 is not
a special case. Exact match only: no parameters, no wildcards, no 405. 53 tests pass under
`-race`.

**Learned:** Three things.

1. *The map does not care how many routes exist.* Lookup is flat — 38.52, 38.37 and 38.34
   ns/op at 10, 100 and 1000 routes, a spread of 0.18 ns across a hundredfold change, which is
   noise. The design doc predicted "close to flat" and predicted the consequence: that a radix
   tree may well lose on purely static paths, which would mean it earns its keep on parameters
   and shared prefixes instead. Both halves held. That is a finding, not a failure, but it does
   narrow M3's case considerably.

2. *The 404 path costs zero allocations, and nobody designed that.* Both the M1 retrospective
   and D6 of the M2 design doc said the miss path would still allocate one `Ctx`. It does not:
   `go build -gcflags=-m` reports `&Ctx{} does not escape` on the miss branch against
   `&Ctx{} escapes to heap` on the hit branch. The miss-path `Ctx` goes only to `handleError`,
   a concrete method the compiler can see through, so it stays on the stack; the hit-path one
   goes through `h(c)`, an indirect call through a `Handler` function value the compiler must
   assume escapes. The caveat outweighs the win and is now recorded in `app.go` and in D6:
   **this zero is a compiler artifact, not a design guarantee.** M5's configurable
   `ErrorHandler` turns the funnel into a function value, at which point the miss-path `Ctx`
   escapes too and the 404 path returns to one allocation. That will be expected behaviour, not
   a regression.

3. *Routing is not free, and the cost is constant rather than scaling.* Dispatch went from
   25.49 ns/op in M1 to 37.91 in M2, about **+12.2 ns** after adjusting for 0.19 ns of baseline
   drift between the two recordings. All of it is paid on the first route: one route costs
   37.91, a thousand cost 38.34. Roughly 3 ns of the 12 is the map probe itself — the gap
   between `StaticRouterMiss` (36.45, probes a 1000-entry map) and `StaticRouterWrongVerb`
   (33.28, hits a nil map and returns). The other ~9 ns is unexplained and unprofiled, which is
   the second milestone running where an unexplained number has been recorded rather than
   chased.

**Measured:** Medians of ten runs from one recording — Apple M2 Pro, go1.25.6 darwin/arm64,
[bench/results/M2-static-router.txt](../bench/results/M2-static-router.txt):

| Benchmark | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| `BenchmarkFasthttpBaseline` | 11.24 | 0 | 0 |
| `BenchmarkRiceDispatch` | 37.91 | 16 | 1 |
| `BenchmarkStaticRouterLookup10` | 38.52 | 16 | 1 |
| `BenchmarkStaticRouterLookup100` | 38.37 | 16 | 1 |
| `BenchmarkStaticRouterLookup1000` | 38.34 | 16 | 1 |
| `BenchmarkStaticRouterMiss` | 36.45 | 0 | 0 |
| `BenchmarkStaticRouterWrongVerb` | 33.28 | 0 | 0 |
| **Framework cost** | **+26.67** | **+16** | **+1** |
| **Lookup scaling, 10 → 1000 routes** | **−0.18** | **0** | **0** |

The one allocation on every hit is still the `Ctx`, still 16 bytes, still M6's to remove.
`BenchmarkRiceDispatchNotFound` moved from 13.96 ns/op in M1 to 33.15 here; that is not a
regression, it is the funnel now writing a real 404 body where M1 wrote only a status code.

**Next:** M3 — the radix tree, with static, parameter and wildcard nodes, common-prefix
splitting, and a `Lookup` that fills a caller-supplied `Params` without allocating. These
numbers mean the tree cannot justify itself on static routes, where it is up against a flat
38 ns that does not degrade with route count; it has to prove its worth on parameters,
wildcards and shared prefixes — the things the map cannot do at any price — and M3 should be
judged on exactly those cases.

---

## 2026-09-06 — M1 — Minimal server: rice serves HTTP, and it costs one allocation

**Did:** Built the thinnest framework that can answer a request. `Handler` returns an error
and nothing else; `Ctx` wraps `*fasthttp.RequestCtx` with `Method`, `Path`, `Status`,
`SetHeader`, `SetContentType`, `String` and `Bytes`; `App` dispatches one handler through a
single error funnel; `Run`, `Serve`, `Addr` and a deadline-racing `Shutdown` put it on a real
socket. 23 tests pass under `-race`, six of them black-box integration tests driving the
server with `net/http` rather than fasthttp. No router, no middleware, no pooling.

**Learned:** Three things, none of them about fasthttp.

1. *A plan can be correct in every detail and still not execute in the order it names.* Task
   5 declared a `Ctx` field of type `*App`; `App` arrives in Task 7. The plan was internally
   consistent and impossible. Tasks 5 to 7 were merged rather than reordered, because the
   field exists to keep `reset`'s signature stable and that is worth more than the task
   boundary. The lesson is to cut plan tasks along compilation units, not concepts: a task
   that cannot end with `go build` succeeding is half a task.

2. *The 404 path allocates a `Ctx` it never uses.* `handle` allocates before checking whether
   a handler is registered, so the miss path pays for a context nobody reads — visible as
   `BenchmarkRiceDispatchNotFound` costing 2.9 ns more than the baseline while doing strictly
   less work. Harmless in M1, where there is one handler. From M2 the miss path is the one
   hostile traffic hits hardest. Deliberately not fixed here, because moving that line would
   optimise the exact thing this milestone exists to measure.

3. *The performance model is an allocation model wearing a broader title.* One `SetHeader`
   call costs 43 ns and zero allocations. Time and allocations are independent axes and
   [05-performance-model.md](05-performance-model.md) tracks only one, which is a defensible
   scope but not what the name promises.

**Measured:** The number M1 exists to produce, all medians of ten runs from one recording on
one machine — Apple M2 Pro, go1.25.6 darwin/arm64,
[bench/results/M1-minimal-server.txt](../bench/results/M1-minimal-server.txt):

| Benchmark | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| `BenchmarkFasthttpBaseline` | 11.05 | 0 | 0 |
| `BenchmarkRiceDispatch` | 25.49 | 16 | 1 |
| **Framework cost** | **+14.44** | **+16** | **+1** |

Sixteen bytes is the `Ctx`: two pointers. The allocation is deliberate, it is the only one on
the path, and it is what M6's `sync.Pool` has to remove. Worth expecting now: the 404 path
suggests the allocation itself is worth roughly 3 ns of the 14.44, so M6's headline is likely
to be "1 alloc to 0" rather than a large latency win.

**Next:** M2 — a deliberately naive `map[string]Handler` per method, with 404 routed through
the error funnel. Its purpose is not to be good. It is to produce a lookup number for 10, 100
and 1000 routes that M3's radix tree has to beat, so that the tree is built on evidence rather
than on faith.

---

## 2026-09-06 — M0 — Scaffold: the rig exists, and its floor is a true zero

**Did:** Created the module at `github.com/vietpham102301/rice-http` with fasthttp v1.73.0 as
its only runtime dependency, a `Makefile` with the six targets every later milestone drives,
the `bench/` package, `scripts/bench.sh` for stamped recordings, `bench/results/` with its
comparability warning, and CI running lint, race tests and a benchmark smoke run. The only
benchmark written measures raw fasthttp with no framework in the path.

**Learned:** Two things, neither of them about benchmarking.

1. *A dependency is not pinned until something imports it.* After `go get`, fasthttp sat in
   `go.mod` marked `// indirect`, because no Go file referenced it yet. `make tidy` at that
   moment would have deleted the require block and silently undone the pin. Task 2 fixed it by
   importing fasthttp for real, but the hazard was invisible until the command was in front of
   me.

2. *A tidiness check that can fail finds things a warning never would.* The plan required the
   Go directive to read exactly `go 1.25`; `go mod tidy` rewrites that to `go 1.25.0`
   deterministically. Two decisions that each looked obviously correct were quietly
   incompatible, and only the CI step's `git diff --exit-code` surfaced it. `go 1.25.0`
   expresses the same minimum, so it is kept and the plan's constraint is the one that gave
   way. Recorded in [M0-scaffold.md](milestones/M0-scaffold.md).

**Measured:** The first real number in the project, and it is deliberately not rice's.
`BenchmarkFasthttpBaseline` — a bare fasthttp handler writing a plaintext body on a reused,
pre-warmed `*fasthttp.RequestCtx` — costs **11.17 ns/op, 0 B/op, 0 allocs/op** (median of ten
runs, range 11.06–11.26) on an Apple M2 Pro under go1.25.6 darwin/arm64. Raw output in
[bench/results/M0-fasthttp-baseline.txt](../bench/results/M0-fasthttp-baseline.txt). Because
the floor is a true zero, every allocation rice reports from here on is rice's own.

**Next:** M1 — `Handler`, a `Ctx` that thinly wraps `*fasthttp.RequestCtx`, an `App` serving
one hardcoded handler over a real socket, and a `Ctx` allocated per request on purpose. Do not
optimise it. The delta from 11.17 ns/op and 0 allocs is the baseline M6 has to beat.

---

## 2026-09-02 — M0 — Design phase: docs written, no code

**Did:** Established the design documents in `docs/`: overview and non-goals, nine design
principles, the layered architecture and request lifecycle, the seven core concepts, a
nine-milestone roadmap, the performance model with allocation budgets, and a glossary. Wrote
six ADRs covering the decisions taken so far: fasthttp as transport, handlers returning
errors, middleware as a pre-built closure chain, the per-method radix tree, context pooling
with a published borrow contract, and the exclusion of reflection from core.

No Go code exists yet. `go.mod` has not been created. That is M0's job.

**Learned:** Three things surfaced while writing rather than while coding.

1. *The build phase was not in the original sketch.* It appeared only when working through
   what happens if `app.Use` is called after `app.GET`. Compiling chains at registration
   time silently drops middleware from already-registered routes, which is a security bug
   when the middleware is authentication. A one-time build step under `sync.Once` removes
   the ordering hazard entirely, at the cost of forbidding dynamic route registration. That
   trade seems clearly right and is recorded in ADR-0003.

2. *The zero-allocation claim only survives contact with reality once its exclusions are
   written down.* "Zero allocations per request" is not true for `c.JSON`, not true during
   pool warmup, and not true for large bodies. Writing the exclusion list in
   `05-performance-model.md` was more useful than writing the claim, and it changed the
   naming convention: borrowing accessors get the short name, copying ones get the long name,
   so the expensive choice is the deliberate one.

3. *`Lookup` has an awkward signature for a good reason.* Filling a caller-supplied `*Params`
   instead of returning a slice looks worse until you notice it is the only way the parameter
   storage can live on the pooled context. The awkwardness is where the allocation went.

**Measured:** Nothing. There is no code. Every number in
[05-performance-model.md](05-performance-model.md) is marked TARGET and is a design
commitment, not an observation. The first real number arrives in M1, and it is deliberately
the *unpooled* baseline, so that M6's pooling has something honest to be compared against.

**Next:** M0 — create `go.mod` at `github.com/vietpham102301/rice-http`, pin fasthttp, lay
out the package skeleton from [02-architecture.md](02-architecture.md), and build the
benchmark harness *before* there is anything to benchmark.
