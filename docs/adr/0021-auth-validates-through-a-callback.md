# ADR-0021 — Authentication validates through a callback and hands a typed identity to the handler

Status: Accepted
Date: 2026-09-27

## Context

rice had no authentication. Applications keep credentials in their own stores — a database of users
and hashed passwords, a table of hashed API keys, a secret manager — and a framework that owned the
store would either duplicate one of them or force its own. rice already had a way to hand a value from
a middleware to a handler with its real type: `rice.Key[T]` (ADR-0016). And a credential check can
fail two different ways: the credentials are wrong, or the store could not be asked. Answering the
second as a 401 would tell a client its password is wrong while the database is down, and would hide
an outage in authentication failure counts.

## Decision

The `middleware` package gains two generic middleware, each configured by a struct:

```go
BasicAuth[T any](BasicAuthConfig[T]{Key, Validate func(ctx, user, password string) (T, bool, error), Realm})
KeyAuth[T any](KeyAuthConfig[T]{Key, Validate func(ctx, key string) (T, bool, error), Header})
```

- **Parsing is rice's; the verdict is the application's.** BasicAuth reads `Authorization: Basic`,
  the scheme compared case-insensitively, decodes standard base64 and splits at the first colon, so a
  password may contain one. KeyAuth reads `Authorization: Bearer <key>` by default, or the whole
  trimmed value of a named header such as `X-API-Key`. A missing header, another scheme, invalid
  base64, no colon or an empty key is a 401 without calling `Validate`. The key is never read from
  the query string.
- **Three outcomes.** `Validate` receives `c.Context()` — a Timeout's deadline and an otelrice span
  reach it — and copied strings it may keep. `ok` stores the identity under `Key` and continues.
  `false` answers 401 through the funnel with a shared `*rice.HTTPError` and a challenge:
  `Basic realm="…", charset="UTF-8"`, `Bearer`, or none for a custom header. A non-nil error is
  returned as it is, whatever `ok` says: the funnel answers 500 and the error handler sees the cause.
  A malformed header and rejected credentials produce the same 401.
- **Placement.** Inside CORS, which answers a preflight without calling `next`, so a preflight — which
  carries no credentials — never reaches authentication, and a 401 carries the CORS headers. Inside
  Logger and otelrice, so 401s are logged and traced. On a group to protect part of an application;
  with `app.Use` it also runs on a route miss (ADR-0012), so an unauthenticated request for a path that
  does not exist gets 401, not 404.
- **What rice does not do.** It compares no secrets — `Validate` does, with
  `crypto/subtle.ConstantTimeCompare` or a hash lookup — and logs nothing. Basic credentials are
  base64, readable on the wire, so BasicAuth belongs behind TLS. Guessing is not throttled here.

A nil `Validate`, a zero `Key`, a `Realm` with a quote, backslash or control byte, and a `Header` that
is not a header name panic at construction.

## Alternatives

**A string identity.** `Validate` returns `(string, bool)` and the handler reads
`middleware.AuthUser(c)`. Simpler, but an application that needs its user record looks it up a second
time, and without an error branch a store outage becomes a 401.

**A callback receiving `*rice.Ctx` and the raw bytes.** The most flexible, and a trap: the bytes are
borrowed (ADR-0005), so a callback that keeps them keeps memory another request will reuse, and a
callback holding `c` can write a response of its own that the middleware then contradicts.

**Static credential lists in the config.** A map of users to passwords or a list of keys, compared by
rice in constant time. Rejected by the owner: rotating a key would need a restart, and a callback can
wrap a static list in three lines.

**Reading the key from the query string.** Convenient for links and webhooks, and the key then lands in
access logs, proxy logs, `Referer` headers and browser history.

**A skipper option.** Rejected: groups already decide which routes a middleware wraps, and a second
mechanism would be a second place to look when a route is unexpectedly open.

## Consequences

**Costs, pinned exactly with and without `-race`:** BasicAuth accepting a request, 2 allocations (the
decoded credentials and the strings handed to `Validate`); KeyAuth accepting a Bearer request, 1 (the
key string); a 401, 0.

**The application owns secret handling.** A `Validate` that compares with `==` leaks timing, and one
that logs its arguments leaks credentials; the doc comments say so, and rice cannot check it.

**Brute force is the next design.** Rate limiting is separate middleware with state of its own.
