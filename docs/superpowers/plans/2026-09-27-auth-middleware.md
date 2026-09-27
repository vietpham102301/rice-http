# BasicAuth and KeyAuth Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `middleware.BasicAuth[T]` and `middleware.KeyAuth[T]` authenticate with the application's own callback, answer 401 or 500 through the funnel, and hand the handler a typed identity through a `rice.Key[T]`.

**Architecture:** Three files in `middleware/`: shared helpers (the shared 401, the challenge, scheme and token parsing, config checks), `BasicAuth`, `KeyAuth`. Each middleware checks its config at construction, parses the credentials without calling the callback when they are malformed, calls `Validate(c.Context(), …)` with copied strings, and either stores the identity and continues, answers 401 with the right `WWW-Authenticate`, or returns the callback's error to the funnel.

**Tech Stack:** Go 1.25, `encoding/base64`, fasthttp v1.73.0. No new dependency.

**Spec:** [docs/superpowers/specs/2026-09-27-auth-middleware-design.md](../specs/2026-09-27-auth-middleware-design.md)

## Global Constraints

- **Branch:** `auth-middleware`, already created, holding the spec commit. Do not commit to `main`.
- **Exported surface added:** exactly `BasicAuthConfig[T]`, `BasicAuth[T]`, `KeyAuthConfig[T]`, `KeyAuth[T]` in package `middleware`. Nothing else exported changes, in `middleware` or in core.
- **Core `go.mod`/`go.sum` and `otelrice/` must not change.**
- **Panics carry the prefix `rice: middleware.BasicAuth:` or `rice: middleware.KeyAuth:`**: nil `Validate`, zero `Key`, a `Realm` with `"`, `\` or a control byte, a `Header` that is not an RFC 9110 token.
- **401 body is `Unauthorized`**, from one shared `*rice.HTTPError`; `WWW-Authenticate` is `Basic realm="<Realm>", charset="UTF-8"` (default realm `Restricted`), `Bearer` for KeyAuth's default header, and absent for a custom header.
- **An error from `Validate` is returned to the funnel whatever `ok` says**; it never carries a challenge.
- **Malformed credentials never reach `Validate`**; the key is never read from the query string.
- **Budgets:** BasicAuth accept 2, KeyAuth accept 1, both 401s 0 — exact pins, as middleware/alloc_test.go's others are.
- **Run `make test`, `make test-debug` and `make lint` before each commit; never commit with a failing suite.** Commit messages: subject, blank line, then `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` on its own line.

## Review Focus

Inputs the spec implies but does not enumerate. Each has a test in the task that owns the code.

1. **A password containing a colon** — split at the first colon only. (Task 1)
2. **A database outage in `Validate`** — must answer 500 with no challenge, never 401. (Task 1)
3. **A browser's CORS preflight to a protected group** — must pass with 204 and no credentials. (Task 2)
4. **An unauthenticated request for a path that does not exist, with auth on `app.Use`** — 401, not 404. (Task 2)
5. **A custom header configured while the client sends only `Authorization`** — 401, not accepted by fallback. (Task 2)

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `middleware/auth.go` (create) | `errUnauthorized`, `unauthorized`, `cutScheme`, `trimOWS`, `equalFoldASCII`, `isToken`, `validRealm` | 1 |
| `middleware/basicauth.go` (create) | `BasicAuthConfig`, `BasicAuth` | 1 |
| `middleware/basicauth_test.go` (create) | BasicAuth tests; the shared test type `user`, `userKey`, `basic` | 1 |
| `middleware/keyauth.go` (create) | `KeyAuthConfig`, `KeyAuth` | 2 |
| `middleware/keyauth_test.go` (create) | KeyAuth tests, placement tests | 2 |
| `middleware/alloc_test.go` (modify) | three budgets | 3 |
| `docs/adr/0021-auth-validates-through-a-callback.md` (create), `docs/adr/README.md`, `docs/03-core-concepts.md`, `docs/05-performance-model.md`, `docs/04-roadmap.md`, `docs/progress.md`, `README.md` | documentation | 4 |

---

### Task 1: Shared helpers and BasicAuth

**Files:**
- Create: `middleware/auth.go`, `middleware/basicauth.go`, `middleware/basicauth_test.go`

**Interfaces:**
- Consumes: `rice.Key[T]` (comparable; `Set`), `rice.HTTPError`, `(*rice.Ctx).Header`, `.SetHeader`, `.Context`; test helpers `newRequest(method, uri string, headers map[string]string) *fasthttp.RequestCtx` and `hdr(fctx, name) string` (middleware/alloc_test.go, middleware/cors_test.go), `middleware.Timeout`.
- Produces: `type BasicAuthConfig[T any] struct{ Key rice.Key[T]; Validate func(ctx context.Context, user, password string) (T, bool, error); Realm string }`, `func BasicAuth[T any](cfg BasicAuthConfig[T]) rice.Middleware`; unexported `errUnauthorized`, `unauthorized(c *rice.Ctx, challenge string) error`, `cutScheme(v []byte, scheme string) ([]byte, bool)`, `trimOWS([]byte) []byte`, `equalFoldASCII([]byte, string) bool`, `isToken(string) bool`, `validRealm(string) bool`. Test declarations `type user struct{ name string }`, `var userKey = rice.NewKey[*user]("test/user")`, `func basic(userpass string) string`.

- [ ] **Step 1: Confirm the branch**

Run: `git branch --show-current`
Expected: `auth-middleware`

- [ ] **Step 2: Write the failing tests**

Create `middleware/basicauth_test.go`:

```go
package middleware_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

