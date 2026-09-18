# M7 — Lifecycle

Status: done
Started: 2026-09-18
Finished: 2026-09-18

## Goal

Stopping a rice server is now something a program can rely on. `Shutdown` delegates the drain
to fasthttp's `ShutdownWithContext` instead of racing M1's goroutine against the context, and
when the deadline passes it closes every connection still open, so nothing is served once it
returns — whatever it returns. `ErrShutdownTimeout` now wraps the context's own error. `OnStart`
hooks run in registration order before the first connection is accepted; `OnShutdown` hooks run
in reverse after the drain and the force-close, once, each with `Shutdown`'s ctx, and their
errors are joined into its result. `Serve` and `Shutdown` agree on a `closed` flag, so an App
serves once and a `Shutdown` that arrives before serving begins cannot leave `Serve` blocked
forever. `RunContext(ctx, addr, grace)` is the signal helper, meant to be wired to
`signal.NotifyContext`; core still does not import `os/signal`. `WithReadTimeout`,
`WithWriteTimeout` and `WithIdleTimeout` expose fasthttp's three server timeouts. New files:
`conns.go` (connection tracking, the force-close) and `lifecycle.go` (the hooks).

## Question this milestone answers

What does fasthttp give for free here, and what has to be built?

**More than M1 assumed, and one thing less than it appears.** Four findings, all from reading
fasthttp v1.73.0's source while writing the design, before any code:

1. **The drain was already there.** M1's comment said fasthttp's shutdown "has no deadline of
   its own". v1.73.0 has `ShutdownWithContext(ctx)`: it closes the listeners, closes idle
   keep-alive connections, polls every 100 ms for the open count to reach zero, and returns
   `ctx.Err()` when the context ends. M1's goroutine was redundant, and on a timeout it leaked,
   waiting on a drain nobody was watching. Free.
2. **At the deadline it stops being a shutdown.** `ShutdownWithContext` runs
   `s.stop.Store(1)` then `defer s.stop.Store(0)` (`server.go:2062–2063`). `serveConn` leaves
   its keep-alive loop only when it reads `stop == 1` after a response (`server.go:2733`). A
   timeout resets the flag, so a busy keep-alive connection finishes its request and goes on
   serving new ones after `Shutdown` has returned. **Built:** connection tracking through
   `ConnState` and a force-close at the deadline — [ADR-0009](../adr/0009-shutdown-force-closes-at-deadline.md).
3. **The timeouts were already fields.** `ReadTimeout`, `WriteTimeout`, `IdleTimeout` on
   `fasthttp.Server`, zero meaning unlimited. Exposing them was API design, three options. Free.
4. **A cancelled-before-serving race.** If `Shutdown` runs after rice's `Serve` has decided to
   serve but before fasthttp's `Serve` has recorded the listener, `ShutdownWithContext` finds no
   listener, returns `nil`, and the `Serve` that follows blocks forever. `RunContext` with an
   already-cancelled context can hit it. **Built:** the `closed` flag, which narrows the race to
   the window between rice publishing the listener and fasthttp recording it — rare enough that
   its guard fails only one run in thousands (see the guard count below) — and a listener close
   in `Shutdown` *after* `ShutdownWithContext`, because fasthttp's `Serve` maps "use of closed
   network connection" to a `nil` return. That window held a second problem, fixed after the
   whole-branch review: a connection accepted inside it could be served after `Shutdown`
   returned (see the correction below).

So the drain and the timeouts are free, and so are the hooks' raw material. What had to be
built is the guarantee: that `Shutdown` returning means the server has stopped. It costs about
2.5 ns on every request, measured below, and the measurement was not what the design predicted.

## Design notes

### Correction: the `ConnState` hook is not "within noise"

The design's D2 said `BenchmarkDispatchConnStateHook` "has to show this is 0 allocations and
within noise of dispatch without the hook; if it is not, this decision is reopened." The first
half held. The second did not:

```
                         │ dispatch.txt │       dispatch+connstate.txt       │
                         │    sec/op    │   sec/op     vs base               │
DispatchConnStateHook-12    36.94n ± 0%   39.41n ± 1%  +6.69% (p=0.000 n=10)
```

