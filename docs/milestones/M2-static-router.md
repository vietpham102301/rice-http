# M2 — Static router

Status: done
Started: 2026-09-06
Finished: 2026-09-06

## Goal

rice routes. `App.SetHandler` is gone; in its place are seven verb helpers and a general
`Handle`, each backed by an `internal/router.Tree[Handler]` that is deliberately a
`map[string]Handler` and nothing cleverer. Common verbs reach their tree through a fixed
array indexed by a `method` constant, so dispatch on `GET` is an array index rather than a
string comparison; uncommon verbs fall back to a lazily created map that stays nil for
applications that never register one. A request whose path or verb has no handler produces
`ErrNotFound`, which travels through the same error funnel that a failing handler uses, and
comes back as a 404 with a plaintext body.

The router is exact-match only. No parameters, no wildcards, no trailing-slash handling, no
405. Those belong to M3 and later. What exists at the end of M2 that did not exist before is
a number.

## Question this milestone answers

How bad is a map, really?

Not bad at all, and that is the finding. Lookup cost is flat: 38.52, 38.37 and 38.34 ns/op
at 10, 100 and 1000 registered routes. A hundredfold increase in route count moves the
figure by 0.18 ns, which is inside the run-to-run noise of the recording. The map does not
care how many routes exist, because hashing a short string does not care either.

The design doc predicted exactly this, and predicted its consequence:

> The honest expectation is that it will not be bad at all. Hashing a short string is close
> to flat in the number of routes, so the map may well beat a radix tree on purely static
> paths. If that is what the numbers say, it is a finding rather than a failure: it would
> mean the radix tree earns its keep on parameters and shared prefixes, not on static
> lookup, and M3 should be judged on those cases.

The prediction held, in both halves. M3 now has a harder and narrower job than the roadmap
implied: on purely static routes the tree is not competing against something slow, it is
competing against a flat 38 ns that does not degrade. If the tree wins there at all it will
win by a small constant, and the honest expectation is that it will lose. Its case has to be
made on parameters, wildcards and shared prefixes — the things the map cannot do at any
price — rather than on beating this number.

## Design notes

**Routing costs about 12 ns, and that is stated rather than buried.** M1 recorded
`BenchmarkRiceDispatch` at 25.49 ns/op against a baseline of 11.05. M2 records the same
benchmark at 37.91 against a baseline of 11.24. Correcting for the 0.19 ns of baseline drift
between the two recordings, adding a router cost roughly **12.2 ns per request**. Framework
overhead over bare fasthttp is now +26.67 ns/op and still exactly one 16-byte allocation.
That is a real, measurable regression against M1's dispatch number and it is the price of
having routes at all; nothing in this milestone is free.

**The lookup itself does not scale with route count, so all of that 12 ns is constant
cost.** `BenchmarkRiceDispatch` registers one route and reports 37.91; `BenchmarkStaticRouterLookup1000`
registers a thousand and reports 38.34. Between one route and a thousand the difference is
0.43 ns, and the two benchmarks use paths of different lengths (`/hello` against
`/route/500`), so even that is not cleanly attributable to route count. Whatever routing
costs, it is paid on the first route and never again.

**`Lookup` is written on one line for a reason, and a test guards it.** The map probe is
`t.routes[string(path)]` as a single expression, which the compiler special-cases so that
the `[]byte` is not copied. Hoisting the conversion into a local — `key := string(path)` —
loses the optimisation and costs one allocation on every request. That regression would be
invisible to every functional test in the package, so `alloc_test.go` asserts zero
allocations for lookup directly. This is the same class of hazard as M0's `go mod tidy`
finding: a change that is obviously harmless and quietly is not.

**The miss path costs zero allocations, and that is an accident.** See the retrospective
below. It is recorded here because the caveat is load-bearing for M5: the zero is produced
by escape analysis, not by design, and M5 is expected to take it away.

**`methodIndex` switches on length before comparing any byte.** Seven verbs across five
distinct lengths means most requests are separated before a single byte is examined, and
each comparison is written as `string(m) == "LITERAL"`, a form the compiler evaluates
without copying. `TestAllocBudgetMethodIndex` pins that down rather than trusting it.

