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
| [ADRs](docs/adr/) | nine decisions, with the alternatives that lost |
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
