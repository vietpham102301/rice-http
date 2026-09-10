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

Add a `ricedebug` build tag that poisons a released `Ctx` — clearing its pointers and
setting a generation counter — so any later method call panics with a message naming the
contract, instead of returning plausible garbage from another request. The check compiles
out entirely in release builds.

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
objects alive. The generation-counter poison is deterministic and free in release builds.

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
