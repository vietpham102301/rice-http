# CORS Middleware Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `middleware.CORS(cfg)`, which answers a browser's preflight and puts the CORS headers on every response — including error responses — for a fixed list of origins.

**Architecture:** `CORS` validates its configuration once and compiles it into two fixed lists of `(key, value)` header pairs, one for a real response and one for a preflight. Per request it adds `Vary: Origin`, matches the `Origin` header byte-for-byte against the configured list, and takes one of three branches: no `Origin` (pass through), a real request (set the origin and the real-response pairs, then `next`), or a preflight (set the origin and the preflight pairs, answer 204, never call `next`). All headers are written before `next` so they survive the error funnel, which never resets headers. The server states its policy; the browser enforces it — the middleware never parses `Access-Control-Request-Method` or `-Headers`. No change to package `rice`.

**Tech Stack:** Go 1.25, fasthttp v1.73.0 (`ResponseHeader.Add`/`Set`, `StatusNoContent`, `MethodOptions`). No new dependency.

**Spec:** [docs/superpowers/specs/2026-09-24-cors-middleware-design.md](../specs/2026-09-24-cors-middleware-design.md)

## Global Constraints

- **Branch:** `cors-middleware`, already created, already holding the spec commit. Do not commit to `main`.
- **No change to package `rice`.** Everything lives in `middleware/`.
- **The middleware never inspects `Access-Control-Request-Method`'s value or `Access-Control-Request-Headers` at all.** It checks only that `Access-Control-Request-Method` is present, to recognise a preflight. It states the configured policy; the browser compares.
- **Every header `CORS` writes is written before `next(c)`.** The funnel (`respond`, `errors.go:156`) sets status, content type and body only, so headers written before `next` reach the client on a 401, a 500 and a recovered panic.
- **`Vary: Origin` is written with `Add` on every request**, including one with no `Origin`. `Access-Control-Allow-Origin` is written with `Set`.
- **Origin matching is exact, byte-for-byte, against the configured strings.** No prefix, no case folding, no wildcard. The value written to `Access-Control-Allow-Origin` is the configured string, never a conversion of the request header.
- **A preflight** is `OPTIONS` + a non-empty `Origin` + a non-empty `Access-Control-Request-Method`. It is answered `c.NoContent(204)` without calling `next`, matched or not.
- **Configuration mistakes panic at construction** with the prefix `rice: middleware.CORS: `.
- **Budgets:** the constant is declared typed — `const want float64 = 0` — and pinned exactly in both directions without `-race`. **No claim that a figure is exact under `-race` may be written until it has been measured on both darwin and Linux, with and without `-race`.**
- **Run `make test` before each commit.** Commit messages: subject, blank line, `Co-Authored-By` trailer on its own line.

## Review Focus

Inputs the spec implies but does not enumerate. Each has a test in the task that owns the code.

1. **An `Origin` header that is present but empty** — must take the no-`Origin` branch: no CORS headers, `next` runs. (Task 2)
2. **An origin that merely begins with an allowed one**, `https://app.example.com.evil.com` against `https://app.example.com` — must not match. (Task 2)
3. **The caller mutates `cfg.Origins` after `CORS(cfg)` returns** — the middleware must keep its own copy, so the allowed set does not change under it. (Task 2)
4. **A real request from an allowed origin to a path with no route** — the 404 must carry the CORS headers, or the browser reports a CORS failure instead of a 404. (Task 2)
5. **`Access-Control-Request-Method` on a request that is not `OPTIONS`** — a misbehaving client; it must be treated as a real request, not a preflight. (Task 3)

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `middleware/cors.go` (new) | `CORSConfig`, `CORS`, construction checks, the three branches | 1, 2, 3 |
| `middleware/cors_test.go` (new) | behaviour | 1, 2, 3 |
| `middleware/alloc_test.go` | three budgets and one measuring helper | 4 |
| `docs/adr/0015-cors-states-a-policy.md` (new), `docs/adr/README.md`, `docs/04-roadmap.md`, `README.md`, `docs/02-architecture.md`, `docs/03-core-concepts.md`, `docs/05-performance-model.md`, `middleware/doc.go`, `docs/progress.md` | documentation | 5 |

**Helper names in package `middleware_test` already taken:** `measure`, `newRequest`, `assertAllocBudget`, `logRequest`, `loggedApp`, `one`, `ipApp`, `dispatchFrom`, `serveApp`, `idApp`, `dispatchWithID`, `dispatch`, `panickingHandler`, `waitDone`, `timedApp`, `send`. This plan's new helpers are `corsApp`, `serveCORS`, `hdr`, `allowedOrigin`, `baseCORS` and, in Task 4, `measureResetting`.

`newRequest(method, uri string, headers map[string]string) *fasthttp.RequestCtx` in `alloc_test.go` builds a request with arbitrary headers; every test here dispatches through it.

---

### Task 1: `CORSConfig`, construction checks, and the no-`Origin` branch

**Files:**
- Create: `middleware/cors.go`
- Create: `middleware/cors_test.go`

**Interfaces:**
- Consumes: `rice.Middleware`, `rice.Handler`, `c.Header`, `c.RequestCtx`.
- Produces: `type CORSConfig struct { Origins, AllowMethods, AllowHeaders, ExposeHeaders []string; MaxAge time.Duration; AllowCredentials bool }`; `func CORS(cfg CORSConfig) rice.Middleware`; unexported `type header struct{ key, value string }`, `func compileCORS(cfg CORSConfig) (origins []string, real, preflight []header)`; test helpers `allowedOrigin`, `baseCORS`, `corsApp`, `serveCORS`, `hdr`.

- [ ] **Step 1: Write the failing tests**

Create `middleware/cors_test.go`:

