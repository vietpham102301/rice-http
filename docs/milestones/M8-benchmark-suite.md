# M8 — Benchmark suite and retrospective

Status: done
Started: 2026-09-19
Finished: 2026-09-19

## Goal

rice has been measured against Gin, Echo and Fiber on the same routes and payloads, on one
machine, in one recording, at two levels. `bench/compare/` is a separate Go module — rice's own
`go.mod` still lists fasthttp alone — holding one scenario table, one adapter per framework, an
equivalence gate every benchmark passes before it produces a number, handler-level benchmarks, a
closed-loop load generator, a server binary and a summariser. `make compare` runs the gate and a
smoke run, and CI runs it; `make compare-record` writes
`bench/results/M8-compare-handler.txt` and `bench/results/M8-compare-e2e.txt`. The allocation
budget table in [05-performance-model.md](../05-performance-model.md) now has a row, and a test,
for every exported `Ctx` method: four new budget tests (`Status`, `SetHeader`,
`SetContentType`, `RequestCtx`), and `c.Query`, `c.Header` and `c.JSON` marked "not
implemented" instead of carrying a target. No rice code changed. The project retrospective is
[07-retrospective.md](../07-retrospective.md).

## Question this milestone answers

Where does rice stand, and — more importantly — why, in terms of the design decisions recorded in
the ADRs?