type user struct{ name string }

var userKey = rice.NewKey[*user]("test/user")

func basic(userpass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(userpass))
}

// basicApp protects GET /me, which answers the stored user's name. seen records
// what Validate received; calls counts how often it ran.
type basicApp struct {
	app   *rice.App
	seen  [2]string
	calls int
}

func newBasicApp(t *testing.T, result func(u, p string) (*user, bool, error), opts ...rice.Option) *basicApp {
	t.Helper()
	b := &basicApp{app: rice.New(opts...)}
	b.app.Use(middleware.BasicAuth(middleware.BasicAuthConfig[*user]{
		Key: userKey,
		Validate: func(ctx context.Context, u, p string) (*user, bool, error) {
			b.calls++
			b.seen = [2]string{u, p}
			return result(u, p)
		},
	}))
	b.app.GET("/me", func(c *rice.Ctx) error {
		u, ok := userKey.Get(c)
		if !ok {
			return errors.New("no user stored")
		}
		return c.String(200, u.name)
	})
	return b
}

func acceptAll(u, p string) (*user, bool, error) { return &user{name: u}, true, nil }

func TestBasicAuthParsesCredentials(t *testing.T) {
	cases := []struct {
		name, header   string
		user, password string
	}{
		{"plain", basic("ann:secret"), "ann", "secret"},
		{"lower-case scheme", "basic " + base64.StdEncoding.EncodeToString([]byte("ann:secret")), "ann", "secret"},
		{"upper-case scheme", "BASIC " + base64.StdEncoding.EncodeToString([]byte("ann:secret")), "ann", "secret"},
		{"several spaces", "Basic    " + base64.StdEncoding.EncodeToString([]byte("ann:secret")), "ann", "secret"},
		{"colon in the password", basic("ann:se:cr:et"), "ann", "se:cr:et"},
		{"empty user", basic(":secret"), "", "secret"},
		{"empty password", basic("ann:"), "ann", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBasicApp(t, acceptAll)
			fctx := newRequest("GET", "/me", map[string]string{"Authorization": tc.header})
			b.app.FasthttpHandler()(fctx)
			if got := fctx.Response.StatusCode(); got != 200 {
				t.Fatalf("status %d, want 200", got)
			}
			if b.seen != [2]string{tc.user, tc.password} {
				t.Errorf("Validate saw %q, want %q", b.seen, [2]string{tc.user, tc.password})
			}
			if got := string(fctx.Response.Body()); got != tc.user {
				t.Errorf("handler read user %q, want %q", got, tc.user)
			}
		})
	}
}

func TestBasicAuthRejectsMalformedCredentialsWithoutCallingValidate(t *testing.T) {
	cases := map[string]string{
		"missing":        "",
		"empty":          "Basic ",
		"other scheme":   "Bearer abc",
		"no space":       "Basic" + base64.StdEncoding.EncodeToString([]byte("ann:secret")),
		"invalid base64": "Basic !!!not-base64!!!",
		"no colon":       basic("annsecret"),
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			b := newBasicApp(t, acceptAll)
			headers := map[string]string{}
			if header != "" {
				headers["Authorization"] = header
			}
			fctx := newRequest("GET", "/me", headers)
			b.app.FasthttpHandler()(fctx)
			if got := fctx.Response.StatusCode(); got != 401 {
				t.Errorf("status %d, want 401", got)
			}
			if b.calls != 0 {
				t.Errorf("Validate ran %d times on malformed credentials", b.calls)
			}
			if got := hdr(fctx, "WWW-Authenticate"); got != `Basic realm="Restricted", charset="UTF-8"` {
				t.Errorf("WWW-Authenticate %q", got)
			}
		})
	}
}

