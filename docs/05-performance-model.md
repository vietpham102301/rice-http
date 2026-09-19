# 05 — Performance Model

This document defines what "fast" means for rice, how it is measured, and how it is kept.
Every number in it is now measured; none is a target. Until M8 some rows were design
commitments not yet verified, and each was replaced with a measured value as its milestone
completed, the replacement noted in `progress.md`. "Not implemented" marks accessors the design
promised and the code does not have.

## The claim

> On a warm server, a request to a static or parameterised route, with any number of
> middleware, allocates **zero** heap objects in framework code.

Everything else in this document exists to make that claim precise, testable and durable.

## What "zero allocations" excludes

The claim is narrow on purpose. It does **not** cover:

- Allocations inside the user's handler, including JSON encoding the handler does itself, for
  example with `encoding/json`.
- The first requests after start, while the pool warms and the trees are cold in cache.
- Request bodies large enough that fasthttp allocates rather than reusing its buffer.
- `ParamString`, `QueryString` and every other method whose name says it copies.

Stating the exclusions is more useful than the claim itself. A framework that says
"zero allocations" without them is measuring a benchmark, not a system.

## Allocation budgets

The contract, per operation, on the hot path. Each row is enforced by a test using
`testing.AllocsPerRun`, which runs the operation repeatedly and reports the average — a
test, not a benchmark, so a regression fails CI rather than merely looking worse. The status
names the milestone whose measurement set the row; the test column names what holds it there.

### Request path — `Ctx` methods

Every exported method of `Ctx`, one row each.