0 B/op and 0 allocs/op on both arms, every sample. About 2.5 ns per request, significant at
p=0.000, for the two calls fasthttp makes on every request once any hook is installed —
`setState` is `if hook := s.ConnState; hook != nil { hook(nc, state) }`, called with
`StateActive` (`server.go:2436`) and `StateIdle` (`server.go:2729`). The design's reasoning
that "one indirect call and a `switch` that returns" would be invisible was wrong at this
scale: a whole dispatch is 37 ns, and two calls into a non-inlined method are a visible share
of that.

The decision was reopened, as D2 required, and kept on purpose. The only way to track
connections without per-request calls is to wrap the listener, and that was rejected on
correctness grounds, not speed: a wrapper `net.Conn` allocates per connection and hides the
concrete type from the `keepAliveConn` assertion in fasthttp's `acceptConn`
(`server.go:2123`), so the `TCPKeepalive` setting would silently stop applying. Dropping
tracking instead brings back finding 2. ADR-0009's Consequences carry the number, and the
performance model has a row for it. What the design got wrong was the prediction, not the
choice; what it got right was writing down in advance what would reopen the choice, so the
measurement had something to be checked against.

### Correction: an idle server still waits one poll

The plan expected `Shutdown` with only idle keep-alive connections to return "near 0" — it
attributed the figure to the design, which says only that `Shutdown` "returns promptly" —
because fasthttp closes idle connections before its first poll. Measured median **101.4 ms**
(`idle-keepalive-only`, range 100.9–101.7 ms). Reading `ShutdownWithContext` again explains
it: the loop is `closeIdleConns()`, then `if open := s.open.Load(); open == 0 { return }`,
then wait for the ticker. Closing an idle connection does not decrement `s.open`. The count
drops only when the connection's serving goroutine exits (`serveConnCleanup`,
`server.go:2302`), and fasthttp's accept loop counts itself as open too, decrementing only when
its own `Serve` returns (`defer s.open.Add(-1)`, `server.go:1994`). Both run on other
goroutines, woken by the closes, and in all ten samples the count was still non-zero at the
check that immediately follows. So the first check sees a non-zero count, and the drain waits one
100 ms tick.

The other case, `after-last-request`, reads **95.20 ms** median (94.78–95.81 ms). The
benchmark starts `Shutdown`, waits 5 ms so it is inside its poll loop, and then releases the
last request, so the drain is noticed at the next tick: 100 ms minus the 5 ms head start. In a
real shutdown that figure lands anywhere from 0 to 100 ms. Neither is a target; they are what
delegating the drain costs, and D1 accepted that because shutdown is not a hot path.

### Why the listener close comes after `ShutdownWithContext`

Finding 4 needs `Shutdown` to close a listener fasthttp may never have recorded. Closing it
first looks equivalent and is not: fasthttp's own `closeListenersLocked` then fails with "use of
closed network connection", `ShutdownWithContext` returns that error, and every clean shutdown
reports a failure. Task 1's fault injection moved the close before the call and got exactly
that (quoted below). When fasthttp did record the listener, rice's close is a second close and
its error is discarded.

### The plan's tests could hang instead of failing

Several tests in the plan — copied verbatim into the code by the implementers — waited without
a bound: bare `<-inFlight` and `<-entered` receives, and `net.Pipe` reads with no deadline. A
regression in the code those tests guard would not have failed them; it would have hung
`go test` until its ten-minute timeout. This was not hypothetical. Removing the `forceClosed`
guard in Task 2 did not produce the brief's predicted assertion failure; the test hung on
`latePeer.Read`, and only an explicit `-timeout 15s` turned it into a stack dump. Reviews of
Tasks 2 and 3 caught the pattern, and the controller ruled that "a regression must fail
visibly, never hang the suite" outranks the plan's verbatim code: every blocking wait in the
lifecycle tests now goes through `within` or a read deadline, and the benchmarks' waits through
a bounded `select`. The same injection now fails in 1.00 s with the assertion the brief had
predicted. The lesson is about the plan, not the code: a plan that specifies a fault injection
should also specify how long the test may take to report it, because a test that hangs is
indistinguishable in CI from a slow one.

