# Content negotiation — Design

**Status:** approved, not yet implemented
**Date:** 2026-09-26
**Milestone:** none. Work after the roadmap — see [04-roadmap.md](../../04-roadmap.md), *Explicitly deferred*
**Decision record:** ADR-0018 (new), "Accepts picks by the client's q, breaks ties by the server's order, and adds Vary"
**Depends on:** the error funnel ([ADR-0002](../../adr/0002-handler-returns-error.md)), the borrow contract
([ADR-0005](../../adr/0005-context-pooling-and-borrow-contract.md)), no reflection in core
([ADR-0006](../../adr/0006-no-reflection-in-core.md))

## Goal

Let one handler answer the same resource in more than one format — JSON to an API client, HTML or
plain text to a browser — by choosing from the formats it can produce the one the request's `Accept`
header prefers, at zero allocations.

## Question this design answers

Can `Accept` be parsed and matched by RFC 9110's rules on the request path without allocating, and
without adding state to the pooled `Ctx`?

## What the design is for

The owner chose format selection only: the `Accept` header. Language (`Accept-Language`), encoding
and charset are out of scope. This is the second of three deferred items, after static files and
before streaming.

## What exists

Read against rice at `0ca0442`, fasthttp v1.73.0.

- `ctx_response.go` has `String`, `Bytes`, `NoContent`, `JSON`, `SetContentType`, and the constants
  `MIMETextPlainUTF8` and `MIMEApplicationJSON`. Nothing reads `Accept`.
- `ErrNotFound` in `errors.go` is a shared `*HTTPError` with no `Err`; `DefaultErrorHandler`'s type
  switch answers it without allocating.
- `middleware/cors.go` adds `Vary: Origin` with `Add`, not `Set`, so a handler's own `Vary` survives.
- `TestEveryCtxMethodPanicsAfterRelease` (ricedebug) calls every exported `*Ctx` method by reflection
  with zero-valued arguments.

## What probing found

A throwaway program against fasthttp v1.73.0:

1. **`Peek` returns only the first of several `Accept` lines.** A request with `Accept: text/html`
   and `Accept: application/json;q=0.5` gives `Peek("Accept") == "text/html"`. `PeekAll("Accept")`
   returns both, and costs 0 allocations on a warm header.
2. **Reading and adding `Vary` on the response is allocation-free.** `Response.Header.PeekAll("Vary")`
   and `Response.Header.Add("Vary", "Accept")` each measured 0 allocations; two `Add` calls produce
   two `Vary` lines.
3. **The ricedebug walker cannot call a variadic method.** `reflect.Value.Call` with a zero `[]string`
   for a `...string` parameter panics inside reflect before the method runs, so the walker would
   report a reflect panic, not the use-after-release panic. It must use `CallSlice` for a variadic
   method.

## Non-goals

- `Accept-Language`, `Accept-Encoding`, `Accept-Charset`.
- Matching media-type parameters other than `q` (for example `version=2` or `charset`), and structured
  suffixes (`application/vnd.app+json` matching `application/json`).
- A helper that negotiates and renders in one call (`c.Negotiate(code, map[string]any)`), and any
  renderer beyond the existing ones.
- Answering 406 automatically.

## Decisions

### D1 — `c.Accepts(offers ...string) string`

```go
func (c *Ctx) Accepts(offers ...string) string

var ErrNotAcceptable = &HTTPError{Code: fasthttp.StatusNotAcceptable, Message: "Not Acceptable"}
```

```go
switch c.Accepts(rice.MIMEApplicationJSON, "text/html; charset=utf-8") {
case rice.MIMEApplicationJSON:
	return c.JSON(200, u)
case "text/html; charset=utf-8":
	c.SetContentType("text/html; charset=utf-8")
	return c.Bytes(200, page)
}
return rice.ErrNotAcceptable
```

`Accepts` returns the chosen offer exactly as passed — the same string, not a copy — or `""` when none
is acceptable. With no offers it returns `""`. It calls `c.poison.check()` first.

An offer is a full media type, `type/subtype`, optionally with parameters. Only `type/subtype` takes
part in matching; the parameters are returned with the offer, so it can go straight to
`SetContentType`. An empty offer, or one whose type or subtype is `*`, panics with a `rice: ` message:
the server must name what it produces, and a wildcard offer is a programming error.

### D2 — matching follows RFC 9110 §12.5.1

- **No `Accept`**, or only empty `Accept` lines: the first offer. The client accepts anything.
- **Every `Accept` line is read** (`PeekAll`, finding 1) and treated as one comma-separated list.
- **For each offer, the most specific matching range decides its quality.** `type/subtype` beats
  `type/*`, which beats `*/*`. Among ranges of equal specificity the first in the header wins.
- **`q=0` means not acceptable.** `text/*, text/html;q=0` excludes `text/html` even though `text/*`
  would give it 1.
- **The highest quality wins; a tie goes to the earlier offer.** The client ranks by `q`; the server
  ranks among what the client ranks equally.
- **No acceptable offer**, all at `q=0` or unmatched: `""`.

### D3 — parsing is lenient where RFC 9110 allows and strict where it matters

- Type and subtype compare case-insensitively (ASCII).
- Optional whitespace (space and tab) is allowed around `,`, `;` and `=`.
- `q` follows RFC 9110's `qvalue`: `0`, `0.` followed by up to three digits, `1`, or `1.` followed by
  up to three zeros. Anything else — `q=1.5`, `q=0.1234`, `q=abc`, `q=` — makes that element invalid.
  A leading-dot qvalue, `.` followed by one to three digits, is also accepted and read as `0.` and
  those digits, for Java's HttpURLConnection, which sends `q=.2`.
