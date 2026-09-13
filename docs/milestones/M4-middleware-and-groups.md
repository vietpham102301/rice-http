# M4 — Middleware and groups

Status: done
Started: 2026-09-11
Finished: 2026-09-12

## Goal

`Middleware` exists, it is `func(next Handler) Handler`, and every route's chain is folded
into a single `Handler` before the socket opens. `internal/chain.Compile` is the six-line
generic fold that does the folding and the one place the direction is written down.
`App.Build` runs that fold once under `sync.Once`, discards the trees registration built and
inserts the compiled handlers into fresh ones; `Run`, `Serve` and `FasthttpHandler` all
trigger it, and registering or calling `Use` afterwards panics. `Group` carries a prefix and
its own middleware and a pointer to its parent — not a copy of the parent's list — so
`parent.Use(auth)` written after a child group was created still reaches the child's routes.
Order is application, then outer group, then inner group, then route, then handler, and the
unwind is the reverse.

What exists at the end of M4 that did not before: middleware and groups, and the number that
says what a compiled chain costs a request.

## Question this milestone answers

Does compiling chains at build time actually remove the per-request cost, or does the closure
indirection eat the gain?

**It removes it.** Five middleware add **zero** allocations and roughly **4.6 ns** to a
dispatch of about 86 ns. The design predicted zero cost, which was always going to be
slightly too strong — five extra indirect calls are not literally free — and what the
measurement found instead is a cost small enough that the prediction was right in every way
that matters.

Getting to that number took three readings of the cost, two of them wrong, and then a fourth
correction to the explanation of why. The record has to say so, because the wrong ones were
confident.

**Reading one (the first task report):** 11.57 ns for five middleware, "about 2.3 ns per
call" — the difference of the ten-run means, 95.66 − 84.09, divided flat across five calls.

**Reading two (mine, as controller, and the task reviewer agreed):** not a per-call average
but a step — a jump at the first middleware and then nothing. `ChainDispatch1`'s recorded
median of 91.71 sits 7.70 ns above `ChainDispatch0`'s 84.01, while `ChainDispatch5` adds only
another 0.97. That is a clean, tidy shape and it is wrong.

**Reading three, which is the one that holds:** measurement caught both of us. Two further
experiments were run after the correction had already landed.

A sweep at 0, 1, 2 and 10 middleware put **zero measuring slower than one** — 94.9 against
87.8. That cannot be a real effect, and it is what first showed the 0-versus-1 comparison was
unstable rather than informative.

Then eight counts of exactly the three committed benchmarks, same harness, same binary:

| Benchmark | eight-count median | step |
| --- | ---: | --- |
| `ChainDispatch0` | ~86.6 | — |
| `ChainDispatch1` | ~86.3 | 0 → 1 is about **−0.3 ns**, which is nothing |
| `ChainDispatch5` | ~91.2 | 1 → 5 is **+4.9 ns** for four more, about 1.2 ns each |

The sweep's 1-to-10 leg agreed independently at roughly 1.1 ns each. So the shape is a
**slope of about 1.1 ns per middleware, roughly linear** — not a step at all.

The 0 → 1 leg does not contradict that slope, though it looks like it might: −0.3 ns against
an expected +1.1 is a 1.4 ns disagreement, and the run-to-run spread on these benchmarks is
several times that. The honest statement is that **the 0 → 1 leg is flat within noise**, and
that the slope is measured over the legs long enough to carry a signal — 1 → 5 and 1 → 10 —
rather than over a single-middleware difference that no experiment here can resolve. Two
attempts to read a shape off that one leg produced two wrong answers, which is the practical
argument for not trying a third time.

**Why reading two was wrong, named rather than buried, and not by the explanation I first
reached for.** `ChainDispatch1`'s ten recorded samples span 84.20 to 120.10 ns/op, and the
obvious diagnosis is that the single 120.10 outlier dragged the median up. It did not — that
was a fourth wrong reading, caught by arithmetic while writing this section. Deleting the
120.10 sample moves `ChainDispatch1`'s median from 91.71 only to 90.94. A median is supposed
to survive one bad sample, and it does.

