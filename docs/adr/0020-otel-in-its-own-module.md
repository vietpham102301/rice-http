# ADR-0020 — OpenTelemetry lives in its own module, on the API only, keyed by the matched route

Status: Accepted
Date: 2026-09-26

## Context

Core's `go.mod` requires only fasthttp. The owner's services use OpenTelemetry for both traces and
HTTP server metrics, exported to whichever backend the deployment chooses. Before this work there was
no way for anything outside routing to learn which route matched a request — `App` keeps one
`router.Tree[Handler]` per method, and `lookup` returned only the handler — so an integration keyed by
path would have had to use the raw request path, which is unbounded: `/users/1`, `/users/2`, … become
one metric series per user id, and a Prometheus-shaped backend explodes.

## Decision

**`c.Route()` in core.** Each method's tree now stores a `routeEntry{h Handler; pattern string}`
instead of a bare `Handler`; `lookup`/`lookupEntry` return the entry, and `handle` sets `c.route` from
it. `Ctx` gained one field, `route string`, cleared by `reset`. `Route()` calls `c.poison.check()` first
and returns the pattern as registered and joined with its group's prefix — `"/users/:id"` — or `""` on
a miss; a Static route reports its pattern too, `"/assets/*filepath"`. The pattern string is built once
at registration and lives as long as the `App`, so it is outside the borrow contract — the one other
row besides `Context()` — and the doc comment says so. It is 0 allocations, pinned by
`TestAllocBudgetCtxRoute`, and every existing dispatch budget was re-measured and did not move.

**A separate module, `github.com/vietpham102301/rice-http/otelrice`.** Its non-test code imports only
`go.opentelemetry.io/otel`, `/trace` and `/metric` — the OpenTelemetry API — and semconv; its `go.mod`
also requires rice and fasthttp directly (`carrier.go` reads the request header through it), and, for
its own tests, the SDK (`sdk`, `sdk/metric`). Core's `go.mod` and `go.sum` do not change. Versions are
OpenTelemetry Go v1.46.0 and semconv `go.opentelemetry.io/otel/semconv/v1.40.0`.

**One middleware, three options, defaulting to the globals.** `Middleware(opts ...Option)
rice.Middleware`, with `WithTracerProvider`, `WithMeterProvider` and `WithPropagators`, each defaulting
to `otel.GetTracerProvider()`, `otel.GetMeterProvider()` and `otel.GetTextMapPropagator()`, read when
`Middleware` is called. A nil provider or propagator panics with a `rice: otelrice:` message. The
tracer and meter are named `github.com/vietpham102301/rice-http/otelrice`, and the duration histogram
is created once, in `Middleware`.

**What one request does**, in one paragraph (spec D4): the middleware extracts the caller's trace
context from the request headers through the configured propagator and a carrier over
`fasthttp.RequestHeader`; starts a span of kind Server named `"{method} {route}"`, or `"{method}"`
alone when `c.Route()` is `""`, so a raw path never becomes a span name — a method outside the fixed
list GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS, CONNECT, TRACE and QUERY is recorded as `_OTHER` in
`http.request.method`, named `"HTTP"` in the span in place of the method, and carries the real verb in
`http.request.method_original` on the span only, never the metric; installs the span's context with
`c.SetContext`, derived from `c.Context()`, so the handler's own instrumented calls become children;
runs the chain, settling a returned error with `c.HandleError` as `middleware.Logger` does and
returning nil, so the status recorded is the one the client receives; restores the previous context;
sets the sampler-relevant attributes at span start — `http.request.method`,
`http.request.method_original` when unknown, `http.route` when known, `url.path`, `url.scheme` (from
the connection, `fctx.IsTLS()`, so a service behind a TLS-terminating proxy still reports `"http"`),
and `user_agent.original` when present — and the attributes known only after the chain has run,
`http.response.status_code` and `client.address` (from `c.ClientIP()`, read after the chain so
`middleware.RealIP` inside it still counts), never the query string in either group; marks the span
Error and sets `error.type` to the status code as a string, on the span and on the metric point, for a
status of 500 or above, and calls `span.RecordError(err)` when the chain returned that error to this
middleware directly and the status is 500 or above; records `http.server.request.duration` with
`http.request.method`, `http.route` (when known), `http.response.status_code`, `url.scheme` and
`error.type` (for 500 and above); and ends the span in a deferred call, so a panic reaching this frame —
no `Recover` inside it — still restores the previous context, sets the same attributes with status 500
and `error.type` `"500"`, records the panic with `span.RecordError` and a stack trace rather than a
custom event, records the duration, and ends the span exactly as a normal response does, before the
panic continues unwinding.

