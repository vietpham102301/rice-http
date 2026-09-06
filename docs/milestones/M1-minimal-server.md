# M1 — Minimal server

Status: done
Started: 2026-09-06
Finished: 2026-09-06

## Goal

rice serves HTTP. An `App` holds one handler, binds a socket, answers a real request from a
standard-library client, and shuts down on a deadline. `Handler` returns an error and
nothing else; a single funnel turns that error into a 500 without leaking the cause. `Ctx`
wraps `*fasthttp.RequestCtx` thinly enough that `Method`, `Path`, `String` and `Bytes` all
cost zero allocations, and the whole dispatch path costs exactly one — the `Ctx` itself,
allocated per request on purpose.

There is no router, no middleware, no pooling. Every request reaches the same handler.

## Question this milestone answers

What does the fasthttp handler boundary actually look like, and what does one unpooled
request cost?

The boundary is narrower than expected: `fasthttp.Server.Handler` is a
`func(*fasthttp.RequestCtx)` with no return, so the error funnel has to live entirely inside
rice — fasthttp has no notion of a handler that failed. That single fact shapes `handle`.

The cost is **+14.44 ns/op and exactly one 16-byte allocation** over bare fasthttp doing the
same work. Sixteen bytes is `Ctx`: two pointers. The number is fully accounted for, which is
the condition for recording it at all — a baseline nobody can explain is worthless.

## Design notes

**`FasthttpHandler` is exported, and it is real API rather than a test hook.** It was
exported so that benchmarks in package `bench` could drive dispatch without opening a
socket. That reason alone would have made it a test hook wearing an exported name. It
survives as API because mounting rice inside an existing fasthttp server needs exactly the
same thing, and because a user who wants rice's dispatch without rice's server should not
have to fork the package to get it.

**`Shutdown` reports "I stopped waiting", not "I stopped the server".** The signature takes
a `context.Context` because `docs/03-core-concepts.md` committed to it. fasthttp's own
`Shutdown` takes no deadline, so rice runs it in a goroutine and races it against
`ctx.Done()`. On timeout `ErrShutdownTimeout` comes back while the drain continues in the
background. This is blunt and the doc comment says so. Neither of these was promoted to an
ADR: the first had no defensible alternative once benchmarks needed socket-free dispatch,
and the second is a constraint imposed by the transport rather than a decision rice made.
M7 revisits the second.

**The tasks in the plan did not match the compilation units.** `ctx.go` declares an
`app *App` field so that `reset` takes its final signature from the start, but `App` is not
defined until two tasks later, so the package does not compile in between. Tasks 5, 6 and 7
were merged into one commit rather than reordered, because the field's purpose — a stable
`reset` signature — is worth more than the task boundary.

**A handler that writes and then fails has its body discarded.** ADR-0002 left this
ambiguous; M1 settles it. `handleError` calls `ResetBody` before writing its own response,
so a partial write cannot survive an error return. Recorded in ADR-0002 and pinned by
`TestHandleDiscardsAPartialBodyWhenTheHandlerErrors`.

## Exit criteria

- [x] Tests covering the `Ctx` read and response sides, `App` dispatch and its error funnel,
      and real-socket integration for `Serve`, `Run`, `Addr` and `Shutdown` — 23 tests,
      passing under `-race`
- [x] Benchmark recorded in `bench/results/M1-minimal-server.txt`
- [x] Allocation budgets in `docs/05-performance-model.md` updated from TARGET to measured —
      `c.Method`/`c.Path` and `c.String`/`c.Bytes` are now MEASURED M1, plus a new
      end-to-end row recording the unpooled baseline of 1 alloc
- [x] Retrospective section below filled in
- [x] Journal entry appended to `docs/progress.md`

## Measurements

