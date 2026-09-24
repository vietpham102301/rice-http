# Static file serving — Design

**Status:** approved, not yet implemented
**Date:** 2026-09-25
**Milestone:** none. Work after the roadmap — see [04-roadmap.md](../../04-roadmap.md), *Explicitly deferred*
**Decision record:** ADR-0017 (new), "Static files wrap fasthttp.FS behind the error funnel"
**Depends on:** the radix router's wildcard ([ADR-0004](../../adr/0004-radix-tree-router.md)), the error
funnel ([ADR-0002](../../adr/0002-handler-returns-error.md)), Shutdown's drain
([ADR-0009](../../adr/0009-shutdown-force-closes-at-deadline.md))

## Goal

Serve files from an `fs.FS` under a URL prefix — a directory on disk through `os.DirFS`, or assets
compiled into the binary through `embed.FS` — with the same error, middleware and lifecycle rules as
any other route.

## Question this design answers

Can rice reuse fasthttp's file server, with its handle cache, byte ranges and conditional requests,
without letting it answer errors, redirects or shut down on terms that contradict rice's own?

## What the design is for

The owner chose one API for both sources (disk and embed), directories answered by their
`index.html`, and no SPA fallback. This is the first of three deferred items, taken in the order
static files, content negotiation, streaming.

## What exists

Read against rice at `3e6792e`, fasthttp v1.73.0.

- The router's wildcard `*name` captures the rest of the path and must capture at least one byte, so
  `/assets/*filepath` matches `/assets/a` but not `/assets/` or `/assets`.