| Method | Budget | Status | Enforced by |
| --- | --- | --- | --- |
| `c.Method` | 0 | MEASURED M1 | `TestAllocBudgetMethodAndPath` |
| `c.Path` | 0 | MEASURED M1 | `TestAllocBudgetMethodAndPath` |
| `c.Param` | 0 | MEASURED M3 | `TestAllocBudgetCtxParam` |
| `c.ParamString` | 1, exactly | MEASURED M3 | `TestAllocBudgetCtxParamString` |
| `c.Status` | 0 | MEASURED M8 | `TestAllocBudgetStatus` |
| `c.SetHeader` | 0 | MEASURED M8 | `TestAllocBudgetSetHeader` |
| `c.SetContentType` | 0 | MEASURED M8 | `TestAllocBudgetSetContentType` |
| `c.String` | 0 | MEASURED M1 | `TestAllocBudgetString` |
| `c.Bytes` | 0 | MEASURED M1 | `TestAllocBudgetBytes` |
| `c.RequestCtx` | 0 | MEASURED M8 | `TestAllocBudgetRequestCtx` |
| `c.Set` with a pointer value | 0 | MEASURED M6 | `TestAllocBudgetCtxSetPointer` |
| `c.Set` with a non-constant string (the caller's boxing) | 1, exactly | MEASURED M6 | `TestAllocBudgetCtxSetString` |
| `c.Get` | 0 | MEASURED M6 | `TestAllocBudgetCtxGet` |
| `c.Query`, `c.Header` | — | not implemented — see the roadmap's [Explicitly deferred](04-roadmap.md#explicitly-deferred) | — |
| `c.JSON` | — | not implemented — see the roadmap's [Explicitly deferred](04-roadmap.md#explicitly-deferred) | — |

The four M8 rows are new tests, not new code: `Status`, `SetHeader` and `SetContentType` write
into fasthttp's reused response header, and `RequestCtx` returns a field. Each was broken once on
purpose with an allocating line and failed — for example
`Ctx.RequestCtx allocated 9.0 objects per call, budget is 0`; the four texts are in the
[M8 retrospective](milestones/M8-benchmark-suite.md).

### Request path — dispatch

| Operation | Budget | Status | Enforced by |
| --- | --- | --- | --- |
| Pool acquire + reset + release | 0 | MEASURED M6 | `TestAllocBudgetPoolAcquireRelease` |
| Router lookup, static route | 0 | MEASURED M2 | `TestAllocBudgetLookupHit` |
| Router lookup, 1 parameter | 0 | MEASURED M3 | `TestAllocBudgetLookupOneParameter` |
| Router lookup, 5 parameters | 0 | MEASURED M6 | `TestAllocBudgetLookupFiveParameters` |
| Chain call, 0 middleware | 0 | MEASURED M4 | `TestAllocBudgetDispatchNoMiddleware` |
| Chain call, 5 middleware | 0 | MEASURED M4 | `TestAllocBudgetDispatchFiveMiddleware`, `TestAllocBudgetChainCompile` |
| Recovery installed, nothing panics | 0 extra | MEASURED M5 | `TestAllocBudgetDispatchWithRecover` |
| `ConnState` hook, per request (`StateActive` + `StateIdle`) | 0 | MEASURED M7 | `TestConnStateActiveAndIdleAreFree` |
| 404 through the funnel | 0 | MEASURED M6 | `TestAllocBudget404` |
| Handler returns a fresh HTTPError | 1 | MEASURED M6 | `TestAllocBudgetHTTPErrorReturn` |
| Panic recovered, stack captured | documented, not bounded | MEASURED M5 | `BenchmarkDispatchPanic` (a benchmark, not a budget) |
| **End to end: single handler, unpooled Ctx (M1 baseline)** | 1 | MEASURED M1 | historical — `bench/results/M1-minimal-server.txt`; the pooled rows below replaced it |
| **End to end: static route, no middleware, plaintext** | **0** | MEASURED M6 | `TestAllocBudgetHandleDispatch` |
| **End to end: `/users/:id`, plaintext, read with `Param`** | **0** | MEASURED M6 | `TestAllocBudgetHandleDispatchParameterised` |
| **End to end: static route, five middleware, plaintext** | **0** | MEASURED M6 | `TestAllocBudgetDispatchFiveMiddleware` |

### Not on the request path

| Operation | Budget | Status |
| --- | --- | --- |
| Registration (`Handle`, `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD`, `OPTIONS`, `Use`, `Group` and the same methods on `*Group`), `Build`, `FasthttpHandler`, lifecycle (`Run`, `Serve`, `RunContext`, `Shutdown`, `Addr`, `OnStart`, `OnShutdown`) and the options (`WithErrorHandler`, `WithReadTimeout`, `WithWriteTimeout`, `WithIdleTimeout`) | not budgeted — runs once per App | — |

What building costs is measured rather than budgeted: `BenchmarkBuild1000Routes` reports 7503
allocations for 1000 routes, most recently in `bench/results/M7-lifecycle.txt` (the [M4 retrospective](milestones/M4-middleware-and-groups.md) decomposes
them).

The two rows that changed value rather than status did so because the `Ctx` stopped being an
allocation. A 404 was 1 and is 0; a handler returning a fresh `HTTPError` was 2 and is 1, the
one that remains being the `HTTPError` itself. Neither path was touched in M6 — the allocation
that left them was the one every dispatch carried.

`c.Set` is the one row where the number is not rice's. The store never grows within its
capacity, but converting a non-pointer value to `any` at the *call site* allocates the
interface's data word, so `c.Set("k", s)` for a non-constant string costs one and a pointer or
a constant costs nothing. The budget test pins all three so the distinction cannot rot.

Measured values come from the results files in `bench/results/` and from the `AllocsPerRun`
assertions in `alloc_test.go` and, for the `ConnState` row, `lifecycle_internal_test.go`. The
most recent recording of rice's own suite is `bench/results/M7-lifecycle.txt`: M8 changed no
root code — it added four tests and a separate comparison module — so the root suite was not
re-recorded. Re-run `make bench-record` on your own machine before comparing.

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

## What the lifecycle costs

M7 added two costs, one on every request and one at shutdown. Both are from
`bench/results/M7-lifecycle.txt`, ten samples each.

**The `ConnState` hook, per request.** rice installs `fasthttp.Server.ConnState` to track open
connections, so that a `Shutdown` whose deadline passes can close them
([ADR-0009](adr/0009-shutdown-force-closes-at-deadline.md)). Once a hook is installed, fasthttp
calls it twice per request, `StateActive` and `StateIdle`; both return before touching a lock
or the map. `BenchmarkDispatchConnStateHook`, both arms in one session:

```
                         │ dispatch.txt │       dispatch+connstate.txt       │
                         │    sec/op    │   sec/op     vs base               │
DispatchConnStateHook-12    36.94n ± 0%   39.41n ± 1%  +6.69% (p=0.000 n=10)
```

0 B/op and 0 allocs/op on both arms, pinned as a budget by `TestConnStateActiveAndIdleAreFree`.
The time is real: about 2.5 ns per request, significant at p=0.000. The design expected it to
be within noise; it is not, and it is kept because the alternatives either break TCP keep-alive
settings or let a connection serve after `Shutdown` returns. Like ADR-0008's `defer recover()`,
every App pays it whether or not it uses what it buys. The benchmark calls `connState`
directly, so the func-field load in fasthttp's `setState` is not in the figure.

**Shutdown latency.** `Shutdown` delegates the drain to fasthttp's `ShutdownWithContext`, which
checks for open connections, then again every 100 ms. `BenchmarkShutdownLatency`:

| Case | Median | Range |
| --- | ---: | --- |
| `after-last-request` — `Shutdown` started, last request released ~5 ms later | 95.20 ms | 94.78–95.81 ms |
| `idle-keepalive-only` — no request in flight, one idle keep-alive connection | 101.4 ms | 100.9–101.7 ms |

Both are one tick of the poll. The first is the tick minus the benchmark's 5 ms head start; in
a real shutdown it lands anywhere from 0 to 100 ms after the last request. The second is the
more surprising: fasthttp closes idle connections before its first check, but counts a
connection gone only when its serving goroutine exits, and that has not happened by the check
that immediately follows, so an idle server still waits one tick. Shutdown is not a hot path
and neither number is a target; they are what fasthttp gives for free.

## Where rice stands

M8 put rice beside Gin, Echo and Fiber on the same routes and payloads, on one machine, in one
recording, at two levels: each framework's own cost with a request already parsed (handler
level), and what a client in another process sees (end to end). The code is `bench/compare/`, a
separate Go module so rice's own `go.mod` still lists fasthttp alone; the numbers are
[`bench/results/M8-compare-handler.txt`](../bench/results/M8-compare-handler.txt) and
[`bench/results/M8-compare-e2e.txt`](../bench/results/M8-compare-e2e.txt), and `make
compare-record` reproduces both.

