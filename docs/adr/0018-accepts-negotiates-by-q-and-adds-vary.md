# ADR-0018 — Accepts picks by the client's q, breaks ties by the server's order, and adds Vary

Status: Accepted
Date: 2026-09-26

## Context

Content negotiation was on the roadmap's *Explicitly deferred* list. The owner scoped it to format
selection: one handler answering JSON to an API client and HTML or plain text to a browser, chosen by
the `Accept` header. Two choices in RFC 9110 §12.5.1 are left to the server, and one choice about
caching is left to the framework.

## Decision

`c.Accepts(offers ...string) string` returns the offer the request prefers, or `""`.

- **Quality is the client's.** Each offer takes the q of the most specific matching range —
  `text/html` over `text/*` over `*/*` — and `q=0` excludes it.
- **Ties are the server's.** Among offers of equal quality the earlier one wins. A browser sends
  `*/*;q=0.8` after its preferred types; with `*/*` alone, as curl sends, every offer ties and the
  handler's first choice is served.
- **A leading-dot qvalue is accepted.** `q=.2` is not an RFC 9110 `qvalue`, but Java's
  `HttpURLConnection` sends it by default, so `.` followed by one to three digits is read as `0.`
  and those digits. A bare `*` range stays invalid.
- **`Accepts` adds `Vary: Accept`, once.** Every call adds it, whatever it returns and whether or
  not the request sent `Accept`, unless a `Vary` line already lists `Accept` or `*`. It adds rather
  than sets, so CORS's `Vary: Origin` survives.
- **It writes no status.** A handler with nothing acceptable returns `ErrNotAcceptable`, a shared
  `*HTTPError` like `ErrNotFound`.
- **It allocates nothing and stores nothing on `Ctx`.** Each offer scans the `Accept` bytes once.

## Alternatives

**Leave `Vary` to the handler.** Keeps `Accepts` a pure read, like every other accessor. Rejected by
the owner: forgetting it is silent, and its cost is a shared cache serving one client's format to
another. The write is named in the doc comment's second paragraph.

**Two methods, one pure and one that adds `Vary`.** Rejected as twice the API for a choice with one
right answer in practice.

**Break ties by the client's order.** Some frameworks do. Rejected because RFC 9110 gives ordering
meaning only through q, and because with `*/*` — the commonest header after a browser's — the
client expresses no order at all; the server's order is the only preference there is.

**Parse `Accept` into a buffer on `Ctx`.** Faster when a handler calls `Accepts` several times.
Rejected: it adds per-request state that `reset` must clear and a size limit to choose, for a case
that is rare.

**A third-party parser.** Rejected: a dependency in core, and the candidates allocate.

## Consequences

**A read that writes.** `Accepts` is the one `Ctx` read-side method with a side effect on the
response. Calling it on a path that then serves something unrelated still adds `Vary: Accept`, which
is harmless to correctness and costs a cache some hit rate.

**Parameters are not matched.** `text/html;level=1` matches `text/html`; `application/vnd.app+json`
does not match `application/json`. Versioning by media-type parameters is out of scope. A
parameterised range such as `text/html;level=1;q=0` is therefore treated as `text/html;q=0`.

**Offers are validated as tokens.** An offer's type and subtype must each be an RFC 9110 token
without `*`; `text /html`, `text/html/x` and `application/*+json` panic like `json` does. Its
parameters are not validated.

**The ricedebug walker changed.** `TestEveryCtxMethodPanicsAfterRelease` calls variadic methods with
`CallSlice`; `reflect.Call` would panic inside reflect first and hide the use-after-release panic.
