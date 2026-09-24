# ADR-0017 — Static files wrap fasthttp.FS behind the error funnel

Status: Accepted
Date: 2026-09-25

## Context

Static file serving was on the roadmap's *Explicitly deferred* list. The owner wanted one API for
files on disk and files embedded in the binary, a directory answered by its `index.html`, and no
SPA fallback. fasthttp v1.73 ships a file server, `fasthttp.FS`, that accepts an `fs.FS` and
already answers byte ranges, `If-Modified-Since` and HEAD, with a cache of open file handles.

Probing it behind a prefix found six things (the design's *What probing found*): it writes its
own error responses with `ctx.Error`, so a JSON ErrorHandler never sees them; its directory
redirect is built from the rewritten path and loses the prefix; its root must be `""`, not `"."`;
its handle cache runs a goroutine until `CleanStop` is closed; it logs every miss through the
server's logger; and with `Compress` it writes compressed copies of files to disk.

## Decision

`App.Static(prefix, fsys, mw...)` and `Group.Static` wrap one `fasthttp.FS` per call, created in
`Build`, and rice owns every edge fasthttp gets wrong for it:

- **Errors.** After fasthttp's handler returns, rice reads the status it wrote. 404, and the 403
  for a directory without an index, become `ErrNotFound`; any other status ≥ 400 becomes an
  `HTTPError` with that code. The funnel's `respond` overwrites fasthttp's body.
- **Middleware headers.** fasthttp answers a 304 with `ctx.NotModified` and every error with
  `ctx.Error`, and both reset the whole response. When middleware has set a header before `next`,
  rice copies the response header aside first and puts it back, with fasthttp's status, on any
  response that is not a 2xx, so CORS, a request ID or `Cache-Control` survive a 304, a 404 or a
  416 as they do on any other route. A 2xx is never reset and is not copied.
- **Traversal.** fasthttp normalises `fctx.Path()` unconditionally before routing, so a request
  carrying a `..` segment is already a route miss — a 404 — before a Static handler runs; the
  handler's own `..` check is a second line, kept in case that ever changes, not the reason a `..`
  fails today. A NUL byte does survive normalisation and reaches the handler, which refuses it
  with a 404 before fasthttp sees the path — fasthttp.FS alone would answer 400.
- **Directory redirect.** fasthttp's 302 is replaced by a 301 whose relative `Location` is the
  request's normalised path — `fctx.Path()`, the one routing matched — percent-encoded, plus `/`,
  query kept. Never `URI.RequestURI`: with `URI.DisablePathNormalizing` set it returns the raw
  path, and `//evil.example/../docs` would redirect off the site.
- **Lifecycle.** `Shutdown` closes every `CleanStop` after the drain and before the `OnShutdown`
  hooks. A `Build` after `Shutdown` creates no `fasthttp.FS`, since nothing would stop its
  goroutine; its Static routes answer 500.
- **Configuration is fixed:** `index.html` only, no listings, ranges on, compression off.

## Alternatives

**Implement file serving on `fs.FS` directly.** Full control, no goroutine, errors native to the
funnel. Rejected because byte ranges and conditional requests are exactly the code where a new
implementation grows bugs, and fasthttp's is exercised by its users already. Nothing rice needs
to control lies inside that code; everything it needs to control lies at its edges.

**Stat each path first, then hand only files to fasthttp.** Makes a 404 and a directory rice's
own decision before fasthttp runs, and silences fasthttp's miss log. Rejected because it costs a
stat per request and bypasses the handle cache that is the reason for reusing fasthttp.

**Let fasthttp's responses stand.** Rejected: an App with a JSON ErrorHandler would answer every
static failure in plain text, and the directory redirect would send clients out of the prefix.

## Consequences

**The redirect does not contradict ADR-0007.** ADR-0007 refuses to treat `/users` and `/users/`
as one route. A directory without its slash is one resource whose relative links resolve wrongly;
redirecting it is the resource's semantics, as `net/http.FileServer` also does.

**Mapping by status depends on fasthttp's behaviour.** Each row of the mapping has a test, so an
upgrade that changes a status fails a test rather than a client.

**fasthttp's miss log is accepted.** Each missing file logs one line through the server's logger.
Silencing it needs a stat per request or replacing fasthttp's logger for everything; an
application that cares sets `fasthttp.Server.Logger` when mounting, or puts a proxy in front.

**A mounted App keeps its goroutines while its handler is reachable — normally the life of the
process.** fasthttp registers a
`runtime.AddCleanup` on each FS handler, so the cache goroutine stops at `Shutdown` or when the
App's handler becomes unreachable and is garbage collected. For an App mounted through
`FasthttpHandler` and never shut down, its handler stays reachable for as long as the process
runs, which is normally the life of the process.

**fasthttp's 416 carries no `Content-Range`.** RFC 9110 says an unsatisfiable range SHOULD be
answered with `Content-Range: bytes */<length>`; fasthttp's `ctx.Error` writes none, and rice does
not add one.

**Static files are outside the zero-allocation claim.** A cache hit measures 0 allocations, but
the budget and the benchmark measure the handler only: they exclude writing the file's bytes to
the connection, which fasthttp does after the handler returns. Their cost is pinned in
[05-performance-model.md](../05-performance-model.md).
