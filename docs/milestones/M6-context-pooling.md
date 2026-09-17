# M6 — Context pooling and the borrow contract

Status: done
Started: 2026-09-18
Finished: 2026-09-18

## Goal

The `Ctx` comes from a `sync.Pool`, one per `App`, created in `New` because the dispatch path is
reachable before `Build`. `acquire` binds a pooled `Ctx` to a request and `release` unbinds it
from inside M5's existing deferred closure, after the `ErrorHandler` has run, whether the handler
returned, errored or panicked. `Params` is a slice pre-sized from the largest parameter count any
registered route declares, so the fixed eight-parameter limit and `router.MaxParams` are gone and
`add` can no longer fail. `Set` and `Get` exist at last, backed by a four-entry slice of
key/value pairs rather than a map. And `-tags ricedebug` poisons a released `Ctx` and keeps it out
of the pool, so every later call on it panics — deterministically, not when the timing happens to
suit. Every dispatch benchmark in the suite reported one allocation from M1 through M5. They now
report zero.

## Question this milestone answers

What does pooling actually save versus M1's baseline, and what class of bug does it introduce in
exchange?

**The saving, measured with both arms in one session** — `BenchmarkDispatchPooledVsUnpooled`,
which exists precisely so the answer does not have to cross two recording sessions:

| | sec/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| `pooled` | 35.94n ± 0% | 0 | 0 |
| `unpooled` | 82.11n ± 4% | 240 | 3 |

Three allocations on the unpooled arm, not one. It builds its `Ctx` through `newCtx`, so it pays
for the struct, the pre-sized parameter slice and the pre-sized store slice. That number should
**not** be read as "3 versus M1's 1": M1 measured a smaller, pre-pooling `Ctx` with no separate
storage of its own. The honest statement of what the pool buys is the table above — today's `Ctx`
shape with and without a pool, in one run — and it is roughly 46 ns and 240 bytes per request.

Against M5's recorded file, `BenchmarkRiceDispatch` moved 105.05n → 33.27n with allocations
1 → 0. The allocation half of that is exact. The time half is directional only: the host OS moved
from Darwin 25.6.0 to Darwin 27.0.0 between the two recordings, and `BenchmarkFasthttpBaseline`,
which runs no rice code at all, moved −3.88% across the same pair. This file quotes the
same-session number as the answer and the cross-session number as corroboration, never the other
way round.

**The bug class,** in exchange: a `*Ctx` retained past its handler now reads whatever request
holds it next, silently, and only under load. That is not a new observation — ADR-0005 wrote it
down in M0 — but M6 is where the detector for it got built, and where the detector ADR-0005
specified turned out not to work.

## Design notes

### The mechanism ADR-0005 specified would not have caught the bug it was written for

ADR-0005 decided that `ricedebug` would poison a released `Ctx` by "clearing its pointers and
setting a generation counter," and never said whether the poisoned object went back into the
pool. If it does, the mechanism fails at exactly the moment it is needed:

1. A handler retains `c` and returns. `release` poisons it and `Put`s it.
2. The next request `Get`s the same object. `reset` clears the poison and binds it to that
   request.
3. The stale reference calls `c.Param("id")`. No panic. It returns the *other* request's
   parameter.

A generation counter cannot rescue it, because the code holding the stale pointer has no
generation of its own to compare against — the stale pointer and the reused object are the same
pointer. D5 replaced the mechanism: under `ricedebug`, `release` marks the `Ctx` and drops it, and
`acquire` always builds a fresh one, at one allocation per request in that build only.
ADR-0005 is corrected in place.

This was found while reading the design, not in production, which is the cheapest place to find
it and also the reason it is worth recording: a documented safety mechanism sat in an accepted
ADR for five milestones without anyone asking whether it worked. Nothing tested it, because there
was nothing to test — the mechanism did not exist yet. The lesson is narrower than "test your
guards": an ADR that specifies a mechanism is making a checkable claim, and the claim can be
checked by argument on the day it is written.

The empirical form of the correction is `TestARetainedCtxPanicsEvenAfterAnotherRequest`, and it
took two attempts to write — see the first entry under "What surprised me".

### The `-race` interaction with `sync.Pool`, and the three-object ceiling on `newCtx`

