# BasicAuth and KeyAuth middleware — Design

**Status:** approved, not yet implemented
**Date:** 2026-09-27
**Milestone:** none. Work after the roadmap; the first of two security middleware designs (rate limiting
is the second, separately)
**Decision record:** ADR-0021 (new), "Authentication validates through a callback and hands a typed
identity to the handler"
**Depends on:** typed store keys ([ADR-0016](../../adr/0016-typed-store-keys.md)), the error funnel
([ADR-0002](../../adr/0002-handler-returns-error.md)), CORS's preflight
([ADR-0015](../../adr/0015-cors-states-a-policy.md)), misses running application middleware
([ADR-0012](../../adr/0012-application-middleware-runs-on-route-misses.md))

## Goal

Let a rice service require HTTP Basic credentials or an API key, check them with the application's own
logic, and give the handler the authenticated identity with its real type.

## Question this design answers

How does a middleware authenticate without owning the credential store, keep "wrong credentials" (401)
apart from "the store is down" (500), and hand the handler a typed identity without a shared string
key?

## What the design is for

The owner chose a callback for validation — the application checks against its database, cache or
secret manager — and chose that KeyAuth reads `Authorization: Bearer <key>` by default, a configurable
header instead, and never the query string. Rate limiting, which is what stops brute force, is a
separate design.

## What exists

Read against rice at `45b51c9`.

- `middleware/` holds Logger, RequestID, RealIP, CORS, Timeout and Recover. CORS takes a config struct
  and panics at construction on a configuration that cannot work (`CORSConfig`, `compileCORS`).
- `middleware.RequestID` stores its value under a `rice.Key[string]` and exposes `RequestIDFrom(c)`
  (ADR-0016).
- CORS answers a preflight with `c.NoContent(204)` and never calls `next` (`middleware/cors.go`, the
  `isPreflight` branch), so a middleware installed inside CORS never sees a preflight.
