# 07 — Retrospective

The project retrospective the roadmap asks for, written when M8 closed on 2026-09-19. Each
milestone has its own retrospective in [milestones/](milestones/) and its own entry in
[progress.md](progress.md); this document does not repeat them. It says what the whole of it
answered, what I would change, and what surprised me most.

## What the project set out to learn

[00-overview.md](00-overview.md) names two learning goals: how a router, a middleware chain, a
request context and a server lifecycle fit together, and where the seams belong; and how to
design an API whose hot path allocates nothing, prove it, and keep it that way. Its success
criteria put the second in testable form — a static route and a parameterised route read as
bytes each serve at zero allocations per request, "proven by a test, not a benchmark eyeball" —
and said throughput against Gin or Fiber would be reported in M8 but was not a criterion.

Both zero-allocation criteria hold, as tests: `TestAllocBudgetHandleDispatch` and
`TestAllocBudgetHandleDispatchParameterised`, in `alloc_test.go`, which CI runs on every commit.
The throughput comparison is reported, and it says something the overview did not anticipate
(below).

## What each milestone answered

**[M0 — Scaffold](milestones/M0-scaffold.md).** What the rig looks like before there is
anything to measure. A bare fasthttp handler costs 11.17 ns and **zero** allocations, so every
allocation rice ever reported was rice's own. The two lessons were about the module, not the
benchmark: a dependency is not pinned until something imports it, and a tidiness check that can
fail finds what a warning never would.

**[M1 — Minimal server](milestones/M1-minimal-server.md).** What one unpooled request costs:
25.49 ns, 16 bytes, one allocation, against an 11.05 ns baseline. That was the number the pool had
to remove. The process lesson — cut plan tasks along compilation units, not concepts — held for
every plan after it.

**[M2 — Static router](milestones/M2-static-router.md).** How bad a map is. Not bad at all:
38.52, 38.37 and 38.34 ns at 10, 100 and 1000 routes, flat. The tree would have to earn its place
on parameters and shared prefixes, not on static lookups. The zero-allocation 404 that nobody
designed turned out to be a compiler artifact, and M3 ended it, as the notes warned something
would.

**[M3 — Radix tree router](milestones/M3-radix-tree-router.md).** Where the tree wins and where it
loses. It loses on static routes, as predicted, and dispatch roughly doubled — but the tree was not
why: the `Ctx` grew from 16 bytes to 344, and the allocation alone accounted for about 61 ns. M3 is
also where "a guard nobody has broken on purpose is not a guard" was first written down, after
five of them turned out not to guard.

**[M4 — Middleware and groups](milestones/M4-middleware-and-groups.md).** Whether compiling
chains at build time removes the per-request cost. It does: five middleware add zero allocations
and about 4.6 ns — the fourth reading of the same data, three of them wrong, every wrong one
derived rather than measured.

**[M5 — Error handling](milestones/M5-error-handling.md).** Whether one funnel can take every
failure without special cases. It can, with a type-switch in front of `errors.As` so the 404
path did not start allocating; recovery in core costs about 2.1 ns and no allocations
([ADR-0008](adr/0008-rice-recovers-panics-in-core.md)).

**[M6 — Context pooling](milestones/M6-context-pooling.md).** What pooling saves, and what bug it
introduces. Both arms in one session: 35.94 ns and zero allocations pooled, 82.11 ns, 240 bytes
and three allocations unpooled. The bug class is use-after-release, and the debug build that
catches it had to be redesigned, because the one the ADR described would not have
([ADR-0005](adr/0005-context-pooling-and-borrow-contract.md)).

**[M7 — Lifecycle](milestones/M7-lifecycle.md).** What fasthttp gives for free and what has to be
built. The drain and the timeouts were free; the guarantee that nothing is served after
`Shutdown` returns was not, and it costs about 2.5 ns on every request
([ADR-0009](adr/0009-shutdown-force-closes-at-deadline.md)).