**Versions and machine.** Gin v1.12.0 (`gin.New()`, release mode, no Logger or Recovery), Echo
v5.3.1 (`echo.New()`), Fiber v3.5.0 (`fiber.New()`), rice at `b131556` through a `replace`
directive. fasthttp resolves to v1.73.0 in the comparison module, the same version rice pins, so
rice and Fiber run on the fasthttp rice ships with. One indirect dependency differs:
`klauspost/compress` is v1.19.2 in `bench/compare` and v1.19.1 at the root. Apple M2 Pro,
go1.25.6 darwin/arm64, Darwin 27.0.0, recorded 2026-09-19. **On battery power** — the user's
choice, stated in both files' `# power:` line — under `caffeinate -i`, from 75% to 17% over
about 42 minutes. Every framework ran under the same conditions and the end-to-end rounds
interleave them, so the relative positions are the claim; absolute numbers may be lower or
noisier than on mains power. The per-round mean across all 28 end-to-end pairs moved by 1.8%
(143,192 to 145,831 req/s) with no downward trend.

### Handler level

`BenchmarkHandler/<scenario>/<framework>`, ten runs each, read with `benchstat`: median time
with its ± spread, bytes and allocations per request.

| Scenario | rice | Fiber | Gin | Echo |
| --- | --- | --- | --- | --- |
| `static` | 53.66 ns ± 1%, 0 B, 0 | 65.20 ns ± 2%, 0 B, 0 | 84.83 ns ± 2%, 48 B, 1 | 107.5 ns ± 1%, 16 B, 1 |
| `param` | 59.84 ns ± 1%, 0 B, 0 | 72.80 ns ± 0%, 0 B, 0 | 93.49 ns ± 1%, 48 B, 1 | 116.5 ns ± 1%, 16 B, 1 |
| `middleware5` | 59.63 ns ± 1%, 0 B, 0 | 72.89 ns ± 1%, 0 B, 0 | 97.85 ns ± 3%, 48 B, 1 | 109.2 ns ± 2%, 16 B, 1 |
| `notfound` | 58.03 ns ± 2%, 0 B, 0 | 134.7 ns ± 1%, 0 B, 0 | 73.87 ns ± 23%, 0 B, 0 | 537.1 ns ± 4%, 464 B, 8 |
| `githubapi` | 118.8 ns ± 1%, 0 B, 0 | 680.6 ns ± 1%, 0 B, 0 | 133.9 ns ± 1%, 48 B, 1 | 177.5 ns ± 1%, 16 B, 1 |
| `json` | 216.8 ns ± 1%, 96 B, 2 | 225.1 ns ± 1%, 96 B, 2 | 243.6 ns ± 5%, 112 B, 3 | 270.1 ns ± 1%, 64 B, 2 |
| `body64k` ¹ | 68.40 ns ± 2%, 5 B, 1 | 86.27 ns ± 2%, 5 B, 1 | 32.87 µs ± 5%, 278.5 KiB, 18 | 31.91 µs ± 2%, 278.4 KiB, 18 |

