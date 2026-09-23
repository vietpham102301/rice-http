# CORS middleware — Design

**Status:** approved, not yet implemented
**Date:** 2026-09-24
**Milestone:** none. Work after the roadmap — see [04-roadmap.md](../../04-roadmap.md), *Done after M8*
**Decision record:** ADR-0015 (new), "CORS states a policy; the browser enforces it"
**Depends on:** the miss chain ([ADR-0012](../../adr/0012-application-middleware-runs-on-route-misses.md)),
`c.NoContent`, and the funnel's promise not to reset response headers (finding 1)

## Goal

Let a browser application served from another origin call a rice JSON API: answer its preflight,
put the right headers on the real response, and put them on the error responses too, so the
browser reports a 401 as a 401 and not as a CORS failure.

## Question this design answers

What does a CORS middleware have to check on the server, and what can it leave to the browser?

## What the design is for

The first service to use rice is a JSON API behind a reverse proxy, called by a single-page
application from a fixed set of origins known when the process starts. Credentials are a
per-deployment choice, off by default. Beyond origins and credentials, the service needs to say
which methods and request headers are allowed, which response headers a script may read, and how
long a browser may cache a preflight. Nothing needs a wildcard origin, a per-request callback or
subdomain matching.

## What probing found

Read against rice at `3248fb3`.

### 1. The funnel never resets headers

Every error response is written by `respond` (`errors.go:156`), through `DefaultErrorHandler` and
through the last-resort net in `callErrorHandler`. It sets the status, the content type and the
body. It does not call `Response.Reset` or touch any other header. The panic path goes through the
same function. So a header set *before* `next` survives a returned error, an `*HTTPError`, and a
recovered panic. That is the property that lets a 401 from an authentication middleware inside
`CORS` reach the browser as a 401.

### 2. Only application middleware sees a preflight

A preflight `OPTIONS /users` against a path registered for `GET` and `POST` is a route miss. ADR-0012
runs the application's middleware on a miss and nothing else: group and route middleware never
run there. A `CORS` installed on a group therefore decorates real responses and never sees a
preflight, and the browser fails on the first non-simple request. Nothing at run time can detect
that placement.

### 3. The browser is the one that checks method and headers

Under the Fetch standard, after a preflight the browser compares the method and each header it
intends to send against `Access-Control-Allow-Methods` and `Access-Control-Allow-Headers` in the
response, and blocks the request itself when they are not listed. A server that validates
`Access-Control-Request-Method` and `Access-Control-Request-Headers` and withholds the headers on a
mismatch reaches the same outcome by a longer road. The only check the server cannot delegate is
the origin, because `Access-Control-Allow-Origin` must name one origin when credentials are
involved.

## Non-goals

- **A wildcard origin (`"*"`).** Not needed by the service this is for, and it brings a
  cross-field rule — a wildcard may not be combined with credentials — for a feature nobody asked
  for. An origin of `"*"` panics at construction so that nobody discovers this by reading the
  code. It can be added as its own entry later.
- **A per-request origin callback, or subdomain patterns.** Each is a design of its own.
- **Server-side validation of the requested method and headers.** Finding 3.
- **Answering preflights for a group or route only.** Finding 2; a preflight can only be answered
  from `app.Use`.
- **Origin `null`.** A browser sends the string `null` from `file://` and sandboxed frames; an
  entry of `"null"` fails the origin check at construction, so it cannot be allowed. Allowing it is
  a known hole.

## Decisions

### D1 — `CORS(cfg CORSConfig) rice.Middleware`

```go
type CORSConfig struct {
    Origins          []string      // required, e.g. "https://app.example.com"
    AllowMethods     []string      // empty: "GET, HEAD, POST, PUT, PATCH, DELETE"
    AllowHeaders     []string      // empty: no Access-Control-Allow-Headers is sent
    ExposeHeaders    []string      // empty: no Access-Control-Expose-Headers is sent
    MaxAge           time.Duration // zero: no Access-Control-Max-Age is sent
    AllowCredentials bool
}
```

