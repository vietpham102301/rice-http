# ADR-0015 — CORS states a policy; the browser enforces it

Status: Accepted
Date: 2026-09-24

## Context

The first service to use rice is a JSON API behind a reverse proxy, called by a single-page
application from a fixed set of origins known when the process starts. For that to work across
origins the service must answer the browser's preflight, and put the CORS headers on every other
response — the error responses included, or the browser reports a 401 as a CORS failure and the
application never learns it was a 401. Credentials are a per-deployment choice. Nothing needs a
wildcard origin, a per-request callback or subdomain matching.

A CORS middleware has to decide what it checks and what it leaves to the browser. Three findings
settled it: two from reading rice at `3248fb3`, and one from the Fetch standard.

**The funnel never resets headers.** Every error response is written by `respond`
(`errors.go:156`): through `DefaultErrorHandler`, through the last-resort net in
`callErrorHandler` (`app.go:337`), and for a recovered panic through `respondPanic`, which calls
the same function. It sets the status, the content type and the body. It does not call
`Response.Reset` and touches no other header. So a header written *before* `next` survives a
returned error answered 500, an `*HTTPError` 401, and a recovered panic answered 500. No document
said so; it was true because nothing had needed it to be false. It is what lets a 401 from an
authentication middleware inside `CORS` reach the browser as a 401.

**Only application middleware sees a preflight.** A browser's preflight `OPTIONS /users`, against
a path registered for `GET` and `POST` only, is a route miss. Since
[ADR-0012](0012-application-middleware-runs-on-route-misses.md) a miss runs the application's
middleware and nothing else (`app.go:315`); group and route middleware never run there. A CORS
middleware installed on a group therefore decorates real responses and never sees a preflight,
and the browser fails on the first non-simple request. Nothing at run time can detect that
placement.

**The browser is the one that checks the method and the headers.** Under the Fetch standard, after
a preflight the browser compares the method and each header it intends to send against
`Access-Control-Allow-Methods` and `Access-Control-Allow-Headers` in the response, and blocks the
request itself when they are not listed. A server that validates `Access-Control-Request-Method`
and `Access-Control-Request-Headers` and withholds its headers on a mismatch reaches the same
outcome by a longer road. The one check the server cannot delegate is the origin:
`Access-Control-Allow-Origin` must name one origin when credentials are involved, and the server
decides which origins those are.

## Decision

`middleware.CORS(cfg CORSConfig) rice.Middleware`, where `CORSConfig` has six fields: `Origins`
(required), `AllowMethods` (empty means `GET, HEAD, POST, PUT, PATCH, DELETE`), `AllowHeaders`,
`ExposeHeaders`, `MaxAge` and `AllowCredentials` (each sending nothing when empty, zero or false).
It states a policy and leaves the browser to enforce it.

- **Everything that can be done once is done at construction.** The lists are joined into
  `"A, B, C"` strings and `MaxAge` is rendered in whole seconds. Two fixed lists of header pairs
  are built: for a real response, `Access-Control-Allow-Credentials` when set and
  `Access-Control-Expose-Headers` when non-empty; for a preflight, `Access-Control-Allow-Methods`,
  `-Allow-Headers` when non-empty, `-Max-Age` when non-zero, and `-Allow-Credentials` when set. An
  empty field produces no pair, so per request there is no branch on configuration. The origins
  are copied, so a caller changing its slice later changes nothing.
- **A configuration that cannot work panics at construction**, with the `rice: middleware.CORS: `
  prefix: no origins; an origin that is not `scheme://host[:port]` in lower case — `"*"`, `"null"`,
  a path, a trailing slash, a query, an upper-case letter, or an explicit default port such as
  `:443` on `https` or `:80` on `http`, which a browser never sends and which could then never
  match; a negative `MaxAge`, or a positive one under one second, since it would render as `0` and
  silently discard what the caller asked for; a list entry that is empty or contains a comma,
  whitespace or a control character, which would corrupt the joined value.
- **Three branches per request**, chosen from the method and two header reads.
  1. *No `Origin`, or an empty one.* Same-origin, or not a browser. `Vary: Origin` is added and
     `next` is called.
  2. *`Origin` present, not a preflight.* The origin is compared byte-for-byte with the configured
     list. On a match, `Access-Control-Allow-Origin` is set to the configured string and the
     real-response pairs follow. On no match nothing is set. Either way `next` is called: the
     server still serves, and the browser blocks the script from reading the result.
  3. *A preflight:* `OPTIONS` with `Origin` and `Access-Control-Request-Method` both present.
     An `OPTIONS` without `Access-Control-Request-Method` is an ordinary request and takes
     branch 2; one without `Origin` takes branch 1. On a match, `Access-Control-Allow-Origin` and
     the preflight pairs are set. The answer is
     `c.NoContent(204)`, **without calling `next`**, matched or not: the absence of headers is the
     "no". `Access-Control-Request-Method` is read for its presence only, and
     `Access-Control-Request-Headers` is never read.
