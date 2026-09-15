# 03 — Core Concepts

Seven concepts. If you understand these, you understand rice. Everything else is a helper.

All signatures below are the *planned* API. Nothing here is implemented until the
corresponding milestone in [04-roadmap.md](04-roadmap.md) is marked done.

---

## 1. Handler

```go
type Handler func(c *Ctx) error
```

The single unit of work. It receives a borrowed context and returns an error or nil. It
does not receive a writer and a request as separate values, because with fasthttp they are
two views of one reused object, and pretending otherwise would invite the wrong mental
model.

Returning an error is the *only* way to signal failure. There is no abort flag, no
`c.Abort()`, no sentinel status the framework inspects. This means control flow is visible
at the call site: if you see `return err`, the request is over.

```go
func getUser(c *rice.Ctx) error {
    u, err := store.Find(c.ParamString("id"))
    if err != nil {
        return rice.NewHTTPError(404, "user not found").Wrap(err)
    }
    return c.JSON(200, u)
}
```

---

## 2. Ctx

```go
type Ctx struct { /* unexported */ }
```

A pooled handle to one in-flight request. It is the only argument a handler gets, and it
is **borrowed** — see principle 3. It carries four things: the underlying
`*fasthttp.RequestCtx`, the captured route parameters, a small per-request key/value store
for middleware, and a back-pointer to the `App`.

### Read side

| Method | Returns | Allocations | Note |
| --- | --- | --- | --- |
| `Method() []byte` | verb | 0 | borrowed |
| `Path() []byte` | request path | 0 | borrowed |
| `Param(name string) []byte` | route param | 0 | borrowed, empty if absent |
| `ParamString(name string) string` | route param | 1 | copies, safe to keep |
| `Query(name string) []byte` | query value | 0 | borrowed |
| `Header(name string) []byte` | request header | 0 | borrowed |
| `Body() []byte` | request body | 0 | borrowed, may be empty on streamed bodies |

The pattern is consistent and worth stating once: **byte-returning methods are free and
borrowed; string-returning methods copy and are yours.** Nothing in rice returns a string
that secretly aliases a buffer.

### Write side

| Method | Effect | Allocations |
| --- | --- | --- |
| `Status(code int) *Ctx` | sets status, chainable | 0 |
| `SetHeader(k, v string)` | sets a response header | 0 amortised |
| `String(code int, s string) error` | plaintext body | 0 |
| `Bytes(code int, b []byte) error` | raw body | 0 |
| `JSON(code int, v any) error` | JSON body via `encoding/json` | see below |

`JSON` is the one place core touches `encoding/json`, and it is not free: encoding an
arbitrary value costs allocations proportional to the value's shape, and `v any` boxes the
argument. This is documented rather than hidden, and the number is recorded in the
benchmark suite. A user who needs zero-allocation JSON writes bytes they encoded
themselves and calls `Bytes`.

### Per-request store

```go
func (c *Ctx) Set(key string, v any)
func (c *Ctx) Get(key string) (any, bool)
```

Backed by a small inline slice of key/value pairs, not a map. Middleware typically stores
one or two values, and a linear scan over three entries beats a map allocation. The slice
grows to a heap allocation past its inline capacity; that is a documented cliff, not a
secret.

### Lifetime

`*Ctx` and everything borrowed from it die when the handler returns. To use a value later:

```go
id := c.ParamString("id")          // copied — safe
go audit(id)                        // fine

id := c.Param("id")                 // borrowed
go audit(id)                        // BUG: reads a reused buffer
```

Under `-tags ricedebug`, a released `Ctx` is poisoned and any later method call panics with
a message pointing at this section. That check is compiled out of release builds entirely,
so it costs nothing in production. See [ADR-0005](adr/0005-context-pooling-and-borrow-contract.md).

---

## 3. Middleware

```go
type Middleware func(next Handler) Handler
```

A decorator: it takes the next handler and returns a replacement. This is the
`func(Handler) Handler` shape, not the `c.Next()` shape used by Gin and Fiber.