**`RecordError` needs help under the recommended placement.** With `middleware.Logger` installed inside
otelrice, as D4 and the placement below both do, `Logger` settles the chain's error with its own
`c.HandleError` and returns `nil`; otelrice's `next(c)` call above therefore returns `nil` too, so the
`span.RecordError(err)` in the previous paragraph never fires — the span still gets `error.type` for a
500, but no exception event, unless the application records the error itself. The recipe is a custom
`ErrorHandler`:

	rice.WithErrorHandler(func(c *rice.Ctx, err error) {
		trace.SpanFromContext(c.Context()).RecordError(err)
		rice.DefaultErrorHandler(c, err)
	})

`c.Context()` there is still the span's context: `HandleError` calls the `ErrorHandler` mid-chain, and
otelrice restores the previous context only after the whole chain — including `Logger`'s own
`HandleError` call — has returned.

**Placement: outermost.** `app.Use(otelrice.Middleware(), middleware.Logger(l), middleware.Recover(),
middleware.RealIP(1), middleware.RequestID())`, so its span covers every other middleware and
`Logger`'s `c.Context()` carries the span. Application middleware runs on a route miss (ADR-0012), so
misses are traced and counted too, named by method alone.

**The stream limit.** For `c.Stream`/`c.SSE` the span and the duration end when the handler returns,
not when the stream does — the same limit `middleware.Logger` has (ADR-0019 D8). The stream's context
is not `c.Context()`, so a callback must carry the span context along explicitly if it wants the
request's trace: `trace.ContextWithSpanContext(s.Context(), sc)`, `sc` captured in the handler before
the stream starts.

## Alternatives

**Put it in `middleware/`.** Rejected: every rice user's module graph would gain OpenTelemetry whether
or not they use it, which is exactly what a separate `go.mod` exists to prevent.

**A neutral instrumentation hook in core, with no OpenTelemetry-specific shape.** Rejected: users
would each rewrite propagation, span naming and the HTTP semantic conventions' attribute set by hand,
and get it wrong in different ways. A hook that is neutral enough to avoid the dependency is not
neutral enough to be useful.

**Raw paths as span names and metric attributes.** Rejected: unbounded cardinality — one series per
path instance rather than per route — is the failure this design exists to avoid; see Context.

## Consequences

- A multi-module repository, with separate release tags: `otelrice` can be tagged `otelrice/vX.Y.Z`
  only after a core tag that carries `Route`, since `otelrice/go.mod` will then require that core
  version instead of the development `replace`.
- **Releasing otelrice**, in order: tag core with a version that carries `Route`; update
  `otelrice/go.mod` to `require` that version — the `replace` line stays, for development inside this
  repository, but a consumer of the published module ignores it and resolves the required version
  instead; then tag `otelrice/vX.Y.Z`.
- Measured, per request (darwin/arm64, Apple M2 Pro, go1.25.6): with no-op providers and no
  `traceparent`, 11 allocations (`TestAllocBudgetNoop`); with no-op providers and a `traceparent`, 13
  (`TestAllocBudgetNoopTraceparent`); with the SDK, an in-memory span recorder and a manual metric
  reader, 21 under `-race` (19 without) (`TestAllocBudgetSDK`). `BenchmarkMiddleware`, no-op providers:
  median 651.25 ns/op, 1088 B/op, 11 allocs/op over ten runs, cited in prose — no results file is
  committed for it. Each budget is a regression guard, not a target; the cost is mostly the
  OpenTelemetry API's own allocations, not rice's.
- `otelrice/go.mod` requires rice at `v0.0.0-00010101000000-000000000000` with `replace
  github.com/vietpham102301/rice-http => ../`, the same shape `bench/compare/go.mod` uses. That
  `replace` serves development inside this repository only; a consumer of the published module ignores
  it and resolves a tagged core version instead. Until core is tagged with `Route` and `otelrice` is
  tagged in turn, `otelrice` is usable only by checking out this repository — it cannot yet be `go
  get`.
- `05-performance-model.md` gains `c.Route` at 0 in the `Ctx` methods table, and the three otelrice
  budgets and the benchmark median under *Opt-in packages*, outside the zero-allocation claim.
