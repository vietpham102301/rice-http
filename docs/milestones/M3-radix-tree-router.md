# M3 — Radix tree router

Status: done
Started: 2026-09-10
Finished: 2026-09-11

## Goal

The map is gone. In its place is one radix tree per HTTP method, reached through the same
method array M2 built, supporting named parameters (`/users/:id`) and a trailing catch-all
(`/files/*path`). Insertion splits nodes on the longest common prefix. Lookup walks static
children first, then the parameter child, then the wildcard child, and unwinds to the next
alternative when a branch strands part of the path — so `/users/newx` finds `/users/:id` even
though `/users/new` matched three bytes of it first. Registration rejects everything
ambiguous or unreachable at startup: a duplicate pattern, two parameter names at one
position, a wildcard that is not last, a repeated parameter name, more than eight parameters,
and any pattern written in a form `fctx.Path()` never produces. Captured parameters land in a
fixed inline `Params` array living on the `Ctx` by value, and `c.Param` (borrowed) and
`c.ParamString` (copied) are the public read side.

What exists at the end of M3 that did not before: routes with parameters, and the number that
says what the tree costs against the map it replaced.

## Question this milestone answers

Where does the tree win, and where does it lose to the map?

It loses on static routes, which is what the design document predicted, and it wins by
existing at all on parameters, which is the thing the map cannot express at any price.

The comparison has to be read carefully, because the two benchmark series do not measure the
same work. `BenchmarkMapLookup*` times a bare `map[string]int` probe with no framework around
it. `BenchmarkTreeLookup*` times a full dispatch through `FasthttpHandler` — a 352-byte `Ctx`
allocation, the tree walk, the handler call and a response write — because `internal/router`
is not reachable from package `bench` and the tree cannot be measured any other way. **The two
figures must not be divided.** 93.47 over 6.19 is not "the tree is fifteen times slower than
the map"; it is the ratio of an end-to-end request to a data-structure probe, and it is not a
router comparison. An earlier draft of this milestone's benchmark report made exactly that
mistake, and the correction is recorded in `bench/router_bench_test.go` as a comment on the
benchmark itself so the next reader cannot repeat it.

What the numbers do support is each series read against itself:

- The map is **flat** in route count: 6.19, 5.96 and 6.23 ns/op at 10, 100 and 1000 routes.
  A spread of 0.27 ns across a hundredfold change, pointing both ways, which is noise.
  Hashing a short string does not care how many routes exist. This reproduces M2's finding on
  a different harness.
- The tree's dispatch cost **rises**: 93.47, 98.87 and 109.40 ns/op across the same three
  route sets, +15.93 ns from 10 routes to 1000. Everything in that dispatch path except the
  lookup is constant across `n`, so the rise is attributable to the lookup.

The tree's lookup cost grows with route count where the map's flat cost does not. That is the
finding, and it is the one the design document wrote down in advance:

> The honest expectation is that **the tree will not beat the map on purely static routes,
> and may lose to it.** If that is the result, it is a finding rather than a failure: it would
> mean the tree earns its keep on parameters, which the map cannot express at all, and on
> shared prefixes, and that static-only route sets are a case where the naive structure was
> already right.

The prediction held in every part, including the uncomfortable last clause. For an application
whose routes are all static, M2's map was already the right data structure and M3 replaced it
with a worse one. The tree is not justified by this number and was never going to be; it is
justified by `/users/:id` returning a handler at all, and by `/files/*path`, and by the fact
that a route table containing either cannot be expressed as a map key. Where the tree's
advantage against a hash would show most — many long shared prefixes, where a tree decides on
the first few bytes and a hash walks the whole string — is not in this suite, because the
route set is `/route/0` through `/route/999` and its prefixes are short.

One caveat on the rise, stated rather than smoothed over: the three route sets differ in more
than size. The probe paths are `/route/5`, `/route/50` and `/route/500`, so the longer path
walks more bytes as well as meeting more static children to scan. Both are properties of the
tree's lookup and both belong in the number, but the benchmark cannot say how the 15.93 ns
divides between them.

## Design notes