```go
package middleware_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

const allowedOrigin = "https://app.example.com"

// baseCORS is the configuration a JSON API behind a reverse proxy needs: one
// origin, the two request headers the browser's safelist does not cover, the
// request id exposed so a client can quote it, and a cached preflight.
func baseCORS() middleware.CORSConfig {
	return middleware.CORSConfig{
		Origins:       []string{allowedOrigin},
		AllowHeaders:  []string{"Authorization", "Content-Type"},
		ExposeHeaders: []string{"X-Request-Id"},
		MaxAge:        10 * time.Minute,
	}
}

// corsApp returns an App with CORS(cfg) installed with app.Use — the only
// placement that sees a preflight, since a preflight is a route miss — and a
// handler on GET and POST /r that records whether it ran. Extra middleware
// goes inside CORS.
func corsApp(cfg middleware.CORSConfig, ran *bool, inside ...rice.Middleware) *rice.App {
	app := rice.New()
	app.Use(append([]rice.Middleware{middleware.CORS(cfg)}, inside...)...)
	h := func(c *rice.Ctx) error {
		*ran = true
		return c.String(200, "ok")
	}
	app.GET("/r", h)
	app.POST("/r", h)
	return app
}

// serveCORS dispatches one request with the given headers and returns the
// response.
func serveCORS(t *testing.T, app *rice.App, method, path string, headers map[string]string) *fasthttp.RequestCtx {
	t.Helper()
	fctx := newRequest(method, path, headers)
	app.FasthttpHandler()(fctx)
	return fctx
}

// hdr returns a response header's value, or "" when it is absent. Vary may
// carry several values; read it with PeekAll.
func hdr(fctx *fasthttp.RequestCtx, name string) string {
	return string(fctx.Response.Header.Peek(name))
}

func TestCORSPanicsOnAConfigurationThatCannotWork(t *testing.T) {
	cases := []struct {
		name string
		cfg  middleware.CORSConfig
		want string // a substring of the panic message
	}{
		{"no origins", middleware.CORSConfig{}, "Origins is empty"},
		{"wildcard", middleware.CORSConfig{Origins: []string{"*"}}, `"*"`},
		{"trailing slash", middleware.CORSConfig{Origins: []string{"https://app.example.com/"}}, "https://app.example.com/"},
		{"path", middleware.CORSConfig{Origins: []string{"https://app.example.com/login"}}, "/login"},
		{"query", middleware.CORSConfig{Origins: []string{"https://app.example.com?x=1"}}, "?x=1"},
		{"upper case", middleware.CORSConfig{Origins: []string{"https://App.example.com"}}, "App"},
		{"no scheme", middleware.CORSConfig{Origins: []string{"app.example.com"}}, "app.example.com"},
		{"empty host", middleware.CORSConfig{Origins: []string{"https://"}}, `"https://"`},
		{"null", middleware.CORSConfig{Origins: []string{"null"}}, `"null"`},
		{"negative max age", middleware.CORSConfig{Origins: []string{allowedOrigin}, MaxAge: -time.Second}, "MaxAge"},
		{"empty method", middleware.CORSConfig{Origins: []string{allowedOrigin}, AllowMethods: []string{""}}, "AllowMethods"},
		{"comma in header", middleware.CORSConfig{Origins: []string{allowedOrigin}, AllowHeaders: []string{"A,B"}}, "AllowHeaders"},
		{"space in exposed header", middleware.CORSConfig{Origins: []string{allowedOrigin}, ExposeHeaders: []string{"X Y"}}, "ExposeHeaders"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("CORS did not panic")
				}
				msg, _ := r.(string)
				if !strings.HasPrefix(msg, "rice: middleware.CORS: ") {
					t.Errorf("panic = %q, want the rice: middleware.CORS: prefix", msg)
				}
				if !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %q, want it to name %q", msg, tc.want)
				}
			}()
			middleware.CORS(tc.cfg)
		})
	}
}

func TestCORSAcceptsTheOriginsABrowserSends(t *testing.T) {
	for _, o := range []string{"https://app.example.com", "http://localhost:3000", "https://[::1]:8443", "https://app.example.com:8443"} {
		t.Run(o, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("CORS panicked on %q: %v", o, r)
				}
			}()
			middleware.CORS(middleware.CORSConfig{Origins: []string{o}})
		})
	}
}

func TestCORSPassesARequestWithoutOriginThrough(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(baseCORS(), &ran), "GET", "/r", nil)

	if !ran {
		t.Error("the handler did not run")
	}
	if got := fctx.Response.StatusCode(); got != 200 {
		t.Errorf("status = %d, want 200", got)
	}
	if got := hdr(fctx, "Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want absent on a request with no Origin", got)
	}
	if got := hdr(fctx, "Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin: a cache must learn the response depends on Origin from the response that had none", got)
	}
}

// keep the imports used until later tasks add their tests
var _ = errors.New
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./middleware/ -run 'TestCORS' -count=1`
Expected: build failure, `undefined: middleware.CORSConfig` and `undefined: middleware.CORS`.

- [ ] **Step 3: Write `cors.go` — the type, the checks, the compiled pairs, and the pass-through**

Create `middleware/cors.go`. The `Origin`-bearing branches do not exist yet in this task: every request is passed through with `Vary` added. Tasks 2 and 3 replace the returned closure.

