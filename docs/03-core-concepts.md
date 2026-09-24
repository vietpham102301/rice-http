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
is **borrowed** — see principle 3. It carries six things: the underlying
`*fasthttp.RequestCtx`, the captured route parameters, a small per-request key/value store
for middleware, a back-pointer to the `App`, the context a middleware has installed, if
any, and whether a middleware has already settled the request's error with `HandleError`.

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
| `ClientIP() net.IP` | connection peer address | 0 | borrowed, reads no header |
| `Context() context.Context` | the context for work this request triggers | 0 | **owned** — safe to keep, see below |

The pattern is consistent and worth stating once: **byte-returning methods are free and
borrowed; string-returning methods copy and are yours.** Nothing in rice returns a string
that secretly aliases a buffer. `ClientIP` follows the rule — a `net.IP` is a `[]byte`, and it
is borrowed like every other one. `Context` is the single row that does not, and it has its own
section below.

`ClientIP` reports the address the connection came from, and reads no header. Behind a reverse
proxy that is the proxy's address, which is the honest answer: trusting `X-Forwarded-For` by
default lets any client declare its own address. `middleware.RealIP(trustedHops)` resolves that
header, against a declared number of trusted proxy hops, and it does so by rewriting the
connection address with fasthttp's `SetRemoteAddr` — `ClientIP` then reports the result without
knowing it happened. It counts from the right, because only the entries the trusted proxies
appended cannot be forged, and it sets the address on every request, resolved or not: fasthttp
serves every request on a keep-alive connection from one context and clears a rewritten address
only when the connection closes, so a middleware that sometimes skipped the rewrite would report
the previous request's client. It parses bare IP addresses only. An entry with a port
(`203.0.113.9:4711`) or a bracketed IPv6 entry (`[2001:db8::1]`) falls back to the connection's
address — safe, since it never reports an address the client chose, but behind a proxy that
always appends a port, `RealIP` always reports the proxy. There is no `ClientIPString`; a caller
who wants one calls `.String()` and pays the allocation where it can be seen.

### The context

```go
func (c *Ctx) Context() context.Context
func (c *Ctx) SetContext(ctx context.Context)
```

`Context` is the context to pass to work the request triggers — a database query, an outbound
HTTP call. It is **the one exception to the borrow contract**: the context belongs to the
`App`, not to the request, and stays valid after the handler returns. Everything else reachable
from a `*Ctx` dies when the handler returns; this does not.

It is cancelled at exactly one moment: when a `Shutdown` gives up waiting and force-closes the
connections, meaning this response is about to be discarded. It is **not** cancelled when the
client disconnects, not when `Shutdown` begins, and not when a clean drain finishes. It carries
no deadline and no values of its own.

The disconnect case is the one to read twice if you are coming from `net/http`, where
`(*http.Request).Context()` does cancel when the client hangs up. fasthttp does not report a
disconnect, and detecting one would cost a channel or a goroutine per request, which principle
1 refuses. A handler that must bound its own work sets a deadline; it will not be told the
caller went away. See
[ADR-0010](adr/0010-request-context-cancels-at-force-close.md).

`SetContext` replaces what `Context` returns for the rest of the request: how a middleware
installs a deadline or a tracing value. **Derive from `c.Context()`, not from
`context.Background()`** — a context derived from `Background` is not connected to the
force-close signal, and silently drops it. A nil context panics, as every other configuration
mistake in rice does.

That deadline is already written: `middleware.Timeout(d)` derives one from `c.Context()`,
installs it, cancels it and restores the previous context when the chain returns, and turns the
error a timed-out chain returns into a 503. Reach for it before writing the same three lines by
hand. It is cooperative — see [ADR-0014](adr/0014-timeout-is-cooperative.md).

### Write side

| Method | Effect | Allocations |
| --- | --- | --- |
| `Status(code int) *Ctx` | sets status, chainable | 0 |
| `SetHeader(k, v string)` | sets a response header | 0 amortised |
| `String(code int, s string) error` | plaintext body | 0 |
| `Bytes(code int, b []byte) error` | raw body | 0 |
| `JSON(code int, v any) error` | JSON body via `encoding/json` | see below |
| `NoContent(code int) error` | status only: no body, no content type | 0 |

`JSON` is the one place core touches `encoding/json`, and it is not free: encoding an
arbitrary value costs allocations proportional to the value's shape, and `v any` boxes the
argument. This is documented rather than hidden, and the number is recorded in the
benchmark suite. A user who needs zero-allocation JSON writes bytes they encoded
themselves and calls `Bytes`.

