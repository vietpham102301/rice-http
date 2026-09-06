# Progress Journal

Append-only. Newest entry first. Every entry is dated, names its milestone, and says what
was learned — not just what was done. Entries are never edited after the fact; a correction
is a new entry that says what the old one got wrong.

Each entry uses this shape:

```
## YYYY-MM-DD — Mn — Short title
**Did:**       what changed
**Learned:**   the non-obvious thing
**Measured:**  numbers, if any
**Next:**      the immediate next step
```

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