- Every error reaches the client through `DefaultErrorHandler` (or the App's replacement) and
  `respond`, which overwrites the body.
- `fasthttp.FS` accepts an `fs.FS` in its `FS` field, a `PathRewrite` hook, `IndexNames`,
  `AcceptByteRange`, `Compress`, `CleanStop` and `PathNotFound`.

## What probing found

A throwaway program drove one `fasthttp.FS` handler, with `PathRewrite` returning the path after a
`/static` prefix, over `fstest.MapFS` and `os.DirFS`. Both sources behaved identically.

### 1. fasthttp writes its own error responses

`/nodir/` (a directory with no index) answers 403 "Directory index is forbidden"; a missing file 404
"Cannot open requested path"; `Range: bytes=99-` on a 3-byte file 416; a rewritten path containing a
`..` segment 500 "Internal Server Error". All are written with `ctx.Error`, so an App whose
ErrorHandler answers JSON would still send these as plain text. Only 404 has a hook (`PathNotFound`).

### 2. The directory redirect loses the prefix

`/static/docs` (a directory, no trailing slash) answers 302 with `Location: /docs/`: fasthttp
appends `/` to the *rewritten* path, not the request's. Followed, it leaves the static tree.

### 3. `Root` must be empty, not `"."`

With `Root: ""` and `AllowEmptyRoot: true`, `/` serves `index.html`. With `Root: "."` it answers 403:
the root directory's index is looked up as `./index.html`, which is not a valid `fs.FS` path.

### 4. The handle cache starts a goroutine, lazily

Constructing `fasthttp.FS` starts nothing; the first `NewRequestHandler` starts one cleanup goroutine,
which runs until `CleanStop` is closed.

### 5. Every miss is logged by fasthttp

Each 403, 404 and 416 above printed one line through `ctx.Logger()`, which is the Server's logger. The
funnel's own 404 logs nothing. A scanner walking `/static/…` produces one log line per request.

### 6. `Compress` writes to disk

With `Compress` set, fasthttp creates `<file>.fasthttp.gz` (and `.br`, `.zst`) next to the file, via
`os.CreateTemp` under the compress root. For an `fs.FS` with an empty root, that is relative to the
process's working directory — a write nobody asked a static file server to make.

## Non-goals

- SPA fallback (answering `index.html` for a path that matches no file). It is additive: an option
  later, not a change to what `Static` means now.
- Directory listings.
- Compression, in either direction: no on-the-fly compression and no serving of pre-compressed
  siblings. A reverse proxy or CDN does it better, and fasthttp's version writes to disk (finding 6).
- Options of any kind — cache duration, index names, `Cache-Control`. Headers come from middleware,
  passed in `mw`.
- Methods other than GET and HEAD.

## Decisions

### D1 — `Static(prefix, fsys, mw...)` on App and Group

```go
func (a *App) Static(prefix string, fsys fs.FS, mw ...Middleware)
func (g *Group) Static(prefix string, fsys fs.FS, mw ...Middleware)
```

```go
//go:embed web
var web embed.FS

sub, _ := fs.Sub(web, "web")
app.Static("/assets", sub)
app.Static("/uploads", os.DirFS("/var/uploads"), cacheControl)
```

`prefix` follows the group-prefix rule (`checkGroupPrefix`): empty, or beginning with `/` and not
ending with `/`. An empty prefix serves from the root of the URL space. A nil `fsys` panics at the
call, as a nil handler does. On a Group the prefix is joined to the group's and the routes inherit its
middleware, like every other group route.

### D2 — three patterns, GET and HEAD

`Static("/assets", fsys)` registers GET and HEAD for:

| Pattern | Purpose |
|---|---|
| `/assets` | redirect to `/assets/` (D4) |
| `/assets/` | the fs root, answered by its `index.html` |
| `/assets/*filepath` | everything beneath it |

The first two exist because a wildcard captures at least one byte. With an empty prefix only `/` and
`/*filepath` are registered. Each registration goes through `register`, so a pattern that collides
with an existing route panics at the call, naming it. Static and parameter routes outrank the
wildcard, so `GET /assets/health` registered separately is never shadowed by a file.

### D3 — one long-lived fasthttp.FS per Static call, built in Build

Each `Static` call records an entry; `Build` constructs its `fasthttp.FS` and calls
`NewRequestHandler` once. Constructing in `Build` rather than at the call means a program or test that
registers routes and never builds starts no goroutine (finding 4). The fixed configuration:

| Field | Value | Why |
|---|---|---|
| `FS` | `fsys` | |
| `Root` | `""`, `AllowEmptyRoot: true` | finding 3 |
| `IndexNames` | `["index.html"]` | owner's choice |
| `GenerateIndexPages` | false | no listings |
| `AcceptByteRange` | true | range requests are what fasthttp is being reused for |
| `Compress` | false | finding 6 |
| `CacheDuration` | fasthttp's default (10 s) | |
| `CleanStop` | a channel owned by the App | D6 |
| `PathRewrite` | returns `/` + the `filepath` param, or `/` for the root pattern | |

### D4 — rice owns the directory redirect

A request for a directory without its trailing slash answers **301** with `Location` set to the
request's own path plus `/`, and the query string kept. fasthttp's 302 (finding 2) is replaced: when
its handler answers 302, rice rewrites the status and `Location` from `c.Path()` rather than trusting
fasthttp's. The bare prefix pattern (`/assets`) redirects to `/assets/` directly, without calling
fasthttp. 301 is safe because only GET and HEAD are registered, so no method is being changed.

This does not contradict [ADR-0007](../../adr/0007-no-trailing-slash-or-case-insensitive-matching.md).
ADR-0007 refuses to treat `/users` and `/users/` as one *route*. Here the resource is a directory, and
without the slash every relative link in its `index.html` resolves against the parent. That is the
resource's semantics, not a routing convenience, and `net/http.FileServer` answers it the same way.
ADR-0017 says so explicitly.

### D5 — every failure goes through the funnel

The route's handler calls fasthttp's handler, then reads the status it wrote:

| fasthttp wrote | the handler returns |
|---|---|
| 2xx, 304 | `nil` — the response stands |
| 302 (directory without slash) | `nil`, after D4's rewrite |
| 404, or 403 (directory without index) | `ErrNotFound` |
| 416 | `NewHTTPError(416, "")` |
| any other status ≥ 400 | `NewHTTPError(status, "")` |

`respond` overwrites the body, so fasthttp's texts ("Directory index is forbidden") never reach a
client. A directory without an index answers 404, not 403, so a response never confirms that a
directory exists. Before calling fasthttp, the handler rejects a path containing a NUL byte or a `..`
segment with `ErrNotFound`, so rice's behaviour does not depend on fasthttp's checks, which answer 400
and 500 for them (finding 1). `fctx.Path()` is already normalised by fasthttp, so this is a second
line, not the only one.

### D6 — Shutdown stops the cache goroutines after the drain

`Shutdown` closes every `CleanStop` channel after in-flight requests have drained — a body stream may
still be reading a cached file until then — and before it runs the `OnShutdown` hooks. It is internal
to `Shutdown`, not an `OnShutdown` hook, so a user's hooks cannot reorder it. An App mounted through
`FasthttpHandler()` and never shut down keeps one goroutine per `Static` call until the process exits;
the doc comment says so.

### D7 — fasthttp's miss logging is accepted

Finding 5 is accepted rather than worked around. Silencing it needs either a stat before every
request (bypassing the handle cache fasthttp is being reused for) or replacing the Server's logger for
all of fasthttp. The doc comment on `Static` and ADR-0017 name the behaviour; an application that
cares puts a proxy in front or sets `fasthttp.Server.Logger` itself when mounting.

## Consequences to record

- Static files are outside the zero-allocation claim. `05-performance-model.md` adds them to the
  exclusion list with the measured figure.
- A body stream is read after the handler returns. It belongs to `*fasthttp.RequestCtx`, not to
  `Ctx`, so releasing `Ctx` to the pool on return is still correct; fasthttp's recover guards the
  stream write.
- The status-inspection in D5 depends on fasthttp's current behaviour. Tests pin each mapping, so an
  upgrade that changes a status fails a test rather than a client.

## Components

| File | Contents |
|---|---|
| `static.go` | `App.Static`, `Group.Static`, the entry type, the route handler, the D4/D5 mapping |
| `build.go` | construct each entry's `fasthttp.FS` in `Build` |
| `server.go` | close the `CleanStop` channels in `Shutdown`, after the drain |
| `static_test.go` | behaviour tests |
| `alloc_test.go` | the static-file budget |
| `bench/rice_bench_test.go` | `BenchmarkStaticSmallFile` |

## Allocation budget

One budget, a GET for a small file already in fasthttp's handle cache, pinned at whatever is measured.
It is a regression guard, not a target. A 404 through the static route is measured too, because it is
the path a scanner exercises.

## Testing

Mostly `fstest.MapFS`; one test repeats the core cases over `os.DirFS(t.TempDir())`.

- **Serving:** a file, a nested file, `index.html` at the root and in a subdirectory, HEAD (headers,
  no body), `Content-Type` from the extension.
- **Directories:** without index → 404; without slash → 301 with the prefix and query kept; the bare
  prefix → 301.
- **Funnel:** an App with a JSON ErrorHandler sees 404 and 416 as errors and answers them in JSON.
- **Ranges and caching:** `Range: bytes=0-0` → 206; an unsatisfiable range → 416;
  `If-Modified-Since` → 304.
- **Traversal:** `/assets/../go.mod`, `%2e%2e` encoded, and a NUL byte never serve a file outside
  `fsys`.
- **Routing:** empty prefix; `Group.Static` with middleware that runs; a separately registered
  `/assets/health` wins over the wildcard; a colliding pattern panics; a bad prefix and a nil `fsys`
  panic; `Static` after `Build` panics.
- **Lifecycle:** the goroutine count returns to its starting value after `Shutdown`; an App that is
  never built starts none.
- **Budget:** as above.

## Documentation

- ADR-0017: wrapping fasthttp.FS, the D5 mapping, the D4 distinction from ADR-0007, and D7.
- `03-core-concepts.md`: a Static section.
- `05-performance-model.md`: the exclusion and its figures.
- `04-roadmap.md`: static files move from *Explicitly deferred* to *Done after M8*.
- `docs/progress.md`: a new entry.
- README: a short example.

## Exit criteria

`make test` passes with and without `-race` and under `ricedebug`; every mapping in D5 has a test; the
budget test passes; the benchmark is recorded; coverage of the root package does not fall.

## Open questions

None.