`make test` runs with the race detector, and in race builds `sync.Pool.Put` deliberately drops
one object in four. Each drop sends the next `Get` to `newCtx`, which allocates three objects: the
`Ctx`, its parameter slice, its store slice. So a dispatch that allocates nothing in a release
build really does allocate an average of at most 0.75 objects per call under `-race`.

`testing.AllocsPerRun` divides integer malloc counts before converting to float, so 0.75 reads as
0 and the zero budgets pass, while a genuine per-request allocation adds a full 1 and fails. The
margin is real but thin, and it is one-sided: a *fourth* allocation in `newCtx` would push the
average to 1.0 and break every zero budget under `make test` while leaving them green under plain
`go test`. That makes "`newCtx` allocates at most three objects" a design constraint rather than
an implementation detail, imposed by the test harness rather than by the framework, and it is
written on `newCtx` and on the `budget` helper so that a future reader who adds a fourth knows
what they broke and why the failure looks like a race-detector flake.

### Why `Params` appends instead of being hard-sized

The roadmap's wording suggested a hard-sized slice with an `add` that reports "full". Its failure
mode is a lookup that *silently fails to match* whenever the sizing is wrong — and the sizing is
wrong exactly when a `Ctx` was built before a route with more parameters was registered, which
the package's own direct-`handle` tests do constantly. With `append`, a too-small `Ctx` grows once
and the pool keeps the larger slice: a one-time allocation instead of a wrong answer. After
`Build`, registration is rejected, so in a served App the pre-sizing is always sufficient and the
grow path is never taken.

### `budget_test.go` exists because a build tag has a blast radius

`alloc_test.go` had to become `!ricedebug`, since the debug build allocates a `Ctx` per request on
purpose. That took the `budget` helper out of the debug build with it — and `method_test.go`'s
`TestAllocBudgetMethodIndex`, untagged and unrelated to pooling, calls `budget` too, so
`go test -tags ricedebug` stopped compiling. The helper moved to its own untagged file. Small, but
worth recording as the shape of the problem: a build tag applied to a file, not to a concern,
takes everything else in that file with it.

## Exit criteria

- [x] `AllocsPerRun` asserts 0 allocations for dispatch to a static route
      (`TestAllocBudgetHandleDispatch`), to a parameterised route read as bytes
      (`TestAllocBudgetHandleDispatchParameterised`), and for a 404 (`TestAllocBudget404`)
- [x] Under `-tags ricedebug`, a `Ctx` retained past its handler panics on use, including while a
      later request holds the same object — `TestARetainedCtxPanicsEvenAfterAnotherRequest`
- [x] The exhaustive reflection test covers every exported `Ctx` method —
      `TestEveryCtxMethodPanicsAfterRelease`, 12 of 12, so a method added later without a
      `check()` fails it without anyone remembering to extend the test
- [x] The concurrency test passes under `-race` in both builds —
      `TestConcurrentRequestsNeverSeeEachOthersState`, 16 goroutines × 500 requests
- [x] A route with more than eight parameters works —
      `TestARouteWithMoreThanEightParametersDispatches`, `TestLookupCapturesMoreThanEightParameters`
- [x] `BenchmarkDispatchPooledVsUnpooled` recorded, so "what does pooling save" has a same-session
      number
- [x] `make lint`, `make test`, `make test-debug`, `make cover` and `make bench` all exit 0; root
      package coverage 98.9%, above the 98.8% floor

M6's `☐` in `docs/04-roadmap.md` is now `☑`.

## Measurements

From `bench/results/M6-context-pooling.txt`, ten samples per benchmark, read through `benchstat`.

| Case | Time | Allocs | Bytes |
| --- | ---: | ---: | ---: |
| `DispatchPooledVsUnpooled/pooled` | 35.94n ± 0% | 0 | 0 |
| `DispatchPooledVsUnpooled/unpooled` | 82.11n ± 4% | 3 | 240 |
| `RiceDispatch` | 33.27n ± 0% | 0 | 0 |
| `RiceDispatchNotFound` | 33.84n ± 0% | 0 | 0 |
| `DispatchParameterised` | 38.03n ± 0% | 0 | 0 |
| `Dispatch404` | 37.40n ± 0% | 0 | 0 |
| `CtxSetGet` | 29.46n ± 0% | 0 | 0 |
| `DispatchParallel` | 4.461n ± 10% | 0 | 0 |
| `ChainDispatch0` / `ChainDispatch5` | 33.19n / 37.61n ± 0% | 0 | 0 |
| `DispatchHTTPError` | 51.63n ± 0% | 1 | 48 |
| `DispatchPanic` | 7.374µ ± 0% | 3 | 3.048Ki |

