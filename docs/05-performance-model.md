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
| Pool acquire + reset + release | 0 | TARGET (M6) |
| Router lookup, static route | 0 | MEASURED M2 |
| Router lookup, 1 parameter | 0 | MEASURED M3 |
| Router lookup, 5 parameters | 0 | TARGET (M6, needs slot sizing) |
| Chain call, 0 middleware | 0 | MEASURED M4 |
| Chain call, 5 middleware | 0 | MEASURED M4 |
| **End to end: single handler, unpooled Ctx (M1 baseline)** | 1 | MEASURED M1 |
| `c.Method`, `c.Path` | 0 | MEASURED M1 |
| `c.String`, `c.Bytes` | 0 | MEASURED M1 |
| `c.Param` | 0 | MEASURED M3 |
| `c.Query`, `c.Header` | 0 | TARGET (unscheduled) |
| `c.ParamString` | 1 | MEASURED M3 |
| `c.Set` / `c.Get`, up to inline capacity | 0 | TARGET (M6) |
| **End to end: static route, no middleware, plaintext** | **0** | TARGET (M6) |
| **End to end: `/users/:id`, 3 middleware, plaintext** | **0** | TARGET (M6) |
| `c.JSON` of a small struct | documented, not bounded | TARGET (M8) |

Measured values come from the results files in `bench/results/`, most recently
`bench/results/M3-radix-tree-router.txt`. Re-run `make bench-record` on your own
machine before comparing.

Raising a budget is a design change. It requires a note in the pull request explaining what
was bought with the allocation, and if the reasoning is interesting, an ADR.

## The techniques, and what each one costs

Each of these buys allocations back. None is free, and the cost is the interesting half.

**Context pooling** (`sync.Pool`). Buys: the per-request `Ctx` allocation, which is the
single largest one. Costs: the borrow contract, and a whole class of use-after-release bugs
that the compiler cannot catch. Mitigated by the `ricedebug` poisoning build.

**Caller-supplied parameter slices.** `Lookup` fills a `*Params` the caller owns instead of
returning a fresh slice. Buys: one slice allocation per parameterised request. Costs: an
awkward signature, and a maximum parameter count that must be computed at build time.

**Inline arrays before heap slices.** Route parameters and the per-request store both start
as small fixed arrays inside the pooled `Ctx`. Buys: the common case entirely. Costs: a
performance cliff at the inline capacity, which must be documented rather than discovered.

**Byte views instead of strings.** Buys: one allocation per accessor call. Costs: the
entire read-side API is `[]byte`, which is less pleasant than `string` and is the most
visible ergonomic sacrifice in the framework.

**Compiled middleware chains.** Buys: per-request chain state. Costs: the build phase, and
the rule that routes cannot be registered after the server starts.

**`unsafe` string/byte conversion**, confined to `internal/bytesconv`. Buys: the copy when
a `[]byte` is used as a map key or compared to a string. Costs: genuine memory-safety risk
if the underlying bytes are mutated. Confined to one file, used only for values proven
immutable for the duration, and every call site carries a comment naming why it is safe.

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