- An invalid element is skipped: a range that is not `type/subtype` (`text`, `/html`, `*/html`) or a
  bad `q`. The rest of the header still counts.
- Parameters other than `q` are skipped, including quoted-string values, so a `,` or `;` inside quotes
  does not split the element.
- There is no length limit of rice's own; fasthttp bounds the header size.

### D4 — one pass per offer, no state on Ctx

For each offer, `Accepts` scans the `Accept` bytes once, finds the most specific matching range and
its quality, and keeps the best offer so far. Nothing is parsed into a buffer, nothing is stored on
`Ctx`, and there is nothing for `reset` to clear. The cost is offers × ranges; a real request has two
to four offers and fewer than ten ranges. Qualities are compared as integers in thousandths, so no
float parsing is involved.

### D5 — `Accepts` adds `Vary: Accept`, once

Every call adds `Vary: Accept` to the response, whether or not the request carried `Accept`: the
response depends on the header either way, and a cache that does not know that serves one client's
format to another. Before adding, `Accepts` scans every existing `Vary` line (finding 2),
comma-separated and case-insensitive, and adds nothing when one already lists `Accept` or `*`. It uses
`Add`, so a `Vary: Origin` from CORS survives.

This is a side effect on something that reads like an accessor. It is deliberate — the owner chose
it over leaving `Vary` to the handler, because forgetting it is silent and wrong — and the doc comment
says so in its first paragraph. ADR-0018 records it.

### D6 — 406 is the handler's decision, and goes through the funnel

`Accepts` never writes a status. `ErrNotAcceptable` is a shared `*HTTPError` like `ErrNotFound`, so
returning it costs nothing and `DefaultErrorHandler`'s type switch answers it without allocating. The
funnel does not reset headers, so `Vary: Accept` stays on the 406, which is correct: the 406 varies by
`Accept` too.

### D7 — the ricedebug walker learns variadic methods

`TestEveryCtxMethodPanicsAfterRelease` uses `CallSlice` when the method is variadic (finding 3), so
`Accepts` on a released `Ctx` reports the use-after-release panic. Without this the test fails for the
wrong reason.

## Consequences to record

- ADR-0018: tie-breaking by server order, and a read-like method that writes `Vary`.
- `Accepts` returns a borrowed string only in the sense that it returns one of the caller's own
  arguments; it holds no reference into the request, so the borrow contract adds nothing.
- `05-performance-model.md`: `Accepts` joins the zero-allocation `Ctx` methods, with its budgets.

## Components

| File | Contents |
|---|---|
| `ctx_accept.go` | `Accepts`, the per-offer scan, the `qvalue` parser, the `Vary` check |
| `errors.go` | `ErrNotAcceptable` |
| `ctx_accept_test.go` | table tests, `Vary` tests, panics, funnel test, a fuzz test |
| `ricedebug_test.go` | `CallSlice` for variadic methods |
| `alloc_test.go` | the budgets |
| `bench/rice_bench_test.go` | `BenchmarkCtxAccepts` |

## Allocation budget

Zero, on a warm response buffer, for: Chrome's real `Accept` header with two offers; no `Accept`
header; two calls in one request (the second adds no `Vary`); a handler returning `ErrNotAcceptable`
through the funnel. Measured with and without `-race`. The variadic slice of constant offers must stay
on the caller's stack; `-gcflags=-m` confirms that `Accepts` does not let `offers` escape.

## Testing

- **Matching table:** no header; empty header; single exact match; `*/*`; `type/*`; specificity
  overriding a broader range in both directions; `q=0` exclusion through a wildcard; a quality tie
  going to the earlier offer; the client's higher `q` beating server order; case-insensitivity; offers
  with parameters; several `Accept` lines; whitespace and tabs around separators; quoted parameter
  values containing `,` and `;`.
- **Invalid input:** `q=1.5`, `q=0.1234`, `q=abc`, `q=`, `1.001`; ranges `text`, `/html`, `*/html`,
  empty elements (`,,`); each skipped without discarding the valid elements around it.
- **`Vary`:** one call; two calls; an existing `Vary: Origin`; an existing `Vary: accept`; an existing
  `Vary: Origin, Accept`; an existing `Vary: *`.
- **Panics:** empty offer, `*/*`, `text/*`, an offer without `/`.
- **Funnel:** a handler returning `ErrNotAcceptable` answers 406 with `Vary: Accept`.
- **Fuzz:** `FuzzAccepts` over the header value: never panics, and the result is one of the offers or
  `""`.
- **ricedebug:** `Accepts` on a released `Ctx` panics with the use-after-release panic.

## Documentation

- ADR-0018.
- `03-core-concepts.md`: `Accepts` and `ErrNotAcceptable` in the `Ctx` read side.
- `05-performance-model.md`: the budgets.
- `04-roadmap.md`: content negotiation moves from *Explicitly deferred* to *Done after M8*.
- `docs/progress.md`: a new entry.

## Exit criteria

`make test`, `make test-debug` and `make lint` pass; every row of D2 and D3 has a test; the budgets pass
at 0 with and without `-race`; the fuzz target runs for at least a minute without a failure; coverage
of the root package does not fall.

## Open questions

None.