### Corrected after M7's whole-branch review

The whole-branch review found two ways the headline guarantee — nothing is served after
`Shutdown` returns — still failed, and both are fixed rather than documented.

**Concurrent `Shutdown` calls did not honour their own ctx (Important 1).** fasthttp's
`ShutdownWithContext` holds its server lock for the whole drain and forgets its listeners on
the first call. A second concurrent `Shutdown` therefore blocked on that lock for the whole of
the first call's drain, whatever its own ctx said, and then found no listener, returned `nil`
without force-closing, and raced the first call to the `OnShutdown` hooks — so it could return,
and run the hooks, before the first call's force-close. The realistic trigger is `RunContext`'s
grace silently ignored because something else had already called
`Shutdown(context.Background())`. Now the calls take turns through a capacity-one channel,
`a.shutdownSem`: the call holding it drives the drain, the force-close and the hooks before the
next starts, and a call whose ctx ends while waiting force-closes every connection and returns
an error wrapping `ErrShutdownTimeout`. The force-close is safe at any moment because
`forceClosed` is sticky: from then on every connection fasthttp reports is closed on arrival.
The first `select` prefers the turn over the ctx, so a first `Shutdown` with an already-ended
ctx still does the work. `TestAShutdownWaitingForAnotherHonoursItsOwnContext` and
`TestAConcurrentShutdownReturnsOnlyAfterTheForceClose` reproduce the review's two cases and
failed on the pre-fix code.

**A connection accepted between `Serve` publishing its listener and fasthttp recording it could
be served after `Shutdown` returned (Important 2).** The `closed` flag narrows finding 4 to that
window but does not close it. A `ShutdownWithContext` landing inside it finds no listener,
returns `nil` and resets fasthttp's `stop` flag; if fasthttp then records the listener and
accepts before rice's listener close, that connection is on a keep-alive loop that nothing
drains or force-closes. The review found it by reasoning. Now `Shutdown`, after closing the
listener, calls `ShutdownWithContext` a second time: if fasthttp recorded the listener late, it
drains what was accepted — fasthttp's open count includes its own accept loop until `Serve`
returns, so nothing slips past — and a timeout force-closes as usual; otherwise it finds no
listener and returns at once. Its only other error is the second close of the listener, which is
discarded. `TestShutdownDrainsAConnectionAcceptedBeforeFasthttpRecordedTheListener` and its
timed-out twin hold a test inside the window deterministically, by playing `Serve`'s part in
the package: they publish `a.ln` as `Serve` does, pause `Shutdown`'s listener close, and only
then hand the listener to fasthttp. The same test is the deterministic companion finding 4's
listener close lacked: with that close removed it fails at once instead of one run in
thousands.

Bundled with those: `closeConns` now copies the tracked set under `connMu` and closes outside it,
because a `tls.Conn`'s `Close` can block for seconds and `connState` needs the lock meanwhile;
`RunContext` waits for a `Shutdown` called elsewhere to drain and run the hooks before returning,
rather than returning as soon as the listener closes; and the docs say that `RunContext` can
outlast `grace` while an `OnStart` hook runs and that, after a timeout, an `OnShutdown` hook may
run while a cut-off handler is still running. Nothing on the per-request path changed.

### Things a user needs to know, written where a user will find them

Three behaviours found during review are documented in `Shutdown`'s and `OnShutdown`'s doc
comments and in ADR-0009 rather than changed:

- fasthttp logs one `error when serving connection … use of closed network connection` line for
  each busy connection a timed-out `Shutdown` force-closes, when that handler's write fails.
  rice sets no fasthttp `Logger`, so it goes to fasthttp's default.
- A `Shutdown` that lands while an `OnStart` hook is running does not wait for it: its
  `OnShutdown` hooks run, and it returns, before that `OnStart` returns
  (`TestShutdownDuringOnStartStopsServe` has `Shutdown` return while the hook is still blocked).
  Whatever that `OnStart` opens afterwards is never released by an `OnShutdown` hook.
- The ctx an `OnShutdown` hook receives is already done when the drain timed out.

## Exit criteria

