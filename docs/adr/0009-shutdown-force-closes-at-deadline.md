# ADR-0009 — Shutdown force-closes connections at the deadline

Status: Accepted
Date: 2026-09-18

## Context

M1's `Shutdown` ran fasthttp's `Shutdown` in a goroutine and returned when the caller's
context ended. Its comment said fasthttp's shutdown "has no deadline of its own". That was
false for the pinned version: fasthttp v1.73.0 has `Server.ShutdownWithContext(ctx)`, which
closes the listeners, closes idle keep-alive connections, polls every 100 ms for the open
count to reach zero, and returns `ctx.Err()` when the context ends. M7 delegates the drain to
it (design D1). Reading it closely for that purpose turned up the problem this ADR exists for.

`ShutdownWithContext` (fasthttp v1.73.0, `server.go:2062–2063`) begins:

```go
s.stop.Store(1)
defer s.stop.Store(0)
```

`serveConn` reads that flag after each response (`server.go:2733`) to leave its keep-alive
loop. When the context ends first, `ShutdownWithContext` returns `ctx.Err()`, and the
`defer` puts the flag back to 0. A keep-alive connection whose handler was still running at
the deadline then finishes that request, finds `stop == 0`, and reads the next one. fasthttp
does not close it. It goes on serving new requests after `Shutdown` has returned.

This is not a theoretical reading. `TestNothingIsServedAfterATimedOutShutdown` sends a
request on a keep-alive connection, holds a second one in its handler past a 100 ms
deadline, lets it finish, and writes a third. With plain `ShutdownWithContext` the third is
answered:

```
lifecycle_test.go:222: the connection served a request after Shutdown returned: "HTTP/1.1 200 OK...slowHTTP/1.1 200 OK...third"
lifecycle_test.go:226: the connection was still open 2s after a timed-out Shutdown
--- FAIL: TestNothingIsServedAfterATimedOutShutdown (2.11s)
```

A program that reads "`Shutdown` returned an error" as "the server has stopped" and goes on
to close its database would close it under live requests. The failure is silent, and it only
happens on the path that exists for when things are already going wrong.

## Decision

At the deadline, rice closes every connection it still tracks. After `Shutdown` returns,
whatever it returns, nothing is served.

Rice tracks open connections through `fasthttp.Server.ConnState`, installed in `New`
(`conns.go`):

- `StateNew` adds the `net.Conn` to `a.conns`, a map under `a.connMu`.
- `StateClosed` and `StateHijacked` remove it.
- `StateActive` and `StateIdle` — which fasthttp reports on every request — return before
  touching the lock.

When `ShutdownWithContext` returns the context's error, `Shutdown` calls `closeConns`: under
`a.connMu` it sets `a.forceClosed = true` and copies the tracked set, then closes every copied
connection after releasing the lock — a `tls.Conn`'s `Close` can block for seconds, and
`connState` needs the lock for every connection that opens or closes meanwhile. The blocked
read or write in that connection's `serveConn` fails, the goroutine exits, and fasthttp's
`StateClosed` untracks it.

**The late-connection rule.** A connection fasthttp accepted before the listener closed can be
reported with `StateNew` *after* the sweep. Because `forceClosed` is set under the same lock
the sweep copies the set under, `connState` closes such a connection on arrival instead of
tracking it, and no connection can fall between the copy and the flag. That
closes the window without depending on timing, and
`TestCloseConnsClosesTrackedAndLateConnections` pins it.

`Shutdown` then returns an error wrapping both `ErrShutdownTimeout` and the context's error
(`fmt.Errorf("%w: %w", ...)`), so `errors.Is` holds for either.

## Alternatives

**Keep fasthttp's behaviour and document it.** The cheapest option: no tracking, no
per-request cost, and a sentence in `Shutdown`'s comment. Rejected because the sentence would
have to say "after a timed-out Shutdown, busy keep-alive connections keep serving, and there is
no way to stop them from outside fasthttp". A shutdown whose failure path leaves the server
running is not a shutdown, and the case it fails in — a request that will not finish — is the
case the deadline exists for.

