# 02 — Architecture

## Layers

Four layers, each depending only on the one below it. The dependency rule is enforced by
import restrictions: nothing under `internal/` may import package `rice`.

```
┌─────────────────────────────────────────────────────────┐
│  User code: handlers, middleware, route registrations   │
├─────────────────────────────────────────────────────────┤
│  rice — public API                                      │
│  App · Ctx · Handler · Middleware · Group · HTTPError    │
├─────────────────────────────────────────────────────────┤
│  internal/ — mechanism                                   │
│  router (radix tree) · chain (compiler) · bytesconv      │
├─────────────────────────────────────────────────────────┤
│  fasthttp — transport, parsing, connection lifecycle     │
└─────────────────────────────────────────────────────────┘
```

The public layer is deliberately thin: it is mostly a facade that names things well and
owns lifetimes. The interesting algorithms are one layer down, unexported, and free to
change.

The diagram is the finished shape, not today's. As of M7, `internal/bytesconv` is named there
but not built, and no milestone owns it; the layout below marks precisely what exists.

## Package layout

Package `rice` is the repository root. That is not an accident of tidying: in Go a library's
root directory *is* its import path, so every file below sits in the one package users
import. Splitting them into subdirectories would create separate packages and force callers
into several imports, which is the opposite of the thin single facade the layer diagram
describes.

**What exists today** (through M7):

```
rice-http/
├── app.go              App, New (builds the fasthttp.Server), Option, the timeouts and body limit, the transport error handler, dispatch, the funnel
├── handler.go          Handler
├── middleware.go       Middleware
├── ctx.go              Ctx: the per-request handle, reset, Method/Path, Query/Header/Body
├── ctx_param.go        Ctx read side: route parameters, borrowed and copied
├── ctx_response.go     Ctx write side: Status, headers, String/Bytes/JSON
├── ctx_store.go        Ctx per-request store: Set, Get, and its pre-sized slice
├── pool.go             sync.Pool wiring: newCtx, acquire, release
├── poison_debug.go     ricedebug: mark a released Ctx, panic on any later use
├── poison_release.go   !ricedebug: the same API, empty, compiled away
├── route.go            registration for all eight verbs, App.Use, tree selection, lookup
├── group.go            Group: prefix and middleware scoping, and the registration guards
├── build.go            Build: chain compilation and tree rebuild, once
├── method.go           the method enum and the fixed-array index
├── errors.go           HTTPError, PanicError, ErrNotFound, ErrorHandler, DefaultErrorHandler
├── server.go           Run, Serve, RunContext, Addr, Shutdown, ErrShutdownTimeout
├── lifecycle.go        OnStart and OnShutdown: registration, and running them in order
├── conns.go            connection tracking through ConnState, and the force-close at the deadline
├── doc.go              package documentation
├── internal/
│   ├── router/         radix tree: insert, lookup, param capture, priority
│   └── chain/          middleware chain compilation
├── middleware/         optional, opt-in: recover
├── docs/               these documents, the ADRs, milestone retrospectives, the journal
├── bench/              benchmark suite and recorded results, one file per milestone
├── scripts/            bench.sh, the recording harness
└── .github/workflows/  CI
```

logger, requestid and timeout are still unbuilt; `middleware/` currently holds only
`Recover`.

**What later milestones add.** These are named here so the layout is predictable, not
because they exist:

```
└── internal/bytesconv/ the only place unsafe string/[]byte views are allowed (unbuilt)
```

`middleware/` is a separate package on purpose. Importing rice must not drag in anything
a user did not ask for, and the import graph is the honest signal of what costs what.

## The two phases

rice has a **registration phase** and a **serving phase**, separated by an explicit build
step. Almost everything expensive happens in the first phase.

```mermaid
flowchart TD
    A["rice.New()<br/>(creates the Ctx pool)"] --> B["app.Use(mw...)<br/>app.GET/POST(...)<br/>app.Group(...)<br/>→ tracks the max param count"]
    B --> C["app.build()<br/>(called once by Run or Test)"]
    C --> C1["compile middleware chains<br/>into one closure per route"]
    C --> C2["insert compiled handlers<br/>into per-method radix trees"]
    C1 & C2 --> D["serving phase:<br/>tree lookup + one closure call"]
```