func TestBasicAuthOutcomes(t *testing.T) {
	t.Run("rejected", func(t *testing.T) {
		b := newBasicApp(t, func(u, p string) (*user, bool, error) { return nil, false, nil })
		fctx := newRequest("GET", "/me", map[string]string{"Authorization": basic("ann:wrong")})
		b.app.FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 401 || string(fctx.Response.Body()) != "Unauthorized" {
			t.Errorf("got %d %q, want 401 Unauthorized", fctx.Response.StatusCode(), fctx.Response.Body())
		}
		if hdr(fctx, "WWW-Authenticate") == "" {
			t.Error("no challenge on a rejected request")
		}
	})
	t.Run("store error is a 500", func(t *testing.T) {
		b := newBasicApp(t, func(u, p string) (*user, bool, error) { return nil, false, errors.New("db down") })
		fctx := newRequest("GET", "/me", map[string]string{"Authorization": basic("ann:secret")})
		b.app.FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 500 {
			t.Errorf("status %d, want 500", got)
		}
		if hdr(fctx, "WWW-Authenticate") != "" {
			t.Error("a store failure carried a challenge, as if the credentials were wrong")
		}
	})
	t.Run("error wins over ok", func(t *testing.T) {
		b := newBasicApp(t, func(u, p string) (*user, bool, error) { return &user{}, true, errors.New("partial") })
		fctx := newRequest("GET", "/me", map[string]string{"Authorization": basic("ann:secret")})
		b.app.FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 500 {
			t.Errorf("status %d, want 500", got)
		}
	})
	t.Run("a custom ErrorHandler sees the callback's error", func(t *testing.T) {
		cause := errors.New("db down")
		var seen error
		b := newBasicApp(t, func(u, p string) (*user, bool, error) { return nil, false, cause },
			rice.WithErrorHandler(func(c *rice.Ctx, err error) { seen = err; _ = c.String(503, "later") }))
		fctx := newRequest("GET", "/me", map[string]string{"Authorization": basic("ann:secret")})
		b.app.FasthttpHandler()(fctx)
		if !errors.Is(seen, cause) || fctx.Response.StatusCode() != 503 {
			t.Errorf("ErrorHandler saw %v and answered %d", seen, fctx.Response.StatusCode())
		}
	})
}

func TestBasicAuthRealm(t *testing.T) {
	app := rice.New()
	app.Use(middleware.BasicAuth(middleware.BasicAuthConfig[*user]{Key: userKey, Validate: func(context.Context, string, string) (*user, bool, error) { return nil, false, nil }, Realm: "admin area"}))
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	fctx := newRequest("GET", "/x", nil)
	app.FasthttpHandler()(fctx)
	if got := hdr(fctx, "WWW-Authenticate"); got != `Basic realm="admin area", charset="UTF-8"` {
		t.Errorf("WWW-Authenticate %q", got)
	}
}

func TestBasicAuthPassesTheRequestContext(t *testing.T) {
	var hadDeadline bool
	app := rice.New()
	app.Use(
		middleware.Timeout(time.Minute),
		middleware.BasicAuth(middleware.BasicAuthConfig[*user]{Key: userKey, Validate: func(ctx context.Context, u, p string) (*user, bool, error) {
			_, hadDeadline = ctx.Deadline()
			return &user{}, true, nil
		}}),
	)
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	app.FasthttpHandler()(newRequest("GET", "/x", map[string]string{"Authorization": basic("a:b")}))
	if !hadDeadline {
		t.Error("Validate did not receive the Timeout's deadline")
	}
}

func TestBasicAuthPanicsOnAConfigurationThatCannotWork(t *testing.T) {
	ok := func(context.Context, string, string) (*user, bool, error) { return nil, false, nil }
	cases := map[string]middleware.BasicAuthConfig[*user]{
		"nil Validate":     {Key: userKey},
		"zero Key":         {Validate: ok},
		"quote in Realm":   {Key: userKey, Validate: ok, Realm: `a"b`},
		"backslash":        {Key: userKey, Validate: ok, Realm: `a\b`},
		"control in Realm": {Key: userKey, Validate: ok, Realm: "a\nb"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.HasPrefix(msg, "rice: middleware.BasicAuth:") {
					t.Errorf("panic %q", msg)
				}
			}()
			middleware.BasicAuth(cfg)
		})
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test -count=1 -run BasicAuth ./middleware/`
Expected: FAIL to compile, `undefined: middleware.BasicAuth` and `undefined: middleware.BasicAuthConfig`.

- [ ] **Step 4: Write the implementation**

Create `middleware/auth.go`:

```go
package middleware

import (
	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
)

// errUnauthorized is the 401 BasicAuth and KeyAuth answer with. It is shared,
// like rice.ErrNotFound, so a rejected request allocates nothing of its own;
// never mutate it.
var errUnauthorized = &rice.HTTPError{Code: fasthttp.StatusUnauthorized, Message: "Unauthorized"}

// unauthorized sets the challenge, when there is one, and returns the 401. The
// funnel does not reset headers, so the challenge reaches the client.
func unauthorized(c *rice.Ctx, challenge string) error {
	if challenge != "" {
		c.SetHeader("WWW-Authenticate", challenge)
	}
	return errUnauthorized
}