```go
package middleware

import (
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
)

// CORSConfig is what CORS needs to know. Only Origins is required.
//
// Origins are compared byte-for-byte with the Origin header a browser sends:
// lower case, scheme://host, a port only when it is not the scheme's default,
// no path and no trailing slash. "https://app.example.com/" never matches
// anything, and CORS panics on it. There is no wildcard and no pattern; each
// origin is listed.
//
// AllowMethods defaults to GET, HEAD, POST, PUT, PATCH and DELETE — the verbs
// a JSON API uses — when empty. AllowHeaders has no default: the browser's
// safelist does not include Authorization or Content-Type: application/json,
// so a JSON API lists both. ExposeHeaders names the response headers a script
// may read beyond the safelist; X-Request-Id lets a client quote its id.
// MaxAge is sent in whole seconds and omitted when zero, in which case the
// browser caches a preflight for five seconds. AllowCredentials adds
// Access-Control-Allow-Credentials: true to every response for an allowed
// origin, which a browser requires before it will send cookies.
type CORSConfig struct {
	Origins          []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	MaxAge           time.Duration
	AllowCredentials bool
}

// header is one compiled response header.
type header struct{ key, value string }

var defaultAllowMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}

// CORS lets a browser application served from one of cfg.Origins call this
// service: it answers the browser's preflight and puts the CORS headers on
// every other response, including a 401, a 404 and a 500.
//
// Install it with app.Use. A browser's preflight is an OPTIONS request for a
// path that usually has no OPTIONS route — a miss — and only application
// middleware runs on a miss (ADR-0012). A CORS installed on a group or a route
// decorates real responses and never sees a preflight, so the browser fails on
// its first non-simple request. Install it inside Logger, so the preflight's
// 204 is logged, and before any middleware that answers 401 or 403, so those
// answers carry the headers and the browser reports the status rather than a
// CORS failure.
//
// It states a policy and leaves the browser to enforce it. On a preflight it
// sends the configured Access-Control-Allow-Methods and -Headers; the browser
// compares the method and headers it intends to send against them and blocks
// the request itself when they are not listed. Access-Control-Request-Method
// and -Headers are never parsed here. The one check the server cannot delegate
// is the origin, and that is the only one made: an origin not in the list
// receives no CORS headers. A real request from it is still served — the
// browser blocks the script from reading the result — and a preflight from it
// is still answered 204, with the absence of headers as the answer.
//
// Every header is written before the rest of the chain runs, and rice's error
// funnel does not reset headers, so they survive whatever the chain returns.
// A custom ErrorHandler that resets the response drops them.
//
// Vary: Origin is added to every response, with or without an Origin header,
// so a shared cache never serves one origin's response to another.
//
// A configuration that cannot work panics at construction: see CORSConfig.
func CORS(cfg CORSConfig) rice.Middleware {
	origins, _, _ := compileCORS(cfg)
	_ = origins
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			c.RequestCtx().Response.Header.Add("Vary", "Origin")
			return next(c)
		}
	}
}

// compileCORS validates cfg and does every piece of work that can be done
// once: it copies the origins, joins the lists, renders MaxAge, and builds the
// fixed header pairs for a real response and for a preflight. An empty field
// produces no pair, so the per-request path has no branch on configuration.
func compileCORS(cfg CORSConfig) (origins []string, real, preflight []header) {
	if len(cfg.Origins) == 0 {
		panic("rice: middleware.CORS: Origins is empty")
	}
	origins = make([]string, len(cfg.Origins))
	for i, o := range cfg.Origins {
		if !validOrigin(o) {
			panic("rice: middleware.CORS: origin " + strconv.Quote(o) + " is not scheme://host[:port] in lower case")
		}
		origins[i] = o
	}
	if cfg.MaxAge < 0 {
		panic("rice: middleware.CORS: MaxAge must not be negative")
	}
	methods := cfg.AllowMethods
	if len(methods) == 0 {
		methods = defaultAllowMethods
	}

	preflight = append(preflight, header{"Access-Control-Allow-Methods", joinList("AllowMethods", methods)})
	if v := joinList("AllowHeaders", cfg.AllowHeaders); v != "" {
		preflight = append(preflight, header{"Access-Control-Allow-Headers", v})
	}
	if cfg.MaxAge > 0 {
		preflight = append(preflight, header{"Access-Control-Max-Age", strconv.FormatInt(int64(cfg.MaxAge/time.Second), 10)})
	}
	if cfg.AllowCredentials {
		cred := header{"Access-Control-Allow-Credentials", "true"}
		preflight = append(preflight, cred)
		real = append(real, cred)
	}
	if v := joinList("ExposeHeaders", cfg.ExposeHeaders); v != "" {
		real = append(real, header{"Access-Control-Expose-Headers", v})
	}
	return origins, real, preflight
}

// validOrigin reports whether o has the shape a browser puts in Origin:
// scheme://host[:port], lower case, with no path, query or fragment. A
// trailing slash is a path. "*" and "null" have no scheme and fail.
func validOrigin(o string) bool {
	scheme, rest, ok := strings.Cut(o, "://")
	if !ok || scheme == "" || rest == "" {
		return false
	}
	if strings.ContainsAny(rest, "/?# \t") {
		return false
	}
	for i := 0; i < len(o); i++ {
		if b := o[i]; b >= 'A' && b <= 'Z' || b < 0x21 {
			return false
		}
	}
	return true
}

// joinList joins items as "A, B, C", or returns "" for none. An empty item, or
// one containing a comma or whitespace, would corrupt the joined value and
// panics naming the field.
func joinList(field string, items []string) string {
	for _, it := range items {
		if it == "" || strings.ContainsAny(it, ", \t") {
			panic("rice: middleware.CORS: " + field + " entry " + strconv.Quote(it) + " must not be empty or contain a comma or whitespace")
		}
	}
	return strings.Join(items, ", ")
}

var _ = fasthttp.StatusNoContent // used from Task 3 on
```

- [ ] **Step 4: Run the tests**

Run: `go test ./middleware/ -run 'TestCORS' -count=1 -v`
Expected: `TestCORSPanicsOnAConfigurationThatCannotWork` PASS on every case, `TestCORSAcceptsTheOriginsABrowserSends` PASS, `TestCORSPassesARequestWithoutOriginThrough` PASS. Then `make test` and `make lint`: green, clean.

- [ ] **Step 5: Commit**

```bash
git add middleware/cors.go middleware/cors_test.go
git commit -F - <<'EOF'
middleware: CORSConfig, its construction checks, and the compiled header pairs

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 2: The real-request branch

**Files:**
- Modify: `middleware/cors.go` (the returned closure)
- Modify: `middleware/cors_test.go`

**Interfaces:**
- Consumes: `compileCORS`, `header`, the test helpers from Task 1; `middleware.Recover` from `recover.go`; `panickingHandler` from `recover_test.go`.
- Produces: unexported `func matchOrigin(origins []string, origin []byte) string`.

- [ ] **Step 1: Write the failing tests**

Append to `middleware/cors_test.go`, replacing the `var _ = errors.New` line:

```go
func TestCORSDecoratesAResponseForAnAllowedOrigin(t *testing.T) {
	for _, credentials := range []bool{false, true} {
		t.Run(map[bool]string{false: "without credentials", true: "with credentials"}[credentials], func(t *testing.T) {
			cfg := baseCORS()
			cfg.AllowCredentials = credentials
			var ran bool
			fctx := serveCORS(t, corsApp(cfg, &ran), "GET", "/r", map[string]string{"Origin": allowedOrigin})

			if !ran {
				t.Error("the handler did not run")
			}
			if got := hdr(fctx, "Access-Control-Allow-Origin"); got != allowedOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
			}
			if got := hdr(fctx, "Access-Control-Expose-Headers"); got != "X-Request-Id" {
				t.Errorf("Access-Control-Expose-Headers = %q, want X-Request-Id", got)
			}
			want := ""
			if credentials {
				want = "true"
			}
			if got := hdr(fctx, "Access-Control-Allow-Credentials"); got != want {
				t.Errorf("Access-Control-Allow-Credentials = %q, want %q", got, want)
			}
			for _, name := range []string{"Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Max-Age"} {
				if got := hdr(fctx, name); got != "" {
					t.Errorf("%s = %q on a real response, want it only on a preflight", name, got)
				}
			}
			if got := hdr(fctx, "Vary"); got != "Origin" {
				t.Errorf("Vary = %q, want Origin", got)
			}
		})
	}
}