Against M5's file — allocations exact, times directional for the OS-version reason stated above:

| | M5 | M6 | allocs M5 → M6 |
| --- | ---: | ---: | ---: |
| `RiceDispatch` | 105.05n | 33.27n | 1 → 0 |
| `Dispatch404` | 99.67n | 37.40n | 1 → 0 |
| `ChainDispatch5` | 107.55n | 37.61n | 1 → 0 |
| `DispatchHTTPError` | 119.80n | 51.63n | 2 → 1 |
| `DispatchPanic` | 8.347µ | 7.374µ | 4 → 3 |
| `FasthttpBaseline` (control, no rice code) | 11.60n | 11.15n | 0 → 0 |

`DispatchHTTPError` and `DispatchPanic` each lost exactly one allocation, and M6 opened neither
the error funnel nor the panic path. The one they lost is the `Ctx`.

The `check()` calls that `ricedebug` needs in all twelve exported `Ctx` methods cost nothing in
the release build. `go build -gcflags=-m` reports `inlining call to (*poison).check` at every call
site, and the release-build method body is empty, so there is nothing left to inline. A
same-session benchstat of dispatch before and after the calls were added:

```
                │  precheck.txt  │              postcheck.txt              │
                │     sec/op     │    sec/op     vs base                   │
RiceDispatch-12    31.88n ± 1%       31.80n ± 0%  -0.27% (p=0.001 n=10)
CtxSetHeader-12     72.22n ± 0%      73.08n ± 0%  +1.19% (p=0.000 n=10)
```

0 B/op and 0 allocs/op on both sides of both rows. Both deltas are flagged significant and they
have **opposite signs**, which is what code layout and session drift look like — a real per-call
cost cannot make one benchmark faster. What this table supports is that the check compiles out,
with `-gcflags=-m` as the direct evidence; it does not support a claim of "no measurable
difference", and this document does not make one.

Hardware, Go version, OS: Apple M2 Pro, go1.25.6 darwin/arm64, Darwin 27.0.0 arm64.
Recorded 2026-09-17T18:23:46Z.
Raw output: [`bench/results/M6-context-pooling.txt`](../../bench/results/M6-context-pooling.txt).

## What `ricedebug` still cannot catch

It catches misuse of the `*Ctx`, and nothing else. It cannot catch:

- **A retained `[]byte`.** `go audit(c.Param("id"))` — the exact example
  `docs/03-core-concepts.md` uses to teach the contract — reads a reused buffer, and the debug
  build stays silent. That slice points into fasthttp's request buffer, not into anything rice
  owns, so there is nothing for rice to poison. Same for `c.Path()`.
- **A retained `*fasthttp.RequestCtx`** from `RequestCtx()`, for the same reason.
- **Anything, in a build nobody runs.** `make test-debug` is in CI so this project's own code is
  covered; a user who never passes the tag gets no detector at all.

The first of these is the more dangerous of the two data-corruption paths, because it is the one
the documentation's own example uses, and it is now stated as a limit in `doc.go`, in
`docs/03-core-concepts.md` §2 and here rather than left to be inferred from silence.

## Retrospective

**What surprised me:**

*The plan's own test could not fail on its own fault, and a review is what caught it.* The design
argued, in prose, that returning a poisoned `Ctx` to the pool defeats the poison. The plan turned
that into `TestARetainedCtxPanicsEvenAfterAnotherRequest`, and Task 4's fault injection flipped
`poolReuse` to `true` to watch it go red. It stayed green. The reason is not subtle once seen:
the test drove both requests to completion before touching the retained pointer, and `release`
marks unconditionally, so by the time the assertion ran the shared object had been re-marked by
request 2's own release. The test passed whether or not the live window was protected. The
implementer reported this honestly rather than adjusting the number until it matched, the review
confirmed the root cause, and the test was rewritten to make the stale call from *inside* request
2's handler, while that request still holds the object. With ADR-0005 modelled faithfully —
`poolReuse = true` plus clearing the poison on acquire — it then fails the way the argument said
it would:

```
$ go test . -tags ricedebug -run TestARetainedCtxPanicsEvenAfterAnotherRequest -count=1
    ricedebug_test.go:111: stale call on request 1's Ctx while request 2 held it: recovered <nil> (returned "2"), want the use-after-release panic
--- FAIL: TestARetainedCtxPanicsEvenAfterAnotherRequest (0.00s)
```

`recovered <nil>` — nothing panicked — and the return value was `"2"`, request 2's parameter,
handed to a reference that belonged to request 1. That is the leak, reproduced on demand. The
precise condition matters and the ADR now states it: the original mechanism's blind spot is the
window while a later request holds the reused `Ctx`, not any use after release. A test written
against "any use after release" cannot see it.

This is a new species for the guards list. M5 collected a redundant line no test could detect and
a live guard an optimisation quietly detached. This one is a guard that was *never* connected —
written from the same argument it was meant to verify, and unable to observe the failure that
argument describes. The plan's test was the defect.

*Two of the milestone's assertions would have been sensitive to `AllocsPerRun`'s warm-up call.*
The plan's `TestNewCtxIsSizedForTheLargestRoute` measured allocations to prove `newCtx` pre-sizes
the parameter slice. `AllocsPerRun` calls `f` once, unmeasured, before it starts counting; that
warm-up grows an undersized slice by `append`, and every measured call afterwards reuses the
grown backing array, because `Reset` truncates in place and keeps the capacity. The measured count
is 0 whether or not `newCtx` pre-sized anything. It was replaced with a direct
`c.params.Cap() == 3` assertion before the production code was written, and the injection
confirmed the replacement goes red where the original would not have. Task 1 hit the same shape
from the other end: with `MakeParams` stripped of its capacity argument,
`TestAddWithinCapacityAllocatesNothing` stayed green for exactly this reason, and
`TestMakeParamsPreSizesStorage`'s `Cap()` check was the only thing that caught it.

*A test-harness detail became a design constraint.* "`newCtx` must allocate no more than three
objects" is not a performance target. It is the condition under which `AllocsPerRun`'s integer
division hides the race detector's one-in-four `Put` drop, and therefore the condition under which
the zero budgets mean anything at all under `make test`. Nothing in the framework wants three
rather than four; the measuring apparatus does.

**What I would do differently:**

Write the fault injection *before* the test it is meant to validate, or at least demand that the
plan state, for each guard, which observable the guard reads and at what moment. Both surprises
above are the same failure at different stages: a test whose timing or measurement window cannot
see the fault it names. The plan reviewed cleanly because each test's *description* matched its
purpose. What nobody checked, until an implementer ran the injection and read the result
carefully, was whether the assertion could physically observe the thing the description claimed.
For `TestARetainedCtxPanicsEvenAfterAnotherRequest` that took a rewrite after the code had
already shipped; for `TestNewCtxIsSizedForTheLargestRoute` it was caught at plan-review time and
cost nothing.

**Fault injections that stayed green, and what each meant.**

Every milestone since M4 records these, because a deliberately broken guard that stays green is
the finding. Four this milestone:

1. **Task 4, injection 2 — `poolReuse = true` alone.**
   `TestARetainedCtxPanicsEvenAfterAnotherRequest` **passed**; only
   `TestTheDebugBuildNeverReusesAContext` failed. Unanticipated, and the milestone's headline
   process finding: both the test and the injection were wrong, the injection because
   `poolReuse = true` on its own does not model ADR-0005 (nothing un-poisons on acquire), and the
   test because it looked after the window rather than inside it. Fixed, re-injected as the real
   ADR-0005 model, and red — quoted above. **Counted below.**
2. **Task 1, injection 3 — `MakeParams` dropped its capacity argument.**
   `TestAddWithinCapacityAllocatesNothing` stayed green, *as the brief predicted it would*, for
   the `AllocsPerRun` warm-up reason above. `TestMakeParamsPreSizesStorage`'s `Cap()` assertion
   went red. This is the good case: a known-blind assertion, documented as blind, with a second
   assertion positioned to cover it — which is why `Params.Cap()` exists at all.