**The `Ctx` is now allocated before the lookup, and that is the design working rather than
failing.** `Lookup` fills a caller-supplied `*Params`, `Params` lives on the `Ctx` by value,
so the `Ctx` has to exist before the walk starts. M2's D6 had deliberately put the lookup
first so a request matching nothing would not pay for a context no handler reads; M3 gives
that ordering back. Parameter storage living on the borrowed handle is what buys
zero-allocation capture, and the price is that the handle is constructed first.

**Parameter storage is eight inline slots, and the cost is in the struct size rather than in
the allocation count.** `Param` is 40 bytes, `Params` is 328, and `unsafe.Sizeof(Ctx{})` is
exactly **344** — which is what the design document predicted, to the byte. The benchmarks
report **352 B/op** because Go's allocator rounds a 344-byte request up to its next size
class. Both numbers are correct and they are not the same quantity: 344 is the struct, 352 is
the block. Allocations per request stayed at one. Bytes per request went from 16 to 352, and
the consequences of that are the subject of the Measurements section below.

**Registration rejects patterns rather than normalising them.** `/a//b`, `/a/./b` and
`/caf%C3%A9` all registered cleanly in M2 and could then never match, because fasthttp
collapses empty segments, resolves dot segments and percent-decodes before rice sees a path.
M3 panics on all three at registration, naming the offending pattern and the form to use
instead. Normalising would be friendlier and worse: `app.GET("/a//b", h1)` followed by
`app.GET("/a/b", h2)` would silently become a duplicate-route panic naming a pattern neither
line contains.

**M2's map is frozen inside the benchmark package.** `bench/mapbaseline_test.go` holds a
fifteen-line copy of M2's exact-match router, used by nothing but the benchmarks, carrying an
instruction not to "fix" it. `bench/results/README.md` says numbers from different stamps are
not comparable; without the frozen copy, every milestone after this one would have had no way
to produce the map figure on its own machine in its own run. It is a deliberate duplicate and
its value is that it does not change.

**Parameters cost about five nanoseconds each, and backtracking is not pathological.** One
parameter dispatches at 91.32 ns/op and five at 110.25, which is 18.93 ns for four more
captures — roughly 4.7 ns per parameter, each one a name write and a byte-slice write into a
slot that already exists. The wildcard case is the cheapest parameterised match at 89.45,
which makes sense: it consumes the remainder in one step instead of scanning for the next
slash. The backtracking worst case the design admits costs 97.09 against the single-parameter
match's 91.32 — about 6 ns for a failed static branch and an unwind, or about the price of one
extra parameter. A worst case nobody measured is a worst case nobody knows; this one is
measured and it is unremarkable.

Nothing here had a real alternative worth promoting to an ADR. ADR-0004 fixed the tree's
shape in advance and M3 is that commitment carried out; the one genuinely open decision it
carried, trailing-slash and case-insensitive matching, is now
[ADR-0007](../adr/0007-no-trailing-slash-or-case-insensitive-matching.md).

## Exit criteria

- [x] Table-driven tests covering prefix splits, priority ordering, backtracking, conflicting
      registrations and deep nesting — 119 tests across the two packages (72 in `rice`, 47 in
      `internal/router`), passing under `-race`, including the mandatory backtracking case
      `TestLookupBacktracksFromAPartialStaticMatch` (`/users/new` and `/users/:id` registered,
      `/users/newx` requested) and `TestBacktrackingUnwindsCapturedParameters`
- [x] `AllocsPerRun` asserts 0 for lookup including with parameters captured —
      `TestAllocBudgetLookupOneParameter`, `...FiveParameters`, `...Wildcard`, `...Backtrack`
      and `TestAllocBudgetCtxParam`, with `TestAllocBudgetCtxParamString` asserting **exactly**
      one rather than at most one
- [x] Benchmark recorded in
      [`bench/results/M3-radix-tree-router.txt`](../../bench/results/M3-radix-tree-router.txt),
      with the frozen map and the tree measured by the same binary on the same machine in the
      same session
- [x] Allocation budgets in [`05-performance-model.md`](../05-performance-model.md) updated
      from TARGET to measured — `Router lookup, 1 parameter`, `c.Param` and `c.ParamString`
      are now `MEASURED M3`; `Router lookup, 5 parameters` stays `TARGET (M6, needs slot
      sizing)`; the bundled `c.Param, c.Query, c.Header` row was **split** rather than
      relabelled, because M3 delivered only the first of the three