**Wrap the listener.** Hand fasthttp a listener whose `Accept` returns a wrapped `net.Conn`
that rice can record and close, with no `ConnState` hook and so no per-request calls. Rejected
on correctness grounds, not cost: a wrapper `net.Conn` allocates per connection, and it hides
the concrete connection type from the `keepAliveConn` type assertion in fasthttp's
`acceptConn` (`server.go:2123`), so fasthttp's `TCPKeepalive` setting would silently stop
applying to every connection the App accepts. It is the only alternative that avoids calls on
every request, and it was rejected before the cost of those calls was measured.

**Re-implement the drain.** Stop delegating to `ShutdownWithContext` and write rice's own
close-wait-force loop. Rejected because it cannot work from outside the package:
`serveConn` leaves its keep-alive loop only on fasthttp's unexported `stop` flag, and nothing
rice could write would set it.

## Consequences

**Makes easy:** `Shutdown` has one guarantee, with no "unless": when it returns, nothing more
is served, so no new request can reach a resource an `OnShutdown` hook releases. Hooks run
after the drain and after the force-close for that reason. After a clean drain no handler is
running either; after a timeout, a handler that was cut off may still be, because a goroutine
cannot be stopped (below), and a hook releasing something handlers use must allow for that.

**The guarantee holds for concurrent calls.** `Shutdown` calls take turns through a
capacity-one channel, `a.shutdownSem`: one call drives `ShutdownWithContext`, the force-close
and the hooks before the next starts, so a call that waited returns only after all of that.
A call whose own ctx ends while it waits does not wait on: it runs `closeConns`, which is safe
to run at any moment because `forceClosed` is sticky, and returns an error wrapping
`ErrShutdownTimeout`. Before M7's whole-branch review the calls did not take turns, and
`ShutdownWithContext`, which holds fasthttp's lock for its whole drain, made a second call
wait out the first's drain whatever its own ctx said, then return `nil` — possibly before the
first call's force-close.

**The guarantee holds when `Shutdown` lands as `Serve` starts.** `Serve` publishes its
listener for `Shutdown` to find, then hands it to fasthttp, which records it under its own
lock. A `ShutdownWithContext` between the two finds no listener and returns at once; if
fasthttp then records the listener and accepts a connection before rice closes it, that
connection was never drained, and fasthttp's `stop` flag has been reset. So after closing the
listener, `Shutdown` calls `ShutdownWithContext` again: it finds the late listener and drains
what it accepted — fasthttp's open count covers its accept loop until that returns, so nothing
slips past — or finds nothing and returns at once.
`TestShutdownDrainsAConnectionAcceptedBeforeFasthttpRecordedTheListener` pins it.

One trace of that window outlives the `Shutdown` that closed it: if fasthttp records the
listener only after the second `ShutdownWithContext`, it keeps the already-closed listener in
its list, and the next `ShutdownWithContext` closes it again and reports "use of closed
network connection". `Shutdown` drops any error that `errors.Is(err, net.ErrClosed)`: rice
closed that listener on purpose, and a context error never matches, so a timeout still gets
through. `TestShutdownAfterFasthttpRecordedAClosedListenerReturnsNil` pins it. Before M7's
follow-up a later `Shutdown` returned that error.

**Handlers are not stopped.** Go cannot kill a goroutine. A handler running at the deadline
runs to completion; its connection is gone, so its response is lost, and its pooled `Ctx` is
released when it returns, as M6's borrow contract requires. When the handler's response write
fails, fasthttp logs one line for that connection —
`error when serving connection "…"<->"…": write tcp …: use of closed network connection` — which
is what a force-close looks like in a program's logs. rice sets no fasthttp `Logger`, so the
line goes to fasthttp's default logger.

**The 100 ms poll is fasthttp's, and rice inherits it.** Measured in
`bench/results/M7-lifecycle.txt` (`BenchmarkShutdownLatency`, ten samples each):