// cutScheme reports whether v is scheme, compared case-insensitively, followed
// by at least one space, and returns what follows with the spaces removed.
func cutScheme(v []byte, scheme string) ([]byte, bool) {
	n := len(scheme)
	if len(v) <= n || v[n] != ' ' || !equalFoldASCII(v[:n], scheme) {
		return nil, false
	}
	rest := v[n:]
	for len(rest) > 0 && rest[0] == ' ' {
		rest = rest[1:]
	}
	return rest, true
}

// trimOWS trims spaces and tabs from both ends.
func trimOWS(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

// equalFoldASCII compares b and s, folding ASCII letters only.
func equalFoldASCII(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := 0; i < len(b); i++ {
		x, y := b[i], s[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// isToken reports whether s is a non-empty RFC 9110 token, the grammar of a
// header name.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '!', c == '#', c == '$', c == '%', c == '&', c == '\'', c == '*',
			c == '+', c == '-', c == '.', c == '^', c == '_', c == '`', c == '|', c == '~':
		default:
			return false
		}
	}
	return true
}

// validRealm reports whether r can sit inside a quoted-string without escaping:
// no quote, no backslash, no control byte.
func validRealm(r string) bool {
	for i := 0; i < len(r); i++ {
		c := r[i]
		if c == '"' || c == '\\' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}
```

Create `middleware/basicauth.go`:

```go
package middleware

import (
	"bytes"
	"context"
	"encoding/base64"

	rice "github.com/vietpham102301/rice-http"
)

// BasicAuthConfig is what BasicAuth needs. Key and Validate are required.
//
// Validate receives the request's context — carrying a Timeout's deadline or
// an otelrice span when those are installed — and the user and password, as
// strings the callback may keep. It returns the identity to store under Key and
// whether the credentials are good; a non-nil error means the check itself
// failed (the store is down, say) and answers 500, whatever ok says.
//
// Validate compares secrets, not rice: compare a password against a stored
// bcrypt or argon2 hash, or a fixed secret with crypto/subtle's
// ConstantTimeCompare, never with ==. It must not log what it receives.
//
// Realm names the protection space in the WWW-Authenticate challenge; ""
// means "Restricted". It may not contain a quote, a backslash or a control
// byte.
type BasicAuthConfig[T any] struct {
	Key      rice.Key[T]
	Validate func(ctx context.Context, user, password string) (T, bool, error)
	Realm    string
}

// BasicAuth requires HTTP Basic credentials, checks them with cfg.Validate,
// and stores the identity it returns under cfg.Key for the rest of the chain:
//
//	u, _ := userKey.Get(c)
//
// A request without credentials, with another scheme, with credentials that
// are not base64 or have no colon, or that Validate rejects, gets 401 with
// WWW-Authenticate: Basic realm="…", charset="UTF-8"; all of these look the
// same to the client. An error from Validate goes to the error funnel, which
// answers 500 unless a custom ErrorHandler decides otherwise.
//
// Basic credentials are base64, which anyone on the wire can read: use it
// only behind TLS. It does not throttle guessing; see the rate limiting
// middleware.
//
// Install it inside CORS, so a preflight — which carries no credentials — is
// answered before it, and inside Logger and otelrice, so 401s are logged and
// traced. Install it on a group to protect part of an application; with
// app.Use it also answers a route miss with 401 (ADR-0012), so an
// unauthenticated client learns nothing about which routes exist. See
// ADR-0021.
//
// A nil Validate, a zero Key or an invalid Realm panics.
func BasicAuth[T any](cfg BasicAuthConfig[T]) rice.Middleware {
	if cfg.Validate == nil {
		panic("rice: middleware.BasicAuth: Validate is nil")
	}
	if cfg.Key == (rice.Key[T]{}) {
		panic("rice: middleware.BasicAuth: Key is the zero Key; make one with rice.NewKey")
	}
	realm := cfg.Realm
	if realm == "" {
		realm = "Restricted"
	}
	if !validRealm(realm) {
		panic("rice: middleware.BasicAuth: Realm may not contain a quote, a backslash or a control byte")
	}
	challenge := `Basic realm="` + realm + `", charset="UTF-8"`
	validate, key := cfg.Validate, cfg.Key

	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			enc, ok := cutScheme(c.Header("Authorization"), "Basic")
			if !ok {
				return unauthorized(c, challenge)
			}
			enc = trimOWS(enc)
			dec := make([]byte, base64.StdEncoding.DecodedLen(len(enc)))
			n, err := base64.StdEncoding.Decode(dec, enc)
			if err != nil {
				return unauthorized(c, challenge)
			}
			dec = dec[:n]
			i := bytes.IndexByte(dec, ':')
			if i < 0 {
				return unauthorized(c, challenge)
			}

			id, ok, err := validate(c.Context(), string(dec[:i]), string(dec[i+1:]))
			if err != nil {
				return err
			}
			if !ok {
				return unauthorized(c, challenge)
			}
			key.Set(c, id)
			return next(c)
		}
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race -count=1 -run BasicAuth ./middleware/`
Expected: PASS.

- [ ] **Step 6: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add middleware/auth.go middleware/basicauth.go middleware/basicauth_test.go
git commit -m "middleware: BasicAuth checks credentials with the application's callback

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: KeyAuth

**Files:**
- Create: `middleware/keyauth.go`, `middleware/keyauth_test.go`

**Interfaces:**
- Consumes: from Task 1 `unauthorized`, `cutScheme`, `trimOWS`, `isToken`, and the test declarations `user`, `userKey`; `middleware.CORS`, `middleware.CORSConfig`; `newRequest`, `hdr`.
- Produces: `type KeyAuthConfig[T any] struct{ Key rice.Key[T]; Validate func(ctx context.Context, key string) (T, bool, error); Header string }`, `func KeyAuth[T any](cfg KeyAuthConfig[T]) rice.Middleware`.

- [ ] **Step 1: Write the failing tests**

Create `middleware/keyauth_test.go`:

```go
package middleware_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// keyApp protects GET /me with KeyAuth reading header ("" for Bearer); the
// callback accepts only "k1" and records what it saw.
func keyApp(t *testing.T, header string, seen *string, calls *int) *rice.App {
	t.Helper()
	app := rice.New()
	app.Use(middleware.KeyAuth(middleware.KeyAuthConfig[*user]{
		Key:    userKey,
		Header: header,
		Validate: func(ctx context.Context, key string) (*user, bool, error) {
			*calls++
			*seen = key
			if key == "k1" {
				return &user{name: "svc"}, true, nil
			}
			return nil, false, nil
		},
	}))
	app.GET("/me", func(c *rice.Ctx) error {
		u, _ := userKey.Get(c)
		return c.String(200, u.name)
	})
	return app
}

func TestKeyAuthBearer(t *testing.T) {
	cases := []struct {
		name, header string
		status       int
		validated    bool
	}{
		{"valid", "Bearer k1", 200, true},
		{"lower-case scheme", "bearer k1", 200, true},
		{"trailing spaces", "Bearer k1  ", 200, true},
		{"wrong key", "Bearer nope", 401, true},
		{"empty token", "Bearer ", 401, false},
		{"only spaces", "Bearer    ", 401, false},
		{"basic scheme", "Basic k1", 401, false},
		{"missing", "", 401, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			var calls int
			app := keyApp(t, "", &seen, &calls)
			headers := map[string]string{}
			if tc.header != "" {
				headers["Authorization"] = tc.header
			}
			fctx := newRequest("GET", "/me", headers)
			app.FasthttpHandler()(fctx)
			if got := fctx.Response.StatusCode(); got != tc.status {
				t.Errorf("status %d, want %d", got, tc.status)
			}
			if (calls > 0) != tc.validated {
				t.Errorf("Validate called %d times, want called=%v", calls, tc.validated)
			}
			if tc.status == 401 && hdr(fctx, "WWW-Authenticate") != "Bearer" {
				t.Errorf("WWW-Authenticate %q, want Bearer", hdr(fctx, "WWW-Authenticate"))
			}
			if tc.status == 200 && (seen != "k1" || string(fctx.Response.Body()) != "svc") {
				t.Errorf("Validate saw %q, handler answered %q", seen, fctx.Response.Body())
			}
		})
	}
}

func TestKeyAuthCustomHeader(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"valid", map[string]string{"X-API-Key": "k1"}, 200},
		{"surrounding whitespace", map[string]string{"X-API-Key": " \tk1\t "}, 200},
		{"empty", map[string]string{"X-API-Key": "  "}, 401},
		{"only Authorization sent", map[string]string{"Authorization": "Bearer k1"}, 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			var calls int
			app := keyApp(t, "X-API-Key", &seen, &calls)
			fctx := newRequest("GET", "/me", tc.headers)
			app.FasthttpHandler()(fctx)
			if got := fctx.Response.StatusCode(); got != tc.status {
				t.Errorf("status %d, want %d", got, tc.status)
			}
			if tc.status == 401 && hdr(fctx, "WWW-Authenticate") != "" {
				t.Errorf("a custom header got a challenge %q", hdr(fctx, "WWW-Authenticate"))
			}
		})
	}
}

