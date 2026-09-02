# ADR-0001 — Use fasthttp as the transport

Status: Accepted
Date: 2026-09-02

## Context

The project's stated learning goals are framework architecture and zero-allocation
performance. The transport choice determines how much of the second goal is even reachable:
a transport that allocates a fresh request object per request puts a floor under the whole
system that no amount of care above it can lift.

## Decision

Build on `github.com/valyala/fasthttp`. Depend on it directly and openly in the public API's
behaviour, rather than abstracting it behind a transport interface.

## Alternatives

**`net/http`.** The standard, the ecosystem, HTTP/2 for free, and `http.Handler`
compatibility with every Go middleware ever written. Rejected because it allocates a fresh
`*http.Request` and response writer per request by design, which removes the possibility of
demonstrating a zero-allocation hot path — the second learning goal. Its allocations are
also *invisible*, which is pedagogically worse than fasthttp's visible ones.

**An abstraction over both.** A `Transport` interface with fasthttp and net/http backends.
Rejected on principle 6 and principle 1 together: the abstraction would have to be the
intersection of two incompatible memory models, so it would either force copying at the
boundary (destroying the performance goal) or leak fasthttp's lifetime rules into a net/http
implementation that cannot honour them. It would also roughly double the surface to learn.

**Raw `net.Conn` with hand-written HTTP parsing.** Maximum learning about the protocol.
Rejected as a scope decision: the stated goals are framework architecture and allocation
discipline, not protocol parsing. Writing a correct HTTP/1.1 parser is a whole project, and
it would consume the time budget before any framework existed.

## Consequences

**Makes easy:** a genuinely zero-allocation hot path; visible, teachable memory behaviour;
a small dependency tree.

**Makes hard:** every value handed to a handler is borrowed, which is a real footgun
requiring documentation and a debug build to manage (ADR-0005). Testing needs fasthttp's
in-memory listener rather than `httptest`.

**Forecloses:** HTTP/2 and HTTP/3. The `http.Handler` middleware ecosystem. Any library
that takes an `*http.Request`. This is the single largest thing the project gives up, and
it is given up knowingly.