| Case | Median | Range |
| --- | ---: | --- |
| `after-last-request` | 95.20 ms | 94.78–95.81 ms |
| `idle-keepalive-only` | 101.4 ms | 100.9–101.7 ms |

Both are one poll tick. The benchmark releases the last request about 5 ms after starting
`Shutdown`, so the drain is noticed at the next tick, about 95 ms later. With only idle
connections open, `closeIdleConns` closes them before the first check, but the first check
still sees a non-zero count: fasthttp decrements `s.open` only when a connection's serving
goroutine exits (`serveConnCleanup`, `server.go:2302`) and when its own accept loop exits
(`defer s.open.Add(-1)`, `server.go:1994`). Both run on other goroutines, woken by the closes,
and in all ten samples the count was still non-zero at the check that immediately follows. So even an
idle server takes one tick to shut down.

**Every request pays for the hook.** fasthttp's `setState` is
`if hook := s.ConnState; hook != nil { hook(nc, state) }` (`server.go:2760–2763`), and it calls
it with `StateActive` and `StateIdle` on every request (`server.go:2436`, `2729`) once any hook
is installed. `BenchmarkDispatchConnStateHook` measures dispatch with and without those two
calls, both arms in one session. It calls `connState` directly, so the load of the func field in
`setState` is not in the number:

```
                         │ dispatch.txt │       dispatch+connstate.txt       │
                         │    sec/op    │   sec/op     vs base               │
DispatchConnStateHook-12    36.94n ± 0%   39.41n ± 1%  +6.69% (p=0.000 n=10)
```

0 B/op and 0 allocs/op in both arms, every sample. **About 2.5 ns per request, and it is not
noise.** The design predicted this would be "within noise of dispatch without the hook" and
said that if it was not, the decision would be reopened. The prediction was wrong. The
decision was reopened and kept: the only way to track connections without per-request calls
is the listener wrapper, rejected above for reasons that have nothing to do with speed, and
dropping tracking brings back the connection that serves after `Shutdown`. Two and a half
nanoseconds per request is the price of the guarantee, and it is paid by every App, whether or
not it ever calls `Shutdown` — a second measured exception to design principle 7, after
ADR-0008's `defer recover()`.

**The ctx an `OnShutdown` hook receives may already be done.** Hooks get the `ctx` passed to
`Shutdown`, and after a timed-out drain that ctx has ended. A hook that needs time of its own
has to make it.

**A `Shutdown` that lands while an `OnStart` hook is running does not wait for it.** It runs
the `OnShutdown` hooks and returns before that `OnStart` hook does; `Serve` then sees `closed`
and returns without serving (`TestShutdownDuringOnStartStopsServe`), but anything the
`OnStart` hook opened after `Shutdown` began is never released by an `OnShutdown` hook.

**A hook must not call `Shutdown` with a ctx that never ends.** Hooks run while their
`Shutdown` holds the turn, so a `Shutdown` from inside a hook waits for a turn released only
after the hook returns. It returns an error wrapping `ErrShutdownTimeout` when its own ctx
ends (`TestShutdownFromAHookWaitsOnlyUntilItsOwnCtxEnds`); with `context.Background()` it
deadlocks. rice cannot tell such a call from a concurrent one on another goroutine, so this is
documented rather than detected.

**Forecloses:** a `ConnState` hook of the user's own on the App's server. rice owns the field,
and offering it would mean chaining a second call onto the per-request path.

**Revisit if fasthttp changes `ShutdownWithContext`.** Every part of this decision rests on
fasthttp v1.73.0's implementation: the `stop` flag reset by `defer`, the 100 ms ticker, where
`s.open` is decremented, and `setState` calling the hook per request. A fasthttp upgrade that
touches `ShutdownWithContext` or `setState` must re-read them. If fasthttp starts closing busy
connections itself at the deadline, the tracking and its 2.5 ns can go.