What actually happened is visible when the ten samples are read **in the order they were
recorded** rather than sorted:

    ChainDispatch1   85.38  84.20  85.66  92.48  88.19  90.94  99.31  120.10  114.40  96.98
    ChainDispatch5   98.13 105.40 102.50  89.78 103.20  94.06  91.03   90.06   91.11   91.29

`ChainDispatch1` drifts upward across its run — its first three samples average 85.08, right
on top of `ChainDispatch0`'s 84.01, and its last five average 104.35. `ChainDispatch5`, which
`bench-record` runs immediately afterwards, drifts the other way: 99.80 for its first five,
91.51 for its last five. That is one contiguous slow window in the recording session
straddling the boundary between the two benchmarks, and it inflates both medians — which is
also why the recorded `5` − `0` delta of +8.67 ns is nearly twice the +4.6 ns a dedicated
re-run finds. `ChainDispatch0`, recorded before the excursion, is flat across its own run
(83.59 then 84.58) and unaffected.

So the step was an artifact of *when* each benchmark ran, not of what it measured, and the
statistic that would have caught it is not a bigger `-count` but reading the samples in
recording order. A median and a range hid a drift that ten numbers in sequence show at a
glance.

The recorded file is **not** being re-run and the drift stays in it. It is real data from a
real recording, the same `bench-record` output every other milestone commits, and naming it is
worth more than hiding it behind a cleaner second recording. The first report's per-call
instinct had the right shape and roughly double the right magnitude. My correction had the
wrong shape entirely, and it was the more confident of the two.

**A measured negative result on the mechanism.** One explanation was offered for the step and
then refuted rather than dropped quietly. The hypothesis was that `Compile` returning the
handler unwrapped for an empty slice lets the dispatch call site inline at zero middleware and
not at one. `go build -gcflags=-m` says otherwise:

    cannot inline (*App).handle: function too complex: cost 390 exceeds budget 80

and there is no `inlining call to h` line anywhere in the output for any package. `h` in
`handle` is a `Handler` value read out of the tree at run time, so `h(c)` compiles to an
indirect call identically whether the tree holds a bare handler or a five-deep chain.
Inlining is decided once, over source text; it cannot vary with a runtime configuration
value like middleware count. The difference is not inlining.

**The slope's mechanism is left unexplained, deliberately.** Each additional middleware is one
more indirect call in a chain of tail-position calls, and roughly a nanosecond per indirect
call is unremarkable on this hardware — but nobody measured branch prediction, took a counter
reading, or read the generated assembly. That is a plausible account, not a finding, and this
project has spent M2, M3 and M4 removing claims that had nothing behind them. It goes in the
"do not understand" section rather than getting dressed up as a cause.

## Design notes

**Groups cost nothing at request time, and the two ways of reading the data disagree about the
sign — which is the point.** `ChainDispatch5Grouped` sends the same five middleware through
three nested `Group` calls instead of five flat `Use` calls. By recorded median it is 1.18 ns
*slower* than `ChainDispatch5`; by mean of the same ten runs it is 0.18 ns *faster*. Two
summaries of one recording pointing in opposite directions is what a null result looks like,
and it is better evidence than either figure alone would have been. The design says a group
exists only at registration time — `build` walks the route's group chain, flattens it into one
slice, and folds it; what dispatch finds in the tree is a `Handler` with no memory of how it
was assembled. The numbers agree.

**The per-request allocation is still one, and M4 is not why it is 352 bytes.** Every
`ChainDispatch` variant reports 1 alloc/op and 352 B/op, identical to `RiceDispatch`. That
flat line is not M4's achievement to claim: M3 grew the `Ctx` from 16 bytes to 344 (352 as the
allocator rounds it), and M4 adds no field to it, no per-request state, and no cursor —
which is the whole reason ADR-0003 chose the decorator over an index walk. M4's contribution
to that row is having changed nothing about it, and M3's cost is not restated here as though
it were new. `alloc_test.go` pins it: `App.handle` with 0 middleware is 1 allocation, with 5
middleware is 1 allocation, and a compiled five-middleware chain called directly is 0.