- [x] ADR-0004's open question closed by
      [ADR-0007](../adr/0007-no-trailing-slash-or-case-insensitive-matching.md)
- [x] [ADR-0005](../adr/0005-context-pooling-and-borrow-contract.md) gained a note recording
      that M3, not M5, ended the accidental zero-allocation 404 path, and why
- [x] Retrospective section below filled in
- [x] Journal entry appended to [`progress.md`](../progress.md)

## Measurements

| Case | Time | Allocs | Bytes |
| --- | --- | --- | --- |
| `BenchmarkFasthttpBaseline` — no framework, the zero point | 10.92 ns/op | 0 | 0 |
| `BenchmarkRiceDispatch` — full dispatch, one static route | 79.70 ns/op | 1 | 352 |
| `BenchmarkRiceDispatchNotFound` — no routes registered at all | 97.68 ns/op | 1 | 352 |
| `BenchmarkCtxSetHeader` — dispatch plus one response header | 127.85 ns/op | 1 | 352 |
| `BenchmarkMapLookup10` — M2's frozen map, bare probe, 10 routes | 6.19 ns/op | 0 | 0 |
| `BenchmarkMapLookup100` — the same, 100 routes | 5.96 ns/op | 0 | 0 |
| `BenchmarkMapLookup1000` — the same, 1000 routes | 6.23 ns/op | 0 | 0 |
| `BenchmarkTreeLookup10` — tree through full dispatch, 10 routes | 93.47 ns/op | 1 | 352 |
| `BenchmarkTreeLookup100` — the same, 100 routes | 98.87 ns/op | 1 | 352 |
| `BenchmarkTreeLookup1000` — the same, 1000 routes | 109.40 ns/op | 1 | 352 |
| `BenchmarkTreeLookup1Param` — `/users/:id` matching `/users/42` | 91.32 ns/op | 1 | 352 |
| `BenchmarkTreeLookup5Params` — five captures at depth | 110.25 ns/op | 1 | 352 |
| `BenchmarkTreeLookupWildcard` — `/files/*path` matching a nested path | 89.45 ns/op | 1 | 352 |
| `BenchmarkTreeLookupBacktrack` — `/users/newx`, one branch failed first | 97.09 ns/op | 1 | 352 |
| `BenchmarkTreeMiss` — unregistered path, 1000 routes | 111.10 ns/op | 1 | 352 |
| `BenchmarkTreeWrongVerb` — registered path, wrong verb, 1000 routes | 107.70 ns/op | 1 | 352 |
| **Framework cost (Dispatch − Baseline)** | **+68.78 ns/op** | **+1** | **+352** |
| **Map lookup scaling (1000 routes − 10 routes)** | **+0.04 ns/op** | **0** | **0** |
| **Tree dispatch scaling (1000 routes − 10 routes)** | **+15.93 ns/op** | **0** | **0** |
| **Parameter cost (5 params − 1 param)** | **+18.93 ns/op** | **0** | **0** |
| **Backtracking cost (Backtrack − 1 param)** | **+5.77 ns/op** | **0** | **0** |

All figures are medians of ten runs from a single recording, so the baseline is measured on the
same machine in the same session as the rice numbers rather than compared across stamps. The
map and the tree come from the same binary in that same session, which is the entire reason
M2's map was frozen into `bench/`.

Hardware, Go version, OS: Apple M2 Pro, go1.25.6 darwin/arm64, Darwin 25.6.0 arm64.
Recorded 2026-09-11T14:10:10Z.
Raw output: [`bench/results/M3-radix-tree-router.txt`](../../bench/results/M3-radix-tree-router.txt).

Allocation budgets are additionally enforced as tests in `alloc_test.go` via
`testing.AllocsPerRun`, so a regression fails CI rather than merely looking worse in a
benchmark.

### M3 roughly doubled the cost of every request, and the radix tree is not why

This is the milestone's real headline and it belongs above everything else in this section.

