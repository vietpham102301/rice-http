# OpenTelemetry tracing and metrics — Design

**Status:** approved, not yet implemented; D4 aligned with the HTTP semantic conventions in review
**Date:** 2026-09-26
**Milestone:** none. Work after the roadmap; the first of the observability middleware discussed after
it emptied
**Decision record:** ADR-0020 (new), "OpenTelemetry lives in its own module, on the API only, keyed by
the matched route"
**Depends on:** the middleware chain ([ADR-0003](../../adr/0003-middleware-as-prebuilt-closure-chain.md)),
the request context ([ADR-0010](../../adr/0010-request-context-cancels-at-force-close.md)),
`HandleError` ([ADR-0013](../../adr/0013-middleware-can-settle-a-request.md)), streams
([ADR-0019](../../adr/0019-streams-run-after-the-handler.md))

## Goal

Give a rice service OpenTelemetry traces and HTTP server metrics with one middleware, following the
OpenTelemetry HTTP semantic conventions, without adding a dependency to rice's core module.

## Question this design answers

Where does an OpenTelemetry dependency live so that users who do not want it pay nothing, and what
does rice's core need to expose so that spans and metrics are keyed by route rather than by raw path?

## What the design is for

The owner's services will use OpenTelemetry, which covers both traces and metrics and exports to any
backend. The owner chose a separate Go module for the integration. Auth, rate limiting and health
checks are later, separate designs.

## What exists

Read against rice at `9d420a3`.

- `go.mod` requires only fasthttp. `middleware/` holds Logger (on `log/slog`), RequestID, RealIP, CORS,
  Timeout and Recover.
- There is no way to learn which route matched. `App` keeps one `router.Tree[Handler]` per method; the
  router is generic in its value type (`internal/router/router.go`, `Tree[H any]`), and `lookup`
  returns only the handler.
- `c.SetContext` lets a middleware install a derived context (ADR-0010); `Timeout` does so and restores
  the previous one when the chain returns.
- `Logger` settles an error with `c.HandleError` and returns nil, so it records the status the client
  receives.

## What probing found

A throwaway module requiring rice (via `replace`) and OpenTelemetry v1.43.0 (`otel`, `trace`, `metric`,
`sdk`, `sdk/metric`), with a prototype middleware:

1. **Propagation works through a carrier over fasthttp's request header.** A request carrying
   `traceparent: 00-4bf92f…4736-00f067aa0ba902b7-01` produced a server span in trace `4bf92f…4736`
   whose parent was `00f067aa0ba902b7`; the handler saw the same trace in `c.Context()`.
2. **A 503 through the funnel set the span's status to Error**, and
   `http.server.request.duration` was recorded.
3. **The cost is the OpenTelemetry API's, mostly.** Per request, with no-op providers: 11 allocations
   with a `traceparent`, 9 without; with the SDK and an in-memory recorder: 12. The prototype built the
   span name per request and passed attributes through variadic slices; the implementation measures
   its own figure.
4. **The route pattern is what makes the output usable.** Without it the span name and the metric's
   attributes would have to carry the raw path, which is unbounded — one metric series per user id.

## Non-goals

- Exporters, the SDK, resource detection, sampling configuration: the application configures them.
  `otelrice` imports only the OpenTelemetry API and semconv.
- Client-side instrumentation, database instrumentation.
- `http.server.active_requests`, request and response body size metrics.
- Recording the query string, request or response headers, or bodies.
- Spans that cover a stream's body: the span ends when the handler returns (ADR-0019, D8).
- Auth, rate limiting, health checks.

## Decisions

### D1 — `c.Route()` in core

```go
// Route returns the pattern of the route that matched, as registered and joined with its group's
// prefix — "/users/:id" — or "" when no route matched.
func (c *Ctx) Route() string
```

Each method's tree stores a `routeEntry{h Handler; pattern string}` instead of a `Handler`; `lookup`
returns the entry, and `handle` sets `c.route` from it. `Ctx` gains one field, `route string`, which
`reset` clears. A miss leaves it "". The pattern string is built once at registration and lives as long
as the App, so `Route` returns a value the caller may keep: it is outside the borrow contract, and the
doc comment says so. It calls `c.poison.check()` first. The dispatch budgets stay at their figures; a new
budget pins `Route` at 0. A Static route reports its pattern, `"/assets/*filepath"`.