3. **Task 3, injection 4 — `newCtx` stopped pre-sizing the store.**
   `TestAllocBudgetHandleDispatch`, `TestAllocBudgetHandleDispatchParameterised` and
   `TestAllocBudgetCtxSetPointer` all stayed green, again as predicted. The first two never call
   `Set`, so a nil store is never touched; the third's warm-up call grows the nil store to
   capacity 1 and the measured calls reuse it. `TestThePoolBuildsContextsThroughNewCtx`, added for
   this purpose, went red on the capacity directly. Same lesson as 2, confirmed rather than taken
   on faith.
4. **Task 2, injection 2 — `release` moved above the `recover()` block.** Not green: it crashed
   the test binary with a SIGSEGV instead of reporting an assertion failure, because
   `TestErrorHandlerReceivesALiveCtx`'s `ErrorHandler` called `c.Path()` on an unbound `Ctx` while
   the runtime was still unwinding. `go test` still exits non-zero, so CI would catch it. Recorded
   because "the guard went red" and "the guard reported what was wrong" are not the same thing.

Task 5's two injections both failed loudly under `-race` — data races reported at `pool.go`'s
`release`, and, for a package-level shared `Ctx`, races in `reset`, `Set` and fasthttp's own
header writing, ending in a crash. Nothing unexpected there.

**Guards, and the count.**

M4 closed at nine, M5 at twelve. M6 adds one:

- **`TestARetainedCtxPanicsEvenAfterAnotherRequest` as the plan wrote it** — a guard written from
  the very argument it existed to verify, whose assertion could not observe the failure that
  argument describes. **Counted.** It is distinct from M5's species: not a redundant line, and not
  a live guard detached by a later change, but a guard that never had contact with its invariant
  from the moment it was written.

Two near-misses are deliberately **not** counted, for the reason M4 and M5 both gave — the tally
is of guards that turned out not to guard, not of every place the process around guards went
wrong:

- **`TestNewCtxIsSizedForTheLargestRoute` as the plan wrote it.** The same species as the counted
  item, caught at plan-review time and replaced before any production code existed. It was never a
  guard; it was a draft of one.
- **Task 3's and Task 1's `AllocsPerRun` blind spots.** Anticipated in the briefs, with a
  capacity-asserting companion test written specifically to cover them. A documented blind spot
  with a live guard behind it is a design, not a defect.

**Twelve plus one is thirteen.**

**What I still do not understand:**

M5 left the project's fifth unexplained entry as instability itself: benchmarks in files M5 never
touched moved more between two recording sessions than the benchmarks its changes ran through.
M6's recording does not resolve that and adds a second data point to it. Every benchmark in the
M6 file whose movement against M5 is statistically significant moved *faster*, including
`BenchmarkFasthttpBaseline` (−3.88%), `BenchmarkNoRecoverBaseline` (−4.77%) and
`BenchmarkMapLookup10` (−3.11%) — three benchmarks with no pooled `Ctx` anywhere near them. The
one benchmark in the whole file that `benchstat` does not call a change, `MapLookup100` at
p=0.101, is also the only one that moved the other way at all. The host OS version changed
between the two sessions, which is
a candidate explanation and the reason every cross-session figure in this document carries a
caveat, but "the OS was upgraded" is a correlation, not a mechanism: nothing here identifies what
in the upgrade made an allocation-free map lookup 3% faster. The practical consequence is already
acted on — `BenchmarkDispatchPooledVsUnpooled` exists so the milestone's central claim never
depends on a cross-session comparison — but the underlying question is the same one M5 left open
and it is still open.

Second, smaller: `BenchmarkDispatchParallel` reads 4.461 ns ± 10%, against 33.27 ns for the same
dispatch single-goroutine. A factor of roughly 7.5 on a 12-thread machine with `sync.Pool`'s
per-P caches is plausible on its face, and it is also the only benchmark in the file with
double-digit variance. Whether that is genuine per-P scaling, or `b.RunParallel` measuring
something subtly different from what the serial benchmark measures, is not established here. The
number is recorded as an observation about the parallel path rather than quoted as a throughput
claim anywhere.
