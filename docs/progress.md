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
