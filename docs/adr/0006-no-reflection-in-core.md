# ADR-0006 — No reflection in core

Status: Accepted
Date: 2026-09-02

## Context

Every mainstream Go web framework eventually grows `c.Bind(&v)`: decode the request body,
query string or path parameters into a struct, driven by struct tags and `reflect`. It is
the single most requested convenience, and it is also the point at which a framework's
behaviour stops being predictable by reading the call site.

## Decision

Package `rice` imports neither `reflect` nor `encoding/json` anywhere in routing, context
handling or middleware. The one exception is `c.JSON`, which uses `encoding/json` at the
response edge, is documented as allocating, and has a zero-allocation escape hatch in
`c.Bytes`.

Binding and validation may exist later as an opt-in package outside core, so that the cost
appears in the user's import list.

## Alternatives

**Reflection-based binding in core.** The convenient answer, and what users will expect.
Rejected on principles 1 and 2: reflection allocates, it puts type walking on the hot path,
and it makes framework behaviour depend on struct tags that the reader of a handler cannot
see from the handler.

**Code generation.** A `go:generate` step emitting non-reflective decoders. Fast and
type-safe, and it genuinely solves the performance objection. Rejected as scope: a code
generator is a second project with its own build integration, and it does not serve either
stated learning goal.

**Generics-based binding.** `rice.Post[In, Out](app, path, handler)` with a `Decoder[In]`
interface the user implements or generates. Rejected for *core* only. It is the most
promising future direction, and it composes on top of the current handler signature rather
than replacing it — which is another reason ADR-0002 keeps the core signature primitive.

## Consequences

**Makes easy:** a predictable hot path with no type walking. Behaviour readable from the
call site. A small dependency and concept surface.

**Makes hard:** users write their own decoding. For a JSON body that means calling
`json.Unmarshal(c.Body(), &v)` themselves — three lines instead of one, and the borrow
contract matters there, because `c.Body()` is borrowed.

**Forecloses nothing permanently:** an opt-in package can add binding later without touching
core, and that is the point of drawing the line here rather than after the convenience has
been baked in.
