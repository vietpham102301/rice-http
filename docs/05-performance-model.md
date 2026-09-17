# 05 — Performance Model

This document defines what "fast" means for rice, how it is measured, and how it is kept.
Numbers marked TARGET are design commitments not yet verified. They are replaced with
measured values as milestones complete, and the replacement is noted in `progress.md`.

## The claim

> On a warm server, a request to a static or parameterised route, with any number of
> middleware, allocates **zero** heap objects in framework code.

Everything else in this document exists to make that claim precise, testable and durable.

## What "zero allocations" excludes

The claim is narrow on purpose. It does **not** cover:

- Allocations inside the user's handler, including `c.JSON`, which encodes a user value.
- The first requests after start, while the pool warms and the trees are cold in cache.
- Request bodies large enough that fasthttp allocates rather than reusing its buffer.
- `ParamString`, `QueryString` and every other method whose name says it copies.

Stating the exclusions is more useful than the claim itself. A framework that says
"zero allocations" without them is measuring a benchmark, not a system.

## Allocation budgets

The contract, per operation, on the hot path. Each row is enforced by a test using
`testing.AllocsPerRun`, which runs the operation repeatedly and reports the average — a
test, not a benchmark, so a regression fails CI rather than merely looking worse.

| Operation | Budget | Status |
| --- | --- | --- |
| Pool acquire + reset + release | 0 | MEASURED M6 |
| Router lookup, static route | 0 | MEASURED M2 |
| Router lookup, 1 parameter | 0 | MEASURED M3 |
| Router lookup, 5 parameters | 0 | MEASURED M6 |
| Chain call, 0 middleware | 0 | MEASURED M4 |
| Chain call, 5 middleware | 0 | MEASURED M4 |
| Recovery installed, nothing panics | 0 extra | MEASURED M5 |
| 404 through the funnel | 0 | MEASURED M6 |
| Handler returns a fresh HTTPError | 1 | MEASURED M6 |
| Panic recovered, stack captured | documented, not bounded | MEASURED M5 |
| **End to end: single handler, unpooled Ctx (M1 baseline)** | 1 | MEASURED M1 |
| `c.Method`, `c.Path` | 0 | MEASURED M1 |
| `c.String`, `c.Bytes` | 0 | MEASURED M1 |
| `c.Param` | 0 | MEASURED M3 |
| `c.Query`, `c.Header` | 0 | TARGET (unscheduled) |
| `c.ParamString` | 1 | MEASURED M3 |
| `c.Set` with a pointer value | 0 | MEASURED M6 |
| `c.Get` | 0 | MEASURED M6 |
| `c.Set` with a non-constant string (the caller's boxing) | 1 | MEASURED M6 |
| **End to end: static route, no middleware, plaintext** | **0** | MEASURED M6 |
| **End to end: `/users/:id`, plaintext, read with `Param`** | **0** | MEASURED M6 |
| **End to end: static route, five middleware, plaintext** | **0** | MEASURED M6 |
| `c.JSON` of a small struct | documented, not bounded | TARGET (M8) |

The two rows that changed value rather than status did so because the `Ctx` stopped being an
allocation. A 404 was 1 and is 0; a handler returning a fresh `HTTPError` was 2 and is 1, the
one that remains being the `HTTPError` itself. Neither path was touched in M6 — the allocation
that left them was the one every dispatch carried.

`c.Set` is the one row where the number is not rice's. The store never grows within its
capacity, but converting a non-pointer value to `any` at the *call site* allocates the
interface's data word, so `c.Set("k", s)` for a non-constant string costs one and a pointer or
a constant costs nothing. The budget test pins all three so the distinction cannot rot.

Measured values come from the results files in `bench/results/`, most recently
`bench/results/M6-context-pooling.txt`, and from the `AllocsPerRun` assertions in
`alloc_test.go`. Re-run `make bench-record` on your own machine before comparing.

Two notes on reading the M6 file. `alloc_test.go` is built `!ricedebug`, because the debug
build allocates a fresh `Ctx` per request on purpose — up to three objects through `newCtx`,
since nothing is ever returned to the pool there. That is D5's price, documented rather than
asserted. And `make test` runs with `-race`, where `sync.Pool` deliberately drops one `Put` in
four; each drop sends the next `Get` to `newCtx`, so a zero-allocation dispatch really allocates
a fraction of a `Ctx` per call there. Measured on this package: **0.75 per call for a route with
a parameter, 0.50 for a static one** (0.7492 and 0.4982 exactly; the M6 retrospective records the
method). The asymmetry is `newCtx` — three objects when the App has
a parameterised route, two when it does not, because `MakeParams(0)` is a zero-capacity slice and
Go serves that from `runtime.zerobase` without a malloc. `AllocsPerRun` divides integer counts and
reports either fraction as 0, while a genuine per-request allocation adds a full 1 and fails. That
is why a zero budget means something under the race detector.

It is *not* why `newCtx` stays at three objects. A fourth takes the parameterised figure to about
1.0 — the boundary, so those budgets go red on some runs and green on others, and the static
budgets keep passing outright. Measured, with a fourth allocation injected: the parameterised
dispatch budget failed on two runs out of three and the static one on none.
`TestNewCtxStaysWithinThreeAllocations` is the guard, and it needs no race detector to fail.

Raising a budget is a design change. It requires a note in the pull request explaining what
was bought with the allocation, and if the reasoning is interesting, an ADR.

## The techniques, and what each one costs

Each of these buys allocations back. None is free, and the cost is the interesting half.

**Context pooling** (`sync.Pool`). Buys: the per-request `Ctx` allocation, which is the
single largest one. Costs: the borrow contract, and a whole class of use-after-release bugs
that the compiler cannot catch. Mitigated by the `ricedebug` poisoning build.

**Caller-supplied parameter slices.** `Lookup` fills a `*Params` the caller owns instead of
returning a fresh slice. Buys: one slice allocation per parameterised request. Costs: an
awkward signature, and a maximum parameter count tracked during registration to size the
pool's contexts.

**Pre-sized slices on the pooled `Ctx`.** Route parameters and the per-request store are
slices allocated once when the pool builds a `Ctx`, sized from the largest registered route
and to four store entries. Buys: the common case entirely, and no fixed limit on parameters.
Costs: a cliff at the store's capacity, paid once per pooled `Ctx` rather than per request,
and a `Ctx` built before a larger route was registered grows once on first use.

**Byte views instead of strings.** Buys: one allocation per accessor call. Costs: the
entire read-side API is `[]byte`, which is less pleasant than `string` and is the most
visible ergonomic sacrifice in the framework.

**Compiled middleware chains.** Buys: per-request chain state. Costs: the build phase, and
the rule that routes cannot be registered after the server starts.

**`unsafe` string/byte conversion**, confined to `internal/bytesconv`. Buys: the copy when
a `[]byte` is used as a map key or compared to a string. Costs: genuine memory-safety risk
if the underlying bytes are mutated. Confined to one file, used only for values proven
immutable for the duration, and every call site carries a comment naming why it is safe.

## Pooled versus unpooled, same session

What the pool actually buys, measured with both arms in one process and one session, so no
cross-session drift is in the number (`BenchmarkDispatchPooledVsUnpooled`,
`bench/results/M6-context-pooling.txt`, ten samples each, read through `benchstat`):

| | sec/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| `pooled` | 35.94n ± 0% | 0 | 0 |
| `unpooled` | 82.11n ± 4% | 240 | 3 |

Three allocations, not one: the unpooled arm builds the `Ctx` through `newCtx`, so it pays for
the struct, the pre-sized parameter slice and the pre-sized store slice. That is the honest
comparison — today's `Ctx` shape with and without a pool in the same run — and it is not the
same figure as M1's single-allocation baseline, which measured a smaller, pre-pooling `Ctx`
with no separate storage to allocate.

The `check()` calls the `ricedebug` build needs in every exported `Ctx` method cost nothing in
the release build. `go build -gcflags=-m` shows `(*poison).check` inlined at every call site,
which is the direct evidence: the method body is empty, so the inlined call is nothing. A
benchstat of dispatch before and after the calls were added — two back-to-back runs on the same
machine, minutes apart, not one process like the table above — reads:

```
                │  precheck.txt  │              postcheck.txt              │
                │     sec/op     │    sec/op     vs base                   │
RiceDispatch-12    31.88n ± 1%       31.80n ± 0%  -0.27% (p=0.001 n=10)
CtxSetHeader-12     72.22n ± 0%      73.08n ± 0%  +1.19% (p=0.000 n=10)
```

with 0 B/op and 0 allocs/op on both sides of both rows. Both deltas are flagged significant and
they point in opposite directions, which is what code-layout and session drift look like, not
what a cost looks like: a real per-call cost cannot make one benchmark faster. The claim this
table supports is that the check compiles out, and that nothing at the sub-1.2% scale here is
attributable to it either way.

## How it is measured

Three layers, because each catches something the others miss.

1. **Allocation tests.** `testing.AllocsPerRun` in the normal test suite. These are the
   contract. They fail loudly and immediately, and they run on every commit.
2. **Benchmarks.** `go test -bench . -benchmem -count=10`, compared with `benchstat`
   against the committed baseline in `bench/results/`. These catch time regressions that
   allocation counts do not, such as a tree walk that got deeper.
3. **Profiles.** `-cpuprofile` and `-memprofile` on demand, when a benchmark moves and the
   reason is not obvious. Not automated; a profile is an investigation, not a gate.

Benchmark results are committed. A slowdown should appear in a diff, not in someone's
memory of what the number used to be.

## Benchmark honesty rules

Self-imposed, because framework benchmarks are notoriously rigged.

- Compare against Gin, Echo and Fiber on the **same routes, same payloads, same machine,
  same run**, never against numbers copied from someone's README.
- Report the full distribution, not the best run. `benchstat` output, with variance.
- Include cases rice is expected to lose: many-route lookup, large bodies, JSON-heavy
  responses. A benchmark suite with no losses is a marketing document.
- Record the hardware, Go version and OS alongside every result set.
- Never tune a benchmark's route table to favour the radix tree's shape.

## Known trade-offs accepted

- **fasthttp is not `net/http`.** No HTTP/2, no `http.Handler` ecosystem, and a request
  object model most Go developers have to learn. Accepted as the premise of the project.
- **The borrow contract is a real footgun.** Mitigated by documentation and the debug
  build, not eliminated. A framework built for safety over speed would copy by default and
  make the borrowing opt-in.
- **The build phase forbids dynamic route registration.** Routes cannot be added after the
  server starts. Accepted; the use case is rare and the alternative is a lock on the hot
  path.