func TestKeyAuthStoreErrorIsA500(t *testing.T) {
	app := rice.New()
	app.Use(middleware.KeyAuth(middleware.KeyAuthConfig[*user]{Key: userKey, Validate: func(context.Context, string) (*user, bool, error) {
		return nil, false, errors.New("db down")
	}}))
	app.GET("/me", func(c *rice.Ctx) error { return nil })
	fctx := newRequest("GET", "/me", map[string]string{"Authorization": "Bearer k1"})
	app.FasthttpHandler()(fctx)
	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status %d, want 500", got)
	}
}

func TestKeyAuthPlacement(t *testing.T) {
	newApp := func() *rice.App {
		app := rice.New()
		app.Use(middleware.CORS(middleware.CORSConfig{Origins: []string{"https://app.example.com"}, AllowHeaders: []string{"Authorization"}}))
		api := app.Group("/api", middleware.KeyAuth(middleware.KeyAuthConfig[*user]{Key: userKey, Validate: func(ctx context.Context, key string) (*user, bool, error) {
			return &user{name: "svc"}, key == "k1", nil
		}}))
		api.GET("/me", func(c *rice.Ctx) error { return c.String(200, "me") })
		app.GET("/public", func(c *rice.Ctx) error { return c.String(200, "open") })
		return app
	}

	t.Run("a preflight passes without credentials", func(t *testing.T) {
		fctx := newRequest("OPTIONS", "/api/me", map[string]string{
			"Origin": "https://app.example.com", "Access-Control-Request-Method": "GET",
		})
		newApp().FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 204 {
			t.Errorf("preflight status %d, want 204", got)
		}
	})
	t.Run("a 401 carries the CORS headers", func(t *testing.T) {
		fctx := newRequest("GET", "/api/me", map[string]string{"Origin": "https://app.example.com"})
		newApp().FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 401 || hdr(fctx, "Access-Control-Allow-Origin") != "https://app.example.com" {
			t.Errorf("got %d with Allow-Origin %q", fctx.Response.StatusCode(), hdr(fctx, "Access-Control-Allow-Origin"))
		}
	})
	t.Run("routes outside the group stay open", func(t *testing.T) {
		fctx := newRequest("GET", "/public", nil)
		newApp().FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 200 {
			t.Errorf("status %d, want 200", got)
		}
	})
	t.Run("with app.Use a miss is a 401", func(t *testing.T) {
		app := rice.New()
		app.Use(middleware.KeyAuth(middleware.KeyAuthConfig[*user]{Key: userKey, Validate: func(context.Context, string) (*user, bool, error) { return nil, false, nil }}))
		app.GET("/x", func(c *rice.Ctx) error { return nil })
		fctx := newRequest("GET", "/nowhere", nil)
		app.FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 401 {
			t.Errorf("status %d, want 401: a miss must not reveal that the route is absent", got)
		}
	})
}