¹ Not a framework comparison — see the caveats below.

These are not the same benchmark as `BenchmarkRiceDispatch` in rice's own suite (33.83 ns in
`M7-lifecycle.txt`): the harness, the routes and the session differ, and the two must not be
set against each other.

### End to end

Server and load generator as separate processes, `GOMAXPROCS=6` each on a 12-CPU machine;
64 keep-alive connections in a closed loop; 5 s warm-up, 10 s measured; five rounds, the four
frameworks interleaved within each round and rotated between rounds. Median of the five rounds.

| Scenario | rice | Fiber | Gin | Echo |
| --- | --- | --- | --- | --- |
| `static` | 166,225 req/s, p99 753 µs | 165,912, p99 655 | 158,881, p99 1,048 | 158,679, p99 1,114 |
| `param` | 163,887, p99 688 | 164,914, p99 655 | 157,517, p99 1,114 | 156,542, p99 1,114 |
| `middleware5` | 164,991, p99 655 | 165,962, p99 688 | 157,775, p99 1,114 | 159,246, p99 1,048 |
| `notfound` | 166,419, p99 786 | 165,240, p99 720 | 159,097, p99 1,048 | 157,572, p99 1,114 |
| `githubapi` | 165,815, p99 688 | 165,535, p99 753 | 155,352, p99 1,179 | 157,110, p99 1,179 |
| `json` | 163,747, p99 688 | 166,272, p99 622 | 156,801, p99 1,179 | 155,638, p99 1,114 |
| `body64k` | 85,777, p99 1,114 | 85,930, p99 1,114 | 19,062, p99 15,728 | 18,982, p99 15,728 |

The results file carries each median's min–max across the five rounds and all 140 raw lines.

### What each level can and cannot compare

**The handler level compares framework code on top of a parsed request, and nothing else.** Gin
and Echo are driven through `ServeHTTP` with a reused `*http.Request` and a minimal reusable
`http.ResponseWriter`; Fiber and rice through their fasthttp handler with a reused
`RequestCtx`, its response reset every iteration. A reused `net/http` request leaves out the
parsing and allocation `net/http`'s server does per request, and a reused `RequestCtx` leaves out
fasthttp's parsing too. What remains is routing, context and response code.

**`body64k` breaks that rule, by the transports' design.** `net/http` hands a handler an unread
body stream, so Gin and Echo read the 64 KiB inside the handler and that read is timed; a fasthttp
server reads the body into its buffer while parsing, before the handler runs, so Fiber and rice
are timed without it. The handler-level `body64k` row measures `net/http`'s handler-side body
read, not framework overhead. The end-to-end row is the comparison for `body64k`.

**The end-to-end level separates the two transports and nothing finer.** At about 166,000
requests a second on six server CPUs, each request has up to about 36 µs of server CPU; the
handler-level differences between frameworks are 0.01 to 0.6 µs. Routing, middleware and JSON
costs are invisible at this level by construction: Fiber's `githubapi` is ten times its `static`
at the handler level (680.6 against 65.20 ns) and serves the same end-to-end rate (165,535
against 165,912 req/s). Within one transport, rice against Fiber and Gin against Echo, the
medians differ by less than the min–max across rounds, and across all six light scenarios the
same-round differences go both ways, typically by 1–3%: rice was ahead of Fiber in 12 of the 30
same-round pairs, Gin ahead of Echo in 19 of 30. Per scenario they do not always change sign.
**In `json`, Fiber was ahead of rice in all five rounds**, by 0.2% to 6.0% of rice's rate; Gin was
ahead of Echo in all five rounds of `param` and of `notfound`. Five rounds with overlapping ranges
are not enough to claim an order from those — a sign test on five of five is p ≈ 0.06 — and none is
claimed; nor is the opposite. The end-to-end numbers do not rank frameworks within a transport,
and nothing here does.