**At the handler level, ahead of all three in five scenarios, at zero allocations in every
scenario but `json` and `body64k`; end to end,
level with Fiber and ahead of Gin and Echo by the transport.** In full, with the tables, in
[05-performance-model.md — Where rice stands](../05-performance-model.md#where-rice-stands). In
short:

- Handler level, framework code on top of a parsed request: rice is fastest in `static`,
  `param`, `middleware5`, `githubapi` and `json`, every difference real (the ten runs of any
  two frameworks never overlap), and allocates nothing in the first four (in `json` its 2
allocations are the handler's `json.Marshal`). That is
  [ADR-0005](../adr/0005-context-pooling-and-borrow-contract.md) — the pooled `Ctx` and borrowed
  parameters — in the allocation column,
  [ADR-0003](../adr/0003-middleware-as-prebuilt-closure-chain.md) in `middleware5` and
  [ADR-0004](../adr/0004-radix-tree-router.md) in `githubapi`. Why rice's time is below Fiber's
  is not explained; no profile was taken.
- End to end, a client in another process: the level separates the two transports and nothing
  finer. rice and Fiber serve 3.6–6.8% more requests a second than Gin and Echo in the six light
  scenarios, and about 4.5 times as many in `body64k`. That is
  [ADR-0001](../adr/0001-use-fasthttp-as-transport.md). No order between rice and Fiber can be
  claimed in any scenario. In `json` Fiber was ahead in all five rounds, by 0.2–6.0%; five
  rounds with overlapping ranges are too few to claim an order, and none is claimed.

So the "why" divides cleanly by level. What rice's own decisions buy shows in the allocation
column and in nanoseconds of framework code; what a client over a socket sees in this setup is
the choice of fasthttp.

## Design notes

### A separate module, and what it pinned

The comparison needs Gin, Echo and Fiber; rice's import graph is one dependency on purpose. A
nested module with `replace github.com/vietpham102301/rice-http => ../..` keeps both true, and
the root's `go test ./...` does not descend into it, so no existing target changed. The design
worried that Fiber might pull a newer fasthttp than rice's v1.73.0 pin through minimal version
selection and so measure rice on a version it does not ship with. It did not: fasthttp resolves
to v1.73.0 in `bench/compare` too, and both results headers say so. One indirect dependency did
move — `klauspost/compress` is v1.19.2 in `bench/compare` against v1.19.1 at the root — and
is recorded here because the design asked for any version difference to be.

### Two apps per framework

The 203-route GitHub API table, copied unedited from julienschmidt/go-http-routing-benchmark,
declares `/users/:user`; the `param` scenario declares `/users/:id`. rice and Gin reject two
parameter names at one position, so one app cannot serve both. Each adapter builds a *small* app
with every scenario but `githubapi`, and a *github* app with the 203 routes and nothing else —
which also keeps the other scenarios from being measured inside a 203-route tree.
`TestEveryFrameworkServesTheWholeGitHubTable` sends one request to each of the 203 routes in every
framework, so a route one framework silently rejected cannot hide behind the scenario's single
lookup, and `TestTheGitHubTableIsThePublishedOne` pins the table's length and its first and last
entries.

### The equivalence gate, and its one-newline rule

`Check` sends a scenario to a fresh app and compares status and body; `TestEveryFrameworkAnswersEveryScenarioAlike`
runs it for all 28 pairs, and `BenchmarkHandler` calls it before timing each one, so a broken
adapter fails the run instead of producing a number for different work. Two exceptions, both
written into `check.go`. It ignores one trailing newline, because Echo's JSON
encoder ends its output with one and the others do not — a difference in bytes, not in work.
And `notfound` is compared by status only, because each framework answers with its default 404
and overriding those would stop measuring the default miss path: rice and Fiber `Not Found`
(9 bytes), Gin `404 page not found` (18), Echo `{"message":"Not Found"}` and a newline (24),
measured through `Do` while writing this retrospective.

What the gate cannot see is how much work produced the answer. `middleware5` must run exactly
five no-op middleware in every adapter, and removing one changes no output (injection 9 below).
That count is held by review of the four adapters, not by a test — a ruling, recorded here
because it is the one property of the comparison nothing mechanical guards.

### The handler level's fairness limit, and `body64k`

Gin and Echo are driven through `ServeHTTP` with a reused `*http.Request` and a minimal reusable
`ResponseWriter` — `httptest.ResponseRecorder` grows a buffer per response and would charge that
to the framework — and rice and Fiber through their fasthttp handler on a reused `RequestCtx`.
Neither side's parsing is in the number. That is what the design said, and it holds for six
scenarios. Task 4's review found it false for the seventh, and the benchmark's own doc comment
had said "excludes parsing for both". `net/http` hands a handler an unread body stream, so Gin
and Echo read the 64 KiB body inside the timed handler; a fasthttp server reads the body into
its buffer while parsing, before the handler, so rice and Fiber are timed without it. The ruling
was to keep the design and label it, not to re-arm the fasthttp body inside the loop: that would
charge rice and Fiber a copy their server does not make in that form. The handler-level
`body64k` row measures `net/http`'s handler-side body read, not framework overhead; the
end-to-end row is the comparison. The same fix added a `ctx.Response.Reset()` to every fasthttp
iteration, as a real server resets per request, which kept the zero-allocation rows at zero.

### The load generator

A closed loop on one `fasthttp.HostClient`: `-conns` keep-alive connections (64), each sending
its next request when the last one answers; a warm-up (5 s) whose requests are not counted, then
a measured window (10 s). Latencies go into a log-linear histogram of 1024 fixed buckets whose
`Record` does not allocate (`TestHistogramRecordDoesNotAllocate`), so the generator adds no
garbage to the machine it measures; a quantile is its bucket's lower bound, up to 6.25% low
(`TestHistogramQuantilesAreWithinOneSixteenth`). Every response's status is checked, and one
mismatch or error fails the run. `e2e.sh` starts each server with `GOMAXPROCS` set to half the
CPUs and the load generator with the other half, five rounds, the four frameworks interleaved
within a round and rotated between rounds so drift hits them alike, and kills and waits for
every server it started, on success, on failure and on interrupt. It also fails if the server it
started for a measurement is gone when the measurement ends, so a server that could not bind its
port cannot have another process's numbers recorded under its name. The interrupt handling and
that check were added by the final review's fixes, after the recording: before them a Ctrl-C
killed the script but left the running server behind, and a server that failed to start went
unnoticed. The recorded run's 140 raw lines all passed the load generator's status checks; that
each came from the server the script had started for it was not checked at the time.

Two rulings shaped it after review. **Retries are off.** fasthttp's `HostClient` retries an
idempotent request up to five times by default on any read error, so for every scenario but the
`body64k` POST a dropped connection or a timeout was invisible — counted as a success, its
reconnect folded into latency. `MaxIdemponentCallAttempts: 1`, a per-request timeout, and a
worker that stops at its first error now make every failure count once. **The test for it had
to be rewritten to guard it** — injections 16 and 17 below.

### Recorded on battery

The user chose to record on battery rather than wait for mains power. The ruling was to record
under `caffeinate -i`, so the machine could not sleep, and to add a `# power:` line to the results
header, which both files carry: `'Battery Power'`, 75% at the start and 17% at the end of about
42 minutes. The four frameworks ran under the same conditions and the end-to-end rounds
interleave them, so relative positions are the claim; the absolute figures may be lower or
noisier than on mains power. The per-round mean across all 28 end-to-end pairs moved 1.8%, with
no downward trend.

### Smaller rulings

- **The GitHub table's regeneration command was fixed in the plan.** `githubapi.go`'s header
  points readers at the plan's command for regenerating the table, and that command ran
  `go mod download` in an empty temporary directory, which fails there. It gained
  `go mod init tmp` first, so the pointer leads to a command that works; the generated file was
  unchanged.
- **`cmd/loadgen` rejects bad flags.** `-duration` of zero or less, a negative `-warmup` and an
  empty `-framework` exit 2 with a message. An empty framework name would have produced a result
  line with five fields instead of six, which the summariser rejects only after a whole run.
- **The recording ran in the background.** `make compare-record` takes about 40 minutes, longer
  than one foreground tool call may run, so it was started in the background and its completion
  awaited rather than polled.

## Exit criteria

- [x] `make compare-record` reproduces both results files with one command —
      `bench/results/M8-compare-handler.txt` (280 benchmark lines) and
      `bench/results/M8-compare-e2e.txt` (seven tables, 140 raw lines)
- [x] The equivalence gate and the GitHub-table registration test pass for all four frameworks —
      `TestEveryFrameworkAnswersEveryScenarioAlike`, `TestEveryFrameworkServesTheWholeGitHubTable`
- [x] The results include the three cases rice was expected to lose (`githubapi`, `json`,
      `body64k`), and what they showed is stated, including that none was a loss
- [x] Every exported `Ctx` method has a budget row backed by a test; `grep -n TARGET
      docs/05-performance-model.md` prints nothing
- [x] [07-retrospective.md](../07-retrospective.md) names three things that surprised the author
- [x] `make lint`, `make test`, `make test-debug`, `make cover`, `make bench` and `make compare`
      all exit 0; the root `go.mod` and `go.sum` are unchanged

M8's `☐` in `docs/04-roadmap.md` is now `☑`, and so is every milestone's.

## Measurements

The full tables, the caveats and the per-scenario explanations are in
[05-performance-model.md — Where rice stands](../05-performance-model.md#where-rice-stands).
The headline rows, with the columns grouped by transport (rice and Fiber on fasthttp, Gin and
Echo on `net/http`) rather than in the design's rice, Gin, Echo, Fiber order:

| Case | rice | Fiber | Gin | Echo |
| --- | ---: | ---: | ---: | ---: |
| Handler, `static` — ns/op, allocs | 53.66, 0 | 65.20, 0 | 84.83, 1 | 107.5, 1 |
| Handler, `githubapi` — ns/op, allocs | 118.8, 0 | 680.6, 0 | 133.9, 1 | 177.5, 1 |
| Handler, `json` — ns/op, allocs | 216.8, 2 | 225.1, 2 | 243.6, 3 | 270.1, 2 |
| Handler, `notfound` — ns/op, allocs | 58.03, 0 | 134.7, 0 | 73.87 ± 23%, 0 | 537.1, 8 |
| End to end, `static` — median req/s | 166,225 | 165,912 | 158,881 | 158,679 |
| End to end, `body64k` — median req/s, p99 | 85,777, 1,114 µs | 85,930, 1,114 µs | 19,062, 15,728 µs | 18,982, 15,728 µs |

The four new budget tests hold at zero. The root suite was not re-recorded — no root code
changed — so `bench/results/M7-lifecycle.txt` remains the reference for rice on its own.

Hardware, Go version, OS: Apple M2 Pro, go1.25.6 darwin/arm64, Darwin 27.0.0 arm64, on battery.
Handler level recorded starting 2026-09-19T03:54:28Z; the end-to-end file's `# date:`,
2026-09-19T04:36:05Z, is the **end** of its roughly 35-minute run, and its `# power:` line the
battery state at the end — the committed `e2e.sh` wrote its header after the run, unlike
`handler.sh` and `scripts/bench.sh`, which stamp at the start. `e2e.sh` now takes its header
before the run and adds the line that server and load generator share memory bandwidth and
caches; the committed file predates that fix and lacks the line, and it was not re-recorded. Gin
v1.12.0, Echo v5.3.1, Fiber v3.5.0, fasthttp v1.73.0; rice at `b131556`.
Raw output: [`bench/results/M8-compare-handler.txt`](../../bench/results/M8-compare-handler.txt),
[`bench/results/M8-compare-e2e.txt`](../../bench/results/M8-compare-e2e.txt).

## What the numbers contradicted

**The design's "expected to favour: not rice" column.** The scenario table marked `githubapi`,
`json` and `body64k` as cases rice was not expected to win, and the honesty rules require such
cases so the suite is not a marketing document. None produced a loss. `githubapi` and `json`
went to rice at the handler level; `body64k` is a tie with Fiber, decided by the transport. The
loser on the many-route table was Fiber, by a mechanism specific to this route. The cases are
kept and reported as they came out: a case chosen to be a loss that turned out not to be one is a
finding about the prediction, and dropping it would be tuning the suite after the fact.

**The design's "where is two numbers per scenario".** The design expected each scenario to be
placed twice. End to end places it only by transport: at about 166,000 requests a second on six
server CPUs each request has up to about 36 µs of server CPU, and the frameworks' handler-level
differences in the light scenarios are 0.01 to 0.6 µs. Fiber's `githubapi`, ten times its
`static` at the handler level, serves the same end-to-end rate. The second number per scenario is
really one number per transport.

**The benchmark's own comment,** "excludes parsing for both", which was false for `body64k`, as
above.

**Two readings in the milestone's own reports.** Task 8's report called Gin's and Echo's
handler-level `body64k` cost "harness set-up"; it is `net/http`'s body read inside the handler,
by that transport's design. And the review of the recording counted rice ahead of Gin in "all 35
same-round comparisons across the six non-body scenarios": six scenarios over five rounds is 30,
and rice is ahead in all 30 — the conclusion held and the count did not. Neither reached these
docs, and both are recorded because the project has a habit of finding its errors in its own
summaries.

**M3's retrospective,** which said M8 "is the milestone that owns profiling" and put the 43 ns
header write first on its list. M8 took no profile. The header write, and every other number on
the unexplained list, is still unexplained; so are the new ones below.

## Retrospective

**What surprised me:**

*The scenarios built to be losses were not.* The many-route case was chosen because a radix tree
built for a learning project should lose to routers tuned for years. rice's tree resolved the
route in 118.8 ns, ahead of Gin's, and the framework that lost it did so by 5.7 times, through a
bucket-and-scan design that meets this route 28th in its bucket. The prediction was not wrong
about trees in general; it was wrong about which design a 203-route table punishes, and only
measuring found that out.

*The end-to-end level erased almost everything the handler level found.* The handler-level
table orders the four frameworks cleanly, with no overlap between any two in five scenarios.
End to end, rice's and Fiber's ranges overlap, and so do Gin's and Echo's; across the light
scenarios the same-round differences go both ways, though not in every scenario — Fiber was
ahead of rice in all five `json` rounds, Gin ahead of Echo in all five of `param` and of
`notfound`, too few rounds to claim an order. The only line that survives is the one between fasthttp and `net/http`. Two numbers per scenario were
planned; the setup delivered one per transport. The difference between a nanosecond-scale
framework cost and a microsecond-scale request is not subtle once written down, and it was not
written down before the recording.

*The most important fix was to the load generator, not to anything measured.* With fasthttp's
default retries, a load generator that "verifies every response" would have counted a dropped
connection as a success and folded the reconnect into latency. Nothing in the results would have
looked wrong. The review found it by reading `HostClient`'s defaults, the same way M7 found
fasthttp's `stop` flag: the dependency does something sensible for its own users that is wrong
for a measurement.

**What I would do differently:**

Decide in the design what each level can separate before choosing it. A back-of-envelope
division — six CPUs, the expected rate, the handler differences — would have said that the
end-to-end level cannot rank frameworks within a transport, and the design could have said so
instead of the review of the recording. The level is still worth having: `body64k` is the
scenario where the client's view differs by a factor, and the handler level cannot show it.

Interleave the handler-level runs across frameworks, as the end-to-end script does. The ten runs
of each pair ran back to back; the recording took 388.7 s and shows no trend, but on battery,
across a 75%-to-17% discharge, that was luck rather than design.

Record on mains power, or record twice. The ruling made the battery visible in the files, which
is the honest minimum. Relative positions are the claim, and they are robust in the cases the
review checked; a second recording on mains power would say whether the absolute figures moved.

**Fault injections, and what each printed.**

Every guard below was broken on purpose, the listed test run, and the code restored.

1. **Task 1 — `sink = fmt.Sprint(code)` added to `Status`.**
   ```
   Ctx.Status allocated 1.0 objects per call, budget is 0
   ```
2. **Task 1 — `sink = fmt.Sprint(key)` added to `SetHeader`.**
   ```
   Ctx.SetHeader allocated 2.0 objects per call, budget is 0
   ```
3. **Task 1 — `sink = fmt.Sprint(value)` added to `SetContentType`.**
   ```
   Ctx.SetContentType allocated 2.0 objects per call, budget is 0
   ```
4. **Task 1 — `sink = fmt.Sprint(c.fctx)` added to `RequestCtx`.**
   ```
   Ctx.RequestCtx allocated 9.0 objects per call, budget is 0
   ```
5. **Task 2 — rice's `/users/:id` handler answering `"43"`.**
   ```
   compare_test.go:25: rice param: body "43", want "42"
   ```
6. **Task 2 — rice's GitHub loop skipping the last route.**
   ```
   compare_test.go:42: DELETE /user/keys/vid: got 404 "Not Found", want 200 "ok"
   ```
7. **Task 2 fix round — the first two GitHub table entries swapped.** Added after review: the
   test had never been broken on purpose.
   ```
   compare_test.go:13: first route = {GET /authorizations/:id}, want GET /authorizations
   ```
8. **Task 3 — Echo's `/json` answering `{"id":42}`.**
   ```
   compare_test.go:25: echo json: body "{\"id\":42}", want "{\"id\":42,\"name\":\"rice\",\"tags\":[\"a\",\"b\"]}"
   ```
9. **Task 3 — one `fiberNoop` removed from `/mw`.** **Stayed green, as the brief predicted.**
   Four no-op middleware and five produce the same bytes, so the equivalence gate cannot see
   the difference. The middleware count is held by review.
10. **Task 3 — Gin's GitHub routes registered as `rt.Path + "x"`.** Gin panicked at
    registration, which the test harness reports as the `gin` subtest failing:
    ```
    panic: ':id' in new path '/notifications/threads/:id/subscriptionx' conflicts with existing wildcard ':idx' in existing prefix '/notifications/threads/:idx' [recovered, repanicked]
    ```
11. **Task 4 — the `static` scenario's expected body changed to `"hellO"`.** All four
    benchmarks failed at the gate before timing:
    ```
    handler_bench_test.go:22: equivalence gate: rice static: body "hello", want "hellO"
    ```
    and the same line for `gin`, `echo` and `fiber`.
12. **Task 5 — the mismatch and error check removed from `Load`.**
    ```
    loadgen_test.go:86: Load returned <nil>, want a mismatch error
    ```
13. **Task 5 — `bucketOf`'s `e - 4` changed to `e - 3`.**
    ```
    loadgen_test.go:26: Quantile(0.5) = 507.904µs, want within 1/16 below 500µs
    loadgen_test.go:26: Quantile(0.99) = 1.015808ms, want within 1/16 below 990µs
    ```
14. **Task 5 — `WaitReady` made to ignore its deadline,** run under `-timeout 3s`. The test
    failed by not finishing, and the watchdog's stack pinned the hang in `WaitReady`:
    ```
    panic: test timed out after 3s
    	running tests:
    		TestWaitReadyTimesOut (3s)
    ```
15. **Task 5 — `fmt.Sprintf` added to `Histogram.Record`.**
    ```
    loadgen_test.go:34: Record allocates 2 times, want 0
    ```
16. **Task 5 fix round 1 — `MaxIdemponentCallAttempts: 1` removed.**
    **`TestLoadFailsWhenTheServerNeverAnswers` stayed green.** It asserted an error, an error
    count and a 2 s bound; with retries back, `Load` still failed, in about 0.51 s instead of
    0.10 s — five attempts of 100 ms — and the bound accepted that. A timing assertion cannot tell
    "retried" from "slow". Counted below.
17. **Task 5 fix round 2 — the same injection, against the rewritten test**, which now counts the
    connections the never-answering listener accepts:
    ```
    loadgen_test.go:157: accepted 10 connections, want exactly 2 (one attempt per worker, no retries)
    ```
18. **Task 6 — `Summarize`'s round-number check removed.**
    ```
    loadgen_test.go:206: Summarize accepted a round that is not a number
    ```
19. **Task 6 — rice's `/hello` answering status 201,** through a short `e2e.sh` run. Exit 1:
    ```
    loadgen: rice static: static: 43265 status mismatches, 0 errors
    e2e: rice static round 1 failed
    ```
    Repeated after the fix round that added a `wait` to the failure path: the same two lines,
    exit 1, and `pgrep` found no server left running.
20. **Final-review fixes — `Summarize`'s framework and scenario checks disabled** (`false &&`
    added to both conditions):
    ```
    loadgen_test.go:217: Summarize("rice static 1 100 10 20\nchi static 1 100 10 20\n") = <nil>, want line 2: unknown framework "chi"
    loadgen_test.go:217: Summarize("rice statik 1 100 10 20\n") = <nil>, want line 1: unknown scenario "statik"
    ```
21. **Final-review fixes — `BenchmarkHandler` timing the GitHub app** (`tg.Build(GitHub)` in
    place of `tg.Build(s.App)`), now that the gate runs on the timed instance. Before the fix the
    gate built its own app and this injection would have passed. For every framework:
    ```
    handler_bench_test.go:33: equivalence gate: rice param: body "ok", want "42"
    ```
    A first attempt, `tg.Build(Scenarios()[0].App)`, injected nothing: `static` and `param` are
    served by the same app, and the benchmark rightly passed.
22. **Final-review fixes — `e2e.sh`'s server bound out of its port:** a leftover rice `static`
    server started by hand on 127.0.0.1:18001, the port of round 1's first measurement, then a
    one-round run with 1 s windows. The script's own server failed to bind, the load generator
    measured the leftover and passed its status checks, and the new check caught it. Exit 1, no
    results file written:
    ```
    server: listen tcp 127.0.0.1:18001: bind: address already in use
    e2e: rice static round 1: the server started on 127.0.0.1:18001 exited before the measurement ended; the numbers are not its own
    ```
    The interrupt path was demonstrated rather than injected: SIGINT and SIGTERM sent to a running
    `e2e.sh` mid-measurement ended it at once (exit 130 and 143), and `pgrep` found no server or
    load generator left.

The line numbers are those at the time of each run.

Never broken on purpose, and recorded as deferred: nothing pins that the warm-up window's
requests are left out of the counts — replacing the window check with `true` would pass every
test.

**Guards, and the count.**

M7 closed at fourteen. M8 adds one:

- **`TestLoadFailsWhenTheServerNeverAnswers` as first committed.** Written in the fix round for
  one purpose — to guard the retry setting the review had found — and, with that setting
  removed, it passed. Its assertions were real and its injection was real; its only contact with
  the invariant was a time bound loose enough to accept five retries. Caught from the fix round's
  own injection record by the controller, not by review of the test, and replaced with a
  deterministic count before the task closed. **Counted.** It is M7's species in a different
  form: M7's guard was probabilistic, this one was tolerant, and both passed a normal run with
  their invariant broken.

Not counted, for the reason M4 through M7 gave — the tally is of guards that turned out not to
guard:

- **The `middleware5` count (injection 9).** It was never claimed to be guarded by a test; the
  brief predicted the injection would stay green, and the gap is documented where the numbers
  are. An unguarded property, stated as one, is not a guard that failed.
- **`TestTheGitHubTableIsThePublishedOne` never having been broken.** When it was, it failed as
  it should.
- **The benchmark's "excludes parsing for both" comment.** A false claim, corrected — but it
  guarded nothing.

**Fourteen plus one is fifteen.**

**What I still do not understand:**

Why rice's handler-level time is below Fiber's. Both are on fasthttp v1.73.0, both allocate
nothing in every scenario but `json` and `body64k`, and rice is 11.5–13 ns faster in `static` and `param` with no
overlap in ten runs. No profile was taken of either. It joins the unexplained list with M1's
43 ns header write, M2's ~9 ns of routing, M3's 15.93 ns of tree scaling and M4's ~1.1 ns per
middleware — and like them it is bounded and reproducible and has no mechanism behind it.

What makes `net/http`'s body path 4.5 times slower end to end. The handler-level read explains
at most about 0.6 of the server's six CPUs at 19,000 requests a second. One unrecorded spot
check showed the Gin server collecting garbage about 1,360 times a second, GC about a quarter of
its CPU — a description of where the time goes, not why. It was not profiled, and it is Gin's and
Echo's code rather than rice's, but it is the largest gap the comparison found.

Which side of the end-to-end setup is the limit in the light scenarios. The fasthttp frameworks
reach about the same top in every one, neither process saturated its CPUs in an unrecorded
spot check during the recording's review, and the numbers are
self-consistent. A second machine for the client would answer it; this project had one.