**[M8 — Benchmark suite](milestones/M8-benchmark-suite.md).** Where rice stands, and why. At the
handler level rice is the fastest of the four in five of seven scenarios and allocates nothing
where the design said it would; end to end it is level with Fiber and ahead of Gin and Echo, and
that lead is fasthttp's
([05-performance-model.md — Where rice stands](05-performance-model.md#where-rice-stands)).

## Three things that surprised me

**1. A documented safety guarantee was never real — [M5](milestones/M5-error-handling.md),
[ADR-0008](adr/0008-rice-recovers-panics-in-core.md).** From M1 onward,
`03-core-concepts.md` said the core "installs a panic hook on the fasthttp server". fasthttp
v1.73.0 has no panic hook, and a probe before M5 began showed what that meant: a panicking
handler took the whole process down, exit status 2. The claim was specific and checkable with one
`grep`, it sat in the section that documents the error contract, and it went unchecked for four
milestones in a project whose premise is that claims come with evidence. What surprised me was
not that a sentence was wrong. It was that the most confident sentence about safety was the one
nobody had tested, because it described a dependency rather than code I had written.

**2. An accepted ADR's safety mechanism could not have caught the bug it was written for —
[M6](milestones/M6-context-pooling.md), [ADR-0005](adr/0005-context-pooling-and-borrow-contract.md).**
ADR-0005 specified poisoning a released `Ctx` by clearing its pointers and a generation counter,
and never said whether the poisoned object went back into the pool. If it does, the next request
takes the same object, `reset` clears the poison, and a stale reference reads that request's data
without a panic; a generation counter cannot help, because the stale pointer and the reused object
are the same pointer. It was found by reading the design five milestones after the ADR was
accepted. Then the plan's test for the corrected mechanism could not fail either, because it
looked after the window instead of inside it — the failure it was written to catch, reproduced in
the test that was meant to catch it.

**3. What a client sees is the transport — [M8](milestones/M8-benchmark-suite.md),
[ADR-0001](adr/0001-use-fasthttp-as-transport.md).** Seven milestones went into the per-request
cost of routing, middleware, errors and the context, and at the handler level it shows: rice is
fastest in five scenarios, zero allocations everywhere but `json` and `body64k`, and on the 203-route GitHub table —
chosen because rice was expected to lose it — rice resolved the route in 118.8 ns while Fiber took
680.6. End to end none of that is visible. At about 166,000 requests a second each request has up
to about 36 µs of server CPU, and the frameworks differ by 0.01 to 0.6 µs; no order between rice and
Fiber, or between Gin and Echo, can be claimed from five rounds — not even in `json`, where Fiber
was ahead in all five. The only line a client sees is the one between
fasthttp and `net/http` — 3.6–6.8% in the light scenarios, about 4.5 times for a 64 KiB body —
and ADR-0001 drew it before any code existed. The overview said throughput would be reported and
was not a criterion; I had not expected the report to say that the one decision taken before the
first line of code is the one that shows.

Two more came close. fasthttp's timed-out shutdown resets its own `stop` flag in a `defer`, so the
server goes on serving busy keep-alive connections after `ShutdownWithContext` returns — two lines
nothing in its documentation mentions, found only by reading the source before designing M7. And
M7's `ConnState` hook, which the design called "within noise", costs 2.5 ns a request at
p=0.000; the design had written down in advance what would reopen the decision, so the number had
something to be checked against, and the decision was kept on purpose.

## What I would do differently

**Break every guard on purpose from the first test.** The practice arrived in M3, after five
guards had turned out not to guard, and the count kept rising after it: fifteen by the end of M8,
each a test, comment or budget believed to hold something it did not. Every one was found by
breaking the property and watching the guard stay green, or by reading it afterwards, never by the
guard failing. The plan template should demand, for every guard, the injection that breaks it, the
observable the assertion reads and when, and how long the test may take to fail — the last two
learned in M6 and M7, the hard way.

**Read the dependency's source before writing a sentence about it.** M1's "no deadline of its
own" and the panic hook `03-core-concepts.md` described from M1 were sentences about fasthttp, and both were false; M7's `stop` flag and
M8's default retries were fasthttp behaviour that only reading its source found. The ones found
before designing cost nothing; the ones found afterwards cost a milestone's correction.

**Put both arms of a comparison in one session from the start.** M4's four readings and M5's
false "+13 ns of recovery" came from comparing across recordings; from M5 on, the numbers that
mattered came from paired benchmarks in one run — `BenchmarkDeferRecoverOverhead`,
`BenchmarkDispatchPooledVsUnpooled`, `BenchmarkDispatchConnStateHook`. Cross-session drift was
never explained, and the paired shape made that not matter.

**Build the measurements the retrospectives asked for.** A lookup benchmark inside
`internal/router` was asked for in M2 and M3 and never written, so the hybrid map-in-front-of-tree
question is still open. The unexplained numbers were carried forward milestone after milestone,
and M3 assigned them to M8's profiling; M8 took no profile. A list of unexplained numbers is
honest, and it is not an answer.

**Decide what a measurement level can separate before choosing it.** M8's end-to-end level was
designed to place every scenario and in the event placed only transports. A division — CPUs, the
expected rate, the size of the differences — would have said so in the design.

## What is still not understood

The unexplained list, carried from the milestone retrospectives: M1's 43 ns header write, M2's
~9 ns of routing, M3's 15.93 ns of tree scaling, M4's ~1.1 ns per middleware, the cross-session
drift M5, M6 and M7 each recorded, and M8's two — why rice's handler-level time is below Fiber's,
and what makes `net/http`'s body path 4.5 times slower end to end. Each is bounded and
reproducible, and none has a mechanism behind it.

## What comes after the roadmap

Nothing is scheduled. The roadmap's [Explicitly deferred](04-roadmap.md#explicitly-deferred)
list — binding, timeouts, static files, content negotiation, streaming, typed store keys — stays
as it was: not promised, each needing its own design. `c.Query`, `c.Header` and `c.JSON` have
since left that list, built after the roadmap closed along with `c.Body` and `WithMaxBodySize`;
see [Done after M8](04-roadmap.md#done-after-m8). If work resumes, the cheapest useful step is
the one M2 and M3 asked for and nobody built, a lookup benchmark inside `internal/router`,
followed by the first profile any milestone records.