- [x] The drain test: a slow in-flight request completes during `Shutdown` while a new
      connection is refused — `TestShutdownDrainsAnInFlightRequestAndRefusesNewConnections`
- [x] The deadline test: `Shutdown` returns at the deadline when a request never finishes, with
      `errors.Is` holding for both `ErrShutdownTimeout` and `context.DeadlineExceeded` —
      `TestShutdownReturnsErrShutdownTimeoutWhenTheDeadlinePasses`
- [x] The finding-2 test: nothing is served on a surviving keep-alive connection after a
      timed-out `Shutdown`, and it fails with the force-close removed —
      `TestNothingIsServedAfterATimedOutShutdown`
- [x] Hook order, run-once and before-serve — `TestOnStartHooksRunInOrderBeforeTheFirstRequest`,
      `TestOnShutdownHooksRunInReverseAfterTheDrain`, `TestShutdownRunsHooksOnce`,
      `TestServeAfterShutdownReturnsWithoutServing`, `TestServeAfterShutdownRunsNoOnStartHooks`,
      `TestShutdownDuringOnStartStopsServe`, `TestRunContextWithACancelledContextReturns`
- [x] `BenchmarkDispatchConnStateHook` shows 0 allocations; `BenchmarkShutdownLatency` recorded
      in `bench/results/M7-lifecycle.txt`
- [x] `make lint`, `make test`, `make test-debug`, `make cover` and `make bench` all exit 0; root
      package coverage 99.2%, above the 98.8% floor

M7's `☐` in `docs/04-roadmap.md` is now `☑`.

## Measurements

From `bench/results/M7-lifecycle.txt`, ten samples per benchmark, read through `benchstat`.

| Case | Time | Allocs | Bytes |
| --- | ---: | ---: | ---: |
| `DispatchConnStateHook/dispatch` | 36.94n ± 0% | 0 | 0 |
| `DispatchConnStateHook/dispatch+connstate` | 39.41n ± 1% | 0 | 0 |
| `ShutdownLatency/after-last-request` (median ms/shutdown) | 95.20 ms | — | — |
| `ShutdownLatency/idle-keepalive-only` (median ms/shutdown) | 101.4 ms | — | — |
| `RiceDispatch` | 33.83n ± 0% | 0 | 0 |
| `DispatchParameterised` | 38.43n ± 1% | 0 | 0 |
| `Dispatch404` | 38.12n ± 0% | 0 | 0 |
| `FasthttpBaseline` (control, no rice code) | 11.33n ± 0% | 0 | 0 |

The first two rows are the milestone's one same-session comparison: +6.69%, p=0.000. The
shutdown rows report a custom `ms/shutdown` metric; their `ns/op` and `B/op` columns include
starting a server and a client per iteration and mean nothing.

`BenchmarkRiceDispatch` as the regression check against M6: 33.27n → 33.83n, allocations
0 → 0. The allocation half is exact. The time half is cross-session: `BenchmarkFasthttpBaseline`,
which runs no rice code, moved 11.15n → 11.33n (+1.61%) across the same pair, and
`RiceDispatch` moved +1.68%. M7 did not touch the dispatch path — `handle` does not go through
`ConnState` — so this reads as drift between two sessions, not a regression.

Hardware, Go version, OS: Apple M2 Pro, go1.25.6 darwin/arm64, Darwin 27.0.0 arm64.
Recorded 2026-09-18T10:30:33Z.
Raw output: [`bench/results/M7-lifecycle.txt`](../../bench/results/M7-lifecycle.txt).

## Retrospective

**What surprised me:**

*The two numeric predictions were both wrong, and in the same direction.* The design predicted
the hook would be free, and the plan an idle shutdown instant. The hook costs 2.5 ns; the idle shutdown
takes a full tick. Both predictions came from reading code and reasoning about what it would
cost, and both missed something the code does not say on its face — how much two calls weigh
against a 37 ns dispatch, and that "closed" and "counted as closed" are different moments in
fasthttp. The design deserves credit for one thing: it said in advance what would reopen D2, so
when the number came back there was a decision to make rather than a number to explain away.