// TestCORSMatchesTheOriginExactly pins the comparison: byte-for-byte against
// the configured string. A browser never sends the first two forms; they prove
// there is no case folding and no normalisation. The third is what an
// attacker registers to defeat a prefix match.
func TestCORSMatchesTheOriginExactly(t *testing.T) {
	for _, origin := range []string{
		"https://APP.example.com",
		"https://app.example.com/",
		"https://app.example.com.evil.com",
		"https://evil.example",
	} {
		t.Run(origin, func(t *testing.T) {
			var ran bool
			fctx := serveCORS(t, corsApp(baseCORS(), &ran), "GET", "/r", map[string]string{"Origin": origin})

			if !ran {
				t.Error("the handler did not run: an unknown origin is still served; the browser does the blocking")
			}
			if got := fctx.Response.StatusCode(); got != 200 {
				t.Errorf("status = %d, want 200", got)
			}
			for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Expose-Headers", "Access-Control-Allow-Credentials"} {
				if got := hdr(fctx, name); got != "" {
					t.Errorf("%s = %q for an origin not in the list, want absent", name, got)
				}
			}
			if got := hdr(fctx, "Vary"); got != "Origin" {
				t.Errorf("Vary = %q, want Origin even when the origin is not allowed", got)
			}
		})
	}
}

// TestCORSKeepsItsOwnCopyOfTheOrigins pins that CORS copies cfg.Origins at
// construction. A caller that reuses or mutates its slice afterwards must not
// change the allowed set under a running server.
func TestCORSKeepsItsOwnCopyOfTheOrigins(t *testing.T) {
	cfg := baseCORS()
	var ran bool
	app := corsApp(cfg, &ran)
	cfg.Origins[0] = "https://evil.example"

	fctx := serveCORS(t, app, "GET", "/r", map[string]string{"Origin": allowedOrigin})

	if got := hdr(fctx, "Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q: the middleware must not share the caller's slice", got, allowedOrigin)
	}
}

func TestCORSTreatsAnEmptyOriginAsAbsent(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(baseCORS(), &ran), "GET", "/r", map[string]string{"Origin": ""})

	if !ran {
		t.Error("the handler did not run")
	}
	if got := hdr(fctx, "Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want absent for an empty Origin", got)
	}
}

// TestCORSHeadersSurviveTheFunnel is the reason the headers are written
// before next: the funnel sets status, content type and body and nothing
// else, so a 401 reaches the browser as a 401 and not as a CORS failure.
// The panic cases pin it on both the Recover path and core's own recovery.
func TestCORSHeadersSurviveTheFunnel(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		h      rice.Handler
		inside []rice.Middleware
		want   int
	}{
		{"plain error", "/e", func(c *rice.Ctx) error { return errors.New("db down") }, nil, 500},
		{"HTTPError", "/e", func(c *rice.Ctx) error { return rice.NewHTTPError(401, "who are you") }, nil, 401},
		{"panic under Recover", "/e", panickingHandler, []rice.Middleware{middleware.Recover()}, 500},
		{"panic with core recovery only", "/e", panickingHandler, nil, 500},
		{"no route", "/nowhere", nil, nil, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ran bool
			app := corsApp(baseCORS(), &ran, tc.inside...)
			if tc.h != nil {
				app.GET("/e", tc.h)
			}

			fctx := serveCORS(t, app, "GET", tc.path, map[string]string{"Origin": allowedOrigin})

			if got := fctx.Response.StatusCode(); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
			if got := hdr(fctx, "Access-Control-Allow-Origin"); got != allowedOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q on a %d, want %q: the browser would report a CORS failure instead of the status", got, tc.want, allowedOrigin)
			}
			if got := hdr(fctx, "Vary"); got != "Origin" {
				t.Errorf("Vary = %q, want Origin", got)
			}
		})
	}
}

