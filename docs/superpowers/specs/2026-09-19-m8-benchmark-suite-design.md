# M8 — Benchmark Suite and Retrospective: Design

**Status:** approved, not yet implemented
**Date:** 2026-09-19
**Milestone:** M8 in [docs/04-roadmap.md](../../04-roadmap.md)
**Depends on:** M1–M7 (everything measured here), the honesty rules in
[05-performance-model.md](../../05-performance-model.md)

## Goal

Say where rice stands against Gin, Echo and Fiber, on identical routes and payloads, in one
run on one machine, and say why in terms of the ADRs. Complete the allocation budget table
for every public method on the request path, replace every remaining TARGET in the
performance model with a measured value or "not implemented", and close the project with a
retrospective.

## Question this milestone answers

Where does rice stand, and — more importantly — why, in terms of the design decisions
recorded in the ADRs?

"Where" is two numbers per scenario: the framework's own cost (handler level) and what a
client sees (end to end). "Why" is the retrospective's job: each gap, won or lost, is traced to
an ADR or to the transport, or admitted as unexplained.

## Non-goals

- **New API.** No `c.JSON`, `c.Query` or `c.Header`. Settled in brainstorming: M8 measures,
  it does not build. The JSON scenario encodes in the handler for rice (below).
- **Tuning rice to win.** No change to rice's code is made because of a comparison result. A
  loss is recorded and explained, not fixed in this milestone.
- **Running the end-to-end comparison in CI.** Shared CI runners are too noisy for throughput
  numbers; CI runs the equivalence test and a smoke run only.
- **Comparing against numbers from other projects' READMEs.** Forbidden by the honesty rules.

## Decisions

### D1: The comparison is a separate Go module, `bench/compare/`

`bench/compare/go.mod` declares module `github.com/vietpham102301/rice-http/bench/compare`,
requires rice through `replace github.com/vietpham102301/rice-http => ../..`, and requires
Gin, Echo and Fiber. rice's own `go.mod` is untouched: fasthttp stays its only dependency, and
CI's `go mod tidy` check on the root module is unaffected. `go test ./...` at the root does not
descend into a nested module, so the existing `make` targets are unaffected too.

**Rejected: test-only dependencies in the root module.** They would still appear in the root
`go.mod` and `go.sum`, and the one-dependency import graph is part of the design
(principle: the import list is the honest signal of what costs what).
**Rejected: a separate repository.** Results would drift from the code they describe.

**fasthttp version inside the comparison.** Fiber requires fasthttp too, and Go's minimal
version selection may resolve a newer fasthttp in `bench/compare` than the v1.73.0 rice pins.
If it does, rice is measured on that version in the comparison. The results header records the
fasthttp version resolved in `bench/compare/go.mod`, and if it differs from the root pin, the
M8 retrospective says so and the rice-only numbers in `bench/results/M7-lifecycle.txt` remain
the reference for rice on its pinned version.

**Framework versions.** The latest release of each framework's current major version that
builds with Go 1.25, resolved by `go get` at implementation time and pinned in
`bench/compare/go.mod`. Each results file's header lists all four module versions.

### D2: One scenario table, four adapters, and an equivalence gate

`bench/compare/scenarios.go` defines the scenarios once: method, path, request body, expected
status, expected response body. Each framework has one adapter file (`rice.go`, `gin.go`,
`echo.go`, `fiber.go`) that builds an app serving every scenario in that framework's idiomatic
form:

| Framework | Construction | Notes |
| --- | --- | --- |
| rice | `rice.New()` | handlers use `c.Param`, `c.Bytes`, `c.String` |
| Gin | `gin.New()`, `gin.SetMode(gin.ReleaseMode)` | no Logger, no Recovery |
| Echo | `echo.New()` | no middleware added |
| Fiber | `fiber.New()` | default config |

No framework gets middleware or options the others lack, beyond each one's documented default.
Where a default differs materially (for example a response header one framework adds), the
equivalence test compares status and body only, and the difference is noted in the results.

The one scenario whose body is each framework's own is `notfound`: every framework answers a
miss with its default 404, and those bodies differ by design (plain text in rice and Gin, JSON
in Echo, a "Cannot GET" message in Fiber). Overriding them would stop measuring each
framework's default miss path. For `notfound` the equivalence gate checks the status only, and
the results list each framework's 404 body length.

**Scenarios:**

| Name | Request | Handler does | Expected to favour |
| --- | --- | --- | --- |
| `static` | `GET /hello` | write `hello` as text | — |
| `param` | `GET /users/42` | write the `id` parameter | — |
| `middleware5` | `GET /mw` | five no-op middleware, then write `ok` | — |
| `notfound` | `GET /nope` | framework's default 404 | — |
| `githubapi` | `GET /repos/:owner/:repo/pulls/:number/comments` (filled) on the GitHub API route table | write `ok` | not rice: many-route lookup |
| `json` | `GET /json` | encode `{"id":42,"name":"rice","tags":["a","b"]}` | not rice: JSON-heavy |
| `body64k` | `POST /echo-len` with a 64 KiB body | write the body length as text | not rice: large body |