**What inserting every route twice cost: about 239 microseconds per 1000 routes, and the
benchmark measures one pass, not two.** `BenchmarkBuild1000Routes` times `Build` on an app
whose routes are already registered, so registration's own insertion sits outside the timer.
Stating that explicitly matters, because the obvious reading of "what did inserting twice
cost" is a figure covering both insertions and this is not that figure.

It is the right number anyway. D1's alternatives all insert once somewhere: the rejected
"store `*route` in the tree and assign the compiled handler through the pointer" design
inserts at registration and does no second pass; the rejected "defer insertion to `build`
entirely" design inserts only at build. Either way what D1 bought is exactly one extra pass
over the route set, and `BenchmarkBuild1000Routes` is a measurement of a pass — fresh trees,
a chain folded per route, every route inserted again. 239 µs for a thousand routes is 239 ns
per route, paid once, before the socket opens, in exchange for a duplicate route or a
parameter-name conflict panicking from the `app.GET` line that wrote it rather than from
inside `Run`.

Its 7503 allocations per operation decompose exactly, measured with a throwaway
`runtime.MemStats.Mallocs` diagnostic around each build stage rather than attributed by
guesswork:

| Stage | allocations per route |
| --- | ---: |
| `middlewareFor`'s exactly-sized slice | 1.000 |
| `chain.Compile` wrapping 3 middleware, one closure per wrap | 3.000 |
| `router.Tree.Insert` building radix nodes for 1000 distinct paths | 3.503 |
| **Total** | **7.503** |

1.000 + 3.000 + 3.503 = 7.503, against a recorded 7503 allocs/op for 1000 routes. The tree's
share is not an integer because a radix insert allocates more when it splits an existing edge
and less when it extends one cleanly, and `/route/0` through `/route/999` do both. The
diagnostic was not committed: it measures build stages in isolation, which is not something
`make bench` should carry.

The spec's second open question — whether a very large route set makes startup noticeably
slower — stays deferred, with the reason now recorded rather than shrugged at. At 239 ns per
route a hundred-thousand-route application would spend about 24 ms in `Build`, but that is a
linear extrapolation of a structure whose insert cost is not linear in route count, and
nobody measured it. It is deferred because 0.24 ms for a realistic route set is not worth
optimising, not because the extrapolation is trustworthy.

**`middlewareFor` returns `nil` for a route with no middleware at all**, so an application
that uses none allocates nothing for chains at build either. `Compile` over an empty slice
returns the handler itself, unwrapped, which D5 states as a requirement — an identity wrapper
would behave identically and cost a call per request forever. The requirement turns out to be
enforced by the signature rather than by vigilance: inside `Compile[H any, M ~func(H) H]` the
handler is an opaque `H`, so it cannot be called and no wrapper around it can be constructed.
`return h`, a zero value, or the result of applying an `M` are the only things the body can
produce. `TestCompileWithNoMiddlewareReturnsTheHandlerItself` now compares code pointers
through `reflect.Value.Pointer` — not a general function-identity check, since `Pointer`
cannot distinguish two closures built from the same function literal, but enough here because
both sides are the same handler value — which pins the property that *is* live — that the
empty case returns the handler rather than a zero value — and
`TestCompileWithMiddlewareWrapsTheHandler` pins its complement, that a non-empty slice is not
silently dropped.

**Four places copy a caller's variadic slice, and all four are now guarded.** `App.Use`,
`App.register`, `App.Group` and `Group.Group` each do `append([]Middleware(nil), mw...)`.
D3 predicted the hazard in the abstract — a group's list must not alias one another group can
grow — and the concrete form it takes is a caller who keeps using the slice they passed in.
Each copy has a test that mutates the caller's slice after registration and asserts the
mutation does not reach the framework, and each was proved by breaking the copy and watching
the test go red. See the retrospective: the first two versions of those tests could not have
gone red at all.