The cross-milestone comparison is legitimate here because the floor barely moved:
`BenchmarkFasthttpBaseline` was 11.24 ns/op in M2 and is 10.92 now, so the machine is
comparable and, if anything, marginally faster. Against that floor:

| Benchmark | M2 | M3 | Change |
| --- | --- | --- | --- |
| `BenchmarkFasthttpBaseline` | 11.24 | 10.92 | −0.32 |
| `BenchmarkRiceDispatch` | 37.91 | 79.70 | **+41.79** |
| `BenchmarkRiceDispatchNotFound` | 33.15 | 97.68 | **+64.53**, and 0 → 1 alloc |
| `BenchmarkCtxSetHeader` | 80.59 | 127.85 | **+47.26** |

Every path got about twice as expensive. The obvious suspect is the tree — it is the thing
this milestone built, it is more work than a hash probe, and its own scaling term is real. It
is also not the answer, and rather than speculate about it I measured the other suspect: the
`Ctx` grew from 16 bytes to 344, so the per-request allocation grew with it.

A throwaway benchmark that allocates each `Ctx` shape and does nothing else, run on the same
machine:

    16-byte Ctx  (the M2 shape):  11.8 ns/op
    352-byte Ctx (the M3 shape):  72.9 ns/op

That is about **+61 ns for the allocation alone**, which more than accounts for the entire
+41.79 ns on `RiceDispatch`. The cause is the inline `MaxParams = 8` parameter array inside
`Ctx` — D3's fixed eight slots — and not the tree walk. The probe was deliberately thrown away
rather than committed: it measures a struct shape, not rice, and a committed benchmark of a
shape nothing uses would rot. It was re-run once before this was written and reproduced
(11.8 against 72.4), which is the only reason the figure is quoted rather than described.

Two caveats, because the probe is a tight loop doing nothing else. It sees the allocator's
friendliest possible cache behaviour, and it exceeds the slowdown it is meant to explain —
61 ns of cause for 42 ns of effect. So this accounts for M3's cost in the sense that it
identifies the mechanism and shows it is large enough; on its own it is not an exact
decomposition. The 404 path's +64.53 ns is the closer fit, and it should be: M2's 404
allocated nothing at all, so it absorbs the whole allocation rather than a growth in one.

**Final-review addition: an in-situ bisection, which is stronger evidence than the tight-loop
probe above.** Rather than infer the tree's contribution from a standalone allocation probe,
the final review changed one line — `MaxParams` from 8 to 1 — in an otherwise untouched
checkout, and re-ran the same two benchmarks in the same session on the same machine:

    BenchmarkRiceDispatch    80.2 -> 37.3 ns/op   (352 B/op -> 64 B/op)
    BenchmarkTreeLookup10    91.4 -> 45.2 ns/op

37.3 ns/op at one parameter slot sits right next to M2's frozen map at 37.91 ns/op. That is
the stronger claim the tight-loop probe could not make: at a single route, the radix tree
costs approximately nothing against the map that preceded it, so the tree walk is not where
M3's slowdown lives. The entire +41.8 ns gap between M2 and M3's `RiceDispatch` is `Ctx`
growth, measured directly on the real dispatch path rather than inferred from a shape probe
run in isolation. (The probe was re-run during this fix pass and reproduced within noise:
78.7–80.3 ns/op at `MaxParams = 8`, 36.7–37.0 ns/op at `MaxParams = 1`, for `RiceDispatch`.)

This also closes the residue the tight-loop probe left open. That probe measured +61 ns for
the allocation alone against a +41.79 ns observed effect — 61 ns of cause for 42 ns of effect,
and unexplained. The in-situ bisection measures the same change in place rather than in a
tight loop with no other work around it, and it puts the cost at about 80.2 − 37.3 = **42.9
ns**, which matches the +41.79 ns `RiceDispatch` actually lost. The tight-loop figure was never
wrong about the mechanism; it was measuring a friendlier cache environment than a real
dispatch provides, which is exactly the kind of thing a tight allocation loop overstates. Both
numbers are kept here rather than one replacing the other: the tight-loop probe is what first
identified the mechanism, and the in-situ bisection is what confirms the number and resolves
the residue that probe could not close on its own. The probe for this section was run in a
throwaway worktree, to change one constant and nothing else, and was not committed.