```go
func RequestID() rice.Middleware {
    return func(next rice.Handler) rice.Handler {
        return func(c *rice.Ctx) error {
            c.Set("request_id", newID())
            return next(c)
        }
    }
}
```

Three properties follow from this shape, and they are why it was chosen:

1. **The chain is compiled once.** At build time, `mw1(mw2(mw3(handler)))` collapses into a
   single closure stored on the route. At request time there is no index to advance, no
   slice to walk, and no per-request state for the chain itself.
2. **Control flow is ordinary Go.** "Skip the rest" is `return`. "Do something after" is
   code after `next(c)`. There is no framework rule to memorise about what happens if you
   forget to call `Next`.
3. **Errors compose naturally.** A middleware can inspect and rewrite the error coming back
   from `next(c)` because it is a return value, not a field on shared state.

The cost, stated honestly: closures are less obvious to read than `c.Next()` for someone
who has only seen Gin, and the type is a mouthful. Judged worth it.
See [ADR-0003](adr/0003-middleware-as-prebuilt-closure-chain.md).

**Ordering.** Middleware runs outermost-first on the way in, innermost-first on the way
out. `app.Use(A, B)` on a route with handler H executes A → B → H → B → A.

---

## 4. App

```go
type App struct { /* unexported */ }

func New(opts ...Option) *App

// Option
func WithErrorHandler(h ErrorHandler) Option

func (a *App) Use(mw ...Middleware)
func (a *App) GET(path string, h Handler, mw ...Middleware)
// POST, PUT, PATCH, DELETE, HEAD, OPTIONS, and Handle(method, ...)
func (a *App) Group(prefix string, mw ...Middleware) *Group

func (a *App) Build()

func (a *App) Run(addr string) error
func (a *App) Shutdown(ctx context.Context) error
```

`Build` compiles every route's middleware chain once and is called automatically by `Run`,
`Serve` and `FasthttpHandler`; calling it early is only useful to make a configuration error
surface before the listener opens. Registration after `Build` panics.

The root object. It owns the route trees, the global middleware list, the context pool, the
error handler and the fasthttp server. It has the two phases described in
[02-architecture.md](02-architecture.md): registration, then serving, with `build()`
between them.

`Run` blocks. `Shutdown` stops accepting connections, waits for in-flight requests up to
the deadline in the passed `context.Context`, then returns. Lifecycle hooks
(`OnStart`, `OnShutdown`) exist so users can order their own resource teardown against the
server's, which is the thing that is genuinely hard to get right by hand.

---

## 5. Group

```go
type Group struct { /* unexported */ }

func (g *Group) Use(mw ...Middleware)
func (g *Group) GET(path string, h Handler, mw ...Middleware)
func (g *Group) Group(prefix string, mw ...Middleware) *Group
```

A prefix plus a middleware list. It is purely a registration-time convenience: groups do
not exist at request time, because by then every route holds one flat compiled chain. A
nested group concatenates prefixes and appends middleware, so ordering is
`app middleware → outer group → inner group → route middleware → handler`.

**Prefix and path rules.** A prefix must be empty, or begin with `/` and not end with `/`.
A route path registered on a group must begin with `/`, or be empty — an empty path
registers the group's bare prefix, which is the only way to give the group's own root the
group's middleware. Both rules panic at the offending call rather than at `Build`, because
a prefix already begins with `/`, so `prefix + path` looks well-formed however malformed
`path` is: `Group("/api")` with `GET("users", h)` would otherwise register `/apiusers`
silently.

---

## 6. Router

Not exported. Lives in `internal/router`.

Per-method radix trees. Insert validates and rejects conflicts at registration time; lookup
walks the tree comparing byte slices and fills a caller-provided parameter slice, so lookup
itself allocates nothing.

```go
// internal API, shown to make the zero-allocation contract concrete
func (t *Tree) Lookup(path []byte, params *Params) (Handler, bool)
```