Nothing here had a real alternative worth promoting to an ADR. ADR-0003 fixed the shape and
the build phase in advance and M4 is that commitment carried out; D1 through D8 in the
[design doc](../superpowers/specs/2026-09-11-m4-middleware-and-groups-design.md) record the
decisions taken below that level, and none of them reopened.

## Exit criteria

- [x] Tests asserting exact execution order for nested groups including the unwind order —
      `TestThreeLevelOrder`, `TestUnwindOrderIsTheReverseOfEntryOrder`,
      `TestGroupMiddlewareRunsInsideApplicationMiddleware`,
      `TestRouteMiddlewareRunsInsideApplicationMiddleware`, plus `TestCompileOrder` and
      `TestCompileFiveMiddleware` in `internal/chain` with no HTTP anywhere near them
- [x] A test that `Use` after route registration still applies —
      `TestUseAfterRegistrationStillApplies`, and its group-level counterpart
      `TestParentUseAfterChildCreationStillApplies`, which is D3's reason for existing
- [x] `AllocsPerRun` asserts no additional allocations for a five-middleware chain, with each
      budget asserting its middleware actually ran before measuring —
      `TestAllocBudgetDispatchNoMiddleware` (1), `TestAllocBudgetDispatchFiveMiddleware` (1)
      and `TestAllocBudgetChainCompile` (0), each gated on a `chainSink` count the compiler
      cannot discard, and the five-middleware budget proved to fail when `middlewareFor` is
      broken
- [x] Short-circuiting, an error through the funnel, and middleware reading route parameters —
      `TestMiddlewareCanShortCircuit`, `TestMiddlewareErrorReachesTheErrorFunnel`,
      `TestMiddlewareSeesAnErrorFromTheHandler`, `TestMiddlewareCanReadRouteParameters`
- [x] A grouped, middlewared route answered over a real socket —
      `TestServeAnswersAGroupedMiddlewaredRoute`
- [x] Benchmarks recorded in
      [`bench/results/M4-middleware-and-groups.txt`](../../bench/results/M4-middleware-and-groups.txt),
      with `ChainDispatch5` reported against `ChainDispatch0`, and the M3 suite re-run in the
      same recording so the whole table stays comparable on one machine
- [x] Both `Chain call` rows in [`05-performance-model.md`](../05-performance-model.md) moved
      from `TARGET (M4)` to `MEASURED M4`; no other row touched
- [x] 160 tests pass under `-race` — 105 in `rice`, 47 in `internal/router`, 8 in
      `internal/chain`
- [x] Retrospective section below filled in
- [x] Journal entry appended to [`progress.md`](../progress.md)

## Measurements

| Case | Time | Allocs | Bytes |
| --- | --- | --- | --- |
| `BenchmarkChainDispatch0` — dispatch, no middleware (range 83.00–85.60) | 84.01 ns/op | 1 | 352 |
| `BenchmarkChainDispatch1` — one middleware (range 84.20–**120.10**) | 91.71 ns/op | 1 | 352 |
| `BenchmarkChainDispatch5` — five middleware (range 89.78–105.40) | 92.68 ns/op | 1 | 352 |
| `BenchmarkChainDispatch5Grouped` — the same five through nested groups (range 91.04–101.50) | 93.86 ns/op | 1 | 352 |
| `BenchmarkBuild1000Routes` — the build pass, 1000 routes, 3 app middleware | ~239 µs/op | 7503 | 265001 |
| **Five middleware, recorded medians (`5` − `0`)** | **+8.67 ns/op** | **0** | **0** |
| **Five middleware, eight-count re-run (~91.2 − ~86.6)** | **+4.6 ns/op** | **0** | **0** |
| **Groups at request time (`5Grouped` − `5`), by median** | **+1.18 ns/op** | **0** | **0** |
| **Groups at request time (`5Grouped` − `5`), by mean** | **−0.18 ns/op** | **0** | **0** |