*The one finding that mattered most was a `defer`.* Finding 2 is two lines,
`s.stop.Store(1)` / `defer s.stop.Store(0)`, and it turns fasthttp's timed-out shutdown into a
server that is still running. Nothing in fasthttp's documentation mentions it, and M1's
implementation, which never called `ShutdownWithContext`, would have shipped the same hole the
moment someone "fixed" it by delegating. Reading the source before designing was what found
it.

*A guard for a real race is only as good as how often the race happens.* Finding 4's guard — the
listener close in `Shutdown` — is exercised by `TestRunContextWithACancelledContextReturns`,
which runs `RunContext` with a cancelled context fifty times per test run. With the close
removed, the test passed at `-count=1` and needed `-count=500` — 25,000 calls — to fail, and
even then not on every invocation. See the fault-injection list.

**What I would do differently:**

Bound every wait in the plan itself. The plan specified what each fault injection should print
and never how long a test may take to print it, so a regression's first symptom was a hang.
The rule the controller imposed mid-milestone — every blocking receive through `within`, every
raw read behind a deadline — belongs in the plan template, next to "every guard is watched to
fail".

And find a deterministic test for finding 4. The window is between rice publishing `a.ln` and
fasthttp's `Serve` recording it, which no public API lets a test stop inside. The probabilistic
test is honest about what it is, and it is still not a guard CI can rely on.

Several guards were never broken on purpose, recorded in the controller's ledger as deferred:
`Serve`'s first `closed` check, the listener close on an `OnStart` failure, the hook
registration panics, the hooks-after-drain ordering, the `WithIdleTimeout` and `WithWriteTimeout`
wiring, and the negative-duration panics. Each has a test that exercises it; none has been
watched to fail.

**Fault injections, and what each printed.**

Every guard below was broken on purpose, the listed test was run, and the code was restored.

1. **Task 1 — listener close moved before `ShutdownWithContext`.**
   ```
   lifecycle_test.go:109: Shutdown returned close tcp 127.0.0.1:61545: use of closed network connection after a clean drain, want nil
   lifecycle_test.go:144: first Shutdown returned close tcp 127.0.0.1:61548: use of closed network connection, want nil
   ```
2. **Task 2 — `closeConns()` removed from `Shutdown`** (finding 2, exit criterion 3).
   ```
   lifecycle_test.go:222: the connection served a request after Shutdown returned: "HTTP/1.1 200 OK...third"
   lifecycle_test.go:226: the connection was still open 2s after a timed-out Shutdown
   --- FAIL: TestNothingIsServedAfterATimedOutShutdown (2.11s)
   ```
3. **Task 2 — the `forceClosed` guard removed from `connState`.** First run, before the test's
   reads were bounded: a hang, turned into a failure only by `-timeout 15s`:
   ```
   panic: test timed out after 15s
   ...
   github.com/vietpham102301/rice-http.TestCloseConnsClosesTrackedAndLateConnections(...)
       /Users/vietpham1023/dev/rice-http/lifecycle_internal_test.go:86 +0x238
   ```
   After the fix (read deadlines, membership checked before the read):
   ```
   lifecycle_internal_test.go:95: a connection reported after the sweep was tracked
   lifecycle_internal_test.go:100: reading the peer of a late connection: read pipe: i/o timeout, want io.EOF
   --- FAIL: TestCloseConnsClosesTrackedAndLateConnections (1.00s)
   ```
4. **Task 2 — `closeConns`'s close loop emptied.**
   ```
   lifecycle_internal_test.go:82: reading the peer of a force-closed connection: read pipe: i/o timeout, want io.EOF
   --- FAIL: TestCloseConnsClosesTrackedAndLateConnections (1.00s)
   ```
5. **Task 2 — `connMu` taken above the `switch` in `connState`**, so `Active`/`Idle` lock.
   ```
   lifecycle_internal_test.go:38: connState(Active, Idle) blocked on connMu
   --- FAIL: TestConnStateActiveAndIdleAreFree (1.00s)
   ```
6. **Task 3 — `runShutdown` iterated forwards.**
   ```
   lifecycle_test.go:450: events = [hook 1 hook 2 hook 3], want [hook 3 hook 2 hook 1]
   ```