**Registration panics; lookup does not.** `Handle` panics on an empty path, a path without a
leading slash, a nil handler, or a duplicate route. All four are programmer errors
discovered at startup, and a duplicate in particular means one of two registered handlers can
never run — silent, and expensive to debug. `Tree.Insert` returns `ErrDuplicate` rather than
panicking itself, so the decision about whether a duplicate is fatal stays with the caller
that has the context to make it.

None of these had a real alternative worth promoting to an ADR. ADR-0004 already commits to
the per-method tree; M2 is that commitment with the interesting part deferred.

## Exit criteria

- [x] Routing tests cover hit, miss and wrong-method, including
      `TestHandleReturns404ForTheRightPathUnderTheWrongVerb` and a property test that
      `Lookup` does not retain its `[]byte` argument — 53 tests across the two packages,
      passing under `-race`
- [x] Benchmark recorded in [`bench/results/M2-static-router.txt`](../../bench/results/M2-static-router.txt),
      with lookup cost at 10, 100 and 1000 routes plus miss and wrong-verb
- [x] Allocation budgets in [`05-performance-model.md`](../05-performance-model.md) updated
      from TARGET to measured — `Router lookup, static route` is now `MEASURED M2`
- [x] Retrospective section below filled in
- [x] Journal entry appended to [`progress.md`](../progress.md)

## Measurements

| Case | Time | Allocs | Bytes |
| --- | --- | --- | --- |
| `BenchmarkFasthttpBaseline` — no framework, the zero point | 11.24 ns/op | 0 | 0 |
| `BenchmarkRiceDispatch` — full dispatch, one route registered | 37.91 ns/op | 1 | 16 |
| `BenchmarkRiceDispatchNotFound` — no routes registered at all | 33.15 ns/op | 0 | 0 |
| `BenchmarkCtxSetHeader` — dispatch plus one response header | 80.59 ns/op | 1 | 16 |
| `BenchmarkStaticRouterLookup10` — 10 routes registered | 38.52 ns/op | 1 | 16 |
| `BenchmarkStaticRouterLookup100` — 100 routes registered | 38.37 ns/op | 1 | 16 |
| `BenchmarkStaticRouterLookup1000` — 1000 routes registered | 38.34 ns/op | 1 | 16 |
| `BenchmarkStaticRouterMiss` — unregistered path, 1000 routes | 36.45 ns/op | 0 | 0 |
| `BenchmarkStaticRouterWrongVerb` — registered path, wrong verb | 33.28 ns/op | 0 | 0 |
| **Framework cost (Dispatch − Baseline)** | **+26.67 ns/op** | **+1** | **+16** |
| **Routing cost (M2 Dispatch − M1 Dispatch, baseline-adjusted)** | **+12.2 ns/op** | **0** | **0** |
| **Lookup scaling (1000 routes − 10 routes)** | **−0.18 ns/op** | **0** | **0** |

All figures are medians of ten runs from a single recording, so the baseline is measured on
the same machine in the same session as the rice numbers rather than compared across stamps.
The M1 figures quoted above come from a different recording and are used only for the
baseline-adjusted delta, which is why the adjustment is stated rather than skipped.

Hardware, Go version, OS: Apple M2 Pro, go1.25.6 darwin/arm64, Darwin 25.6.0 arm64.
Recorded 2026-09-06T06:41:03Z.
Raw output: [`bench/results/M2-static-router.txt`](../../bench/results/M2-static-router.txt).

Allocation budgets are additionally enforced as tests in `alloc_test.go` via
`testing.AllocsPerRun`, so a regression fails CI rather than merely looking worse in a
benchmark.

## Retrospective

**What surprised me:**

*The 404 path costs zero allocations, and nobody designed that.* M1's retrospective
complained that the miss path allocated a `Ctx` nobody read, and D6 in the design doc said
M2 would move the lookup above the allocation but that the miss path "still allocates one
`Ctx`". Both benchmarks say otherwise: `BenchmarkRiceDispatchNotFound` and
`BenchmarkStaticRouterMiss` report 0 B/op and 0 allocs/op. `go build -gcflags=-m` explains
it — `&Ctx{} does not escape` on the miss path, `&Ctx{} escapes to heap` on the hit path.
The miss-path `Ctx` is passed only to `handleError`, a concrete method the compiler can see
through, so it lands on the stack; the hit-path `Ctx` goes through `h(c)`, an indirect call
through a `Handler` function value that the compiler cannot analyse and must assume escapes.
The `Ctx` is still constructed in both branches. Only one of them pays for it.