All figures except the eight-count row are medians of the ten runs in one recording. The two
"five middleware" rows are both correct and they are not the same quantity: +8.67 is what the
committed recording's medians say, and it is inflated by the slow window described above,
which straddles the end of `ChainDispatch1` and the start of `ChainDispatch5`; +4.6 is what a
dedicated eight-count re-run of the same three benchmarks on the same binary says, and it is
the figure consistent with the ~1.1 ns/middleware slope the 0/1/2/10 sweep found
independently. Neither is quoted alone anywhere in this document.

The two "groups" rows are the same disagreement in miniature and are reported both ways for
the same reason: a difference whose sign flips between median and mean, on a series spanning
ten nanoseconds, is zero.

Hardware, Go version, OS: Apple M2 Pro, go1.25.6 darwin/arm64, Darwin 25.6.0 arm64.
Recorded 2026-09-12T04:52:35Z.
Raw output: [`bench/results/M4-middleware-and-groups.txt`](../../bench/results/M4-middleware-and-groups.txt).

Allocation budgets are additionally enforced as tests in `alloc_test.go` via
`testing.AllocsPerRun`, so a regression fails CI rather than merely looking worse in a
benchmark. The three M4 budgets each assert that every middleware in the chain actually ran,
through a counter the compiler cannot elide, *before* measuring anything — a budget over a
chain of middleware that do nothing measures a chain the optimiser may have removed.

### What M4 did not change

The M3 benchmarks were re-run in the same recording. `RiceDispatch` is 81.89 ns/op here
against 79.70 in M3, `FasthttpBaseline` 11.39 against 10.92, `TreeLookup1000` 114.3 against
109.40. Everything drifted up by a couple of percent, the floor included, and no M4 code runs
in any of those paths. This is session-to-session drift, not a regression, and it is recorded
so that the next milestone reading these files does not attribute it to middleware.

## Retrospective

**What surprised me:**

*The wrong reading was the confident one.* Three accounts of the same five-middleware cost
were written in sequence. The first, a flat per-call average, was roughly right in shape and
about double in magnitude. The second — mine — replaced it with a crisp step at the first
middleware, was reviewed and agreed with, and was wrong in shape as well as magnitude. The
third came from running the benchmark eight more times. What separates them is not care in
reading, since all three were careful; it is that only the third involved new data. A
correction derived from the same numbers that produced the error inherits the error, and this
one did.

*And then the explanation of the error was itself wrong, twice over.* Both the task report and
I settled on "a single 120.10 ns outlier dragged `ChainDispatch1`'s median up seven
nanoseconds", which is tidy, plausible, and false: deleting that sample moves the median
0.77 ns. A median resists one bad point, which is the entire reason this project reports
medians. Only sorting the ten samples back into recording order showed the real cause — a
contiguous slow window in the session that inflated the tail of `ChainDispatch1` and the head
of `ChainDispatch5` together. Four readings of one small number, three of them wrong, and the
first thing every wrong one had in common is that it was derived rather than measured.

*The statistic that would have caught it was free and nobody looked at it.* The committed file
lists every sample in the order it was produced. No new run, no new tool and no extra
`-count` was needed — only reading down the column instead of summarising it. Ranges next to
medians, which is what the table above now carries, are a weaker version of the same idea and
would also have raised the alarm.

*The milestone's headline claim was the one nobody had to argue about.* Zero additional
allocations for five middleware was true on the first run, needed no code change, and is the
part of the design that ADR-0003 actually staked itself on. All of the difficulty was in the
nanoseconds — a quantity small enough that three different people could read three different
shapes into it.

*Groups turned out to be free in a way that shows up as an argument between the median and
the mean.* Wanting them to be free, and having a number that said so, would have been easy to
report as a clean zero. Reporting that the median says +1.18 and the mean says −0.18 is less
tidy and is the actual evidence.