// TestCORSKeepsAHandlersOwnVary is why Vary is written with Add and not Set.
func TestCORSKeepsAHandlersOwnVary(t *testing.T) {
	var ran bool
	app := corsApp(baseCORS(), &ran)
	app.GET("/v", func(c *rice.Ctx) error {
		c.RequestCtx().Response.Header.Add("Vary", "Accept")
		return c.String(200, "ok")
	})

	fctx := serveCORS(t, app, "GET", "/v", map[string]string{"Origin": allowedOrigin})

	var got []string
	for _, v := range fctx.Response.Header.PeekAll("Vary") {
		got = append(got, string(v))
	}
	if len(got) != 2 || got[0] != "Origin" || got[1] != "Accept" {
		t.Errorf("Vary = %q, want [Origin Accept]", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./middleware/ -run 'TestCORS' -count=1`
Expected: `TestCORSDecoratesAResponseForAnAllowedOrigin`, `TestCORSHeadersSurviveTheFunnel` and `TestCORSKeepsItsOwnCopyOfTheOrigins` FAIL on a missing `Access-Control-Allow-Origin`; `TestCORSMatchesTheOriginExactly`, `TestCORSTreatsAnEmptyOriginAsAbsent` and `TestCORSKeepsAHandlersOwnVary` PASS already (they assert absence or `Vary` alone) — that is fine; they exist to keep passing once the branch is written.

- [ ] **Step 3: Write the real-request branch**

In `middleware/cors.go`, replace the body of `CORS` and add `matchOrigin`:

```go
func CORS(cfg CORSConfig) rice.Middleware {
	origins, real, _ := compileCORS(cfg)
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			h := &c.RequestCtx().Response.Header
			// Every response, with or without an Origin: a shared cache must
			// learn from the response that had none that the next one may
			// differ. Add, not Set, so a handler's own Vary survives.
			h.Add("Vary", "Origin")

			origin := c.Header("Origin")
			if len(origin) == 0 {
				return next(c)
			}
			if allowed := matchOrigin(origins, origin); allowed != "" {
				// The configured string, equal to the header's bytes: no
				// []byte-to-string conversion and no allocation.
				h.Set("Access-Control-Allow-Origin", allowed)
				for _, p := range real {
					h.Set(p.key, p.value)
				}
			}
			return next(c)
		}
	}
}

// matchOrigin returns the configured origin equal to the request's, or "".
// The comparison is exact: no case folding, no prefix, no normalisation.
// string(origin) == o compiles to a comparison, not a conversion.
func matchOrigin(origins []string, origin []byte) string {
	for _, o := range origins {
		if string(origin) == o {
			return o
		}
	}
	return ""
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./middleware/ -run 'TestCORS' -count=1 -v`
Expected: all PASS. Then `make test` and `make lint`: green, clean.

- [ ] **Step 5: Commit**

```bash
git add middleware/cors.go middleware/cors_test.go
git commit -F - <<'EOF'
middleware: CORS decorates a real response, and the headers survive the funnel

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 3: The preflight branch

**Files:**
- Modify: `middleware/cors.go` (the returned closure)
- Modify: `middleware/cors_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1 and 2; `middleware.Logger` from `logger.go`.
- Produces: unexported `func isPreflight(c *rice.Ctx) bool`.

- [ ] **Step 1: Write the failing tests**

Append to `middleware/cors_test.go`. Add `"bytes"`, `"encoding/json"`, `"log/slog"` to the imports.

```go
// preflight is what a browser sends before a cross-origin POST with a JSON
// body and a bearer token. The -Headers value is deliberately not what the
// configuration allows: the middleware must not read it.
func preflight(origin string) map[string]string {
	return map[string]string{
		"Origin":                         origin,
		"Access-Control-Request-Method":  "DELETE",
		"Access-Control-Request-Headers": "authorization, content-type, x-not-allowed",
	}
}

func TestCORSAnswersAPreflight(t *testing.T) {
	for _, credentials := range []bool{false, true} {
		t.Run(map[bool]string{false: "without credentials", true: "with credentials"}[credentials], func(t *testing.T) {
			cfg := baseCORS()
			cfg.AllowCredentials = credentials
			var ran bool
			fctx := serveCORS(t, corsApp(cfg, &ran), "OPTIONS", "/r", preflight(allowedOrigin))

			if ran {
				t.Error("the handler ran: a preflight is answered by CORS and never reaches the chain")
			}
			if got := fctx.Response.StatusCode(); got != 204 {
				t.Errorf("status = %d, want 204", got)
			}
			if got := fctx.Response.Body(); len(got) != 0 {
				t.Errorf("body = %q, want empty", got)
			}
			credWant := ""
			if credentials {
				credWant = "true"
			}
			for name, want := range map[string]string{
				"Access-Control-Allow-Origin":      allowedOrigin,
				"Access-Control-Allow-Methods":     "GET, HEAD, POST, PUT, PATCH, DELETE",
				"Access-Control-Allow-Headers":     "Authorization, Content-Type",
				"Access-Control-Max-Age":           "600",
				"Access-Control-Allow-Credentials": credWant,
				"Access-Control-Expose-Headers":    "", // a preflight has no body for a script to read from
				"Vary":                             "Origin",
			} {
				if got := hdr(fctx, name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// TestCORSStatesThePolicyRatherThanEchoingTheRequest is the design: the
// browser compares what it asked for with what the server allows, so the
// server never reads Access-Control-Request-Headers. A middleware that echoed
// the request would send x-not-allowed here.
func TestCORSStatesThePolicyRatherThanEchoingTheRequest(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(baseCORS(), &ran), "OPTIONS", "/r", preflight(allowedOrigin))

	if got := hdr(fctx, "Access-Control-Allow-Headers"); strings.Contains(strings.ToLower(got), "x-not-allowed") {
		t.Errorf("Access-Control-Allow-Headers = %q echoes the request; want the configured list only", got)
	}
}

func TestCORSAnswersAPreflightFromAnUnknownOriginWithNoHeaders(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(baseCORS(), &ran), "OPTIONS", "/r", preflight("https://evil.example"))

	if ran {
		t.Error("the handler ran: a preflight never reaches the chain, allowed or not")
	}
	if got := fctx.Response.StatusCode(); got != 204 {
		t.Errorf("status = %d, want 204: the absence of headers is the answer", got)
	}
	for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Max-Age"} {
		if got := hdr(fctx, name); got != "" {
			t.Errorf("%s = %q for an origin not in the list, want absent", name, got)
		}
	}
	if got := hdr(fctx, "Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
}

func TestCORSAnswersAPreflightForAPathWithNoRoute(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(baseCORS(), &ran), "OPTIONS", "/nowhere", preflight(allowedOrigin))

	if got := fctx.Response.StatusCode(); got != 204 {
		t.Errorf("status = %d, want 204: the middleware cannot tell a miss from a route, and the real request will get its 404 with the headers on", got)
	}
	if got := hdr(fctx, "Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
}

// TestCORSPreflightNeedsAllThreeSignals pins what a preflight is: OPTIONS,
// Origin, and Access-Control-Request-Method. Missing any one, the request is
// a real request and reaches the chain.
func TestCORSPreflightNeedsAllThreeSignals(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		headers map[string]string
	}{
		{"OPTIONS without a request method", "OPTIONS", map[string]string{"Origin": allowedOrigin}},
		{"OPTIONS with an empty request method", "OPTIONS", map[string]string{"Origin": allowedOrigin, "Access-Control-Request-Method": ""}},
		{"request method on a POST", "POST", preflight(allowedOrigin)},
		{"OPTIONS with a request method and no Origin", "OPTIONS", map[string]string{"Access-Control-Request-Method": "DELETE"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ran bool
			app := corsApp(baseCORS(), &ran)
			app.OPTIONS("/r", func(c *rice.Ctx) error {
				ran = true
				return c.String(200, "an OPTIONS route of the application's own")
			})

			fctx := serveCORS(t, app, tc.method, "/r", tc.headers)

			if !ran {
				t.Error("the handler did not run: this is not a preflight")
			}
			if got := fctx.Response.StatusCode(); got != 200 {
				t.Errorf("status = %d, want 200 from the handler", got)
			}
			if _, hasOrigin := tc.headers["Origin"]; hasOrigin {
				if got := hdr(fctx, "Access-Control-Allow-Origin"); got != allowedOrigin {
					t.Errorf("Access-Control-Allow-Origin = %q, want %q: a real request from an allowed origin is decorated", got, allowedOrigin)
				}
				if got := hdr(fctx, "Access-Control-Allow-Methods"); got != "" {
					t.Errorf("Access-Control-Allow-Methods = %q on a real request, want absent", got)
				}
			}
		})
	}
}

// TestCORSPreflightOmitsWhatIsNotConfigured pins the empty-field rule: no
// pair is built, so no header is sent. MaxAge below one second renders as 0.
func TestCORSPreflightOmitsWhatIsNotConfigured(t *testing.T) {
	var ran bool
	fctx := serveCORS(t, corsApp(middleware.CORSConfig{Origins: []string{allowedOrigin}, AllowMethods: []string{"GET", "POST"}}, &ran), "OPTIONS", "/r", preflight(allowedOrigin))

	for name, want := range map[string]string{
		"Access-Control-Allow-Methods":     "GET, POST",
		"Access-Control-Allow-Headers":     "",
		"Access-Control-Max-Age":           "",
		"Access-Control-Allow-Credentials": "",
	} {
		if got := hdr(fctx, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	cfg := middleware.CORSConfig{Origins: []string{allowedOrigin}, MaxAge: 1500 * time.Millisecond}
	fctx = serveCORS(t, corsApp(cfg, &ran), "OPTIONS", "/r", preflight(allowedOrigin))
	if got := hdr(fctx, "Access-Control-Max-Age"); got != "1" {
		t.Errorf("Access-Control-Max-Age = %q for 1.5s, want whole seconds: 1", got)
	}
}

// TestCORSPreflightIsLoggedAs204 is why CORS goes inside Logger.
func TestCORSPreflightIsLoggedAs204(t *testing.T) {
	var buf bytes.Buffer
	app := rice.New()
	app.Use(middleware.Logger(slog.New(slog.NewJSONHandler(&buf, nil))), middleware.CORS(baseCORS()))
	app.GET("/r", func(c *rice.Ctx) error { return c.String(200, "ok") })

	serveCORS(t, app, "OPTIONS", "/r", preflight(allowedOrigin))

	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("log line is not one JSON object: %v: %s", err, buf.Bytes())
	}
	if got := line["status"]; got != float64(204) {
		t.Errorf("logged status = %v, want 204", got)
	}
	if got := line["method"]; got != "OPTIONS" {
		t.Errorf("logged method = %v, want OPTIONS", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./middleware/ -run 'TestCORS' -count=1`
Expected: `TestCORSAnswersAPreflight` FAIL (`status = 404, want 204` — the preflight fell through to the miss chain), `TestCORSAnswersAPreflightFromAnUnknownOriginWithNoHeaders` FAIL (404), `TestCORSAnswersAPreflightForAPathWithNoRoute` FAIL (404), `TestCORSPreflightOmitsWhatIsNotConfigured` FAIL, `TestCORSPreflightIsLoggedAs204` FAIL (`logged status = 404`); `TestCORSPreflightNeedsAllThreeSignals` and `TestCORSStatesThePolicyRatherThanEchoingTheRequest` PASS already.

- [ ] **Step 3: Write the preflight branch**

In `middleware/cors.go`, replace the body of `CORS` again, add `isPreflight`, and delete the `var _ = fasthttp.StatusNoContent` line at the bottom:

```go
func CORS(cfg CORSConfig) rice.Middleware {
	origins, real, preflight := compileCORS(cfg)
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			h := &c.RequestCtx().Response.Header
			// Every response, with or without an Origin: a shared cache must
			// learn from the response that had none that the next one may
			// differ. Add, not Set, so a handler's own Vary survives.
			h.Add("Vary", "Origin")

			origin := c.Header("Origin")
			if len(origin) == 0 {
				return next(c)
			}
			allowed := matchOrigin(origins, origin)

			if isPreflight(c) {
				if allowed != "" {
					h.Set("Access-Control-Allow-Origin", allowed)
					for _, p := range preflight {
						h.Set(p.key, p.value)
					}
				}
				// Matched or not: the absence of headers is the "no". The
				// chain never sees a preflight, so a route's own OPTIONS
				// handler answers only ordinary OPTIONS requests.
				return c.NoContent(fasthttp.StatusNoContent)
			}

			if allowed != "" {
				// The configured string, equal to the header's bytes: no
				// []byte-to-string conversion and no allocation.
				h.Set("Access-Control-Allow-Origin", allowed)
				for _, p := range real {
					h.Set(p.key, p.value)
				}
			}
			return next(c)
		}
	}
}

// isPreflight reports whether this is a browser's preflight: OPTIONS with an
// Access-Control-Request-Method. The caller has already checked Origin. The
// header's value is not read; only its presence says what the request is.
func isPreflight(c *rice.Ctx) bool {
	return string(c.Method()) == fasthttp.MethodOptions &&
		len(c.Header("Access-Control-Request-Method")) > 0
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./middleware/ -run 'TestCORS' -count=1 -v`
Expected: all PASS. Then `make test`, `make test-debug` and `make lint`: green, clean.

- [ ] **Step 5: Commit**

```bash
git add middleware/cors.go middleware/cors_test.go
git commit -F - <<'EOF'
middleware: CORS answers a preflight with the configured policy

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 4: The three allocation budgets

**Files:**
- Modify: `middleware/alloc_test.go`

**Interfaces:**
- Consumes: `measure`'s pattern, `newRequest`, `assertAllocBudget`, `middleware.CORS`.
- Produces: `func measureResetting(t *testing.T, mw rice.Middleware, method, uri string, headers map[string]string) float64`; `TestAllocBudgetCORSNoOrigin`, `TestAllocBudgetCORSAllowedOrigin`, `TestAllocBudgetCORSPreflight`.

**Why a new helper.** `measure` calls the wrapped handler a thousand times on one `Ctx` without touching the response. `CORS` adds `Vary: Origin` on every call, so a thousand calls would grow the response header's backing slice and the growth would be counted as the middleware's cost. `measureResetting` resets the response header before each call, as fasthttp does between requests on a connection. `ResponseHeader.Reset` re-slices its buffers to zero length and keeps their capacity (`resetSkipNormalize`, fasthttp `header.go:976`), so after the warm-up call every `Set` and `Add` writes into memory that already exists.

- [ ] **Step 1: Write the three budgets, with `want` at 0**

Append to `middleware/alloc_test.go`:

```go
// measureResetting is measure for a middleware that writes response headers:
// it resets the response header before each call, as fasthttp does between
// requests on a connection, so that Vary added a thousand times is not
// counted as growth. Reset keeps the header's buffers, so after the warm call
// every Set and Add writes into memory that already exists.
func measureResetting(t *testing.T, mw rice.Middleware, method, uri string, headers map[string]string) float64 {
	t.Helper()
	wrapped := mw(func(c *rice.Ctx) error { return nil })
	var got float64
	app := rice.New()
	app.Handle(method, "/m", func(c *rice.Ctx) error {
		h := &c.RequestCtx().Response.Header
		_ = wrapped(c) // warm
		got = testing.AllocsPerRun(1000, func() {
			h.Reset()
			_ = wrapped(c)
		})
		return nil
	})
	fctx := newRequest(method, uri, headers)
	app.FasthttpHandler()(fctx)
	return got
}

// corsBudgetConfig is the fixture for the three CORS budgets: the
// configuration a JSON API behind a proxy needs, with credentials on so that
// every pair CORS can build is built.
var corsBudgetConfig = middleware.CORSConfig{
	Origins:          []string{"https://app.example.com"},
	AllowHeaders:     []string{"Authorization", "Content-Type"},
	ExposeHeaders:    []string{"X-Request-Id"},
	MaxAge:           10 * time.Minute,
	AllowCredentials: true,
}

// TestAllocBudgetCORSNoOrigin pins the branch a same-origin request takes:
// one Vary added, nothing compared.
func TestAllocBudgetCORSNoOrigin(t *testing.T) {
	const want float64 = 0

	got := measureResetting(t, middleware.CORS(corsBudgetConfig), "GET", "/m", nil)
	assertAllocBudget(t, "CORS (no Origin)", got, want, 0)
}

// TestAllocBudgetCORSAllowedOrigin pins a real request from an allowed
// origin: the comparison, Allow-Origin, Allow-Credentials, Expose-Headers.
func TestAllocBudgetCORSAllowedOrigin(t *testing.T) {
	const want float64 = 0

	got := measureResetting(t, middleware.CORS(corsBudgetConfig), "GET", "/m", map[string]string{
		"Origin": "https://app.example.com",
	})
	assertAllocBudget(t, "CORS (allowed origin)", got, want, 0)
}

// TestAllocBudgetCORSPreflight pins a preflight from an allowed origin: the
// comparison, the four preflight pairs, and NoContent.
func TestAllocBudgetCORSPreflight(t *testing.T) {
	const want float64 = 0

	got := measureResetting(t, middleware.CORS(corsBudgetConfig), "OPTIONS", "/m", map[string]string{
		"Origin":                         "https://app.example.com",
		"Access-Control-Request-Method":  "DELETE",
		"Access-Control-Request-Headers": "authorization, content-type",
	})
	assertAllocBudget(t, "CORS (preflight)", got, want, 0)
}
```

- [ ] **Step 2: Measure on both platforms, and record every figure verbatim**

```bash
go test ./middleware/ -run 'TestAllocBudgetCORS' -count=1 -v
go test -race ./middleware/ -run 'TestAllocBudgetCORS' -count=1 -v
docker run --rm --user 1000:1000 -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath \
  -e GOFLAGS=-buildvcs=false -v "$PWD":/src -w /src golang:1.25.14 \
  go test ./middleware/ -run 'TestAllocBudgetCORS' -count=1 -v
docker run --rm --user 1000:1000 -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath \
  -e GOFLAGS=-buildvcs=false -v "$PWD":/src -w /src golang:1.25.14 \
  go test -race ./middleware/ -run 'TestAllocBudgetCORS' -count=1 -v
```

Expected: all three PASS at 0 in all four runs. **If any run reports a figure above 0, do not raise `want` to make it pass.** Find the allocation first — `go test ./middleware/ -run TestAllocBudgetCORSPreflight -count=1 -gcflags=-m 2>&1 | grep cors.go` shows what escapes, and a `-memprofile` shows where — and either remove it, or, if it is fasthttp's and unavoidable, pin the measured figure exactly with the mechanism named in the test's doc comment. A figure that differs between darwin and Linux off-race is a stop-and-report: the off-race pin would be platform-dependent.

- [ ] **Step 3: Prove each budget bites**

Temporarily set `want` to 1 in `TestAllocBudgetCORSPreflight`, run it without `-race`, and record the failure text — it must read `CORS (preflight) allocated 0.0 objects per call, want exactly 1`. Restore 0. Do the same once for `TestAllocBudgetCORSNoOrigin`.

- [ ] **Step 4: Record the race reasoning in each test's doc comment**

Add to each of the three doc comments one sentence in the style of `TestAllocBudgetTimeout`: where it was measured (darwin arm64 go1.25.6, Linux amd64 golang:1.25.14), with and without `-race`, and that the race slack is 0 because no mechanism in `assertAllocBudget`'s doc comment applies — no pooled buffer, no buffer the race build moves to the heap. If Step 2 found otherwise, write what it found instead.

- [ ] **Step 5: Run everything and commit**

Run: `make test && make test-debug && make lint`
Expected: green, clean.

```bash
git add middleware/alloc_test.go
git commit -F - <<'EOF'
middleware: pin CORS at zero allocations on all three branches

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 5: ADR-0015 and the documentation

**Files:**
- Create: `docs/adr/0015-cors-states-a-policy.md`
- Modify: `docs/adr/README.md`, `docs/04-roadmap.md`, `README.md`, `docs/02-architecture.md`, `docs/03-core-concepts.md`, `docs/05-performance-model.md`, `middleware/doc.go`, `docs/progress.md`

**Interfaces:**
- Consumes: the measured figures and any race reasoning from Task 4.
- Produces: nothing code depends on.

- [ ] **Step 1: Write ADR-0015, "CORS states a policy; the browser enforces it"**

Follow `docs/adr/README.md`'s format — `Context`, `Decision`, `Alternatives`, `Consequences` — `Status: Accepted`, `Date` the day the work lands. It must contain:

- **Context:** the spec's three findings, each with its evidence. (1) The funnel never resets headers: `respond` (`errors.go:156`) sets status, content type and body, and every error path including a recovered panic goes through it, so a header written before `next` reaches the client on a 401 and a 500. (2) Only application middleware sees a preflight: a preflight is a route miss, and ADR-0012 runs only `app.Use` middleware there. (3) Under the Fetch standard the browser compares the method and headers it intends to send against `Access-Control-Allow-Methods` and `-Headers` and blocks the request itself; the one check the server cannot delegate is the origin.
- **Decision:** `CORS(cfg CORSConfig)` as the spec's D1–D7 describe, stated flatly: compiled pairs, three branches, headers before `next`, `Vary` added on every response, exact origin match, preflight answered 204 without `next` whether matched or not.
- **Alternatives**, each with why it lost: **validating `Access-Control-Request-Method` and `-Headers` on the server**, as rs/cors does — parsing a comma-separated list per preflight, allocating for it, and reaching the same browser outcome by a longer road; it also makes the preflight response depend on those two headers, which would need `Vary` on them too. **A wildcard origin** — brings the rule that `*` may not be combined with credentials, for a service that has no use for it; can be added as its own entry. **A per-group preflight** — impossible without a group-aware miss chain, which ADR-0012 names as a possible future and does not build.
- **Consequences:** makes easy — a SPA calls the API, its 401s arrive as 401s, and all three branches cost nothing; makes hard — a group cannot answer a preflight, a preflight for a path that does not exist is answered 204, a route's own `OPTIONS` handler never sees a preflight, and a custom `ErrorHandler` that resets the response drops the headers. State each plainly.

Add its row to `docs/adr/README.md`'s index: `| [0015](0015-cors-states-a-policy.md) | CORS states a policy; the browser enforces it | Accepted |`.

- [ ] **Step 2: Update the roadmap**

In `docs/04-roadmap.md`, under *Done after M8*, add an entry after the `middleware.Timeout` one, in the same voice: what `middleware.CORS(cfg)` does, that it states a policy and the browser enforces it, that it must be installed with `app.Use` because a preflight is a miss, that the headers are written before `next` and so survive the funnel, that it is pinned at 0 on all three branches in `05-performance-model.md`, and a link to ADR-0015. Then correct the sentence at line 226 — "CORS, which the miss chain makes possible, is still unbuilt and has no design yet" — to say it is built below.

- [ ] **Step 3: Update the remaining documents**

- `middleware/doc.go`: in *What is here*, one sentence for `CORS`. In *The recommended order*, add `middleware.CORS(cfg)` between `RequestID` and `Timeout` with the comment `// before auth, so a 401 carries the CORS headers`. Add a section *CORS must be installed with app.Use* stating the group-level trap in three sentences: a preflight is a miss, only application middleware runs on a miss, a group-level CORS decorates real responses and never answers a preflight. In *What they cost*, add: `CORS 0 on every branch`.
- `README.md`: in *The middleware rice ships*, add `middleware.CORS(cfg)` to the `app.Use` block between `RequestID` and `Timeout` with the same comment, and a bullet **`CORS(cfg CORSConfig)`** — the fields in one sentence each, that it states the policy and the browser enforces it, that it must go in `app.Use` and before auth, and that the headers survive a 401 and a 500. Update the cost sentence at the end of the section: `and 0 for CORS on every branch`.
- `docs/02-architecture.md`: add `│   ├── cors.go         CORS: answers a preflight, decorates every other response, for a fixed list of origins` to `middleware/`'s layout (keep the tree's `└──` on the last entry), and replace the sentence "CORS is still unbuilt and needs its own design; `middleware/` holds ..." with one that lists the six.
- `docs/03-core-concepts.md`: in *What rice ships* (line 256), add `middleware.CORS` to the list. Add one paragraph after it on `CORSConfig`, as principle 6 requires of an exported type: the six fields, what the empty value of each means, and that an origin is compared exactly with what a browser sends.
- `docs/05-performance-model.md`: three rows in the *Opt-in packages* table, one per budget, with the fixture named (`corsBudgetConfig`: one origin, two allowed headers, one exposed header, a ten-minute max age, credentials on), the figure `0, exactly, with and without -race` if Task 4 found that, and the test name. Add a `#### middleware.CORS` subsection after `#### middleware.Timeout` in the same style: why it costs nothing (the pairs are built once; `string(b) == s` compiles to a comparison; `Set` and `Add` write into fasthttp's retained buffers; the configured string is what is written, so nothing is converted), how the measurement resets the header per call and why, and the four measurements by platform.

- [ ] **Step 4: Write the progress entry**

Prepend to `docs/progress.md`, milestone `post-M8`, in the template's shape. **Did:** what was built, in the entry style of the Timeout one. **Learned** must carry three things: that the server cannot delegate the origin check but can delegate the method and header checks to the browser, and what that removed from the hot path; that a CORS middleware can only answer a preflight from `app.Use`, because a preflight is a miss — the first middleware for which ADR-0012's "only the application's middleware" is a constraint on the user rather than a detail; and that the funnel's not resetting headers, never written down before, is what makes a 401 arrive as a 401 — now recorded in ADR-0015 so a future change to `respond` has to reckon with it. **Measured:** the figures from Task 4 by platform, and the coverage from `make cover`. **Next:** none scheduled; the next item comes off the roadmap's *Explicitly deferred* list with a brainstorm of its own.

- [ ] **Step 5: Verify and commit**

Run: `make test && make test-debug && make lint && make cover`
Expected: green, clean; note the coverage figure for the progress entry.

Then search for stale claims: `grep -rn -i 'cors' docs/ README.md middleware/doc.go | grep -i -E 'unbuilt|no design|not yet|still'` must return nothing.

```bash
git add docs/ README.md middleware/doc.go
git commit -F - <<'EOF'
docs: ADR-0015 and the docs for CORS

Co-Authored-By: <your session's trailer>
EOF
```

---

## Self-Review

**Spec coverage.** D1 (type, defaults) → Task 1. D2 (compiled pairs) → Task 1, proven by Task 3's defaults test. D3 (every panic rule, including `*`, `null`, trailing slash, upper case, negative `MaxAge`, bad list entries) → Task 1. D4 branch 1 → Task 1; branch 2 → Task 2; branch 3 → Task 3. D5 (headers before `next`, survive plain error, `HTTPError`, panic) → Task 2. D6 (`Vary` added, `Allow-Origin` set) → Tasks 1 and 2. D7 (placement) → Task 5's docs and Task 3's Logger test. Consequences (no-route preflight 204, own `OPTIONS` route, group trap, `ErrorHandler` reset) → Task 3 test, Task 3 test, Task 5 docs, Task 5 docs. Budget → Task 4. Every test in the spec's list has a named test here. Non-goals: `*` and `null` panic (Task 1); no request-header parsing is pinned by `TestCORSStatesThePolicyRatherThanEchoingTheRequest`.

**Placeholders.** None; every step shows its code or its exact edit.

**Type consistency.** `CORSConfig` fields, `header{key, value}`, `compileCORS` returning `(origins, real, preflight)`, `matchOrigin(origins []string, origin []byte) string`, `isPreflight(c *rice.Ctx) bool` are named the same in every task. The test helpers `baseCORS`, `corsApp(cfg, ran *bool, inside ...rice.Middleware)`, `serveCORS(t, app, method, path, headers)`, `hdr(fctx, name)` and `preflight(origin)` are used with those signatures throughout. `measureResetting(t, mw, method, uri, headers)` is defined and used only in Task 4.

**Review Focus.** Item 1 → `TestCORSTreatsAnEmptyOriginAsAbsent` (Task 2). Item 2 → `TestCORSMatchesTheOriginExactly` (Task 2). Item 3 → `TestCORSKeepsItsOwnCopyOfTheOrigins` (Task 2). Item 4 → the `no route` case of `TestCORSHeadersSurviveTheFunnel` (Task 2). Item 5 → the `request method on a POST` case of `TestCORSPreflightNeedsAllThreeSignals` (Task 3).
