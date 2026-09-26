# Streaming and server-sent events — Design

**Status:** approved, not yet implemented; D2 corrected after prototyping (finding 5)
**Date:** 2026-09-26
**Milestone:** none. Work after the roadmap — see [04-roadmap.md](../../04-roadmap.md), *Explicitly deferred*
**Decision record:** ADR-0019 (new), "Streams run after the handler, on their own context, and stop when Shutdown begins"
**Depends on:** the borrow contract ([ADR-0005](../../adr/0005-context-pooling-and-borrow-contract.md)), rice's
core recovery ([ADR-0008](../../adr/0008-rice-recovers-panics-in-core.md)), Shutdown's force-close
([ADR-0009](../../adr/0009-shutdown-force-closes-at-deadline.md)), the request context
([ADR-0010](../../adr/0010-request-context-cancels-at-force-close.md)), cooperative Timeout
([ADR-0014](../../adr/0014-timeout-is-cooperative.md))

## Goal

Let a handler send a response in pieces as they become available — a raw stream for large or slow
bodies, and server-sent events for pushing updates to a browser — without breaking the borrow
contract, without a panic taking the process down, and with Shutdown able to end open streams
cleanly.

## Question this design answers

fasthttp writes a streamed body on another goroutine after the handler has returned and the `Ctx`
is back in the pool. What does a stream get to hold instead of the `Ctx`, who recovers its panics,
and how does an event stream that never ends on its own let Shutdown finish?

## What the design is for

The owner chose both layers: a raw stream primitive, and SSE on top of it. The owner chose that
open streams are told to stop as soon as Shutdown begins. This is the last of the three deferred
items.

## What exists

Read against rice at `3458973`, fasthttp v1.73.0.

- `RequestCtx.SetBodyStreamWriter(sw func(*bufio.Writer))` wraps `sw` in `NewStreamReader`
  (stream.go:29), which runs `sw` on a new goroutine writing into an in-memory pipe; the server reads
  the pipe and writes chunked output to the connection. fasthttp documents that `sw` must not touch
  the `RequestCtx`.
- `App` holds `baseCtx`/`cancelBase` (app.go:237), cancelled by `closeConns` at force-close
  (conns.go:68). `c.Context()` returns it unless a middleware set one with `SetContext`.
- `Shutdown` (server.go) drains with fasthttp's `ShutdownWithContext`, force-closes at the deadline,
  then runs the OnShutdown hooks.
- `handle` releases the `Ctx` to the pool when the handler returns (app.go, `handle`'s defer).

## What probing found

Throwaway programs against fasthttp v1.73.0 over a real loopback connection:

1. **A disconnect is seen only by a later write.** A client that read the first event and closed its
   socket was not noticed by the writer until its second write after the close, three seconds later
   with a write every second: the first write after the close still fit in kernel buffers. An idle
   stream never learns the client left. Detecting it needs writes; for SSE that is a heartbeat.
2. **A panic in the stream writer kills the process.** `NewStreamReader` runs `sw` in a bare
   `go func(){ sw(bw); ... }()`. The only recover on the streaming path, in
   `Response.writeBodyStream` (http.go:2284), guards the reading side, not that goroutine.
3. **A HEAD request still runs the writer.** For HEAD, fasthttp sent the headers, and the writer ran
   to its end: its first `Flush` returned nil and nothing ever reported that no one was reading. An
   event stream on HEAD would run until the client closed and a heartbeat failed, or forever.
4. **A drain waits for a stream that ends.** With a stream open, `ShutdownWithContext` returned nil
   about 100 ms after the writer returned. A stream told to stop lets Shutdown finish cleanly; one
   that does not stop holds it to its deadline.
5. **The writer goroutine starts when `SetBodyStreamWriter` is called, not when the handler returns.**
   `Response.SetBodyStreamWriter` calls `NewStreamReader` at once (http.go:309), and that starts the
   goroutine. Called from inside a handler, `fn` would run concurrently with the rest of the handler,
   with the middleware unwinding, and with the error funnel — and before the `Ctx` is released, so the
   ricedebug check could not be relied on to catch `fn` touching `c`. Found while prototyping this
   design, after it was first approved; D2 is the correction.

## Non-goals

- WebSockets, HTTP/2 server push, and any bidirectional protocol.
- A middleware that observes the stream's end (Logger recording a stream's true duration).
- Reconnection logic, event buffering or replay on the server; `Last-Event-ID` is the handler's to
  read and act on.
- Compression of streamed bodies.
- Streaming request bodies.

## Decisions

### D1 — `c.Stream(fn)` and `c.SSE(heartbeat, fn)`

```go
func (c *Ctx) Stream(fn func(s *Stream) error) error

type Stream struct{ /* unexported */ }

func (s *Stream) Write(p []byte) (int, error)
func (s *Stream) WriteString(v string) (int, error)
func (s *Stream) Flush() error
func (s *Stream) Context() context.Context

func (c *Ctx) SSE(heartbeat time.Duration, fn func(s *SSE) error) error

type SSE struct{ /* unexported */ }

type Event struct {
	ID    string
	Event string
	Data  string
	Retry time.Duration
}

func (s *SSE) Send(ev Event) error
func (s *SSE) Context() context.Context
```