**What I would do differently:**

I would re-run before correcting. Reading two cost a task report revision, a review round and
a second revision, all because a correction was derived from the dataset that produced the
error instead of from new samples. Eight extra counts of three benchmarks took a few minutes.

I would also look at the samples in recording order before summarising them at all. Every
wrong reading in this milestone came from a summary — a mean, then two medians — of a series
whose ordering was the informative part and was thrown away first. Ranges alongside medians,
which the table above now carries, are the cheap version of that habit and belong in the task
report as well as here.

**The lesson this milestone earned is still about guards, and the count is nine.** Across
M2, M3 and M4, nine separate times something named as a guard turned out not to guard what it
claimed. M3's retrospective enumerated five across M2 and M3; the M4 design doc found a sixth
while writing its allocation-budget section, which is why that section carries a warning about
budgets over middleware that do nothing. M4's implementation and review found the remaining
three — five plus one plus three is the nine:

1. `TestUseDoesNotAliasTheCallersSlice` mutated the caller's slice with an `append`. When a
   variadic slice is kept by reference the callee's header freezes at the length it had at the
   call, so a later `append` writes past that length into memory the alias never reads. The
   test passes identically whether the copy exists or not — it cannot detect aliasing at all.
   A task reviewer had approved it as exercising exactly the trap it is blind to.
2. The Task 3 brief specified the same append-based shape for `Group`'s guard, for the same
   reason, and it would have shipped a second blind test beside the first.
3. `App.built`'s comment claimed a `sync.Once` happens-before guarantee. `sync.Once` orders
   goroutines that both call `Do`; `register` never calls `Do` and gets nothing from it. The
   comment now states the argument that does hold — that registration is a single-goroutine
   setup phase, and a program registering concurrently is already racing on the trees, so this
   unlocked read is not the weak link.

**A tenth was claimed while this document was being written, and it was a false alarm — which
is the more interesting finding.** `internal/chain`'s
`TestCompileWithNoMiddlewareReturnsTheHandlerItself` contained a genuinely dead branch,
`if &got == &h { t.Skip(...) }`, comparing the addresses of two distinct local variables and
therefore never true. A task review had flagged it as a Minor and the controller had deferred
it. That deferral looked harmless and was not: a dead branch sitting beside a live one invites
the reading that the live one is doing more than it is, and this document's author and the
controller in turn both concluded the test could not detect a wrapping `Compile`.

Both of us "confirmed" it by fault injection. Both injections were `M(func(x H) H { return x })`
— an identity middleware, which returns the handler unchanged and wraps nothing. **We each ran
an experiment that could not fail and read its passing as evidence.**

A `Compile` that genuinely wraps an empty chain turns out not to be expressible. Inside
`Compile[H any, M ~func(H) H]`, `H` is opaque: `return M(func(next H) H { return next(h) })(h)`
does not compile —

    invalid operation: cannot call next (variable of type H constrained by any):
    no specific type

— and neither does assigning a closure to an `H`. The body can return `h`, a zero value, or
the result of applying an `M`, and nothing else. So the property the test is named for is
enforced by the generic signature, not by the test, and the test's live assertion guards a
real if smaller thing: that the empty case returns the handler rather than a zero value.

The rewrite landed anyway and improved the suite: the dead branch is gone, identity is now
compared through `reflect.Value.Pointer`, and a new
`TestCompileWithMiddlewareWrapsTheHandler` guards the complement — confirmed red when
`Compile` drops its middleware, reporting "Compile returned the original handler instead of
wrapping it; middleware was silently dropped". A false alarm that leaves the suite stronger
is a cheap outcome; the reasoning that produced it is the expensive part.

The lesson is the milestone's own discipline turned on itself. **An experiment that cannot
fail proves nothing, and it looks exactly like an experiment that passed.** That is precisely
the failure mode of a guard that cannot fail, one level up — in the fault injection meant to
validate the guard rather than in the guard. "Break it and watch it go red" is only worth
anything if the break is real, and neither of us checked that ours was. The count stays at
nine.

