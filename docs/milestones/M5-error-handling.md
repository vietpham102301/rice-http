# M5 — Error handling

Status: done
Started: 2026-09-13
Finished: 2026-09-14

## Goal

One error type, one funnel, one place to change how a service reports failure. `HTTPError`
carries a status and an optional wrapped cause; `PanicError` carries a recovered panic's
value and stack, presented to the funnel as an error like any other. `ErrorHandler` is
`func(c *Ctx, err error)`, one per `App`, set with `WithErrorHandler` and defaulting to
`DefaultErrorHandler`. Every failure — a route miss, a returned error, a panic — reaches it
through `callErrorHandler`, which carries its own last-resort recovery so a panicking
`ErrorHandler` cannot resurrect the failure it exists to prevent. `middleware.Recover` ships
as the project's first opt-in package, converting a panic into an ordinary returned error
before core recovery ever sees it, so outer middleware still observes it as a normal return.

## Question this milestone answers

Can one funnel handle every failure mode — a miss, a returned error, a wrapped error, a
panic — without special cases?

**Yes, and the evidence is in the code rather than an assertion about it:**

```
$ grep -n 'ErrNotFound' app.go
148:		a.callErrorHandler(c, ErrNotFound)
```

One line. No `errors.Is` branch, no separate 404 path — `ErrNotFound` is a prebuilt
`*HTTPError` that reaches `DefaultErrorHandler` through the same type switch and the same
`errors.As` fallback as everything else. The special case M2 built and M5 was supposed to
remove is gone, and this grep is what proves it rather than the plan's word for it.

## The probe, which is the milestone's real story

`docs/03-core-concepts.md` §7 has said since M1: "the core installs a panic hook on the
fasthttp server so a panicking handler produces a 500 and does not take the connection
down." Both halves are false. fasthttp v1.73.0 has no `PanicHandler` and no equivalent —

```
$ grep -rn "PanicHandler" $(go env GOMODCACHE)/github.com/valyala/fasthttp@v1.73.0/*.go
$
```

— and its only `recover()` on the request path guards body-stream writes, nowhere near a
handler. `server.go:2621` calls `s.Handler(ctx)` bare from a worker-pool goroutine. A probe
run against `main` before this milestone began confirmed what that implies:

```
GET /ok   -> 200 <nil>
panic: handler exploded
...
github.com/vietpham102301/rice-http.(*App).handle(...)  app.go:113
github.com/valyala/fasthttp.(*Server).serveConnCounted(...)  server.go:2621
github.com/valyala/fasthttp.(*workerPool).workerFunc(...)  workerpool.go:225
exit status 2
```

Not a dropped connection — the whole process, every other in-flight request with it. A
documented safety guarantee had been describing a mechanism that never existed, in a library
that offers no way to build the one that was described, for four milestones, and nobody
noticed, because nothing tested it. Principle 8 says every decision is recorded including
the wrong ones; this one was recorded and was simply untrue, and Step 1 of the documentation
task exists to say so plainly rather than quietly fix the sentence. See
[ADR-0008](../adr/0008-rice-recovers-panics-in-core.md) for the full argument, including why
Gin and Fiber's opt-in-only pattern does not transfer here — checked against Fiber's actual
source rather than assumed, since Fiber sits on the same fasthttp with the same gap.

## Design notes

**The allocation finding that nearly shipped a regression.** Writing the budgets in
`alloc_test.go` turned up that `errors.As(err, &target)` heap-allocates `target`, because
`&target` is passed as an `any` parameter (`go build -gcflags=-m`: `moved to heap: pe`,
`moved to heap: he`). Unfixed, `DefaultErrorHandler` would have cost two escaped locals per
call on top of the `Ctx` — the 404 path would have gone from 3 allocations to 1 only
partially, landing at 1 → the fast-path number below instead of paying twice for the walk,
and a handler returning a fresh `HTTPError` would have gone from 4 to 2 rather than staying
at the higher figure. D2 promises a 404 costs one allocation, the same as a hit, because
`ErrNotFound` is prebuilt; that promise was false until a type-switch fast path went in front
of the `errors.As` pair. It matters more than a missed budget: M2's original funnel used
`errors.Is(err, ErrNotFound)`, which takes a plain `error` and never escaped anything, so M5's
generalisation would have made the miss path — the one mistaken and hostile traffic hits
hardest — three times more expensive than M4's, which is exactly the case D2 argues a
prebuilt sentinel exists to protect. The fix costs nothing extra: every error the funnel
actually produces (`ErrNotFound`, a fresh `NewHTTPError`, the `*PanicError` `handle` builds)
arrives as a concrete pointer and matches the type switch for free; only a handler-wrapped
error pays for the `errors.As` walk, which is the case that needs it.