### D2 — a separate module, `github.com/vietpham102301/rice-http/otelrice`

```
otelrice/
  go.mod        module github.com/vietpham102301/rice-http/otelrice
                require github.com/vietpham102301/rice-http, go.opentelemetry.io/otel (v1.43.0),
                        go.opentelemetry.io/otel/trace, go.opentelemetry.io/otel/metric
                replace github.com/vietpham102301/rice-http => ../
  otelrice.go   Middleware, Option and the three With… options
  carrier.go    the propagation carrier over fasthttp's request header
```

The SDK (`sdk`, `sdk/metric`) appears only in `otelrice`'s tests. Core's `go.mod` does not change. The
Makefile gains a target that runs `go test`, `go vet` and gofmt in `otelrice/`, and `make test` runs it
after the core suite. Releasing needs a core tag carrying `Route` before `otelrice/v0.1.0` can require
it; the `replace` serves development in this repository only (consumers ignore it).

### D3 — one middleware, three options

```go
func Middleware(opts ...Option) rice.Middleware

func WithTracerProvider(tp trace.TracerProvider) Option
func WithMeterProvider(mp metric.MeterProvider) Option
func WithPropagators(p propagation.TextMapPropagator) Option
```

The defaults are OpenTelemetry's globals — `otel.GetTracerProvider()`, `otel.GetMeterProvider()`,
`otel.GetTextMapPropagator()` — read when `Middleware` is called. A nil provider or propagator passed
to an option panics with a `rice: otelrice:` message. The tracer and meter are named
`github.com/vietpham102301/rice-http/otelrice`. The histogram is created once, in `Middleware`.

### D4 — what one request does

1. Extract the remote context from the request headers through the propagator and a carrier over
   `fasthttp.RequestHeader` (`Get` peeks and copies; `Set` sets; `Keys` lists the header names).
2. Start a span of kind Server, named `"{method} {route}"`, or just `"{method}"` when `c.Route()` is ""
   so a raw path never becomes a span name. A method outside GET, HEAD, POST, PUT, PATCH, DELETE,
   OPTIONS, CONNECT, TRACE and QUERY is recorded as `_OTHER` in `http.request.method` and named `HTTP`
   in the span in place of the method; the span (never the metric) also carries the real verb in
   `http.request.method_original`. The known list is fixed — no option extends it, per the
   conventions' guidance that such instrumentation say so.
3. Install the span's context with `c.SetContext`, derived from `c.Context()` (ADR-0010), so the
   handler's own instrumented calls become children.
4. Run the chain. An error is settled with `c.HandleError` and the middleware returns nil, as Logger
   does, so the status recorded is the one the client receives, including from a custom ErrorHandler.
5. Restore the previous context.
6. Set attributes (semconv v1.40.0 keys). At span start, before the chain runs — sampler-relevant and
   already known: `http.request.method`, `http.request.method_original` (for an unknown method),
   `http.route` (when `c.Route()` is not "", known from dispatch), `url.path`, `url.scheme`, and
   `user_agent.original` (when present). After the chain — known only once it has run:
   `http.response.status_code`, `client.address` (from `c.ClientIP()`, read after the chain so RealIP
   inside counts). No attribute is set twice. Never the query string.
7. Span status Error for a status of 500 or above; 4xx is a client error and leaves the status unset,
   as the conventions require for server spans. A status of 500 or above also sets `error.type` to the
   status code as a string, on the span and on the metric point; when the chain returned an error,
   `span.RecordError(err)` records it too.
8. Record `http.server.request.duration` (unit `s`, the conventions' explicit bucket boundaries as an
   advisory) with `http.request.method`, `http.route` (when not ""), `http.response.status_code`,
   `url.scheme`, and `error.type` (for a status of 500 or above). The raw path is never a metric
   attribute.