**Why a build step exists at all.** If chains were compiled at registration time, a
middleware added with `app.Use` *after* a route was registered would silently not apply to
it — a real and confusing bug that Gin and Fiber both have. Deferring compilation to a
single `build()` means registration order does not matter, and it also gives one clear
place to reject an invalid configuration before the socket is ever opened. `build()` runs
under `sync.Once`; registering a route after build panics with a clear message rather than
racing. See [ADR-0003](adr/0003-middleware-as-prebuilt-closure-chain.md).

Two things deliberately sit outside the build step. The `Ctx` pool is created in `New`, because
the dispatch path is reachable before `Build` — the package's own tests call `handle` on unbuilt
Apps throughout, and a pool created in `build` would be nil there. And the maximum parameter
count the pool sizes contexts from is tracked at each registration rather than computed at
build, so a `Ctx` built at any point is sized from everything registered so far; one built
before a larger route arrives grows once on its first oversized capture and keeps the larger
slice.

## Request lifecycle

```mermaid
sequenceDiagram
    participant N as net.Conn
    participant F as fasthttp.Server
    participant A as app.handle
    participant P as sync.Pool
    participant R as internal/router
    participant H as compiled chain

    N->>F: bytes
    F->>F: parse request into reused RequestCtx
    F->>A: handle(fctx)
    A->>P: acquire *rice.Ctx
    P-->>A: recycled Ctx (zero alloc after warmup)
    A->>A: c.reset(fctx)
    A->>R: Lookup(method, path)
    R-->>A: chain, params (byte views into URI buffer)
    alt no match
        A->>A: notFound chain
    else match
        A->>H: chain(c)
        H-->>A: error or nil
    end
    opt error != nil
        A->>A: app.ErrorHandler(c, err)
    end
    A->>P: release Ctx (reset refs, poison under ricedebug)
    A-->>F: return
    F->>N: write reused Response
```

Three things to notice, because they are the whole design:

1. **Nothing in this path allocates on a warm server.** The `Ctx` comes from a pool, the
   parameters are slices into a buffer fasthttp already owns, and the chain is a single
   closure built long ago. The only allocations are ones the user's handler makes.
2. **There is exactly one error funnel.** Every failure — routing, middleware, handler —
   converges on `app.ErrorHandler`. That is the one place to change how the service reports
   problems.
3. **Release is unconditional.** It happens whether the handler returned, errored, or
   panicked, because that is what makes the pool safe.

## Router shape

One radix tree per HTTP method, held in a fixed-size array indexed by a method constant,
with a map fallback for uncommon verbs. Method dispatch is therefore an array index on the
hot path, not a map lookup or a string comparison.

```
trees[GET]  ── / ─┬─ "users/" ─┬─ ":id" ────── handler
                  │            └─ "search" ─── handler
                  └─ "health" ───────────────── handler
```

Match priority within a node is fixed and total, so there is never an ambiguity to resolve
at request time: **static > parameter (`:name`) > wildcard (`*path`)**. Conflicting routes
are rejected at registration with an error naming both patterns.
See [ADR-0004](adr/0004-radix-tree-router.md).

## Where the bytes live

This is the diagram worth internalising, because the borrow contract falls out of it.

```
fasthttp read buffer  [ GET /users/42 HTTP/1.1\r\nHost: ...\r\n\r\n ]
                              ▲     ▲
                              │     └── params[0].value  = []byte view, len 2
                              └──────── c.Path()         = []byte view
```

`c.Param("id")` hands back a `[]byte` that points into a buffer fasthttp will reuse for
the next request on that connection. It is correct and free during the handler, and it is
a use-after-free the instant the handler returns. `c.ParamString("id")` copies, costs one
allocation, and is safe forever. Both exist, both are documented with their cost, and the
naming makes the expensive one the longer one to type.