**The light scenarios share a ceiling, and this setup cannot say whose it is.** rice and Fiber
reach about the same top in all six — rice's best round per scenario is between 165,908 and
167,255 req/s, Fiber's between 166,636 and 167,521 — and, in an unrecorded spot check during the
recording's review, neither process saturated its six CPUs. Server and client share
the machine's memory bandwidth and caches — splitting the CPUs removes scheduler contention, not
that — and the loopback path is shared too. Latency, connections and throughput agree with each
other (64 connections at about 166,000 req/s is about 385 µs a request, and the p50 is 360–376 µs),
so the data is self-consistent; it still cannot say whether the server or the client is the
limit. The req/s figures are therefore not a capacity figure for any framework: one laptop, on
battery, client and server on the same machine.

**Latency is read at p99, never at p50.** The load generator's histogram (`histogram.go`) has
16 buckets per power of two, each 3–6.25% of its value wide, and reports a quantile as its
bucket's lower bound — up to 6.25% low, the same for every framework. In the six light
scenarios every p50 lands in one of two adjacent buckets, 360 or 376 µs, so p50 separates
nothing. p99 is coarse too: two p99 figures one bucket apart differ by a single bucket width.

**Other things that are true of both files:**

- The equivalence gate (`TestEveryFrameworkAnswersEveryScenarioAlike`) runs before every handler
  benchmark and checks status and body, ignoring one trailing newline — Echo's JSON encoder ends
  its output with one and the others do not.
- `notfound` answers with each framework's default 404, compared by status only: rice
  `Not Found` (9 bytes), Fiber `Not Found` (9 bytes), Gin `404 page not found` (18 bytes), Echo
  `{"message":"Not Found"}` plus a newline (24 bytes). Overriding them would stop measuring each
  framework's default miss path.
- `json` uses each framework's own JSON helper; rice has none, so its handler calls
  `json.Marshal` and `c.Bytes`. rice's row measures the standard library's encoder.
- `middleware5` registers exactly five no-op middleware in every adapter. That count is held by
  review, not by a test: removing one changes no output, so the equivalence gate cannot see it.
- The load generator verifies every response's status and fails the run on any mismatch or
  error; fasthttp's idempotent-request retries are turned off so a dropped connection is counted
  rather than hidden. All 64 of its workers share one `fasthttp.HostClient`, and so its
  connection-pool lock.
- The handler-level runs of one scenario and framework are back to back, not interleaved across
  frameworks; the whole handler recording took 388.7 s and shows no visible trend.

### Per scenario, and why

**`static` and `param` — fastest at the handler level, and allocation-free.** rice is ahead of
Fiber, Gin and Echo in both, and every pairwise difference is real: the ten runs of any two
frameworks never overlap. rice allocates nothing because the `Ctx` is pooled and `c.Param`
returns a view into fasthttp's buffer —
[ADR-0005](adr/0005-context-pooling-and-borrow-contract.md), held by
`TestAllocBudgetHandleDispatch` and `TestAllocBudgetHandleDispatchParameterised`. Fiber allocates
nothing either. Why rice's time is below Fiber's is **not explained**: no profile was taken.
Where Gin's and Echo's single allocation comes from was not traced. End to end, rice and Fiber
serve 3.6–6.8% more requests a second than Gin and Echo across the six light scenarios, with p99
medians of 622–786 µs against 1,048–1,179 µs, and rice beat Gin in all 30 same-round comparisons
and Echo in 29 of 30. That is the transport,
[ADR-0001](adr/0001-use-fasthttp-as-transport.md), not rice: Fiber shows the same margin.

**`middleware5` — five middleware, no allocation.** rice 59.63 ns and 0 allocations with five
no-op middleware, against 53.66 ns and 0 for `static`; the chain is folded into one closure at
build time ([ADR-0003](adr/0003-middleware-as-prebuilt-closure-chain.md)), so a request walks no
slice and carries no cursor. The route and body differ from `static` too, so the difference in
time is not the middleware's alone. None of the four frameworks allocates more with five
middleware than without. End to end: no order within either transport can be claimed.

**`notfound` — rice at zero allocations; Gin roughly level; Echo's default is expensive.** rice's
miss goes through the same funnel as every error, answering with the prebuilt `ErrNotFound`
(`errors.go:29`) on a pooled `Ctx`, at 58.03 ns and 0 allocations
([ADR-0002](adr/0002-handler-returns-error.md), ADR-0005; `TestAllocBudget404`). Gin's ten runs
are bimodal, 55 to 85 ns (± 23%), and its fastest runs are below rice's slowest, so the two are
**roughly equal** here and no order is claimed. Fiber 134.7 ns. Echo's default miss path costs
537.1 ns, 8 allocations and 464 bytes; its cause inside Echo was not traced.