A struct rather than functional options: six fields read by name, and the cost is identical
because everything is compiled at construction (D2). Design principle 4 warns against an options
struct that grows a boolean per feature; here each field is one response header, and no field is
consulted per request.

`AllowMethods` defaults to the six verbs because a preflight is only sent for a non-simple method
and a JSON API uses `PUT`, `PATCH` and `DELETE`. `AllowHeaders` has no default because the
browser's safelist does not include `Content-Type: application/json` or `Authorization`, and
guessing a list would be stating a policy the author did not choose. The documentation's example
sets both.

### D2 — everything that can be done once is done at construction

`CORS` joins `AllowMethods`, `AllowHeaders` and `ExposeHeaders` into `"A, B, C"` strings and
renders `MaxAge` as whole seconds. It then builds two fixed lists of `(key, value)` pairs:

- **for a real response:** `Access-Control-Allow-Credentials: true` when set,
  `Access-Control-Expose-Headers` when non-empty;
- **for a preflight:** `Access-Control-Allow-Methods`, `Access-Control-Allow-Headers` when
  non-empty, `Access-Control-Max-Age` when non-zero, `Access-Control-Allow-Credentials` when set.

An empty field produces no pair, so per request there is no branch on configuration: the
middleware loops over the pairs it has. Only `Access-Control-Allow-Origin` is set outside the
loop, because its value depends on the request.

### D3 — construction panics on a configuration that cannot work

Prefix `rice: middleware.CORS: `, so a mistake dies at start-up rather than silently in
production:

- `Origins` is empty;
- an origin is `"*"`;
- an origin is not `scheme://host[:port]` — it has a path, a trailing `/`, a query, or an
  upper-case letter. A browser sends `Origin` in lower case with no trailing slash, and
  `"https://app.example.com/"` is the classic entry that never matches;
- `MaxAge` is negative;
- an entry in `AllowMethods`, `AllowHeaders` or `ExposeHeaders` is empty or contains `,` or
  whitespace, because it would corrupt the joined string.

### D4 — three branches per request

The branch is chosen from the method and two header reads.

1. **No `Origin` header, or an empty one.** Same-origin or not a browser. Add `Vary: Origin`,
   call `next`.
2. **`Origin` present, not a preflight.** Compare `Origin` byte-for-byte against `Origins`. Add
   `Vary: Origin`. On a match, set `Access-Control-Allow-Origin` to the *configured* string —
   equal to the header's bytes, so no `[]byte`-to-`string` conversion — then the real-response
   pairs. On no match, set nothing. Either way call `next`: the server still serves, and the
   browser blocks the script from reading the result.
3. **Preflight:** method `OPTIONS`, `Origin` present, `Access-Control-Request-Method` present.
   Missing any one, it is an ordinary `OPTIONS` and takes branch 2. Add `Vary: Origin`. On an
   origin match, set `Access-Control-Allow-Origin` and the preflight pairs. Return
   `c.NoContent(204)` **without calling `next`**, matched or not: the absence of headers is the
   "no". Because the response states a policy rather than answering a question, it depends on
   `Origin` alone, and `Vary: Origin` is sufficient.

### D5 — headers are written before `next`

Finding 1. The CORS headers must be on the response whatever the chain returns, and the funnel
preserves them. Three tests pin it: a plain error answered 500, an `*HTTPError` 401, and a panic
under `Recover`. A custom `ErrorHandler` that calls `Response.Reset` drops them; the documentation
says so, and nothing protects against it, because protection would cost every request.

### D6 — `Vary` is added, `Allow-Origin` is set

`Vary: Origin` goes on with `Add`, so a handler's own `Vary` survives, and it goes on every
response including branch 1: a shared cache must learn that the response depends on `Origin` from
the response that had none. `Access-Control-Allow-Origin` goes on with `Set`; there is one
correct value.

### D7 — placement