`NoContent` takes a code so 204, 205 and 304 all work and so it matches the shape of `String`
and `Bytes`. It discards anything already written to the body — a 204 with a body is malformed,
and a handler that wrote before calling it would produce one — and it leaves no
`Content-Type` on the response: a status that carries no body must not describe one. The
getter `ContentType()` cannot show this: fasthttp substitutes a default
(`text/plain; charset=utf-8`) whenever the field is unset, masking a missing clear whether or
not one happened. The test instead asserts against the bytes on the wire, and only after the
handler has written a body first — fasthttp omits the header on the wire for any zero-length
response regardless of the field, so without a prior write the absence would prove nothing.

### Settling an error early

```go
func (c *Ctx) HandleError(err error)
```

| Method | Effect | Allocations |
| --- | --- | --- |
| `HandleError(err error)` | runs the App's `ErrorHandler` on `err` now | 0, plus whatever the `ErrorHandler` costs |

The funnel normally runs after the whole chain has returned, so a middleware that reads the
response status after `next(c)` reads it too early: a request that ends in a 500 still shows 200
there. `HandleError` runs the `ErrorHandler` at the moment of the call and marks the request
settled; when the chain returns, the funnel does not answer it again, even if the same error is
returned. A nil error does nothing, and a second call does nothing — the first settles the request.
A panic after it is still a 500.

It is for middleware that must know the status a request ends with — `middleware.Logger` calls it
on whatever `next` returned, then reads the status. Such a middleware consumes the error, so it
belongs outermost. See [ADR-0013](adr/0013-middleware-can-settle-a-request.md).

### Per-request store

```go
func (c *Ctx) Set(key string, v any)
func (c *Ctx) Get(key string) (any, bool)
```

Backed by a small slice of key/value pairs, not a map, pre-sized to four entries on each
pooled `Ctx`. Middleware typically stores one or two values, and a linear scan over a few
entries beats a map allocation. A fifth key grows the slice — once per pooled `Ctx`, since
the grown slice is kept. That is a documented cliff, not a secret.

`Set` allocates nothing itself, but a non-pointer value is boxed into `any` at the call
site, and that can allocate: `c.Set("id", someString)` costs one allocation, and it is the
caller's. Store a pointer to avoid it.

### Lifetime

`*Ctx` and everything borrowed from it die when the handler returns — everything except what
`Context` returns, which is the `App`'s and outlives the request. To use any other value later:

```go
id := c.ParamString("id")          // copied — safe
go audit(id)                        // fine

id := c.Param("id")                 // borrowed
go audit(id)                        // BUG: reads a reused buffer
```

Under `-tags ricedebug`, a released `Ctx` is poisoned and never reused, and any later method
call panics with a message pointing at this section. That check is compiled out of release
builds entirely, so it costs nothing in production.

The debug build catches misuse of the `*Ctx`. It does **not** catch a retained `[]byte`: in
the example above, `go audit(c.Param("id"))` reads a reused buffer and the debug build stays
silent, because that slice points into fasthttp's memory rather than into anything rice can
poison. See [ADR-0005](adr/0005-context-pooling-and-borrow-contract.md).

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

This one is an illustration of the shape. rice ships a real one, `middleware.RequestID`, which
stores the id under its own unexported key: read it with `middleware.RequestIDFrom(c)`, not
`c.Get("request_id")`, which returns nothing.

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

**Application middleware also runs on a route miss.** A request that matches no route — including
an existing path requested with a method it was not registered for — runs the middleware added
with `app.Use` around a handler that returns `ErrNotFound`, compiled once at build time like any
route's chain. Group and route middleware do not run there: no group matched. `c.Param` returns
empty on a miss, because nothing was captured. This is what lets a logger record 404s. See
[ADR-0012](adr/0012-application-middleware-runs-on-route-misses.md).