This project has now recorded an unexplained number twice — M1's header write, still open
below, and M2's ~9 ns routing cost, addressed a few paragraphs down. The tight-loop probe's
61-versus-42 gap was on its way to becoming a third. Closing it here, with a second
independent measurement rather than a stronger inference, is worth stating for its own sake:
it is the difference between a number that is merely plausible and one that is confirmed.

**A prediction from M1 is now wrong, and M3 is the reason.** M1's retrospective, carried
forward in M1's journal entry, said the `Ctx` allocation was worth roughly 3 ns of the 14.44 ns
of framework cost, and concluded that M6's pooling would therefore be "1 alloc to 0" rather
than a large latency win. M3 made that allocation twenty-two times bigger. Removing it is now
worth about 61 ns per request — roughly three quarters of everything `BenchmarkRiceDispatch`
measures. M6's payoff grew because M3 spent, and that is not a credit to M3. A milestone whose
contribution to a later milestone's headline is having made the problem worse has not earned a
compliment for it; the honest reading is that M3 borrowed against M6 and M6 will be seen
paying it back.

**This accounts for M3's own cost, which breaks a two-milestone habit.** M1 recorded 43 ns for
a single header write with no explanation. M2 recorded roughly 9 ns of routing cost with no
explanation, and said plainly that the project's rule against recording unexplained numbers
had now been bent twice. M3's cost is not mysterious: it is the size of the thing being
allocated, measured directly, on purpose, before this document was written.

The same probe also offers the first actual candidate for M2's ~9 ns, and it is worth
recording even though it is not proof. M1 inferred 3 ns for the `Ctx` allocation by comparing
the 404 path against the baseline — but M2 then discovered that the 404 path *does not
allocate*, which means that 3 ns was never a measurement of an allocation at all. A direct
probe puts a 16-byte allocation at about 11.8 ns, roughly 9 ns more than the figure M2's
subtraction was carrying. M2's unexplained residue was an arithmetic remainder built on that
underestimate, and closing the gap in the estimate closes a gap of about the same size in the
remainder. That is a coincidence of magnitude rather than a profile, and it is offered as a
candidate, not a conclusion. M1's 43 ns header write is untouched by any of this and remains
unexplained; `BenchmarkCtxSetHeader` still costs 48.15 ns more than `BenchmarkRiceDispatch`
here, against 42.68 in M2, and nobody has profiled it yet.

## Retrospective

**What surprised me:**

*The milestone's biggest number had nothing to do with its subject.* M3 exists to answer a
question about a data structure, and the largest thing it changed was the size of an
allocation on a struct the router only borrows. Dispatch roughly doubled and the tree
contributed a minority of it. If this had been left to the sentence that feels true — "the
radix tree is slower than the map, so requests got slower" — the conclusion would have been
wrong in a way that was flattering to nobody and would have sent M8 profiling the wrong
function. It took one throwaway benchmark of two struct shapes to find out, which is a poor
excuse for not having run it before writing the first draft of the number.

*The design document's 344-byte prediction was exact.* Not "roughly", not within a few bytes:
`unsafe.Sizeof(Ctx{})` prints 344, and the design said 344. What was not predicted is that the
benchmarks would report 352, which briefly looked like the prediction had missed before the
size class explained it. Two correct numbers describing different quantities look exactly like
one wrong number until somebody names both.

*The zero-allocation 404 path is gone, and M3 ended it rather than M5.* M2 measured the miss
path at zero allocations, found the cause in escape analysis, and predicted that M5's
configurable `ErrorHandler` would eventually take the zero away. The zero did not last, and
the mechanism that ended it was not the one named: the `Ctx` must now exist before the lookup,
`&c.params` is passed into it, and it escapes on both paths. This is the clearest evidence the
project has produced for a rule it adopted in M2 — **measure a zero, document why it holds,
and do not pin it with a test unless the design guarantees it.** Had M2 written
`TestNotFoundPathAllocatesZero`, M3 would have had to delete a passing test to ship a correct
change, and the deletion would have looked like a retreat instead of the documented
consequence it is. The note lives in ADR-0005 for the same reason.