**Two decisions in the design spec described code that execution overrode, and both are now
corrected in place rather than left describing the wrong thing.** D5 stated "`respond`
resets the body before writing" with a `c.fctx.ResetBody()` call in its code block. The call
is a no-op: `SetBodyString` already calls `closeBodyStream`, then `bodyBuffer()` (which nils
`bodyRaw`), then `Reset()`, so no input to `respond` exists under which the explicit call
changes anything observable. ADR-0002's rule — a handler that writes a body and then errors
has it discarded — still holds, via `SetBodyString`; only the redundant line and its
misleading comment are gone. D8 stated the last-resort response in `callErrorHandler` "is
written with raw fasthttp calls rather than through `respond`, so that a bug in rice's own
response path cannot recurse." `respond` is three fasthttp calls with no path back into
error handling; it cannot recurse into anything, and the rationale does not survive contact
with the code it was defending. It now calls `respond`, like everything else in the funnel.

## Exit criteria

- [x] Tests for wrapped errors via `errors.As` —
      `TestDefaultErrorHandlerFindsAnHTTPErrorThroughAWrapper`,
      `TestDefaultErrorHandlerFindsAWrappedPanicError`,
      `TestAWrappedPanickedHTTPErrorIsStill500`
- [x] A custom error handler — `TestWithErrorHandlerReplacesTheDefault`,
      `TestWithErrorHandlerAlsoHandlesA404`, `TestACustomErrorHandlerReceivesAPanicError`
- [x] A panicking handler producing 500 without dropping the connection —
      `TestServeSurvivesAPanickingHandler` (a real listener, panic on one route, the server
      still answers `/ok` afterward) and `TestAPanicDoesNotKillTheKeepAliveConnection`, which
      writes two requests down one hand-held TCP connection and proves the *connection*
      itself survives the panic — the literal claim the roadmap made, provable only because
      an `http.Client` is free to silently redial and would not have caught a dropped
      connection
- [x] A test asserts the cause string never appears in the response body —
      `TestTheCauseNeverReachesTheBody`, plus
      `TestAPanickingHandlerDoesNotLeakThePanicValueIntoTheBody` and
      `TestThe404BodyDoesNotLeakTheSentinelMessage` for the panic and miss cases specifically

All four are met. M5's `☐` in `docs/04-roadmap.md` is now `☑`.

## Measurements

The recovery mechanism's cost, the trustworthy figure because both arms ran in one
benchmarking session (ten samples each, sorted before reading median and range):

| | median | range | allocs |
| --- | ---: | --- | ---: |
| `BenchmarkNoRecoverBaseline` | 0.91 ns | 0.91 – 0.92 | 0 |
| `BenchmarkDeferRecoverOverhead` | 3.02 ns | 3.01 – 3.03 | 0 |

About **2.1 ns and zero allocations**, ranges non-overlapping. Reproduced across two
independent runs of the whole suite: 0.93/3.04, then 0.91/3.02 — the pair committed above.

The error paths, same session:

| | median | range | allocs |
| --- | ---: | --- | ---: |
| `BenchmarkDispatch404` | 99.67 ns | 97.75 – 107.70 | 1 |
| `BenchmarkDispatchHTTPError` | 119.80 ns | 111.90 – 125.20 | 2 |
| `BenchmarkDispatchPlainError` | 112.95 ns | 88.88 – 124.50 | 1 |
| `BenchmarkDispatchPanic` | 8347 ns | 8290 – 8361 | 4 |

`DispatchPanic` covers the whole panic path including `debug.Stack()`: roughly 8.3
microseconds and 4 allocations is what a panic costs, now a number in a file instead of
folklore — and it is one nobody should expect on the hot path, since a panic is by
definition not the common case.

`DispatchPlainError`'s range spans 36 ns on this run — worth stating rather than smoothing
over, because it means this machine's dispatch-level numbers are not stable to a few
nanoseconds, a caveat the next milestone reading this file should keep.

The allocation budgets, enforced as tests rather than just read off a benchmark
(`alloc_test.go`, `testing.AllocsPerRun`):

| Operation | Budget |
| --- | ---: |
| Recovery installed, nothing panics | 0 extra (1 total, same as no recovery) |
| 404 through the funnel | 1 |
| Handler returns a fresh `HTTPError` | 2 |
| Panic recovered, stack captured | documented, not bounded |