- **Headers are written before `next`.** The funnel does not reset them, so they are on the
  response whatever the chain returns.
- **`Vary: Origin` is added to every response,** branch 1 included, with `Add`, so a handler's own
  `Vary` survives. A shared cache must learn that the response depends on `Origin` from the
  response that had none. Because a preflight's answer depends on `Origin` alone — not on what the
  browser asked for — `Vary: Origin` is sufficient there too. `Access-Control-Allow-Origin` is
  written with `Set`: there is one correct value.
- **The origin match is exact.** No case folding, no prefix, no normalisation. The value written is
  the configured string, equal to the header's bytes, so nothing is converted.
- **Placement.** `CORS` is installed with `app.Use`, inside `Logger` so a preflight's 204 is
  logged, after `RequestID` so a preflight carries `X-Request-Id`, and before any middleware that
  answers 401 or 403, so those answers carry the headers.

All three branches cost 0 allocations. `TestAllocBudgetCORSNoOrigin`,
`TestAllocBudgetCORSAllowedOrigin` and `TestAllocBudgetCORSPreflight` pin that exactly, measured at
0 on darwin (go1.25.6, arm64) and in a Linux container (golang:1.25.14, arm64), with and without
`-race`.

## Alternatives

**Validating `Access-Control-Request-Method` and `-Headers` on the server,** as rs/cors does:
parse the comma-separated list the browser sends, check each entry against the configuration, and
withhold the headers on a mismatch. Rejected because it reaches the browser's own outcome by a
longer road. It parses a list per preflight and allocates for it, on a path that otherwise costs
nothing. And it makes the preflight response depend on those two request headers, so a correct
implementation would need `Vary: Access-Control-Request-Method, Access-Control-Request-Headers`
as well, or a shared cache could serve one request's answer to another. Stating the policy makes
the response a function of `Origin` alone.

**A wildcard origin (`"*"`).** Rejected for now. The service this is for has no use for it, and it
brings a cross-field rule of its own: a wildcard may not be combined with credentials, which a
browser rejects. `"*"` panics at construction so nobody discovers the omission by reading the
code. It can be added later as its own roadmap entry.

**A per-group preflight** — `CORS` installed on `/api` answering the preflights for `/api`.
Impossible as rice stands. A preflight is a miss, and a group's middleware would run on a miss
only with a group-aware miss chain: a lookup that reports the longest matching group prefix,
which ADR-0012 names as a possible future and does not build.

## Consequences

**Makes easy.** A single-page application on another origin calls the API: its preflights are
answered, its real requests carry the headers, and its 401s, 404s and 500s arrive as 401s, 404s
and 500s rather than as CORS failures. All three branches cost nothing, so a service pays nothing
per request for installing it, and nothing for requests from the same origin.

**Makes hard: a group cannot answer a preflight.** `CORS` must be installed with `app.Use`. On a
group or a route it decorates real responses and never sees a preflight, and nothing detects the
mistake. `CORS`'s doc comment and `middleware/doc.go` say so.

**Makes hard: a preflight for a path that does not exist is answered 204.** The middleware cannot
tell a miss from a route — ADR-0012 gives it no signal — so it answers every preflight. The real
request that follows then receives a 404, which carries the CORS headers.

**Makes hard: a route's own `OPTIONS` handler never sees a preflight.** `CORS` answers the
preflight without calling `next`. The route receives only ordinary `OPTIONS` requests — those
without `Origin` or without `Access-Control-Request-Method`.

**Makes hard: a custom `ErrorHandler` that resets the response drops the headers.** The headers
survive the funnel only because `respond` does not reset them, and a custom `ErrorHandler` that
calls `Response.Reset` removes them, so the browser reports that response as a CORS failure; so
does a handler that resets the response through `c.RequestCtx()`, for example with `NotModified`.
Nothing protects against it, because protection would cost every request. This also binds rice
itself: a future change to `respond` that resets headers breaks every CORS error response, and
`TestCORSHeadersSurviveTheFunnel` fails if it does.

**Forecloses little.** A wildcard origin, a per-request origin callback and subdomain patterns
can each be added as a design of its own; nothing here depends on their absence. An origin of
`null` is not allowed and cannot be: it fails the origin check at construction.