9. End the span. It ends in a deferred call, so a panic that reaches this middleware (no Recover
   inside it) still finishes it exactly as a normal response does — through the same helper: the
   previous context is restored, the same D4 attributes are set with status 500 (rice's own recovery
   answers every panic with one) and `error.type` "500", the duration is recorded, and the panic is
   recorded with `span.RecordError` (with a stack trace) rather than a custom event — then the panic
   continues unwinding.

### D5 — placement

Outermost, so its span covers every other middleware and Logger's `c.Context()` carries the span:

```go
app.Use(
	otelrice.Middleware(),
	middleware.Logger(l),
	middleware.Recover(),
	middleware.RealIP(1),
	middleware.RequestID(),
)
```

It reads `ClientIP` after the chain returns, so RealIP inside it still applies. On a route miss the
application middleware runs (ADR-0012), so misses are traced and counted too, named by method alone.

### D6 — streams

For `c.Stream`/`c.SSE`, the span and the duration end when the handler returns, not when the stream
does, the same limit Logger has (ADR-0019 D8). The stream's context is not `c.Context()`, so spans a
stream callback starts are not children of the request span unless the handler passes the span
context along explicitly; the doc comment shows how (`trace.ContextWithSpanContext`).

## Consequences to record

- ADR-0020: the separate module, API-only dependencies, `c.Route()`, the placement, the stream limit.
- `05-performance-model.md`: `c.Route` at 0; otelrice outside the zero-allocation claim with its
  measured budgets.

## Components

| File | Contents |
|---|---|
| `route.go`, `app.go`, `build.go` | trees of `routeEntry`; `lookup` returns the entry; `handle` sets `c.route` |
| `ctx.go` | the `route` field, `reset`, `Route()` |
| `route_test.go` (or a new `ctx_route_test.go`) | Route for a parameterised route, a group, a miss, a Static route |
| `alloc_test.go` | `TestAllocBudgetCtxRoute` |
| `otelrice/*` | the module |
| `Makefile` | the otelrice target, run by `make test` and `make lint` |

## Allocation budget

- `c.Route()`: 0, and every existing dispatch budget unchanged.
- otelrice, per request, measured and pinned in `otelrice`'s tests: with no-op providers and no
  `traceparent`; with no-op providers and a `traceparent`; with the SDK and an in-memory span recorder
  and manual metric reader. Each is a regression guard, not a target.

## Testing

Core: `Route` returns the pattern for a static route, a parameterised route, a wildcard, a route
registered on a nested group, a Static route; "" on a miss and after `reset`; the pooled Ctx does not
leak one request's route into the next.

otelrice, with `tracetest.SpanRecorder` and `sdkmetric.NewManualReader`:

- **Spans:** name `GET /users/:id` for a parameterised route; `GET` on a miss; `HTTP` (or `HTTP {route}`)
  for an unknown method, whose real verb lands in `http.request.method_original` and whose
  `http.request.method` reads `_OTHER`; kind Server; every D4 attribute with the right value; no
  attribute contains the query string.
- **Propagation:** a request with `traceparent` gives a span in that trace with that parent; without
  one, a new root span.
- **Status:** 500 and 503 → Error; 404 and 418 → unset; a custom ErrorHandler answering 502 → Error;
  the middleware returns nil and the client gets the settled status.
- **Context:** the handler sees the span in `c.Context()`; after the chain, the previous context is back.
- **Metrics:** one histogram data point per request with the D4 attributes; two requests to
  `/users/1` and `/users/2` share one series.
- **Panic:** with no Recover inside, the span ends with status Error and the panic keeps unwinding.
- **Options:** custom providers are used; nil panics.
- **Budgets:** as above.

## Documentation

ADR-0020; `03-core-concepts.md` (`Route` in the read side); `05-performance-model.md`; README (an
OpenTelemetry section: installing an SDK and exporter, then the middleware); `04-roadmap.md` (*Done after
M8*); `docs/progress.md`.

## Exit criteria

`make test`, `make test-debug` and `make lint` pass, including the otelrice module; `Route` holds 0 and
no existing budget moves; every D4 step has a test; core's `go.mod` is unchanged.

## Open questions

None.
