# ADR-0002 — Handlers return an error

Status: Accepted
Date: 2026-09-02

## Context

The handler signature is the most-used API in the framework. It determines how failure is
expressed, and therefore how readable every handler in every program built on rice will be.
Go's two mainstream frameworks disagree: Gin uses `func(*gin.Context)` with abort flags,
Echo uses `func(echo.Context) error`.

## Decision

`type Handler func(c *Ctx) error`. Returning a non-nil error ends the request and routes the
error through the single `ErrorHandler`. There is no abort flag, no `c.Abort()`, and no
sentinel value the framework inspects.

## Alternatives

**`func(*Ctx)` with abort state (Gin's shape).** Familiar to the largest number of Go
developers, and it allows a middleware to stop the chain without the caller cooperating.
Rejected because the resulting control flow is invisible: `c.AbortWithStatus(401)` does not
stop execution, so every call site must remember to `return` immediately after, and
forgetting produces a handler that keeps running after the response is written. This is a
well-known recurring bug class in Gin codebases. It also conflicts with principle 5.

**`func(*Ctx) (Response, error)`.** Makes the response a value, which is testable without a
server and composes beautifully. Rejected on principle 1: a `Response` value has to be
constructed per request, and constructing it is an allocation the current shape does not
need. Worth revisiting if the allocation could be avoided.

**Generic typed handlers, `func(*Ctx, In) (Out, error)`.** Type-safe, self-documenting, and
the direction newer frameworks are moving. Rejected for core because automatic decoding of
`In` requires reflection (ADR-0006) and the automatic encoding of `Out` puts JSON on the hot
path. It remains a plausible opt-in layer built *on top of* this signature later, which is
another argument for keeping the core signature primitive.

## Consequences

**Makes easy:** ordinary Go control flow — `return err` really returns. One place in the
program converts errors to responses. Middleware can inspect and rewrite errors on the way
out, because they are return values.

**Makes hard:** a handler that writes a response *and* returns an error is ambiguous, and
the framework has to define what happens (the error handler runs; if a response was already
committed it may only be able to log). This case needs a documented rule and a test.

**Costs:** developers arriving from Gin have to unlearn `c.Abort`. Accepted; the alternative
is inheriting the bug class.