func TestKeyAuthPanicsOnAConfigurationThatCannotWork(t *testing.T) {
	ok := func(context.Context, string) (*user, bool, error) { return nil, false, nil }
	cases := map[string]middleware.KeyAuthConfig[*user]{
		"nil Validate":    {Key: userKey},
		"zero Key":        {Validate: ok},
		"space in Header": {Key: userKey, Validate: ok, Header: "X API Key"},
		"colon in Header": {Key: userKey, Validate: ok, Header: "X-API-Key:"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.HasPrefix(msg, "rice: middleware.KeyAuth:") {
					t.Errorf("panic %q", msg)
				}
			}()
			middleware.KeyAuth(cfg)
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -count=1 -run KeyAuth ./middleware/`
Expected: FAIL to compile, `undefined: middleware.KeyAuth` and `undefined: middleware.KeyAuthConfig`.

- [ ] **Step 3: Write the implementation**

Create `middleware/keyauth.go`:

```go
package middleware

import (
	"context"

	rice "github.com/vietpham102301/rice-http"
)

// KeyAuthConfig is what KeyAuth needs. Key and Validate are required.
//
// Validate receives the request's context and the key, as a string the
// callback may keep, and behaves as BasicAuthConfig's does: the identity to
// store, whether the key is good, and an error when the check itself failed
// (answered with 500, whatever ok says).
//
// Look a key up by its hash — store SHA-256(key), never the key — or compare a
// fixed key with crypto/subtle's ConstantTimeCompare, never with ==. Validate
// must not log what it receives.
//
// Header "" reads Authorization: Bearer <key>. Any other value names a header
// whose whole value, trimmed, is the key, such as "X-API-Key"; it must be a
// valid header name. The key is never read from the query string, which ends
// up in access logs, proxies and browser history.
type KeyAuthConfig[T any] struct {
	Key      rice.Key[T]
	Validate func(ctx context.Context, key string) (T, bool, error)
	Header   string
}

// KeyAuth requires an API key, checks it with cfg.Validate, and stores the
// identity it returns under cfg.Key for the rest of the chain.
//
// A request without a key, with another scheme, or whose key Validate rejects
// gets 401; with the default header the response carries
// WWW-Authenticate: Bearer, and with a custom header no challenge, since none
// is defined for one. An error from Validate goes to the error funnel, which
// answers 500 unless a custom ErrorHandler decides otherwise.
//
// Placement is as for BasicAuth: inside CORS, Logger and otelrice, and on a
// group to protect part of an application. It does not throttle guessing; see
// the rate limiting middleware. See ADR-0021.
//
// A nil Validate, a zero Key or an invalid Header panics.
func KeyAuth[T any](cfg KeyAuthConfig[T]) rice.Middleware {
	if cfg.Validate == nil {
		panic("rice: middleware.KeyAuth: Validate is nil")
	}
	if cfg.Key == (rice.Key[T]{}) {
		panic("rice: middleware.KeyAuth: Key is the zero Key; make one with rice.NewKey")
	}
	if cfg.Header != "" && !isToken(cfg.Header) {
		panic("rice: middleware.KeyAuth: Header " + cfg.Header + " is not a valid header name")
	}
	bearer := cfg.Header == ""
	header, challenge := cfg.Header, ""
	if bearer {
		header, challenge = "Authorization", "Bearer"
	}
	validate, key := cfg.Validate, cfg.Key

	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			v := c.Header(header)
			if bearer {
				var ok bool
				if v, ok = cutScheme(v, "Bearer"); !ok {
					return unauthorized(c, challenge)
				}
			}
			v = trimOWS(v)
			if len(v) == 0 {
				return unauthorized(c, challenge)
			}

			id, ok, err := validate(c.Context(), string(v))
			if err != nil {
				return err
			}
			if !ok {
				return unauthorized(c, challenge)
			}
			key.Set(c, id)
			return next(c)
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race -count=1 -run 'KeyAuth|BasicAuth' ./middleware/`
Expected: PASS.

- [ ] **Step 5: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add middleware/keyauth.go middleware/keyauth_test.go
git commit -m "middleware: KeyAuth reads a Bearer key or a named header, never the query

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Budgets

**Files:**
- Modify: `middleware/alloc_test.go`

**Interfaces:**
- Consumes: `measure(t, mw, headers) float64` (middleware/alloc_test.go), `BasicAuth`, `KeyAuth`, `user`, `userKey`, `basic`.
- Produces: `TestAllocBudgetBasicAuth`, `TestAllocBudgetKeyAuth`, `TestAllocBudgetAuthRejects`; figures for Task 4.

- [ ] **Step 1: Measure**

Create a temporary `middleware/zauth_measure_test.go`:

```go
package middleware_test

import (
	"context"
	"testing"

	"github.com/vietpham102301/rice-http/middleware"
)

func TestMeasureAuth(t *testing.T) {
	u := &user{name: "x"}
	b := middleware.BasicAuth(middleware.BasicAuthConfig[*user]{Key: userKey, Validate: func(context.Context, string, string) (*user, bool, error) { return u, true, nil }})
	k := middleware.KeyAuth(middleware.KeyAuthConfig[*user]{Key: userKey, Validate: func(context.Context, string) (*user, bool, error) { return u, true, nil }})
	t.Logf("basic ok: %.2f", measure(t, b, map[string]string{"Authorization": basic("ann:secret")}))
	t.Logf("key ok: %.2f", measure(t, k, map[string]string{"Authorization": "Bearer k1"}))
	t.Logf("basic 401: %.2f", measure(t, b, nil))
	t.Logf("key 401: %.2f", measure(t, k, nil))
}
```

Run: `go test -count=1 -run MeasureAuth -v ./middleware/` and with `-race`, three times each. The prototype measured 2, 1, 0, 0 in both modes. If any figure differs, use the measured figure in Step 2 and say so in the report. Delete the file.

- [ ] **Step 2: Write the budgets**

Append to `middleware/alloc_test.go` (add `"context"` to its imports):

```go
// TestAllocBudgetBasicAuth pins BasicAuth accepting a request whose identity
// is a pointer: the decoded credentials and the user and password strings
// handed to Validate. Measured at 2 on darwin arm64 (go1.25.6), with and
// without -race.
func TestAllocBudgetBasicAuth(t *testing.T) {
	const want float64 = 2

	u := &user{name: "ann"}
	mw := middleware.BasicAuth(middleware.BasicAuthConfig[*user]{Key: userKey, Validate: func(context.Context, string, string) (*user, bool, error) { return u, true, nil }})
	got := measure(t, mw, map[string]string{"Authorization": basic("ann:secret")})
	if got != want {
		t.Errorf("BasicAuth allocated %.1f objects per call, want exactly %.0f", got, want)
	}
}

// TestAllocBudgetKeyAuth pins KeyAuth accepting a Bearer request whose identity
// is a pointer: the key string handed to Validate. Measured at 1 on darwin
// arm64 (go1.25.6), with and without -race.
func TestAllocBudgetKeyAuth(t *testing.T) {
	const want float64 = 1

	u := &user{name: "svc"}
	mw := middleware.KeyAuth(middleware.KeyAuthConfig[*user]{Key: userKey, Validate: func(context.Context, string) (*user, bool, error) { return u, true, nil }})
	got := measure(t, mw, map[string]string{"Authorization": "Bearer k1"})
	if got != want {
		t.Errorf("KeyAuth allocated %.1f objects per call, want exactly %.0f", got, want)
	}
}

// TestAllocBudgetAuthRejects pins a request without credentials at 0 for both
// middleware: the 401 is a shared error and the challenge a string built once
// at construction. Measured at 0 on darwin arm64 (go1.25.6), with and without
// -race.
func TestAllocBudgetAuthRejects(t *testing.T) {
	const want float64 = 0

	never := func(context.Context, string, string) (*user, bool, error) { return nil, false, nil }
	neverKey := func(context.Context, string) (*user, bool, error) { return nil, false, nil }
	b := measure(t, middleware.BasicAuth(middleware.BasicAuthConfig[*user]{Key: userKey, Validate: never}), nil)
	k := measure(t, middleware.KeyAuth(middleware.KeyAuthConfig[*user]{Key: userKey, Validate: neverKey}), nil)
	if b != want || k != want {
		t.Errorf("a 401 allocated %.1f (BasicAuth) and %.1f (KeyAuth) objects per call, want exactly %.0f", b, k, want)
	}
}
```

- [ ] **Step 3: Run**

Run: `go test -count=3 -run 'AllocBudget(BasicAuth|KeyAuth|AuthRejects)' ./middleware/` and with `-race`.
Expected: PASS on every run.

- [ ] **Step 4: Run the full suites and commit**

Run: `make test && make test-debug && make lint`

```bash
git add middleware/alloc_test.go
git commit -m "middleware: pin BasicAuth and KeyAuth, and a 401 at zero allocations

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Documentation

**Files:**
- Create: `docs/adr/0021-auth-validates-through-a-callback.md`
- Modify: `docs/adr/README.md`, `docs/03-core-concepts.md`, `docs/05-performance-model.md`, `docs/04-roadmap.md`, `docs/progress.md`, `README.md`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: nothing code depends on.

- [ ] **Step 1: ADR-0021**

Create `docs/adr/0021-auth-validates-through-a-callback.md` in the shape of ADR-0019 and ADR-0020, titled `ADR-0021 — Authentication validates through a callback and hands a typed identity to the handler`, Status Accepted, Date 2026-09-27.
- **Context:** rice had no authentication; applications keep credentials in their own stores; `Key[T]` (ADR-0016) exists; a DB outage answered as 401 would look like wrong credentials.
- **Decision:** the two config structs and constructors; D2's parsing; D3's three outcomes (401 with its challenges, 500 for an error whatever ok says, identical 401s for malformed and rejected credentials); D4's placement (inside CORS, Logger, otelrice; on a group; with app.Use a miss is 401); D5's non-duties (no secret comparison, no logging, TLS for Basic, no throttling).
- **Alternatives:** a string identity (rejected: forces a second lookup, and no error branch); a callback receiving `*rice.Ctx` and borrowed bytes (rejected: borrow-contract trap, callbacks writing responses); static credential lists (rejected by the owner: cannot rotate without a restart; a callback can wrap one); reading the key from the query (rejected: logs, proxies, history); a skipper option (rejected: groups scope middleware).
- **Consequences:** the budgets (2, 1, 0); rate limiting is the next design.

- [ ] **Step 2: The rest**

- `docs/adr/README.md`: a row for ADR-0021.
- `docs/03-core-concepts.md`: wherever the middleware package's members are listed, add BasicAuth and KeyAuth with one sentence each.
- `docs/05-performance-model.md`: under *Opt-in packages*, BasicAuth 2, KeyAuth 1, a 401 0, with the test names and "darwin arm64, with and without -race".
- `docs/04-roadmap.md`: a *Done after M8* bullet in the voice of the others.
- `docs/progress.md`: `## 2026-09-27 — post-M8 — BasicAuth and KeyAuth` with Did / Learned / Measured / Next (rate limiting). Learned: at least that CORS answering a preflight without calling next is what lets auth sit inside it; that separating 401 from 500 needs a third return value.
- `README.md`: a short "Authentication" section: the `userKey` / `KeyAuth` example from the spec's D1 (with `crypto/sha256`), and one sentence each on placement inside CORS and on using TLS for BasicAuth.

- [ ] **Step 3: Verify and commit**

Run: `make test && make test-debug && make lint && make cover`
Expected: PASS; root coverage not below 99.6%.

```bash
git add docs README.md
git commit -m "docs: ADR-0021 and the docs for BasicAuth and KeyAuth

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```