7. **Task 3 — `sync.Once` replaced with a direct call.**
   ```
   lifecycle_test.go:517: OnShutdown hook ran 2 times over two Shutdowns, want 1
   ```
8. **Task 3 — `Serve`'s second `closed` check deleted.**
   ```
   lifecycle_test.go:565: Serve returning did not happen within 2s
   --- FAIL: TestShutdownDuringOnStartStopsServe (2.00s)
   ```
9. **Task 3 — `runShutdown` returned on the first hook error.**
   ```
   lifecycle_test.go:467: events = [b], want [b ok a]
   lifecycle_test.go:470: Shutdown returned hook b, which does not match [hook a]
   ```
10. **Task 3 fix round — `runStart()` removed from `Serve`**, to confirm a now-bounded wait fails
    rather than hangs.
    ```
    lifecycle_test.go:559: the OnStart hook running did not happen within 2s
    --- FAIL: TestShutdownDuringOnStartStopsServe (2.00s)
    ```
11. **Task 4 — the listener close in `Shutdown` removed** (finding 4). **Probabilistic.**
    `TestRunContextWithACancelledContextReturns` (fifty `RunContext` calls per run) passed at
    `-count=1`. At `-count=500` the first invocation failed (its text was not retained), the
    second passed, and of five more, the first four passed and the fifth failed at run 32:
    ```
    --- FAIL: TestRunContextWithACancelledContextReturns (2.00s)
        lifecycle_test.go:248: RunContext returning (run 32) did not happen within 2s
    ```
12. **Task 4 — `RunContext`'s shutdown context derived from `ctx`** instead of
    `context.Background()`.
    ```
    lifecycle_test.go:173: in-flight body = "error: Get \"http://127.0.0.1:62626/slow\": EOF", want "finished"
    lifecycle_test.go:176: RunContext returned rice: shutdown timed out: context canceled, want nil
    ```
13. **Task 5 — `WithReadTimeout` wired to `WriteTimeout`.**
    ```
    lifecycle_internal_test.go:187: ReadTimeout = 0s, want 1s
    lifecycle_test.go:728: a stalled client was still connected after 2s with a 100ms ReadTimeout
    ```

The line numbers are those at the time of each run; later tasks appended to the same files.

**Guards, and the count.**

M6 closed at thirteen. M7 adds one:

- **The listener close in `Shutdown`, as guarded by `TestRunContextWithACancelledContextReturns`.**
  The guard is real and the test does reach it, but at the rate this race occurs, removing the
  close passes a normal CI run. Anticipated in the plan ("probabilistic, with a fallback"), and
  unlike M6's anticipated blind spots it has no deterministic companion test behind it.
  **Counted.**

Not counted, for the reason M4, M5 and M6 gave — the tally is of guards that turned out not to
guard, not of every place the process around guards went wrong:

- **Injection 3's hang.** The test *did* fail on its fault; it failed by not finishing. That is
  a defect in how the failure was reported, now fixed, not a guard with no contact with its
  invariant.

**Thirteen plus one is fourteen.**

**What I still do not understand:**

How much of the 2.5 ns is the call and how much is the benchmark. `BenchmarkDispatchConnStateHook`
calls `a.connState` directly; fasthttp calls it through a func-valued field. The direct call
may be cheaper than the real one, or the real one may be hidden among the rest of a request's
network work — the benchmark cannot say which, and nothing here measures a request end to end
over a socket with and without the hook.

Second: of the benchmarks `benchstat` compares against M6's file and flags as changed, all
but three moved slower — `FasthttpBaseline` +1.61%, `DispatchHTTPError` +6.21%, none of them on
a path M7 touched — and the three that moved faster (`CtxSetGet` −0.64%, `TreeWrongVerb`
−0.56%, `MapLookup100` −1.40%) are no closer to M7's changes. Both files were recorded on
Darwin 27.0.0. M6 offered an OS upgrade as a candidate for its cross-session movement; there
was no upgrade this time, and the numbers moved anyway. The
question M5 left open — what moves benchmarks between sessions — is still open, and M7 removes
the one candidate M6 had.
