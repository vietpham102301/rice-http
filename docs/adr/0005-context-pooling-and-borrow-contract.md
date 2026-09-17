# ADR-0005 — Pool the context, publish a borrow contract

Status: Accepted
Date: 2026-09-02

## Context

The per-request `Ctx` is the largest single allocation in a naive implementation, and it
happens on every request. Removing it is the difference between "fast" and the
zero-allocation claim in [05-performance-model.md](../05-performance-model.md). But a
reused object handed to user code can be retained by that user code, and Go's type system
cannot express "this pointer dies when the function returns".

## Decision

Pool `*Ctx` in a `sync.Pool`, reset it on acquire and release it unconditionally after the
handler returns. Publish an explicit **borrow contract**: `*Ctx` and every value reachable
from it are valid only until the handler returns. Provide a copying alternative for every
borrowed accessor, named so the copy is the longer word (`Param` borrows, `ParamString`
copies).

Add a `ricedebug` build tag under which a released `Ctx` is poisoned and **never returned
to the pool**, so any later method call panics with a message naming the contract instead
of returning plausible data from another request. The check compiles out entirely in
release builds.

## Alternatives

**No pooling; allocate a `Ctx` per request.** Completely safe, trivially correct, and any
escape is just a normal Go heap object. Rejected: it puts a floor under the whole system and
abandons the project's second learning goal. It remains the right choice for a framework
optimising for safety, and that is worth stating.

**Pool, but copy every value out of the buffers on acquire.** Safe borrowing, because
nothing aliases fasthttp's memory. Rejected: it trades one allocation for several, and the
copies happen whether or not the handler reads the values.

**Pass `Ctx` by value.** Escape analysis might keep it on the stack, removing the problem at
the root. Rejected: the struct is large enough that copying it per middleware call is worse
than a pointer chase, and any method taking a pointer receiver — needed for the mutable
response state — reintroduces the escape.

**Runtime finalizer to detect retention.** Would catch escapes automatically without a build
tag. Rejected: finalizers are unreliable, non-deterministic, and would themselves keep
objects alive. The poison is deterministic and free in release builds — though not by the
generation counter this ADR originally named; see the M6 correction at the end of this file.

## Consequences

**Makes easy:** the zero-allocation hot path, and with it the project's headline claim.

**Makes hard:** a real, silent, data-corrupting bug class. Storing `c` in a struct, closing
over it in a goroutine, or keeping a `[]byte` from it past the handler all produce garbage
that only appears under load, when a connection is reused. This is the single most dangerous
thing in the framework.

**Requires:** the borrow contract stated in every document that hands out a borrowed value;
a copying counterpart for every borrowing accessor; the `ricedebug` poisoning build; and a
concurrency test under `-race` that hammers the pool.

**Accepted risk:** documentation and a debug build mitigate the footgun; they do not remove
it. A user who never reads the docs and never runs the debug build can ship the bug.

**Recorded in M3.** M2 measured the 404 path at zero allocations, because escape
analysis kept the miss-path `Ctx` on the stack, and M2's retrospective predicted
that M5's configurable `ErrorHandler` would eventually take that zero away. M3
took it away first, for a different reason: `Lookup` fills a `*Params`, `Params`
lives on the `Ctx`, so the `Ctx` must be constructed before the lookup and now
escapes on both paths. The prediction that the zero would not last was correct;
the mechanism named for it was not the one that arrived. This is the clearest
available argument for the rule M2 adopted — measure a zero, document why it
holds, and do not pin it with a test unless the design guarantees it.

**Corrected in M6.** This ADR originally decided to poison a released `Ctx` by "clearing its
pointers and setting a generation counter" and did not say whether the poisoned object went
back into the pool. If it does, the mechanism fails at the one moment it is needed: the next
request acquires the same object, `reset` clears the poison, and a stale reference to it reads
that request's data without panicking. A generation counter cannot help, because the code
holding the stale pointer has no generation of its own to compare — the stale pointer and the
reused object are the same pointer. The debug build now keeps poisoned contexts out of the pool
entirely. Its price is a fresh `Ctx` for every request — up to three objects through `newCtx`:
the `Ctx`, its store slice, and its parameter slice when the App has a parameterised route at all
— paid in that build only.
`TestARetainedCtxPanicsEvenAfterAnotherRequest` fails when the poisoned `Ctx` is returned to the
pool, which is the empirical form of this correction. See the M6 design doc, D5.

The test has to make the stale call from *inside* the next request's handler, while that request
still holds the reused `Ctx`. That window is the original mechanism's blind spot — not any use
after release. M6's first attempt at this test checked the retained `Ctx` only after both
requests had finished, and it could not fail on its own fault: by then the shared object had
been released again and re-poisoned, so it panicked whether or not the live window was
protected. Modelling ADR-0005 faithfully — `poolReuse = true` plus clearing the poison on
acquire — makes the corrected test fail like this:

```
$ go test . -tags ricedebug -run TestARetainedCtxPanicsEvenAfterAnotherRequest -count=1
    ricedebug_test.go:111: stale call on request 1's Ctx while request 2 held it: recovered <nil> (returned "2"), want the use-after-release panic
--- FAIL: TestARetainedCtxPanicsEvenAfterAnotherRequest (0.00s)
```

`recovered <nil>` is the point: nothing panicked. Request 1's retained `*Ctx` answered with
request 2's parameter, `"2"` — silent cross-request data leakage, which is the bug the debug
build exists to make loud.