| Case | Time | Allocs | Bytes |
| --- | --- | --- | --- |
| `BenchmarkFasthttpBaseline` — no framework, the zero point | 11.05 ns/op | 0 | 0 |
| `BenchmarkRiceDispatch` — full M1 dispatch, plaintext body | 25.49 ns/op | 1 | 16 |
| `BenchmarkRiceDispatchNotFound` — no handler registered | 13.96 ns/op | 1 | 16 |
| `BenchmarkCtxSetHeader` — dispatch plus one response header | 68.35 ns/op | 1 | 16 |
| **Framework cost (Dispatch − Baseline)** | **+14.44 ns/op** | **+1** | **+16** |

All figures are medians of ten runs from a single recording, so the baseline is measured on
the same machine in the same session as the rice numbers rather than compared across stamps.

Hardware, Go version, OS: Apple M2 Pro (12 logical CPUs), go1.25.6 darwin/arm64,
Darwin 25.6.0 arm64. Recorded 2026-09-06T04:13:32Z.
Raw output: [`bench/results/M1-minimal-server.txt`](../../bench/results/M1-minimal-server.txt).

Allocation budgets are additionally enforced as tests in `alloc_test.go` via
`testing.AllocsPerRun`, so a regression fails CI rather than merely looking worse in a
benchmark.

## Retrospective

**What surprised me:**

*The 404 path allocates a `Ctx` it never uses.* `BenchmarkRiceDispatchNotFound` does
strictly less work than the baseline — it writes no body and sets no content type, only a
status code — and still costs 2.9 ns more, because `handle` allocates the `Ctx` before
checking whether a handler exists. In M1 this is invisible: there is one handler and it is
either set or not. From M2 onward the 404 path is the one that hostile and mistaken traffic
hits hardest, and it will be paying for a context nobody reads. The fix is trivial — move
the nil check above the allocation — but doing it now would optimise the very line this
milestone exists to measure. It is written down instead, for M2.

*One response header costs 43 ns and zero allocations.* `BenchmarkCtxSetHeader` is nearly
three times `BenchmarkRiceDispatch` for the sake of a single `X-Trace` header, yet the
allocation count does not move. Time cost and allocation cost turn out to be genuinely
independent axes, and `05-performance-model.md` only tracks one of them. The document is
titled "performance model" but is really an allocation model. That is a defensible scope,
but the title oversells it, and a reader optimising for latency would find nothing here.

*A plan can be correct in every detail and still not execute in the order it names.* Task 5
was internally consistent, well-motivated, and impossible: it declared a field whose type
arrives in Task 7. Nothing in the plan was wrong except the boundaries between its tasks.

**What I would do differently:**

I would organise plan tasks by compilation unit rather than by concept. "The `Ctx` read
side", "the `Ctx` response side" and "the `App`" are three clean ideas and one indivisible
Go package. A task that cannot end with `go build` succeeding is not a task, it is half of
one — and TDD makes this worse, not better, because "write the failing test, then make it
pass" has no meaning when the failure is a compile error in code you were not asked to write
yet.

I would also have written `BenchmarkRiceDispatchNotFound` before `app.go` rather than after.
It is the benchmark that exposed the wasted allocation, and it would have exposed it while
the dispatch path was still being designed.

**What I still do not understand:**

Why a single header write costs 43 ns. fasthttp normalises header keys and stores them in a
slice that it scans linearly, so some cost is expected, but three times the entire dispatch
path is more than "some". I have not profiled it, and I am recording the number without an
explanation — which is exactly what this project's own rule says not to do for a baseline.
The mitigation is that `SetHeader` is not on the measured baseline path; the honest position
is that it is an unexplained number sitting in a results file.

Whether removing the `Ctx` allocation in M6 buys the full 14.44 ns is also unknown. The 404
path suggests the allocation itself is worth roughly 3 ns, which would leave about 11 ns of
closure indirection and status/content-type/body writes that pooling cannot touch. If that
holds, M6's headline will be "1 alloc to 0" rather than a large latency win, and it is
better to expect that now than to discover it while writing M6's retrospective.