**`githubapi` — expected to lose, and did not.** The design listed the 203-route GitHub API
table as a case not expected to favour rice. At the handler level rice resolves
`GET /repos/:owner/:repo/pulls/:number/comments` in 118.8 ns with 0 allocations, ahead of Gin
(133.9), Echo (177.5) and Fiber (680.6), every difference real. rice's figure is the per-method
radix tree ([ADR-0004](adr/0004-radix-tree-router.md)) with parameters captured into
caller-supplied storage (ADR-0005). Fiber's is real Fiber behaviour on **this route**, not a
general statement about large route tables: Fiber v3.5.0 buckets routes by method and the first
three characters of the path (`router.go:1214`, `maxDetectionPaths = 3` at `ctx.go:33`) and scans
a bucket linearly (`router.go:494–530`); this route is the 28th of the table's 60 `GET /re…`
routes (`bench/compare/githubapi.go:139`), so it is reached after about 28 match attempts. Why
rice is ahead of Gin, which also walks a tree, is not explained. End to end the lookup does not
show: every framework is within its transport's range.

**`json` — rice ahead, narrowly, on the standard library's encoder.** rice 216.8 ns, Fiber 225.1,
Gin 243.6, Echo 270.1 — every difference significant, but rice leads Fiber by only about 4%, and
rice's row is `encoding/json` in the handler plus `c.Bytes`, not a rice JSON path: core has no
`c.JSON` ([ADR-0006](adr/0006-no-reflection-in-core.md) keeps reflection out of core). Its 2
allocations and 96 bytes are the handler's `json.Marshal` call — dispatch, `SetContentType` and
`Bytes` are each held at zero by their budget tests. Why the standard encoder called this way is
ahead of the other frameworks' helpers is **not explained**. End to end, the one rice–Fiber
result that did not change sign: **Fiber served more than rice in all five `json` rounds**, by
0.2% to 6.0% of rice's rate (medians 166,272 against 163,747, ranges overlapping). With five
rounds that is not enough to claim an order — a sign test on five of five is p ≈ 0.06 — so
none is claimed, in either direction; it is stated because it is the only scenario in which
rice was behind in every round.

**`body64k` — the one scenario that separates the transports by a factor, and rice is level with
Fiber.** End to end, rice (85,777 req/s) and Fiber (85,930) serve about 4.5 times what Gin
(19,062) and Echo (18,982) do, with a p99 of 1,114 µs against 15,728 µs; rice's lead over Gin holds
in every round, +331% to +367%. That is the transport ([ADR-0001](adr/0001-use-fasthttp-as-transport.md)):
the same margin appears for Fiber, and rice's handler reads the body straight from fasthttp's
buffer through `c.RequestCtx().PostBody()`, the documented escape hatch. Inside
`net/http` the mechanism is **not established**. It is `net/http`'s body path, which in one
unrecorded spot check of the Gin server was GC-heavy — about 1,360 collections a second, GC
about a quarter of its CPU; the handler level shows Gin and Echo allocating 278.5 KiB and 18
objects per request there. It is not simply the handler-level read: 32 µs at 19,000 requests a
second is about 0.6 of the server's six CPUs. At the handler level rice's 1 allocation of 5 bytes
is the handler's own `strconv.Itoa` of the length, 65536, in `bench/compare/rice.go`; Fiber's
handler makes the same call (`fiber.go`).

**What rice does not win.** No scenario in this recording shows rice behind another framework by
a margin the data can separate — and that is not the same as rice being faster where a client
can see it. The three cases chosen because rice was expected to lose did not produce a loss:
`githubapi` and `json` went to rice at the handler level, and `body64k` is a tie with Fiber
decided by the transport. End to end, no order between rice and Fiber can be claimed in any
scenario — in `json` Fiber was ahead in all five rounds, too few to claim an order; its
handler-level lead of 11.5–13 ns over Fiber on `static` and `param` is below what a client over a
socket can observe here, and the one gap a client does see, `body64k`, belongs to fasthttp. What
the design decisions buy is visible in the allocation column and at the handler level. What a
client of this setup sees is ADR-0001.

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
