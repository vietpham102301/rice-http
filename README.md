# rice

A small HTTP framework for Go, built on [fasthttp](https://github.com/valyala/fasthttp).

rice is being written as a study in allocation discipline: every milestone ends with a
recorded benchmark, and no performance claim appears in these docs without a number behind
it. The design decisions are written down in [ADRs](docs/adr/) before they are implemented,
and each milestone ends with a [retrospective](docs/milestones/) naming what the
measurements changed.

> **Status: not production ready.** Five of nine milestones are done (M0–M5). There is no
> context pooling, and `Shutdown` is blunt — it races fasthttp's own shutdown against your
> context rather than draining in-flight requests against a deadline. The API will change.
> See the [roadmap](docs/04-roadmap.md).

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

## Two phases

rice separates **registration** from **serving** with an explicit build step. Registration
validates and inserts routes; `Build` then folds each route's middleware into a single
closure, so a request dispatches through one call rather than a loop over a slice.

`Run`, `Serve` and `FasthttpHandler` all call `Build` for you. Calling it yourself is only
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

## Where it stands, measured

Apple M2 Pro, Go 1.25.6, medians of ten runs. Full data in
[`bench/results/`](bench/results/), one file per milestone.

| | ns/op | allocs/op |
|---|---:|---:|
| fasthttp, no framework | 11.39 | 0 |
| rice dispatch, static route, no middleware | 84.01 | 1 |
| rice dispatch, one middleware | 91.71 | 1 |
| rice dispatch, five middleware | 92.68 | 1 |
| rice dispatch, five via nested groups | 93.86 | 1 |
| tree lookup, one parameter | 90.38 | 1 |
| tree lookup, 1000 static routes | 114.30 | 1 |
| build, 1000 routes | 238,840 | 7,503 |

Two things worth reading off that table. **Five middleware cost the same single allocation
as none** — the chain is compiled at build time, so the closures are already folded when the
request arrives; the time cost is roughly 1.1 ns per middleware. And **that one allocation
is the `Ctx`**, 352 bytes, allocated fresh per request. It is deliberate and it is the whole
remaining gap: M6 pools it. Everything above 11 ns in the first column is dominated by it,
which M3 established by bisection rather than by assumption.

Numbers rice does not yet have: comparisons against Gin, Echo or Fiber. Those are M8, and
publishing them earlier would mean publishing them from an unfinished framework.

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
| [ADRs](docs/adr/) | eight decisions, with the alternatives that lost |
| [Milestones](docs/milestones/) | retrospectives: what was measured, what surprised |
| [Journal](docs/progress.md) | the running record, including the wrong turns |

## Development

```
make test     # go test ./... -race
make cover    # coverage, currently 98.9%
make lint     # go vet
make bench    # runs the suite and records to bench/results/
```

Benchmarks are recorded to a committed file rather than read off a terminal, so a claim in
these docs can always be traced to the run that produced it.

## License

MIT — see [LICENSE](LICENSE). fasthttp, the only runtime dependency, is MIT too.
