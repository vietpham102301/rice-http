# rice

A small HTTP framework for Go, built on [fasthttp](https://github.com/valyala/fasthttp).

rice is being written as a study in allocation discipline: every milestone ends with a
recorded benchmark, and no performance claim appears in these docs without a number behind
it. The design decisions are written down in [ADRs](docs/adr/) before they are implemented,
and each milestone ends with a [retrospective](docs/milestones/) naming what the
measurements changed.

> **Status: not production ready.** All nine milestones are done (M0–M8), and nothing further
> is scheduled. The `Ctx` is pooled and dispatch allocates nothing, which makes the borrow
> contract real: a `*Ctx` kept past its handler reads another request's data. Build with
> `-tags ricedebug` (or run `make test-debug`) to turn that into a panic. `Shutdown` drains
> in-flight requests up to a deadline and closes whatever is left when it passes, so nothing is
> served after it returns. The comparison against Gin, Echo and Fiber is below, and the
> [project retrospective](docs/07-retrospective.md) says what was learned. See the
> [roadmap](docs/04-roadmap.md).

## Install

```
go get github.com/vietpham102301/rice-http
```

Requires Go 1.25. fasthttp is the only runtime dependency.

## Hello

```go
package main

import (
	"log"

	rice "github.com/vietpham102301/rice-http"
)

func main() {
	app := rice.New()

	app.GET("/hello/:name", func(c *rice.Ctx) error {
		return c.String(200, "hello, "+c.ParamString("name"))
	})

	log.Fatal(app.Run(":8080"))
}
```

A handler returns an error or nil. There is no abort flag and no sentinel status the
framework inspects — returning a non-nil error is the only way to signal failure, which is
[ADR-0002](docs/adr/0002-handler-returns-error.md).

## Middleware and groups

Middleware is a decorator, not an index walk with a cursor: it wraps a handler and returns
one. Stopping the chain is an ordinary early return rather than a rule to remember.

```go
func logging(next rice.Handler) rice.Handler {
	return func(c *rice.Ctx) error {
		log.Printf("%s %s", c.Method(), c.Path())
		return next(c)
	}
}

app := rice.New()
app.Use(logging)

api := app.Group("/api", authenticate)
v1 := api.Group("/v1")

v1.GET("/users/:id", getUser)         // /api/v1/users/:id
v1.POST("/users", createUser, audit)  // audit runs innermost, just before the handler
```

Order is fixed and does not depend on registration order: **app → outer group → inner group
→ route → handler** on the way in, and the reverse on the way out. `app.Use` written after
a route still applies to it, because chains are compiled once at build time rather than
walked per request ([ADR-0003](docs/adr/0003-middleware-as-prebuilt-closure-chain.md)).

Middleware added with `app.Use` also runs on a request that matches no route, around a handler
that returns the 404; group and route middleware do not, since no group matched, and `c.Param`
is empty there ([ADR-0012](docs/adr/0012-application-middleware-runs-on-route-misses.md)).

`Static` serves an `fs.FS` under a prefix — a directory on disk or files compiled into the
binary — with the same error, middleware and lifecycle rules as any other route:

```go
//go:embed web
var web embed.FS

sub, _ := fs.Sub(web, "web")
app.Static("/assets", sub)                // embedded files
app.Static("/uploads", os.DirFS("data"))  // a directory on disk
```

A directory is answered by its `index.html`, and requesting it without a trailing slash 301s to
one that has it; nothing is ever listed. See
[03-core-concepts.md](docs/03-core-concepts.md#serving-static-files) and
[ADR-0017](docs/adr/0017-static-files-wrap-fasthttp-fs.md).

### The middleware rice ships

`logging` above shows the shape. For an access log, a request deadline and calls from a browser
on another origin, use the opt-in `middleware` package, in this order:

```go
app.Use(
	middleware.Logger(slog.Default()), // outermost: times everything, settles errors, logs last
	middleware.Recover(),              // inside Logger: a panic becomes an error Logger can see
	middleware.RealIP(1),              // only behind proxies you run; 1 is how many
	middleware.RequestID(),
	middleware.CORS(cfg),              // before auth, so a 401 carries the CORS headers
	middleware.Timeout(5*time.Second), // inside Logger, so Logger records its 503
)
```

- **`Logger(l *slog.Logger)`** writes one line per request — method, path, status, latency,
  address and request id — with the status the client actually receives. It settles the error
  with `c.HandleError` before reading the status, so a request that ends in a 500 is logged as
  500, from a custom `ErrorHandler` too
  ([ADR-0013](docs/adr/0013-middleware-can-settle-a-request.md)). It consumes the error, which is
  why it goes outermost. It logs the path, never the query string, and 5xx at `ERROR`.
- **`Recover()`** must sit inside `Logger`. **A request that panics is not logged unless it
  does:** the panic unwinds through `Logger` before it can write anything.
- **`RealIP(trustedHops int)`** makes `c.ClientIP` report the client behind that many trusted
  proxies, counting `X-Forwarded-For` from the right, where the entries cannot be forged. Without
  a proxy, do not install it: the rightmost entry is then whatever the client sent. It reads a
  bare address or one with the port some load balancers append — `203.0.113.9:4711`, or
  `[2001:db8::1]:4711` for IPv6 — and drops the port. A malformed entry falls back to the
  connection's address, never to a guess.
- **`RequestID()`** keeps an incoming `X-Request-Id` only if it is 1–64 characters of
  `[A-Za-z0-9_-]`, so a client cannot inject a line into the log; otherwise it generates one. It
  echoes the id on the response. Read it with `middleware.RequestIDFrom(c)`.
- **`Timeout(d time.Duration)`** puts a deadline of `d` on `c.Context()`, the context a handler
  passes to its database and HTTP clients, and answers **503** when the chain returns an error
  after that deadline has passed — unless the error is already an `*rice.HTTPError`, whose status
  is kept. It goes inside `Logger`, so the 503 is what `Logger` records. A route can set its own:

  ```go
  app.GET("/report", report, middleware.Timeout(30*time.Second))
  ```

  Nesting only shortens: a route's `Timeout` inside an application-wide one expires at whichever
  deadline comes first, so the route above still expires at five seconds under the `app.Use`
  above. **`Timeout` is cooperative: a handler that ignores its context is not stopped, and its
  answer, however late, is what the client receives.** Cutting the client off regardless is a job
  for the transport — `fasthttp.TimeoutWithCodeHandler` around `app.FasthttpHandler()`, since
  plain `fasthttp.TimeoutHandler` answers 408 rather than 503 — and it means running a
  `fasthttp.Server` of your own in place of `Run`, `RunContext` and `Shutdown`, on top of the
  other costs [ADR-0014](docs/adr/0014-timeout-is-cooperative.md) names.
- **`CORS(cfg CORSConfig)`** lets a browser application on one of a fixed list of origins call
  the service: it answers the preflight with 204 and puts the CORS headers on every other
  response.

  ```go
  cfg := middleware.CORSConfig{
  	Origins:          []string{"https://app.example.com"},
  	AllowHeaders:     []string{"Authorization", "Content-Type"},
  	ExposeHeaders:    []string{"X-Request-Id"},
  	MaxAge:           10 * time.Minute,
  	AllowCredentials: true,
  }
  ```

  `Origins` is required, and each is compared exactly with the `Origin` a browser sends: lower
  case, no path, no trailing slash — `"https://app.example.com/"` panics at construction, as do
  `"*"` and `"null"`. `AllowMethods` defaults to `GET, HEAD, POST, PUT, PATCH, DELETE`.
  `AllowHeaders` has no default; a JSON API lists `Authorization` and `Content-Type`, because the
  browser's safelist covers neither `Authorization` nor `Content-Type: application/json`.
  `ExposeHeaders` names the response headers a script may read. `MaxAge` is how long a browser may
  cache a preflight, sent in whole seconds and omitted when zero. `AllowCredentials` lets the
  browser send cookies. **`CORS` states the policy and the browser enforces it:** it checks the
  origin and nothing else, and leaves the browser to compare the method and headers it wants to
  send against the ones listed. **It must go in `app.Use`, and before any middleware that answers
  401 or 403.** A preflight matches no route, and only application middleware runs on a miss, so a
  `CORS` on a group never answers one. Its headers are written before the rest of the chain runs,
  and survive a 401 and a 500 — so the browser reports the status, not a CORS failure — unless a
  custom `ErrorHandler` resets the response ([ADR-0015](docs/adr/0015-cors-states-a-policy.md)).

Unlike core, most of these allocate: 3 objects per request for `RealIP`, 2 for `RequestID` alone,
6 for `Logger` and `RequestID` together with slog's JSON handler, 4 for `Timeout`, and 0 for `CORS`
on every branch — each pinned by a budget test, with its fixture named in the
[performance model](docs/05-performance-model.md#opt-in-packages).

## Reading requests, writing JSON

```go
app.POST("/users", func(c *rice.Ctx) error {
	var in struct{ Name string }
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return rice.NewHTTPError(400, "bad JSON")
	}
	page := c.Query("page")           // []byte, empty if absent
	trace := c.Header("X-Request-Id") // case-insensitive
	log.Printf("page=%s trace=%s", page, trace)
	return c.JSON(201, &User{Name: in.Name})
})
```

`Query`, `Header` and `Body` return borrowed bytes: free, and valid only until the handler
returns. Copy what you keep — `string(b)` does — and do not hand them to a goroutine that
outlives the handler. `json.Unmarshal` copies into its target, so the decoded value is yours.
`JSON` is the one allocating helper in core, and there is no binding in core: decoding is a
line you write ([ADR-0006](docs/adr/0006-no-reflection-in-core.md)).

The opt-in `binding/` package writes that line for you. `binding.JSON[T](c)` decodes strictly
— an unknown field is an error, and so is anything after the first value — runs `T`'s
`Validate() error` if it has one, and returns an error a handler returns into the funnel:
400 for a body it could not parse, 422 for a rule it broke. It costs **9 allocations per
call** for the test suite's fixture, pinned by a budget test rather than bounded. Every
request-path row in the table below is a zero; this package, like the middleware above, buys
ergonomics with allocations, and the figure is the price it charges
([ADR-0011](docs/adr/0011-binding-is-generic-and-validation-is-a-method.md)).

```go
type CreateUser struct {
	Name string `json:"name"`
}

func (u CreateUser) Validate() error {
	if u.Name == "" {
		return errors.New("name is required")
	}
	return nil
}

app.POST("/users", func(c *rice.Ctx) error {
	in, err := binding.JSON[CreateUser](c)
	if err != nil {
		return err
	}
	return c.JSON(201, &User{Name: in.Name})
})
```

## Error handling

A handler returns an error, and every error — from routing, from middleware, from the
handler itself, and from a recovered panic — reaches exactly one place: the app's
`ErrorHandler`. `HTTPError` carries a status code the default handler answers with directly;
anything else becomes a generic 500, and the real cause is logged, never sent to the client.

```go
app.GET("/users/:id", func(c *rice.Ctx) error {
	name, err := store.Find(c.ParamString("id"))
	if err != nil {
		return rice.NewHTTPError(404, "user not found")
	}
	return c.String(200, name)
})
```

Replace the default to change how a service reports failure — RFC 7807 problem documents,
for example — without touching any handler:

```go
app := rice.New(rice.WithErrorHandler(func(c *rice.Ctx, err error) {
	log.Printf("request failed: %v", err)
	_ = c.String(500, "something went wrong")
}))
```

fasthttp has no panic hook of its own, so rice recovers a panicking handler in its own
dispatch path rather than leaving one bad handler able to take the whole process down — a
deliberate, measured exception to the "no cost for unused features" rule, recorded in
[ADR-0008](docs/adr/0008-rice-recovers-panics-in-core.md). A panic reaches the same
`ErrorHandler` as everything else, wrapped in a `*PanicError`, and always answers 500.

## Graceful shutdown

`RunContext` serves until a context is done, then shuts down, giving in-flight requests a grace
period to finish. rice does not catch signals itself; the standard library does, and the
program picks which:

```go
func main() {
	app := rice.New(
		rice.WithReadTimeout(5*time.Second),
		rice.WithWriteTimeout(10*time.Second),
		rice.WithIdleTimeout(60*time.Second),
		rice.WithMaxBodySize(1<<20), // 1 MiB; a larger body gets 413 before any handler runs
	)
	app.GET("/hello", func(c *rice.Ctx) error { return c.String(200, "hello") })

	db := openDB()
	app.OnShutdown(func(ctx context.Context) error { return db.Close() })

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.RunContext(ctx, ":8080", 10*time.Second); err != nil {
		log.Fatal(err)
	}
}
```

When the grace period runs out, rice closes every connection still open and `RunContext`
returns an error wrapping `rice.ErrShutdownTimeout`. Either way, nothing is served once it has
returned, and the `OnShutdown` hooks run in reverse registration order, after the drain. After
a clean drain they can release what requests were using. After a timeout, a handler that was
cut off may still be running, because a goroutine cannot be stopped, so a hook must not assume
nothing is using the resource. fasthttp's own shutdown would let a busy keep-alive
connection keep serving after a timeout; tracking open connections so rice can close them
costs about 2.5 ns on every request, and
[ADR-0009](docs/adr/0009-shutdown-force-closes-at-deadline.md) records why it is paid. The
drain polls every 100 ms, so a shutdown takes about that long even with nothing in flight.

Set all three timeouts in production. They default to zero, which means unlimited: without a
read timeout, a client that sends half a request holds its connection for as long as it likes.
The values above are an example, not a recommendation for any particular service.

## Two phases

rice separates **registration** from **serving** with an explicit build step. Registration
validates and inserts routes; `Build` then folds each route's middleware into a single
closure, so a request dispatches through one call rather than a loop over a slice.

`Run`, `RunContext`, `Serve` and `FasthttpHandler` all call `Build` for you. Calling it yourself is only
useful to make a configuration error surface before the listener opens:

```go
app.Build()        // panics here rather than on the first request
log.Fatal(app.Run(":8080"))
```

Registering a route or adding middleware after `Build` panics. So does a duplicate route, a
parameter name declared twice, a wildcard that is not the last segment, a lowercase HTTP
method, a percent-encoded or dot segment, a nil handler or nil middleware, and a group path
that would silently fuse onto its prefix. Every one of them fires at the call that was
wrong, naming what to write instead — not at the first request that happened to hit it.

## Reading borrowed bytes

Accessors that return `[]byte` hand you a view into the request buffer. It is valid for the
duration of the handler and no longer. Accessors that return `string` copy.

```go
id := c.Param("id")             // []byte, borrowed, free
idStr := c.ParamString("id")    // string, copied, one allocation
```

This is the borrow contract, [ADR-0005](docs/adr/0005-context-pooling-and-borrow-contract.md).
It is why route parameter capture allocates nothing: `Lookup` fills caller-supplied storage
rather than returning a new slice.

The `*Ctx` itself is borrowed too, and since it comes from a pool, keeping one past its handler
reads whatever request holds it next. Under `-tags ricedebug` a released `Ctx` is poisoned and
never reused, so any later call on it panics instead. That build catches misuse of the `*Ctx`;
it cannot catch a retained `[]byte`, which points into fasthttp's memory rather than rice's.

## Where it stands, measured

Apple M2 Pro, Go 1.25.6, medians of ten runs, from
[`bench/results/M6-context-pooling.txt`](bench/results/M6-context-pooling.txt). Full data in
[`bench/results/`](bench/results/), one file per milestone.

| | ns/op | allocs/op |
|---|---:|---:|
| fasthttp, no framework | 11.15 | 0 |
| rice dispatch, static route, no middleware | 33.19 | 0 |
| rice dispatch, one middleware | 34.27 | 0 |
| rice dispatch, five middleware | 37.61 | 0 |
| rice dispatch, five via nested groups | 37.09 | 0 |
| rice dispatch, parameterised route read with `Param` | 38.03 | 0 |
| tree lookup, one parameter | 42.19 | 0 |
| tree lookup, 1000 static routes | 47.69 | 0 |
| build, 1000 routes | 228,496 | 7,503 |

Every request-path row is a zero now. Through M5 each was a 1: the `Ctx`, 352 bytes, allocated
fresh per request. M6 pools it, and what that is worth is measured with both arms in one
session rather than across files — `BenchmarkDispatchPooledVsUnpooled` reads **35.94 ns and
0 allocations pooled against 82.11 ns, 240 bytes and 3 allocations unpooled**. Three, because
the unpooled arm also pays for the parameter and store slices a pooled `Ctx` keeps.

**Five middleware still cost the same allocations as none** — zero — because the chain is compiled
at build time, so the closures are already folded when the request arrives. They are not free in
time: 33.19 ns with none against 37.61 ns with five in the same session, about 0.9 ns per
middleware.

The `ns/op` column is not comparable with earlier milestones' files: the host OS moved from
Darwin 25.6.0 to Darwin 27.0.0 between the M5 and M6 recordings, and even benchmarks with no
rice code on their path moved a few percent. The `allocs/op` column is exact and carries no such
caveat.

### Against Gin, Echo and Fiber

Same routes and payloads, one machine, one recording (M8, on battery power), Gin v1.12.0, Echo
v5.3.1, Fiber v3.5.0, fasthttp v1.73.0. Handler level — each framework's own code on a request
already parsed, median of ten runs — from
[`bench/results/M8-compare-handler.txt`](bench/results/M8-compare-handler.txt); end to end — a
load generator in a separate process, 64 connections, median of five rounds — from
[`bench/results/M8-compare-e2e.txt`](bench/results/M8-compare-e2e.txt). The columns are grouped
by transport: rice and Fiber on fasthttp, Gin and Echo on `net/http`.

| | rice | Fiber | Gin | Echo |
|---|---:|---:|---:|---:|
| `static`, handler: ns/op | 53.66 | 65.20 | 84.83 | 107.5 |
| `static`, handler: allocs/op | 0 | 0 | 1 | 1 |
| `githubapi` (203 routes), handler: ns/op | 118.8 | 680.6 | 133.9 | 177.5 |
| `githubapi` (203 routes), handler: allocs/op | 0 | 0 | 1 | 1 |
| `static`, end to end: req/s | 166,225 | 165,912 | 158,881 | 158,679 |

At the handler level rice is the fastest of the four here. End to end that ordering does not
survive: no order between rice and Fiber can be claimed from five rounds (in `json` Fiber was
ahead in all five, by 0.2–6.0% — too few to call it an order either way), and both lead Gin and Echo because fasthttp leads
`net/http`, not because of anything rice does. Fiber's `githubapi` figure is specific to that
route's place in Fiber's route buckets. The tables for all seven scenarios, what each level can
and cannot compare, and the reason for each result are in the performance model's
[Where rice stands](docs/05-performance-model.md#where-rice-stands).

## Documentation

| | |
|---|---|
| [00 — Overview](docs/00-overview.md) | what rice is and is not |
| [01 — Design principles](docs/01-design-principles.md) | the rules the code is held to |
| [02 — Architecture](docs/02-architecture.md) | layers, package layout, the two phases |
| [03 — Core concepts](docs/03-core-concepts.md) | the public API, type by type |
| [04 — Roadmap](docs/04-roadmap.md) | nine milestones, and what each one answers |
| [05 — Performance model](docs/05-performance-model.md) | the allocation budget, per method |
| [06 — Glossary](docs/06-glossary.md) | terms used precisely in these docs |
| [07 — Retrospective](docs/07-retrospective.md) | what the project learned, and what surprised it |
| [ADRs](docs/adr/) | fourteen decisions, with the alternatives that lost |
| [Milestones](docs/milestones/) | retrospectives: what was measured, what surprised |
| [Journal](docs/progress.md) | the running record, including the wrong turns |

## Development

```
make test        # go test ./... -race
make test-debug  # the same suite under -tags ricedebug
make cover       # coverage, currently 99.2%
make lint        # gofmt and go vet
make bench       # runs the suite and records to bench/results/
make compare     # the comparison against Gin, Echo and Fiber: equivalence gate and smoke run
```

`make test-debug` is not a duplicate of `make test`: the `ricedebug` build never returns a
released `Ctx` to the pool, so it is the only run in which a use-after-release panics rather
than quietly succeeding. CI runs both.

Benchmarks are recorded to a committed file rather than read off a terminal, so a claim in
these docs can always be traced to the run that produced it.

## License

MIT — see [LICENSE](LICENSE). fasthttp, the only runtime dependency, is MIT too.