```go
app.GET("/events", func(c *rice.Ctx) error {
	last := string(c.Header("Last-Event-ID")) // copied before the stream starts
	return c.SSE(15*time.Second, func(s *rice.SSE) error {
		for {
			select {
			case <-s.Context().Done():
				return nil
			case ev := <-updatesSince(last):
				if err := s.Send(ev); err != nil {
					return err
				}
			}
		}
	})
})
```

Both call `c.poison.check()` first, record the callback on the `Ctx` and return nil, so `return c.SSE(...)` is
the idiom. Status and headers set before the call are sent; with no status set it is 200. `SSE` also
sets `Content-Type: text/event-stream` and `Cache-Control: no-cache`. A second `Stream` or `SSE` in
one request panics with a `rice: ` message. A nil `fn` panics. `SSE` with a negative heartbeat
panics; zero disables the heartbeat.

`Write` and `WriteString` buffer; `Flush` sends what is buffered. `Send` writes one event and
flushes. After the client is gone, each of them returns an error.

### D2 — the callback starts after the Ctx is released, and only if the request was not answered

`Stream` and `SSE` only record `fn` (and the heartbeat) in three fields on `Ctx`, which `reset`
clears. `handle` reads them before it releases the `Ctx`, releases it, and only then calls
`SetBodyStreamWriter` (finding 5). So `fn` starts strictly after the handler, every middleware and the
error funnel have finished, and after the `Ctx` is back in the pool.

If the request was answered through the funnel — the chain returned an error, a middleware called
`HandleError`, or the handler panicked — the stream never starts: the error response is the response.
A HEAD request records nothing (D5).

`fn` runs on the writer goroutine. It must not call any method of `c` — the borrow contract,
unchanged — and under `ricedebug` doing so panics, deterministically, because the `Ctx` was poisoned
before `fn` started. Anything `fn` needs from the request is read and copied in the
handler, before the call. `Stream` and `SSE` are separate objects, not views of `Ctx`, so nothing
they hold is pooled.

### D3 — each stream has its own context, derived from a stream context on the App

`New` creates `streamCtx, cancelStreams := context.WithCancel(baseCtx)`. Each stream gets
`context.WithCancel(streamCtx)`, created on the writer goroutine when the stream starts. It is cancelled when:

- **Shutdown begins** — `Shutdown` calls `cancelStreams()` before it drains (the owner's choice);
- **Shutdown force-closes** — `baseCtx` is cancelled by `closeConns`, which cancels `streamCtx`;
- **the client is gone** — the first `Write`, `Flush`, `Send` or heartbeat that fails cancels it;
- **`fn` returns** — the stream's own cancel runs, ending the heartbeat.

It is deliberately not derived from `c.Context()`. That context may be `Timeout`'s, which is cancelled
as soon as the handler returns, which is before the stream starts; a stream derived from it would be
dead on arrival. The consequence, stated in the doc comment: values and deadlines a middleware put on
`c.Context()` do not reach the stream.

A request that opens a stream after Shutdown has begun gets an already cancelled context and sees
`Done()` at once. An App mounted with `FasthttpHandler()` and never shut down only stops a stream when
the client leaves.

This departs from ADR-0010 for streams only: a handler's context is still not cancelled when Shutdown
begins, because the handler will return by itself and the grace period exists to let it. A stream
will not; the grace period is useless to it unless it is told. ADR-0019 records the difference.

### D4 — rice recovers the writer goroutine

rice's writer wrapper recovers a panic from `fn`, logs it with its stack in the same form as a
recovered handler panic, and ends the stream (finding 2). The status is already sent, so the
ErrorHandler is not called and cannot be: the `Ctx` is gone.

An error returned by `fn` is logged as `rice: stream: <err>` unless the stream's context is already
cancelled — the client left or Shutdown began — in which case the error is the expected way a stream
ends and is not logged. When `fn` returns, rice flushes whatever is buffered, ignoring the error, and
the chunked response ends normally.

### D5 — HEAD sends headers and never runs `fn`

For a HEAD request, `Stream` and `SSE` set the headers and return without registering a writer
(finding 3). The response carries the same headers a GET would.

### D6 — the SSE wire format

Per the WHATWG HTML specification's event-stream format:

- `ID` non-empty → `id: <ID>\n`; `Event` non-empty → `event: <Event>\n`; `Retry` > 0 →
  `retry: <milliseconds>\n`.
- `Data` is split on `\n`, and each line is written as `data: <line>\n`; an empty `Data` writes one
  `data: \n`, so every event dispatches. A `\r\n` or lone `\r` in `Data` is treated as a line break.
- The event ends with `\n`, and `Send` flushes.
- An `ID` or `Event` containing `\r`, `\n`, or (for `ID`) NUL makes `Send` return an error and write
  nothing: it would change the stream's framing. It does not panic, because these values may come
  from users.
- `Retry` is formatted with `strconv.AppendInt` into a stack buffer and written byte by byte:
  `bufio.Writer.Write` may pass its slice to the underlying writer, which moves the buffer to the heap
  (measured: one allocation per `Send` with `Write`, none with `WriteByte`).
- The heartbeat is the comment line `:\n\n`, written every `heartbeat` while the stream is idle or
  not, and flushed.

### D7 — one writer, one lock

`Send`, `Write`, `WriteString`, `Flush` and the heartbeat all write through one `bufio.Writer` under
one mutex, so a heartbeat can never land inside an event. The heartbeat goroutine exits when the
stream's context is cancelled, which includes `fn` returning, and the writer waits for it to exit before returning: fasthttp
returns the `bufio.Writer` to a pool when the writer returns, and a heartbeat still writing then would
write into another response.

### D8 — what middleware sees

Documented in the doc comments and ADR-0019, not changed:

- `Logger` records the status and a duration that ends when the handler returns, not when the stream
  does.
- `Timeout`'s deadline does not cover the stream.
- `middleware.Recover` does not cover `fn`; rice's own recovery (D4) does.
- Headers set by middleware before `next` — CORS, `X-Request-Id` — are sent with the stream.

## Consequences to record

- ADR-0019: streams run after the handler on their own context; Shutdown cancels streams at its start
  while handlers keep ADR-0010's rule; rice recovers the writer; HEAD never runs `fn`; D8.
- `05-performance-model.md`: streams are outside the zero-allocation claim; `SSE.Send` is inside it.

## Components

| File | Contents |
|---|---|
| `stream.go` | `Stream`, `Ctx.Stream`, the writer wrapper (recover, logging, context, flush), the HEAD rule |
| `sse.go` | `SSE`, `Event`, `Ctx.SSE`, the event encoder, the heartbeat |
| `app.go` | `streamCtx`, `cancelStreams`; `handle` starts a recorded stream after release |
| `ctx.go` | the three stream fields, cleared by `reset` |
| `server.go` | `Shutdown` cancels streams before draining |
| `stream_test.go`, `sse_test.go` | behaviour over a real loopback server |
| `alloc_test.go` | the budgets |
| `bench/rice_bench_test.go` | `BenchmarkSSESend` |

## Allocation budget

- `SSE.Send` with `ID`, `Event`, a two-line `Data` and a `Retry`: **0**, on a warm writer. `Retry` is
  formatted with `strconv.AppendInt` into a stack buffer.
- The setup of one stream — `c.Stream` or `c.SSE` through the dispatch path, up to the handler's return
  — pinned at its measured figure: the `Stream`/`SSE` object, the stream context, the closure and
  fasthttp's pipe and goroutine allocate, and that is accepted.

Measured with and without `-race`.

## Testing

Over a real loopback server (the lifecycle tests' `serve` helper), reading the raw response:

- **Stream:** chunks arrive before `fn` returns (the test reads the first chunk, then lets `fn`
  continue); status and headers set before `Stream` are sent; the response ends cleanly when `fn`
  returns.
- **SSE format:** `id`, `event`, `retry` in milliseconds, multi-line `Data`, `\r\n` in `Data`, empty
  `Data`; `Content-Type` and `Cache-Control`; `Send` returns an error for an `ID` or `Event` with a line
  break and writes nothing.
- **Heartbeat:** `:\n\n` arrives within the interval; zero disables it; negative panics.
- **Disconnect:** a client that closes is detected — the stream's context is cancelled — within two
  heartbeat intervals plus a margin.
- **Shutdown:** with an SSE stream open that honours its context, `Shutdown` returns nil well before
  its deadline; a stream that ignores its context is force-closed and `Shutdown` returns
  `ErrShutdownTimeout`; a stream opened after Shutdown began sees `Done()` at once.
- **Errors and panics:** a panic in `fn` does not crash the test process and is logged with a stack;
  an error from `fn` is logged; the write error after a disconnect is not logged (use `captureLog`).
- **Rules:** a handler that records a stream and then returns an error answers the error and `fn`
  never runs; a second `Stream`/`SSE` panics; a nil `fn` panics; HEAD returns the headers and `fn`
  never runs; under `ricedebug`, calling a method of `c` inside `fn` panics with the use-after-release
  panic.
- **Leaks:** after the stream ends, no heartbeat goroutine remains (count by stack frame, as the
  Static lifecycle tests do).

## Documentation

ADR-0019; `03-core-concepts.md` (a Streaming section: the API, D2, D3, D8); `05-performance-model.md`;
`04-roadmap.md` (the last item leaves *Explicitly deferred*); `docs/progress.md`; README (a short SSE
example).

## Exit criteria

`make test`, `make test-debug` and `make lint` pass, repeatedly under `-race` for the timing tests;
every decision D1–D8 has a test or is stated in a doc comment; `SSE.Send` holds 0; root-package
coverage does not fall.

## Open questions

None.
