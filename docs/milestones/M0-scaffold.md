# M0 — Scaffold

Status: done
Started: 2026-09-05
Finished: 2026-09-06

## Goal

The repository went from documentation-only to a Go module with a working measurement rig.
There is a `go.mod` at `github.com/vietpham102301/rice-http`, a `Makefile` exposing the six
targets every later milestone drives (`test`, `bench`, `bench-record`, `cover`, `lint`,
`tidy`), a `bench/` package that imports rice the way a user would, a recording script that
stamps every run with its hardware, and CI that runs all of it on every pull request.

Nothing serves traffic. The only benchmark that exists measures fasthttp with no framework
on top of it, which is exactly the point: the rig was built before there was anything to
measure, so the first framework number has something honest to be subtracted from.

## Question this milestone answers

What does the measurement rig look like, before there is anything to measure?

The answer turned out to be more opinionated than expected. The rig's first job is not to
measure rice — it is to measure the transport *without* rice, and to prove that number is
trustworthy. `BenchmarkFasthttpBaseline` reports 0 allocs/op. Because the floor is a true
zero, every allocation rice reports from M1 onward is rice's own, with no noise to hide in.
Had the baseline reported, say, 2 allocs/op, the whole zero-allocation claim in
[05-performance-model.md](../05-performance-model.md) would have needed rephrasing before a
line of framework code was written.

## Design notes

**fasthttp resolved to v1.73.0.** `@latest` picked it, `go.sum` locks it, and it was not
hand-written. It pulls three transitive dependencies: `andybalholm/brotli v1.2.2`,
`klauspost/compress v1.19.1`, `valyala/bytebufferpool v1.0.0`. All three are indirect and
none are imported by rice.

**The Go directive is `go 1.25.0`, not `go 1.25`.** The plan called for the short form, on
the reasoning that a patch-level directive forces contributors onto that exact patch. That
reasoning is sound for `go 1.25.6` but does not apply to `go 1.25.0`, which expresses the
same minimum as `go 1.25`. It matters because `go mod tidy` rewrites the short form to the
`.0` form deterministically — verified by setting it back and re-running tidy. Since CI
verifies tidiness with `git diff --exit-code go.mod go.sum`, holding the short form would
mean either a permanently red build or dropping the `tidy` target. The `.0` form is kept.

**Benchmarks live in their own package and reach rice only through its exported API.** A
benchmark with access to unexported internals measures a program no user can write. This
also forces the public API to be benchmarkable, which is a design constraint worth having
early.

**Handler-level benchmarks reuse a single pre-warmed `*fasthttp.RequestCtx`.** Request
construction happens once, outside the loop, so it cannot pollute the allocation count. The
warm-up call before `ResetTimer` matters: without it the first iteration pays for growing
the response buffers, which a live server would have already paid on an earlier request.

**Recorded results are committed.** A regression should show up as a diff in review, not as
someone's recollection of what the number used to be. Each file carries a stamp — date, Go
version, OS, CPU — and `bench/results/README.md` says plainly that numbers from different
stamps are not comparable.

**CI runs `make bench` with `-count=1` as a smoke test only.** Its job is to prove the
benchmarks still compile and run. Shared CI hardware is too noisy for timings, so real
numbers come from `make bench-record` on a known machine.

## Exit criteria

- [x] Tests covering — none. There is no code to test. `make test` runs green on an empty
      suite, which is the stated exit criterion for this milestone.
- [x] Benchmark recorded in `bench/results/M0-fasthttp-baseline.txt`
- [ ] Allocation budgets in `docs/05-performance-model.md` updated from TARGET to measured —
      **not applicable to M0.** Every budget in that table belongs to M1, M3, M4 or M6.
      There is no rice code whose budget could be measured yet. M1 Task 9 updates the first
      four rows.
- [x] Retrospective section below filled in
- [x] Journal entry appended to `docs/progress.md`

## Measurements

| Case | Time | Allocs | Bytes |
| --- | --- | --- | --- |
| `BenchmarkFasthttpBaseline` — bare fasthttp handler, plaintext "hello" | 11.17 ns/op (median of 10; range 11.06–11.26) | 0 | 0 |

Hardware, Go version, OS: Apple M2 Pro (12 logical CPUs), go1.25.6 darwin/arm64,
Darwin 25.6.0 arm64. Recorded 2026-09-05T10:25:54Z.
Raw output: [`bench/results/M0-fasthttp-baseline.txt`](../../bench/results/M0-fasthttp-baseline.txt).

## Retrospective

**What surprised me:**

Two things, both small and both about the module rather than the benchmarks.

The first is that after Task 1 the fasthttp "pin" was not actually a requirement. `go get`
wrote it into `go.mod` marked `// indirect`, because no Go file imported it yet. Running
`make tidy` at that moment would have deleted the require block outright and undone the pin.
The scaffold had a window where its one dependency was pinned only in the sense that a note
on a whiteboard is pinned. It resolved itself in Task 2 when `bench/` imported fasthttp for
real, but the ordering hazard was invisible until the command was in front of me. A
dependency is not pinned until something imports it.

The second is the `go 1.25` / `go 1.25.0` conflict described in the design notes. What
surprised me was not that the toolchain normalises the directive — it was that a written
constraint and a CI step that had both seemed obviously correct turned out to be quietly
incompatible, and that the incompatibility only surfaced because the CI step was written to
actually fail on drift rather than merely warn. A tidiness check that cannot fail would have
let this sit undetected for months.

**What I would do differently:**

I would have written the baseline benchmark before the Makefile. The Makefile's `bench`
target encodes assumptions about the benchmark's shape — the package path, the `-run '^$'`
filter, the flags — and I wrote all of it against an imagined benchmark that did not exist
yet. It happened to be right, but only by luck. Writing the thing first and then extracting
the command that ran it would have been the honest order.

**What I still do not understand:**

Whether the handler-level benchmark stays representative once M6 introduces pooling.
Reusing one pre-warmed `*fasthttp.RequestCtx` across every iteration is the right call today
— it isolates framework cost — but a pool's whole behaviour is about churn across many
contexts, and a benchmark that never churns may flatter it. I do not yet know whether M6
needs a second benchmark shape, or whether `AllocsPerRun` on a steady-state pool is
sufficient. That question does not need answering until M6, but it is worth carrying.

Also unresolved: whether the CI tidy check is stable across Go patch releases. It passes on
1.25.6 locally and `setup-go` resolves `1.25` to whatever the newest patch is, so a future
patch that changes normalisation rules again would turn the build red for a reason unrelated
to any change in this repository.
