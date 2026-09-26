# ADR-0019 — Streams run after the handler, on their own context, and stop when Shutdown begins

Status: Accepted
Date: 2026-09-26

## Context

Streaming and server-sent events were the last item on the roadmap's *Explicitly deferred* list.
fasthttp streams a body through `SetBodyStreamWriter`, which runs a writer function on its own
goroutine, writing into a pipe the server copies to the connection. Probing it found five things:
a client that leaves is seen only by a later write; a panic in the writer ends the process, because
nothing recovers that goroutine; a HEAD request still runs the writer with nobody reading; a drain
waits for a stream that ends and returns cleanly; and the writer goroutine starts the moment
`SetBodyStreamWriter` is called, not when the handler returns.

Three of rice's rules meet here. The borrow contract (ADR-0005) says a `Ctx` is dead once the
handler returns. The request context (ADR-0010) is deliberately not cancelled when Shutdown begins,
because a handler finishes on its own and the grace period exists to let it. And an event stream
never finishes on its own.

## Decision

- **`c.Stream(fn)` and `c.SSE(heartbeat, fn)` record `fn`; `handle` starts it after the `Ctx` is
  released.** fasthttp's goroutine is started only then, so `fn` never runs beside the handler, the
  middleware or the error funnel, and under `ricedebug` a callback that uses `c` panics every time.
  When the funnel answered the request — an error returned, `HandleError`, a panic — the stream is
  never started and the error response stands. For HEAD nothing is recorded; the headers are the
  answer.
- **A stream gets objects of its own, not the `Ctx`.** `Stream` wraps fasthttp's `bufio.Writer`
  under a mutex; `SSE` wraps a `Stream`. A `Stream` is valid only while `fn` runs: once `fn`
  returns and rice flushes what is buffered, it marks the stream done, and a `Write`,
  `WriteString`, `Flush` or `Send` called after that returns `errStreamClosed` without touching the
  writer — fasthttp puts the `bufio.Writer` back in a pool once the writer returns, and a late
  write could land in another response.
- **A stream's context is its own.** It is `context.WithCancel(streamCtx)`, where the App's
  `streamCtx` is a child of the base context. It is cancelled when Shutdown begins, at force-close,
  when a write fails because the client left, and when `fn` returns. It is not derived from
  `c.Context()`, which may be a middleware's context that ends when the handler does.
- **Shutdown cancels streams when it begins.** Handlers keep ADR-0010's rule. A stream that honours
  its context ends at once, and the drain finishes cleanly; one that does not is force-closed at the
  deadline as before.
- **rice recovers the writer.** A panic in `fn` is logged with its stack; an error is logged unless
  the stream's context was already cancelled. The ErrorHandler is not involved: the status is sent.
- **SSE writes a heartbeat comment on an interval**, because a departed client is noticed only by a
  write. The writer waits for the heartbeat to stop before returning, because fasthttp pools the
  `bufio.Writer` afterwards.

## Alternatives

**Call `SetBodyStreamWriter` from inside `Stream`.** The obvious wrapper. Rejected because fasthttp
starts the goroutine immediately: `fn` would race the handler's remaining code, the middleware and the
funnel, run before the `Ctx` is released, and keep running beside an error response.

**Run the stream synchronously in the handler by hijacking the connection.** Keeps the `Ctx` valid
and lets middleware see the true duration. Rejected: rice would write HTTP framing itself, and a
hijacked connection leaves rice's connection tracking, so Shutdown could no longer force-close it
(ADR-0009).

**Let the handler return a channel of events.** Rice would own the loop and could interleave
heartbeats without a lock. Rejected: it fits SSE only, and a producer blocked on a channel nobody reads
leaks unless it watches a context anyway.

**Derive the stream's context from `c.Context()`.** Carries middleware values. Rejected: `Timeout`'s
context is cancelled when the handler returns, so the stream would start cancelled.

**Treat streams like handlers at Shutdown (ADR-0010).** Rejected by the owner: with any SSE client
connected, every Shutdown would wait out its full deadline and then cut the stream.

## Consequences

**Middleware sees less.** Logger's duration ends when the handler returns; Timeout does not cover the
stream; `middleware.Recover` does not cover `fn` (rice's own recovery does). Headers set before `next`
are sent.

**A stream opened during Shutdown starts cancelled**, and `fn` sees `Done()` at once.

**The write timeout covers the whole stream.** fasthttp sets the connection's write deadline once,
before writing the response, after the handler has returned, so rice cannot lift it for a stream:
with `WithWriteTimeout(d)`, a stream still running d after its handler returned is cut off. An App
serving long-lived streams leaves the write timeout at zero, or serves its streams from a separate App.

**A client that stops reading without closing can pin `fn`.** Once the connection's buffers fill,
`fn` blocks inside a `Send` or `Flush` and cannot see `Done()`; such a stream reaches Shutdown's
deadline and is force-closed, which unblocks the write.

**Proxies may buffer event streams.** Behind nginx, disable proxy buffering for them: set
`X-Accel-Buffering: no` before calling `c.SSE`, or configure `proxy_buffering off`.

**An HTTP/1.0 client gets chunked encoding.** An HTTP/1.0 request without `Connection: keep-alive`
receives the stream with `Transfer-Encoding: chunked` and `Connection: close`; that is fasthttp's
behaviour.

**An App mounted with `FasthttpHandler()` and never shut down stops a stream only when its client
leaves.**

**Three fields on `Ctx`.** `reset` clears them; reading them costs the dispatch path nothing.

**Streams are outside the zero-allocation claim; `SSE.Send` is inside it.**

**A stream is dead once `fn` returns, and a late caller is told, not ignored.** If `fn` handed `s` to
another goroutine and returned before that goroutine's next call, the call gets `errStreamClosed`
rather than a write into whatever `bufio.Writer` fasthttp has by then handed to another response.

**HEAD on a bare `Stream` carries fewer headers than the same GET would.** With no `Content-Type` set
by the handler, HEAD on `Stream` carries neither `Content-Type` nor `Transfer-Encoding: chunked`,
because fasthttp writes those only when it starts the chunked body writer, and HEAD never starts one;
the same GET would carry both, fasthttp's defaults. `SSE` sets its own `Content-Type` before recording
the callback, so HEAD on an SSE stream carries it regardless. The design's D5 said the response carries
the same headers a GET would; that overclaims for a bare `Stream`, and this is the corrected statement.