The GitHub API route table is the 203-route set from julienschmidt/go-http-routing-benchmark,
registered identically in all four frameworks (all four accept `:name` parameters). It is
copied into `bench/compare/githubapi.go` with its source and licence noted. The route table is
used as published, never reordered or trimmed — the honesty rule against tuning a table to the
radix tree's shape.

**JSON.** Each framework uses its own idiomatic JSON response helper (`c.JSON` in Gin, Echo and
Fiber). rice has none (non-goal), so its handler calls `json.Marshal` and `c.Bytes` with the
content type set. The results say so: the JSON row measures each framework's JSON path, and
rice's path is the standard library's.

**The 64 KiB body.** rice reads it through `c.RequestCtx().PostBody()`, the documented
escape hatch; the others through their idiomatic body accessor.

**The equivalence gate.** `TestEveryFrameworkAnswersEveryScenarioAlike` (in `bench/compare`)
sends every scenario to every framework through its handler-level entry point and asserts the
expected status and body (status only for `notfound`, above). A benchmark that ran without this passing would compare different
work. The handler benchmarks call it first via a shared setup check, so a broken adapter fails
the benchmark run rather than producing a number.

### D3: Handler level — each framework's in-process entry point

`bench/compare/handler_bench_test.go`, one `BenchmarkHandler/<scenario>/<framework>` per pair.

- **Gin, Echo:** `ServeHTTP` on a reused `*http.Request` (body re-armed per iteration for
  `body64k`) and a reusable `discardWriter` — a minimal `http.ResponseWriter` that keeps a
  reusable header map and a status, and discards the body. `httptest.ResponseRecorder` is
  rejected: it grows a buffer per response, and those allocations would be charged to the
  framework.
- **Fiber, rice:** the fasthttp request handler (`app.Handler()` for Fiber,
  `app.FasthttpHandler()` for rice) on a reused `*fasthttp.RequestCtx`.
- Each iteration issues one request. `b.ReportAllocs()`. Recorded with `-count=10` and read
  with `benchstat`.

**What this level does not compare fairly, stated in the results and in the performance model:**
`net/http` and fasthttp frameworks are driven through different request objects. A `net/http`
request built once and reused excludes the parsing and allocation that `net/http`'s server
does per request; a reused `RequestCtx` excludes fasthttp's parsing too. The handler level
measures the framework's routing, context and response code on top of an already-parsed
request, and nothing else. The end-to-end level exists because of this.

### D4: End to end — separate processes, one load generator

- **`bench/compare/cmd/server`** — one binary, `-framework rice|gin|echo|fiber -addr`, serving
  every scenario through that framework's own server (`Run`/`Listen`/`http.Server`), with
  default settings except what D2 states.
- **`bench/compare/cmd/loadgen`** — a closed-loop generator on `fasthttp.HostClient`: `-conns`
  keep-alive connections (default 64), each looping one request at a time; `-warmup` (default
  5s), then `-duration` (default 10s) measured. It reports requests per second and latency
  percentiles p50/p99 (from an HDR-style histogram of fixed buckets, no per-request allocation
  in the measuring path), and verifies every response's status against the scenario, counting
  mismatches; any mismatch fails the run.
- **`bench/compare/scripts/e2e.sh`** — for each framework × scenario: start the server with
  `GOMAXPROCS` = half the CPUs, wait until it answers, run the load generator with the other
  half, stop the server; five rounds, interleaving frameworks within each round so drift hits
  all four alike. Writes `bench/results/M8-compare-e2e.txt` with the same header as
  `scripts/bench.sh` (date, Go, OS, CPU) plus module versions, CPU split and loadgen settings,
  and a table of median req/s, p50 and p99 per framework × scenario, with the min–max across
  rounds.

Server and client on one machine share memory bandwidth and caches; splitting CPUs removes
scheduler contention, not that. The results header says so.

### D5: The allocation budget table covers every public method on the request path

