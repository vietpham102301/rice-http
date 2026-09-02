# 00 — Overview

## What rice is

A minimal HTTP framework for Go, built on fasthttp, exposing a context-object handler API:

```go
app := rice.New()

app.GET("/users/:id", func(c *rice.Ctx) error {
    return c.JSON(200, User{ID: c.ParamString("id")})
})

app.Run(":8080")
```

The whole public surface is intended to fit in a single afternoon of reading. Complexity
that cannot be removed is pushed into `internal/`, where it can be as intricate as it
needs to be without leaking into the API that users learn.

## Why fasthttp and not net/http

fasthttp is not a drop-in faster `net/http`. It is a different set of trade-offs, and
those trade-offs are exactly the subject matter of this project:

- It reuses request and response objects across connections instead of allocating fresh
  ones. That forces the framework above it to think about object lifetimes, which is the
  central discipline this project is trying to learn.
- It hands out `[]byte` views into its read buffer rather than `string`. Every conversion
  to `string` is a visible, deliberate allocation. In `net/http` the allocations are real
  but invisible.
- It has no `context.Context` plumbing, no `http.Handler` interface, no middleware
  conventions. There is nothing to inherit, so every structural decision has to be made
  and justified rather than copied.

The cost is real and is accepted knowingly: no HTTP/2, a smaller ecosystem, and an object
lifetime model that will bite anyone who has not read the borrow contract. See
[ADR-0001](adr/0001-use-fasthttp-as-transport.md).

## Non-goals

These are not "later" — they are deliberately outside the framework core, permanently.

| Not in core | Why |
| --- | --- |
| Request binding and validation from struct tags | Requires reflection in the hot path. Contradicts principle 2. Belongs in an optional side package. |
| Dependency injection | A container hides the wiring the project is trying to make visible. |
| Configuration loading | Orthogonal to HTTP. Any config library composes fine from outside. |
| Logging implementation | rice defines a hook, not a logger. |
| OpenAPI generation, CLI scaffolding | Large surface, near-zero learning value for the two stated goals. |
| WebSockets, HTTP/2, TLS management | fasthttp's territory, or a separate package's. |
| ORM, templates, sessions, auth | Not framework concerns. |

Anything on this list that later proves genuinely necessary gets an ADR arguing why the
line moved, not a quiet commit.

## Who this is for

One reader: someone who wants to know how Gin, Echo and Fiber actually work, and is
willing to build a smaller one to find out. The docs are written to that reader — they
explain reasoning, not just outcomes, and they record the options that were rejected.

## Success criteria

The project has succeeded when all of these are true:

1. A static route with no middleware serves a plaintext response at **0 allocations per
   request**, proven by a test, not a benchmark eyeball.
2. A parameterised route (`/users/:id`) resolves and exposes its parameter at
   **0 allocations per request** when the parameter is read as bytes.
3. Every public type in package `rice` can be explained in one paragraph, and the
   explanation exists in [03-core-concepts.md](03-core-concepts.md).
4. Every non-obvious decision has an ADR that names at least one rejected alternative.
5. `docs/progress.md` reads as a coherent story of what was learned, in order.

Throughput relative to Gin or Fiber is *reported* in M8 but is explicitly not a success
criterion. Beating them by copying tricks without understanding them would be a failure
of this project even if the numbers were good.