**What rice ships.** `middleware.Recover`, `middleware.RealIP`, `middleware.RequestID`,
`middleware.Logger`, `middleware.CORS` and `middleware.Timeout`, in the opt-in `middleware`
package. Their recommended order, and the one trap in it — a panicking request is not logged
unless `Recover` sits inside `Logger` — are in that package's documentation and in the
[README](../README.md#the-middleware-rice-ships). `CORS` must be installed with `app.Use`: a
preflight is a miss, and a group's middleware never sees one.

```go
// package middleware
type CORSConfig struct {
    Origins          []string
    AllowMethods     []string
    AllowHeaders     []string
    ExposeHeaders    []string
    MaxAge           time.Duration
    AllowCredentials bool
}

func CORS(cfg CORSConfig) rice.Middleware
```

`CORSConfig` is everything `CORS` needs. `CORS` copies the origins and compiles the rest into
fixed header values when it is called, and never reads the struct again. `Origins` is the one required field: empty,
`CORS` panics. Each origin is compared exactly, byte for byte, with the `Origin` header a browser
sends — lower case, `scheme://host`, a port only when it is not the scheme's default, no path and
no trailing slash — so `"https://app.example.com/"` would never match, and panics instead, as do
`"*"` and `"null"`. `AllowMethods` empty means `GET, HEAD, POST, PUT, PATCH, DELETE`.
`AllowHeaders` empty means no `Access-Control-Allow-Headers` is sent, so the browser allows only
its safelisted headers, which leave out `Authorization` and `Content-Type: application/json`.
`ExposeHeaders` empty means a script reads only the safelisted response headers. `MaxAge` zero
means no `Access-Control-Max-Age` is sent and the browser caches a preflight for five seconds; a
negative value panics. `AllowCredentials` false means no `Access-Control-Allow-Credentials` is
sent, which a browser requires before it will send cookies. An origin not in the list receives no
CORS headers: its real requests are served all the same, and the browser, not rice, keeps the
script from reading the result. The method and headers a script wants to send are checked the same way,
by the browser, against what is listed; rice never parses them. See
[ADR-0015](adr/0015-cors-states-a-policy.md).

---

## 4. App

```go
type App struct { /* unexported */ }

func New(opts ...Option) *App

// Option
func WithErrorHandler(h ErrorHandler) Option
func WithReadTimeout(d time.Duration) Option
func WithWriteTimeout(d time.Duration) Option
func WithIdleTimeout(d time.Duration) Option
func WithMaxBodySize(n int) Option

func (a *App) Use(mw ...Middleware)
func (a *App) GET(path string, h Handler, mw ...Middleware)
// POST, PUT, PATCH, DELETE, HEAD, OPTIONS, and Handle(method, ...)
func (a *App) Group(prefix string, mw ...Middleware) *Group

func (a *App) Build()

func (a *App) OnStart(fn func() error)
func (a *App) OnShutdown(fn func(context.Context) error)

func (a *App) Run(addr string) error
func (a *App) Serve(ln net.Listener) error
func (a *App) RunContext(ctx context.Context, addr string, grace time.Duration) error
func (a *App) Addr() string
func (a *App) Shutdown(ctx context.Context) error

var ErrShutdownTimeout error
```

`Build` compiles every route's middleware chain once and is called automatically by `Run`,
`RunContext`, `Serve` and `FasthttpHandler`; calling it early is only useful to make a configuration error
surface before the listener opens. Registration after `Build` panics.

The root object. It owns the route trees, the global middleware list, the context pool, the
error handler and the fasthttp server. It has the two phases described in
[02-architecture.md](02-architecture.md): registration, then serving, with `build()`
between them.

`Run` and `Serve` block. An App serves once: `Serve` after `Shutdown` closes its listener and
returns `nil` without serving, and a `Serve` while another is running closes its listener and
returns `ErrAlreadyServing`. A `Serve` that returned without a `Shutdown` — an `OnStart` hook
failed, or serving failed — may be called again; after a serve error the old listener stays
open, and `Addr` keeps reporting it, until the retried `Serve` replaces it or a `Shutdown`
closes it.

`Shutdown` stops accepting connections and waits for in-flight requests until they finish or
its `ctx` ends. It returns `nil` after a clean drain, and an error wrapping both
`ErrShutdownTimeout` and the context's own error when the deadline came first. **Whatever it
returns, nothing is served after it returns.** At the deadline rice closes every connection
still open, because fasthttp's own shutdown, when it times out, lets a busy keep-alive
connection go on serving new requests. The handlers on those connections are not stopped — a
goroutine cannot be — so they run to completion and their responses are lost. That decision,
and its measured cost of about 2.5 ns per request, is
[ADR-0009](adr/0009-shutdown-force-closes-at-deadline.md). The drain polls every 100 ms, so
`Shutdown` returns up to about 100 ms after the last request finishes, and takes about one poll
even when only idle connections are open.

`Shutdown` may be called more than once, and from several goroutines. The calls take turns: one
drains, force-closes if its `ctx` ends, and runs the hooks before the next starts, so a call that
waited its turn returns only after all of that, finds nothing left to drain, and returns `nil`.
A call whose `ctx` ends while it waits does not wait on: it closes every open connection, as at
its own deadline, and returns an error wrapping `ErrShutdownTimeout`. Either way the guarantee
holds for every call — nothing is served after it returns.

Hooks let a program order its own setup and teardown against the server's:

- **`OnStart`** hooks run in registration order inside `Serve`, after the listener is bound and
  before any connection is accepted. The first error stops the sequence, closes the listener,
  and is returned by `Serve`, `Run` or `RunContext`; no `OnShutdown` hook runs on its account.
- **`OnShutdown`** hooks run in reverse registration order, as `defer` does, after the drain and
  after any force-close, each with the `ctx` passed to `Shutdown` — which is already done if the
  drain timed out. Every hook runs even when an earlier one fails, their errors are joined into
  `Shutdown`'s result, and they run on the first `Shutdown` only, including on an App that never
  served. A panicking hook is not recovered. After a timed-out drain, a handler that was cut off
  may still be running when the hooks do, so a hook releasing something handlers use must allow
  for that.
- Registering a nil hook, or any hook after `Build`, panics.
- `Shutdown` does not wait for an `OnStart` hook that is still running: its `OnShutdown` hooks
  run, and it returns, before that hook does.
- A hook must not call `Shutdown` with a `ctx` that never ends. The hook runs while its own
  `Shutdown` holds the turn, so the inner call waits until its `ctx` ends and then returns an
  error wrapping `ErrShutdownTimeout`, after closing every open connection as any waiting
  call whose `ctx` ends does; with `context.Background()` it waits forever.

`RunContext` is the signal helper. It binds `addr`, serves until `ctx` is done, then calls
`Shutdown` with a fresh `grace`-long context — not one derived from `ctx`, which is already
done — and returns once serving has stopped. rice does not import `os/signal`; the program
chooses its signals:

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
err := app.RunContext(ctx, ":8080", 10*time.Second)
```

A `grace` of zero force-closes at once; a negative one panics.

If `Shutdown` is called elsewhere, `RunContext` waits for it to drain and run the `OnShutdown`
hooks, then returns `nil`, so `main` does not exit mid-drain; if its own `ctx` ends during that
wait, it shuts down with `grace` as usual, which cuts the other drain short.

`RunContext` never returns while `OnShutdown` hooks are running, whichever `Shutdown` runs them,
so `main` does not exit mid-teardown. `grace` bounds the drain, not the hooks: `RunContext` can
outlast `grace` by as long as the hooks take. It does not wait past `grace` on another
`Shutdown`'s drain, though — a handler that never returns can hold that drain up forever — so if
that drain is still going, `RunContext` returns, and the other call's hooks, when they start, may
be cut short by the program's exit. `RunContext` can also outlast `grace` when `ctx` ends while an
`OnStart` hook is still running, because `Serve` returns only after that hook does.

The three timeout options set the matching `fasthttp.Server` fields. Zero, the default, means
unlimited, and a zero idle timeout falls back to the read timeout, as in fasthttp. A negative
duration panics. Set all three in production: without a read timeout, a client that sends
half a request holds its connection open for as long as it likes.

`WithMaxBodySize` sets fasthttp's `MaxRequestBodySize`. Without it the limit is fasthttp's
default, 4 MiB; zero or a negative size panics rather than meaning "default". A body over the
limit is answered **413 Request Entity Too Large** by the transport, before routing: no handler,
middleware or `ErrorHandler` runs, because no route or `Ctx` exists yet. rice installs its own
fasthttp error handler for this. fasthttp's default answers an oversized body with 400 "Error
when parsing request", telling the client its request was malformed; rice's keeps fasthttp's
other answers — 431 for an oversized header, 408 for a read timeout, 400 for anything else —
and adds the 413.

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
`app middleware → outer group → inner group → route middleware → handler`. On a route miss only
the app middleware runs, even when the path begins with a group's prefix, because no group matched.

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
route miss, and every recovered panic reaches `app.ErrorHandler` — at the end of the chain, or
earlier when a middleware settles it with `c.HandleError` — whose default behaviour is:

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