`CORS` is installed with `app.Use` (finding 2), inside `Logger` so the preflight's 204 is logged,
after `RequestID` so a preflight carries `X-Request-Id`, and before any middleware that answers
401 or 403:

```go
app.Use(
    middleware.Logger(l),
    middleware.Recover(),
    middleware.RealIP(1),
    middleware.RequestID(),
    middleware.CORS(cfg),              // before auth, so a 401 carries the CORS headers
    middleware.Timeout(5*time.Second),
)
```

## Consequences to record

- **A preflight for a path with no route is answered 204.** The middleware cannot tell a miss from
  a route (ADR-0012 provides no signal), and the real request then receives a 404 that carries the
  CORS headers. Every widely used CORS middleware behaves this way.
- **A user-registered `OPTIONS` route never receives a preflight**, only an ordinary `OPTIONS`.
- **A group-level `CORS` cannot answer a preflight.** Documented in `doc.go` and in the type's
  doc comment; not detectable.
- **A custom `ErrorHandler` must not reset the response** if it wants the headers kept.

## Components

| File | Change |
| --- | --- |
| `middleware/cors.go` (new) | `CORSConfig`, `CORS`, construction checks |
| `middleware/cors_test.go` (new) | behaviour |
| `middleware/alloc_test.go` | three budgets |

No change to package `rice`.

## Allocation budget

Target **0** on all three branches. Comparing `string(b) == s` does not allocate; `Header.Set`
and `Header.Add` with `string` arguments copy into fasthttp's warm buffers; the pairs are built
once. Three budgets pin it: a request without `Origin`, a request from an allowed origin, and a
preflight. **Measured on darwin and Linux, with and without `-race`, before any figure is
written.** If a branch measures above 0, the measured figure is pinned exactly, not rounded, with
a race slack derived from the mechanism as `assertAllocBudget` requires.

## Testing

Behaviour, one test per rule so that breaking the rule breaks the test:

- no `Origin` → no CORS headers, `Vary: Origin` present, handler ran
- allowed origin → `Allow-Origin` echoes it, `Expose-Headers` present, `Allow-Credentials`
  present when set and absent when not
- origin not in the list → no CORS headers, handler ran, `Vary: Origin` present
- preflight from an allowed origin → 204, empty body, no `Content-Type`, the preflight pairs,
  **no** `Expose-Headers`, handler did **not** run
- preflight from a disallowed origin → 204, no CORS headers
- `OPTIONS` without `Access-Control-Request-Method` → reaches the handler
- preflight on a path with no route → 204, not 404
- headers survive: a plain error → 500 with headers; an `*HTTPError` 401 with headers; a panic
  under `Recover` → 500 with headers
- a handler's own `Vary` is kept alongside `Origin`
- default `AllowMethods` appears when the field is empty; `Max-Age` absent when zero
- `Logger` outside `CORS` logs the preflight as 204
- each D3 panic, including the trailing slash and the upper-case letter, with the `rice: ` prefix

## Documentation

- **ADR-0015 (new)** — "CORS states a policy; the browser enforces it". Findings 1–3; the
  alternatives that lost — server-side validation, a wildcard origin, a group-level preflight —
  and why.
- **`docs/04-roadmap.md`** — a *Done after M8* entry; correct the sentence that says CORS "has no
  design yet".
- **`docs/03-core-concepts.md`** — a paragraph on `CORSConfig`, as principle 6 requires of an
  exported type.
- **`README.md`**, **`docs/02-architecture.md`**, **`docs/05-performance-model.md`**,
  **`middleware/doc.go`** (the recommended order gains `CORS`, and the group-level trap).
- **`docs/progress.md`** — one entry.

## Exit criteria

1. `make test` green under `-race` and `-tags ricedebug`, on darwin, and in a Linux container.
2. Every D3, D4, D5 and D6 rule is pinned by a test that fails when it is broken.
3. The three budgets are measured on both platforms before any figure is written.
4. ADR-0015 written and indexed; no document still says CORS has no design.

## Open questions

None.