The caveat matters more than the win, and it is why this is written down twice. **This zero
is a compiler artifact, not a design guarantee, and M5 will probably take it away.** M5
replaces `handleError` with a configurable `ErrorHandler`. The moment the funnel dispatches
through a function value instead of a concrete method, the miss-path `Ctx` escapes for
exactly the reason the hit-path one does, and the 404 path returns to one allocation. That
will be correct behaviour and expected behaviour. It must not be read as a regression when
it happens, and D6 in the design doc now carries the same warning so it cannot be lost.

*The flatness is more total than "close to flat" suggested.* The design doc hedged with
"close to". The measured spread across a hundredfold change in route count is 0.18 ns, and
it points the wrong way — a thousand routes are nominally *faster* than ten, which only means
the difference is noise. I expected a small but visible slope from cache behaviour on a
larger bucket array. There is none at this scale.

*`BenchmarkRiceDispatchNotFound` got 19 ns slower than M1 and it is not a regression.* M1
recorded 13.96 ns/op for this benchmark; M2 records 33.15. The two are measuring different
work. M1's 404 wrote a status code and returned. M2's 404 goes through the error funnel,
which calls `ResetBody`, sets the status, sets a content type and writes a `Not Found` body.
The number moved because the behaviour improved, and it took a minute of staring to be sure
of that rather than assuming it. Cross-milestone benchmark comparison is only meaningful
when the benchmark still means the same thing, and this one does not.

*`BenchmarkStaticRouterMiss` costs 3.17 ns more than `BenchmarkStaticRouterWrongVerb`.* Both
are 404s and both allocate nothing, but the miss actually probes a thousand-entry map before
failing, while the wrong-verb request indexes to `POST`'s tree, finds a nil map, and returns
immediately. That 3.17 ns is roughly what a real map probe costs on this machine, arrived at
by subtraction. It is the closest thing M2 produced to an isolated measurement of the map,
and it was a side effect of a benchmark written for a different reason.

**What I would do differently:**

I would have written the escape-analysis check before the benchmark rather than after it.
The zero-allocation 404 was discovered by noticing an unexpected `0 allocs/op` in a results
file and then going to look for the cause. `go build -gcflags=-m` takes two seconds, and
running it while writing `handle` would have turned a surprise into a design note — and,
more usefully, would have caught the false claim in D6 before it was committed rather than
after.

I would also have added a benchmark that measures `App.lookup` alone. Every routing number
in the table is a full dispatch, because that is the smallest thing package `bench` can
reach through the exported API. It answers the milestone's question — the scaling term is
isolated because everything else is constant across `n` — but it means the ~12 ns of routing
cost cannot be decomposed. That is the gap the next section is about.

**What I still do not understand:**

Why routing costs ~12 ns when the lookup is one array index and one map probe. The
subtraction above suggests a probe is worth about 3 ns, which leaves roughly 9 ns
unaccounted for across `methodIndex`, the array index, the extra function call, and whatever
the compiler stopped inlining when `handle` grew a branch. Nine nanoseconds is around
thirty cycles, which is too much to wave away as "call overhead" and too little to guess at.
I have not profiled it. This is the same failure M1 recorded about `SetHeader` costing
43 ns — a number in a results file with no explanation behind it — and it is the second
milestone in a row where I have chosen to record an unexplained figure rather than chase it.
The mitigation is that the figure is at least bounded and reproducible; the honest position
is that the project's own rule about not recording numbers nobody can explain has now been
bent twice.

Whether the M5 prediction actually plays out. The argument that a function-valued
`ErrorHandler` will make the miss-path `Ctx` escape is sound as far as escape analysis
currently behaves, but it is a prediction about a compiler, not about a language guarantee.
A future Go release could devirtualise the call, or M5 could keep a concrete fast path for
the default handler. Writing the prediction down is worth more than being right about it: if
M5's 404 path stays at zero allocations, the interesting question becomes why, and there
will be a record of what was expected.

Whether the map should survive M3 at all. If the radix tree loses on static routes — which
these numbers say is likely — the tempting answer is a hybrid: exact-match map first, tree
only for paths containing `:` or `*`. That is two data structures, two insert paths, and two
places for a conflict-detection bug to hide, in exchange for a handful of nanoseconds on one
route class. I do not know yet whether that trade is worth taking, and M3 should decide it
with a number rather than with taste.