The parameter slice is passed *in*, not returned, precisely so that it can live on the
pooled `Ctx` and be reused. This is the small API decision that makes the whole
zero-allocation claim possible, and it is the reason `Lookup` looks slightly awkward.

---

## 7. Errors

```go
type HTTPError struct {
    Code    int
    Message string
    Err     error   // wrapped cause, optional
}

func NewHTTPError(code int, msg string) *HTTPError
func (e *HTTPError) Error() string
func (e *HTTPError) Unwrap() error

// PanicError is a recovered panic, presented to the error funnel as an error
// like any other — see Panics, below.
type PanicError struct {
    Value any    // what was passed to panic()
    Stack []byte // captured at recovery, while the panicking frames were still live
}

func (e *PanicError) Error() string
func (e *PanicError) Unwrap() error // Value when it is itself an error, nil otherwise

type ErrorHandler func(c *Ctx, err error)

// DefaultErrorHandler is the ErrorHandler an App uses unless WithErrorHandler
// replaces it. Exported so a custom handler can delegate to it.
func DefaultErrorHandler(c *Ctx, err error)
```

One error type and one funnel. Every error returned by any handler or middleware, every
route miss, and every recovered panic reaches `app.ErrorHandler`, whose default behaviour
is:

1. The concrete error is `*PanicError` → respond 500 (a panic is always 500), or a concrete
   `*HTTPError` with no wrapped cause → respond with its own code. Both are checked with a
   type switch in front of the fallback below, which is what every error this funnel builds
   for itself (`ErrNotFound`, a fresh `NewHTTPError`) matches without allocating.
2. Otherwise, `errors.As` the error to `*PanicError` then `*HTTPError`. This is the path a
   handler-wrapped error takes (`fmt.Errorf("...: %w", err)`), and it is also the path an
   `*HTTPError` that itself wraps a cause takes, whether or not anything wraps the
   `*HTTPError` in turn — an `*HTTPError` wrapping a `*PanicError` reaches this pair even
   completely unwrapped, so a recovered panic is always a 500 no matter how many layers wrap
   it or don't. Checking `*PanicError` before `*HTTPError` here is load-bearing for the same
   reason: `PanicError.Unwrap` returns the panicked value, so an `*HTTPError` built from
   `panic(rice.NewHTTPError(400, "x"))` would otherwise answer 400 through this fallback.
3. Otherwise → respond 500 with a generic message, and log the real error rather than
   sending it. **The cause is never written to the response body**, because leaking internal
   error strings to clients is how databases end up described in HTTP responses.

`respond` — the function that actually writes the response — also coerces a status code
outside the range HTTP defines (100–599) to 500, logging the value it replaced. The case
this exists for is `HTTPError`'s own zero value: `&HTTPError{Message: "nope"}`, which the
type above shows as the ordinary way to build one when there is no cause to wrap, leaves
`Code` at 0, and fasthttp's `ResponseHeader.StatusCode()` answers `StatusOK` for a zero
status. Without the coercion, a handler returning an error could get a 200 — the funnel's
entire premise, defeated by the zero value of its own error type.

Replacing the error handler is the supported way to change how a service reports failure —
to emit RFC 7807 problem documents, for example. There is exactly one such place, by
design.

**Panics.** fasthttp has no panic hook to install — its only `recover()` on the request path
guards body-stream writes, nothing upstream of the handler. Without one, a panicking handler
takes the whole process down, not just its connection: `s.Handler(ctx)` is called bare from
a worker-pool goroutine. rice's core therefore installs its own deferred recovery around the
whole dispatch, converting a panic into a `*PanicError` and running it through the same
funnel as everything else. It is a safety net for bugs, not an error mechanism, and it is a
deliberate exception to principle 7 — see [ADR-0008](adr/0008-rice-recovers-panics-in-core.md).
The `middleware.Recover` package exists for users who want a panic converted to an ordinary
returned error *before* it reaches the core recovery, so that outer middleware still sees it
as a normal return value rather than an unwound stack.