New `AllocsPerRun` tests in `alloc_test.go` for the `Ctx` methods that lack one: `Status`,
`SetHeader`, `SetContentType`, `RequestCtx`. Budget 0 each, expected to hold (they delegate to
fasthttp's reused buffers); if one does not, that is a finding, recorded, not hidden.

`05-performance-model.md`'s table is reorganised into three groups:

1. **Request path — `Ctx` methods.** Every exported `Ctx` method, one row each, with its budget,
   status and the test that enforces it.
2. **Request path — dispatch.** The existing dispatch, chain, pool, 404, error, panic and
   `ConnState` rows.
3. **Not on the request path.** Registration (`Handle`, the verb helpers, `Use`, `Group`),
   `Build`, lifecycle (`Run`, `Serve`, `RunContext`, `Shutdown`, `Addr`, `OnStart`,
   `OnShutdown`) and options: listed once, marked "not budgeted — runs once per App".

`c.Query`, `c.Header` and `c.JSON` change from TARGET to "not implemented", with a pointer to
the roadmap's "Explicitly deferred". No TARGET remains in the document.

### D6: Reproducible with one command each

Root `Makefile` gains:

- `compare` — `cd bench/compare && go test ./... -run . -bench . -benchtime=100ms -count=1`:
  the equivalence test plus a handler-level smoke run. CI runs this.
- `compare-record` — the handler level with `-count=10` into
  `bench/results/M8-compare-handler.txt` (same header as `scripts/bench.sh`, plus module
  versions), then `bench/compare/scripts/e2e.sh` into `bench/results/M8-compare-e2e.txt`.

CI gains one step after the benchmark smoke run: `make compare`. It also runs
`go mod tidy` and `git diff --exit-code` inside `bench/compare`, mirroring the root check.

### D7: Documentation and the project retrospective

- **`docs/05-performance-model.md`** — D5's table; a new "Where rice stands" section with both
  comparison tables (handler: benchstat medians with ±; end to end: median req/s and p99), the
  fairness caveats of D3 and D4, and an explanation per scenario tied to an ADR (for example the
  radix tree for `githubapi`, the pool for allocations, fasthttp for `body64k`) or marked
  unexplained. "Numbers marked TARGET" in the preamble is updated to say none remain.
- **`docs/milestones/M8-benchmark-suite.md`** — the milestone retrospective, including every
  finding the equivalence gate caught, the fasthttp version question from D1, and anything the
  numbers contradicted.
- **`docs/07-retrospective.md`** — new: the project retrospective the roadmap asks for. What was
  learned across M0–M8, what would be done differently, and at least three things that surprised
  the author, each tied to a milestone file or ADR. It draws on the milestone retrospectives and
  the progress journal rather than restating them.
- **`docs/04-roadmap.md`** — M8 marked done.
- **`README.md`** — "Where it stands, measured" gains the comparison summary with a link to the
  performance model; status line.
- **`docs/README.md`** — index entry for `07-retrospective.md`.
- **`docs/progress.md`** — journal entry.
- **`bench/results/README.md`** — the two new files described.

## Components

| Unit | Responsibility | Depends on |
| --- | --- | --- |
| `bench/compare/go.mod` | the comparison module; rice via `replace` | rice, Gin, Echo, Fiber |
| `bench/compare/scenarios.go` | scenario table, payloads, expected results | — |
| `bench/compare/githubapi.go` | the 203-route GitHub API table, attributed | — |
| `bench/compare/{rice,gin,echo,fiber}.go` | one adapter each: build an app serving every scenario | scenarios, framework |
| `bench/compare/discardwriter.go` | reusable `http.ResponseWriter` for the `net/http` frameworks | — |
| `bench/compare/equivalence_test.go` | the equivalence gate | adapters |
| `bench/compare/handler_bench_test.go` | handler-level benchmarks | adapters, gate |
| `bench/compare/cmd/server` | one binary serving a chosen framework | adapters |
| `bench/compare/cmd/loadgen` | closed-loop fasthttp load generator | scenarios |
| `bench/compare/scripts/e2e.sh` | rounds, CPU split, results file | server, loadgen |
| `alloc_test.go` (root) | four new budget tests | — |
| `Makefile`, `.github/workflows/ci.yml` | `compare`, `compare-record`, the CI step | — |

## Public API added

None.

## Testing

- **Equivalence gate** (D2): every scenario × every framework, status and body (status only
  for `notfound`).
- **Adapters register the whole GitHub table**: a test asserts each adapter serves one request
  to every one of the 203 routes with 200, so a route one framework silently rejected cannot
  hide inside the scenario's single lookup.
- **Load generator**: a unit test against an in-process server asserting it counts requests,
  records latencies into the histogram, and fails the run on a status mismatch (fault injection:
  a server answering 500 must make the generator exit non-zero).
- **Budget tests** (D5): four new `AllocsPerRun` tests; each broken on purpose once (for example
  an injected allocation) and watched to fail.
- Every wait bounded, as in M7: the server-ready wait in `e2e.sh` and in any Go test times out
  with a clear message.

## Benchmarks

- `bench/results/M8-compare-handler.txt` — `BenchmarkHandler/<scenario>/<framework>`, 10 runs.
- `bench/results/M8-compare-e2e.txt` — five rounds per framework × scenario.
- The root suite is **not** re-recorded unless the root code changes; M8 changes only tests,
  so `bench/results/M7-lifecycle.txt` stays the rice-alone reference.

## Exit criteria

1. `make compare-record` reproduces both results files with one command, on a machine with Go
   1.25 and nothing else installed.
2. The equivalence gate and the GitHub-table registration test pass for all four frameworks.
3. The results include the three cases rice is expected to lose (`githubapi`, `json`,
   `body64k`), whatever they show.
4. Every exported `Ctx` method has a budget row backed by a test; no TARGET remains in
   `05-performance-model.md`.
5. `docs/07-retrospective.md` names at least three things that surprised the author.
6. `make lint`, `make test`, `make test-debug`, `make cover`, `make bench` and `make compare` all
   exit 0; root coverage does not drop below 98.8%; the root `go.mod` is unchanged.

## Open questions

None. Measurement levels (both), the load generator (in-repo Go, separate processes) and the
API scope (none added) were settled in brainstorming. Framework versions are resolved at
implementation time by D1's rule.