*The tree's absolute figures are so dominated by the allocation that the milestone's headline
question is answered only in its shape.* The flat-versus-rising comparison holds, because
everything in the dispatch path except the lookup is constant across `n`. But "how many
nanoseconds does the tree walk cost" is not answerable from this suite at all, which was a
surprise while writing the table rather than while writing the benchmarks.

**What I would do differently:**

I would have written a benchmark inside `internal/router` that times `Tree.Lookup` and nothing
else. Every routing figure in the table is a full dispatch, because package `bench` cannot
reach `internal/router` and `make bench` only runs `./bench/...`. That is enough to answer
M3's question, since the scaling term survives a constant offset, and it is not enough to
compare the tree against the map in absolute terms or to decide whether the hybrid M2 proposed
is worth taking. **Recorded as a known limitation for a later milestone:** a per-lookup
benchmark inside `internal/router`, plus a make target that runs it, would give a figure
comparable in kind to the bare map probe. It does not exist yet and M3 shipped without it.

The lesson worth more than any of the numbers, though, is about guards. Five separate times
across this milestone and the last, something named as a guard turned out not to guard what it
claimed:

1. M2's design decision D2 named an allocation test as the guard for a specific code form.
   Applying the change the decision forbade left the test passing.
2. M2's Task 1 doc comment pointed forward at a test that, when someone finally read it, did
   not constrain what the comment said.
3. This milestone's `TestTreeLookupDoesNotRetainThePathSlice` could not fail. The helper it
   used copied its input before handing it over, and both fixture routes were static, so there
   was nothing for the tree to retain even if it had tried. It is now
   `TestTreeLookupBorrowsTheCapturedValue`, which mutates the caller's buffer under a captured
   parameter and asserts the captured value changes with it.
4. This milestone's `App.lookup` comment claimed parameter capture allocated nothing and cited
   budget tests as evidence. Every cited test was static-only and captured no parameters.
5. This milestone's `Ctx.ParamString` budget measured zero allocations, because discarding the
   result let the compiler elide the conversion — and because the check was an upper bound,
   zero satisfied a budget of one. The test would have kept passing if `ParamString` had
   stopped copying and started aliasing borrowed bytes, which is the borrow-contract violation
   it existed to catch.

Every one of the five was found by someone reading the guard afterwards and asking what it
actually constrains. None was found by the guard failing, because none of them could fail. The
habit that closed the last three is small and mechanical: **break the thing the guard guards,
on purpose, and confirm the guard goes red.** Make `ParamString` alias instead of copy and
watch the budget test fail; the test that survives that is a guard, and the test that does not
is a comment with a `func` keyword. It costs a couple of minutes and it is the only evidence
that distinguishes the two. I would apply it to every test written to pin a property, from the
moment the test is written, and I would treat an upper-bound assertion on a property that
should be exact as a defect in the assertion rather than a convenience.

**What I still do not understand:**

Where the 15.93 ns of tree scaling actually goes. Between 10 routes and 1000 the dispatch cost
rises by that much, and it is attributable to the lookup by elimination rather than by
observation. The probe path also gets two bytes longer across the three sets, so part of it is
walking more bytes and part is scanning more static children at the split — and I have not
profiled it, which means the same unexplained-number problem exists in miniature even in a
milestone that finally explained its main one. It is bounded and reproducible; it is not
explained.

Whether the hybrid M2 proposed is worth taking. M2's retrospective said M3 should decide, with
a number rather than with taste, whether to keep an exact-match map in front of the tree for
static paths. M3 cannot decide it, and the reason is the measurement gap above: the map's
advantage over the tree on static routes is real and visible in the shape of the two series,
but nobody knows whether it is two nanoseconds or twenty, and the answer determines whether
two insert paths and two places for a conflict-detection bug to hide are a sensible trade. The
question rolls forward with a clear statement of what would settle it.

Whether the 43 ns header write will ever get explained. It is now the oldest unexplained figure
in the project, it survived M2 and M3 untouched, and `BenchmarkCtxSetHeader` is 48 ns above
dispatch here. M8 is the milestone that owns profiling, and this is the first thing on its list.