None of the nine was found by a guard failing. What closed M4's three was the same habit
M3 arrived at: break the guarded property on purpose and confirm the guard goes red. The
append-based aliasing tests were rewritten to overwrite `callerSlice[0]` in place — an index
inside the alias's own length, which is the only mutation an alias can observe — and each of
the four copy sites was then broken to `mws: mw` and watched to fail. So was the
five-middleware allocation budget, by making `middlewareFor` return only the app-level list;
it failed on its precondition, reporting "3 middleware ran, want 5", rather than quietly
measuring a shorter chain.

The generalisation worth keeping is narrow. A guard is not a test that describes a property;
it is a test that has been observed to fail when the property is violated. Everything else is
a comment with a `func` keyword. Nine for nine, the ones that turned out to be comments were
written by people who believed they were writing guards, and one of them had been through
review, which is why reading them harder was never going to be the fix. The false alarm above
extends the rule rather than denting it: the observation has to be of a real break, and
"I broke it and it still passed" is a claim that itself needs checking.

**The final whole-branch review found one more mismatch, of a different shape: not a guard
that could not fail, but a design claim the code had already outgrown.** D4 said "There is no
way to register the group's bare prefix from inside the group; register it on the `App`" —
but `g.GET("")` already registered the bare prefix, carrying the group's own middleware, and
had done so since groups were added. The review's fix was not to make the sentence true by
outlawing the empty path; `g.GET("")` is the only way to give a group's own root the group's
middleware, which is worth keeping. Instead the design doc was corrected to say what the code
does, and the review's real finding — that the prefix's leading `/` was doing all the work and
the path was never checked, so `g.GET("users")` joined onto `/api` into `/apiusers` silently —
became the path validation D4 was missing.

**And the scoped re-review of that fix found the pattern once more, in the note explaining
the fix.** The comment added to `chain_test.go` justified its `reflect.Value.Pointer`
comparison by saying both call sites compare a handler value against itself. That is true of
the equality test and false of the inequality one, which compares the handler against
`Compile`'s wrapper; that site is sound for a different reason, because the two closures come
from different function literals. The guard held either way — only its stated rationale was
wrong. Worth recording because it is the same failure one layer out from where this milestone
kept finding it: first tests that named a property they could not detect, then a fault
injection that injected no fault, then a design sentence the code had outgrown, and now an
explanation that was right about the conclusion and wrong about half the reason. Each was
caught by someone checking the claim against the thing rather than reading the claim.

**What I still do not understand:**

Why the chain costs about 1.1 ns per middleware. Inlining is ruled out — measured, with
`-gcflags=-m`, and recorded above as a negative result rather than a dropped hypothesis. One
more indirect call per middleware is the obvious account and roughly a nanosecond per indirect
call is unremarkable on an M2 Pro, but no counter reading, no profile and no assembly
inspection stands behind it. It is bounded, reproducible and reproduced in two independent
experiments; it is not explained. The project's habit of recording unexplained numbers rather
than chasing them continues, and this is the fourth entry on that list after M1's 43 ns header
write, M2's ~9 ns of routing cost and M3's 15.93 ns of tree scaling.

Whether the ~1.1 ns slope is even the right model beyond ten middleware. The sweep went to
ten. Nothing in these measurements says what happens at fifty, and a chain deep enough to
matter for instruction cache or call-depth effects is not in the suite. Nobody has a
reason to expect a cliff; nobody has looked either.

What the machine was doing during the slow window, and where the couple of percent of drift
between the M3 and M4 recordings comes from. These are the same question at two scales: a
twenty-second excursion inside one recording, and a two-percent offset between two recordings
whose floor moved with everything else. Thermal throttling and background load are the usual
suspects and neither was observed. "The machine was slightly different" is a description, not
a cause, and it is exactly why `bench/results/README.md` warns against comparing across
stamps — a warning this milestone can now say it has felt from the inside.