- The funnel does not reset response headers, so a header set before returning an `*HTTPError`
  reaches the client (ADR-0013's context; CORS relies on it).
- `c.Context()` carries a Timeout deadline and an otelrice span when those are installed.

## Non-goals

- Storing, hashing or comparing credentials: the callback does it.
- JWT, OAuth, sessions, cookies: a JWT integration would need a dependency and belongs in its own
  module.
- Rate limiting and lockout: the next design.
- A "skipper" option: groups already scope middleware (`app.Group("/api", auth)`).
- Reading a key from the query string or the body.
- Authorization (roles, permissions): the handler or a middleware of the application's reads the
  identity and decides.

## Decisions

### D1 — two generic middleware with config structs

```go
type BasicAuthConfig[T any] struct {
	Key      rice.Key[T]
	Validate func(ctx context.Context, user, password string) (T, bool, error)
	Realm    string // default "Restricted"
}

func BasicAuth[T any](cfg BasicAuthConfig[T]) rice.Middleware

type KeyAuthConfig[T any] struct {
	Key      rice.Key[T]
	Validate func(ctx context.Context, key string) (T, bool, error)
	Header   string // "" → Authorization: Bearer <key>; otherwise the whole value of this header
}

func KeyAuth[T any](cfg KeyAuthConfig[T]) rice.Middleware
```

```go
var userKey = rice.NewKey[*User]("user")

api := app.Group("/api", middleware.KeyAuth(middleware.KeyAuthConfig[*User]{
	Key: userKey,
	Validate: func(ctx context.Context, key string) (*User, bool, error) {
		u, err := users.ByKeyHash(ctx, sha256.Sum256([]byte(key)))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return u, err == nil, err
	},
}))

api.GET("/me", func(c *rice.Ctx) error {
	u, _ := userKey.Get(c)
	return c.JSON(200, u)
})
```

Construction panics, with a `rice: middleware.BasicAuth:` or `rice: middleware.KeyAuth:` prefix, on a
nil `Validate`, a zero `Key`, a `Realm` containing `"`, `\` or a control byte, and a `Header` that is not
an RFC 9110 token. `Realm` "" means `Restricted`.

### D2 — parsing

- **Basic.** The `Authorization` header's scheme is `Basic`, compared case-insensitively, followed by one
  or more spaces and the credentials. They are standard base64 (with padding); decoded, they split at the
  first `:` — the user is before it, and the password after it may itself contain `:`. An empty user is
  allowed to reach `Validate` (the callback decides). A missing header, another scheme, invalid base64
  or no `:` is a 401 without calling `Validate`.
- **Bearer (KeyAuth, `Header` "").** The scheme is `Bearer`, case-insensitive, then one or more spaces and
  a non-empty token, with trailing spaces trimmed. Anything else is a 401 without calling `Validate`.
- **Custom header (KeyAuth, `Header` set).** The header's value with leading and trailing spaces and tabs
  trimmed; empty is a 401 without calling `Validate`.
- Every value handed to `Validate` is a copied `string`, so the callback may keep it.

### D3 — the three outcomes

`Validate` is called with `c.Context()`, so a Timeout deadline and an otelrice span reach it.

- `ok` true, `err` nil: `cfg.Key.Set(c, identity)`, then the chain runs.
- `ok` false, `err` nil: 401. `WWW-Authenticate` is set before returning: `Basic realm="<Realm>",
  charset="UTF-8"` for BasicAuth, `Bearer` for KeyAuth with the default header, and nothing for a custom
  header (no standard challenge exists for it). The error returned is a shared `*rice.HTTPError{Code:
  401, Message: "Unauthorized"}`, so a 401 allocates nothing of its own.
- `err` non-nil: the error is returned as it is, whatever `ok` says. The funnel answers 500 and
  DefaultErrorHandler logs it; a custom ErrorHandler decides. A database outage never looks like a wrong
  password.

A 401 from a parse failure is the same response as one from `Validate` returning false, so a client
cannot tell a malformed header from wrong credentials.

### D4 — placement and scope

```go
app.Use(
	otelrice.Middleware(),
	middleware.Logger(l),
	middleware.Recover(),
	middleware.RealIP(1),
	middleware.CORS(cfg),
)
api := app.Group("/api", middleware.KeyAuth(keyCfg))
```

Inside CORS, so a preflight — which carries no credentials — is answered by CORS and never reaches the
auth middleware, and so a 401 carries the CORS headers and the browser reports the 401. Inside Logger
and otelrice, so 401s are logged and traced. On a group to protect part of an application. Installed
with `app.Use`, it also runs on a route miss (ADR-0012), so an unauthenticated request for a path that
does not exist gets 401, not 404, and learns nothing about which routes exist.

### D5 — what rice does not do, stated in the doc comments

- It never compares secrets: `Validate` does, and the doc comment shows `subtle.ConstantTimeCompare`
  for a static secret and a hash lookup for stored keys and passwords (bcrypt or argon2 for passwords).
- It logs nothing. Logger records the path, never headers; otelrice records no `Authorization`. The
  callback must not log what it receives.
- Basic credentials are base64, readable by anyone on the wire: use it only behind TLS.
- Brute force is not throttled here; see the rate limiting middleware.

## Consequences to record

- ADR-0021: callback plus `Key[T]`; 401 versus 500; header only; no skipper; D4's placement.
- `05-performance-model.md` (*Opt-in packages*): the measured budgets.

## Components

| File | Contents |
|---|---|
| `middleware/auth.go` | the shared 401 error, the challenge helpers, config validation helpers |
| `middleware/basicauth.go` | `BasicAuthConfig`, `BasicAuth`, Basic parsing |
| `middleware/keyauth.go` | `KeyAuthConfig`, `KeyAuth`, Bearer and custom-header parsing |
| `middleware/basicauth_test.go`, `middleware/keyauth_test.go` | behaviour tests |
| `middleware/alloc_test.go` | the budgets |

## Allocation budget

Measured with and without `-race` and pinned: BasicAuth accepting a request (identity a pointer);
KeyAuth accepting a Bearer request (identity a pointer); a 401 for a missing header (expected 0).

## Testing

- **Basic parsing:** valid; `basic`/`BASIC`; several spaces after the scheme; a password containing `:`;
  an empty user; an empty password; invalid base64; no `:`; missing header; empty header; `Bearer` scheme.
- **Bearer parsing:** valid; `bearer`; empty token; trailing spaces; `Basic` scheme; missing header.
- **Custom header:** valid; surrounding spaces and tabs; empty; the `Authorization` header present but the
  custom header absent → 401.
- **Outcomes:** ok → the handler reads the identity through `Key.Get`; false → 401, the handler does not
  run, `WWW-Authenticate` is exactly as D3 says per mode, and the body is "Unauthorized"; error → 500 and
  the handler does not run; a custom ErrorHandler sees the callback's error; an error with `ok` true is
  still a 500.
- **Context:** `Validate` sees the deadline of a Timeout installed outside the auth middleware.
- **Placement:** CORS outside → an OPTIONS preflight with an allowed Origin and no credentials answers 204
  with the CORS headers; a 401 carries `Access-Control-Allow-Origin`; auth on a group leaves other routes
  open; auth with `app.Use` answers a miss with 401.
- **Construction panics:** nil Validate, zero Key, Realm with `"`, Header with a space.
- **Budgets:** as above.

## Documentation

ADR-0021; `03-core-concepts.md` (the middleware list); `05-performance-model.md`; `04-roadmap.md`
(*Done after M8*); `docs/progress.md`; README (a KeyAuth example with a hashed-key lookup).

## Exit criteria

`make test`, `make test-debug` and `make lint` pass; every D2 and D3 row has a test; the 401 path's budget
is 0; core's `go.mod` is unchanged.

## Open questions

None.