**What must not be read as M5's cost.** `BenchmarkChainDispatch0` reads 84.01 ns in M4's
recorded file and 96.96 ns in M5's — a tempting +13 ns to attribute to recovery. It is not
M5's number to claim: M5 opened neither `ctx_response.go` nor `internal/router`, and both
`BenchmarkCtxSetHeader` (+31.40 ns) and `BenchmarkTreeLookup1000` (+18.85 ns) moved *more*
between the same two files than any dispatch benchmark M5 touched (`ChainDispatch0` itself:
+12.95 ns). `BenchmarkFasthttpBaseline`, which no rice code runs through at all, moved only
+0.21 ns:

| | M4 | M5 | delta |
| --- | ---: | ---: | ---: |
| `BenchmarkCtxSetHeader` | 128.20 | 159.60 | +31.40 |
| `BenchmarkRiceDispatch` | 81.88 | 105.05 | +23.17 |
| `BenchmarkTreeLookup1000` | 114.30 | 133.15 | +18.85 |
| `BenchmarkChainDispatch0` | 84.01 | 96.96 | +12.95 |
| `BenchmarkFasthttpBaseline` | 11.39 | 11.60 | +0.21 |

The shifts are not proportional, they touch code M5 never opened, and the floor barely
moved — this is environmental drift between two recording sessions on the same machine, not
a uniform slowdown with a cause, and this document does not invent one. `ChainDispatch0`
itself read 106.15 ns on M5's own first full-suite run and 96.96 ns on the second, so it does
not even reproduce within one session. The trustworthy figure for what recovery costs is the
same-session pair above, not this cross-file comparison, and both numbers appear in the same
sentence rather than the delta standing alone anywhere in this document.

Hardware, Go version, OS: Apple M2 Pro, go1.25.6 darwin/arm64, Darwin 25.6.0 arm64.
Recorded 2026-09-14T15:14:21Z.
Raw output: [`bench/results/M5-error-handling.txt`](../../bench/results/M5-error-handling.txt).

## Retrospective

**What surprised me:**

*The false claim was in a section that gets read.* `docs/03-core-concepts.md` §7 is not a
footnote — it is the one place the project documents its error-handling contract, and it
named a specific, checkable mechanism ("installs a panic hook on the fasthttp server") that
a single `grep` against the vendored dependency disproves in one line. Four milestones is a
long time for a testable factual claim to sit unchecked in a document whose whole premise is
that claims come with numbers behind them.

*The regression that would have shipped was hiding in the fix, not the feature.* The funnel
generalisation — one `errors.As` walk instead of a special-cased `errors.Is` — was the
milestone's headline idea and it worked on the first pass. What nearly went wrong was
underneath it: `errors.As`'s `any` parameter forces its target to escape, a cost the old
`errors.Is`-based code never paid because `errors.Is` takes a concrete `error`. Nobody set
out to regress the 404 path; it would have happened as a side effect of doing exactly what
the milestone was supposed to do, on the path D2 singles out as the one that matters most.
`-gcflags=-m` is what caught it, not a benchmark reading — the allocation counts would have
looked wrong only after the fact.

*The Gin/Fiber comparison in the plan's own reasoning did not survive being checked.* Writing
ADR-0008's alternatives section against the actual vendored source rather than repeating the
received wisdom found that Fiber — built directly on fasthttp, like rice — carries no
`recover()` outside its test files, the same gap this milestone's probe found in fasthttp
itself. Gin's opt-in `Recovery()` middleware is safe to be opt-in only because `net/http`
already recovers panics per connection underneath it; Fiber's is not obviously safe by the
same reasoning, because nothing underneath it does. That is not a reason to change course —
it is independent confirmation that the gap this milestone closes is real, and that at least
one comparable framework carries it silently.

**What I would do differently:**

Run `-gcflags=-m` over the funnel's hot path before the design spec's code block gets treated
as final, not after `alloc_test.go` was already being written. The escape was there from the
moment `errors.As(err, &he)` was chosen over the old `errors.Is`, and it was invisible to
every test in the suite until something asked the compiler directly.

**Guards, and the count.**

M4 closed at nine, showing the arithmetic — five plus one plus three — on the page. M5's
candidates, and which of them are genuinely new entries rather than the same defect twice:

1. **`respond`'s `ResetBody()` call.** A new species of the pattern: not a test that cannot
   fail, but a *line of code whose removal no test can detect*, with a comment
   ("The reset is ADR-0002's rule, settled in M1") that attributes enforcement of a real
   invariant to a no-op. `SetBodyString` already discards the body by the time `ResetBody`
   would run. Found by a fault injection that removed the call and produced no failure — the
   injection working as designed, on its first try this milestone. **Counted.**
2. **The same no-op, reintroduced one task later** in `callErrorHandler`'s last-resort path,
   because that path was written with raw fasthttp calls on the theory that a bug in
   `respond` "cannot recurse" through it — a claim that does not hold, since `respond` has no
   path back into error handling at all. This is the *same* defect surfacing a second time
   from a false rationale, not a second distinct guard failure, and it is folded into item 1
   above rather than tallied separately. What is worth recording about it is not a second
   count but that a false engineering rationale walked the implementer straight back into a
   defect the previous task had just removed.
3. **`TestAPanickedHTTPErrorIsStill500`, disarmed as a side effect of an optimisation.** Its
   input — a bare `*PanicError` — matched the new type-switch fast path and stopped reaching
   the `errors.As` fallback whose ordering it was written to pin, so swapping the fallback's
   two checks would have left it green with nothing left to catch the swap. The guard did not
   break; it silently stopped being connected to what it guarded. Caught by the implementer
   who wrote the fast path, who added `TestAWrappedPanickedHTTPErrorIsStill500` with a
   `fmt.Errorf`-wrapped input that does reach the fallback and does go red at 400.
   **Counted** — a distinct species from item 1: not a redundant line believed to enforce a
   rule, but a live guard an unrelated optimisation quietly detached from the property it
   named.
4. **Six fault-injection instructions that could not run**, because every plan instruction
   phrased as "delete this code" — Task 1's, Task 4's, Task 5's two, Task 6's two — orphaned
   an import or a variable and produced a compile error instead of the predicted runtime
   failure. Six for six, each noticed and substituted with a behaviour-equivalent break by
   its implementer. This is not a guard that failed to guard; it is the planning process
   itself failing one layer up, the same shape as M4's tenth item (a fault injection that
   injected no fault) — which M4 explicitly declined to add to its tally. **Not counted**,
   for the same reason M4 gave: the count is of guards that turned out not to guard, not of
   every place the process around guards went wrong. The instruction that follows from it —
   an injection must be a change in behaviour, never a deletion of text — is recorded here
   because it is worth keeping even though it does not move the number.

One more is explicitly **not a guard at all**: a task review classified `middleware`'s log
noise as Important because three stack traces "print to stderr on every run." `make test`
runs without `-v`, and Go discards a passing test's captured output, so the default run
emits none — measured, 3 lines and 82 under `-v`, 0 and 14 after the fix. The finding was
real; its stated severity rested on an unmeasured claim about the tool. It is the same
disease — a claim about behaviour nobody checked — in a reviewer rather than in a guard, and
it is named here rather than tallied because it never claimed to guard anything.

**Nine plus two is eleven.** M4's nine, plus item 1 (the redundant `ResetBody`, its
reintroduction folded in rather than double-counted) and item 3 (the fast path silently
detaching a live guard from its invariant): **the count is eleven.**

**What I still do not understand — the fifth entry on this list, and it differs from the
first four.**

M1's 43 ns header write, M2's ~9 ns of routing cost, M3's 15.93 ns of tree scaling and M4's
~1.1 ns per middleware were all **stable costs without an explanation** — reproducible
numbers nobody had traced to a mechanism. M5's entry is not a cost; it is the instability
itself. `BenchmarkCtxSetHeader` and `BenchmarkTreeLookup1000` — files M5 never touched —
moved more between the M4 and M5 recordings than any benchmark M5's changes actually run
through, and the shifts are not proportional to each other, and `BenchmarkFasthttpBaseline`,
the one number with no rice code anywhere near it, barely moved at all. Something about the
measuring environment changed between two sessions on the same machine, unevenly across
files that share no code path, and nothing here identifies what. That is a different kind of
"do not understand" than the previous four: those were waiting for someone to take a
profile; this one says the profile would have to be taken *during* a session that has
already ended, of a condition nobody can currently reproduce on demand. It is recorded as
what it is — evidence that this benchmark suite's cross-session comparisons carry more noise
than a single number and a range communicate — rather than dressed up as a finding about
recovery, which is precisely the mistake this document declines to make with the +13 ns
`ChainDispatch0` reading above.
