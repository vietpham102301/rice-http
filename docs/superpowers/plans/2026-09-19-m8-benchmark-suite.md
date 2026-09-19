# M8 Benchmark Suite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Measure rice against Gin, Echo and Fiber on identical scenarios at the handler level and end to end, complete the allocation budget table, and close the project with a retrospective.

**Architecture:** A separate Go module, `bench/compare/`, holds one scenario table, one adapter per framework (each building a "small" app and a "github" app), an equivalence gate every benchmark runs first, handler-level benchmarks, and an in-repo load generator plus server binary driven by a script. rice's root module gains only four allocation-budget tests; its `go.mod` does not change.

**Tech Stack:** Go 1.25; fasthttp v1.73.0; Gin v1.12.0; Echo v5.3.1; Fiber v3.5.0 (all resolved on 2026-09-19; fasthttp resolves to v1.73.0 inside the comparison module too, so rice is measured on its own pinned version).

**Spec:** [docs/superpowers/specs/2026-09-19-m8-benchmark-suite-design.md](../specs/2026-09-19-m8-benchmark-suite-design.md)

## Global Constraints

- The root `go.mod` and `go.sum` must not change. Gin, Echo and Fiber appear only in `bench/compare/go.mod`.
- `bench/compare/go.mod`: module `github.com/vietpham102301/rice-http/bench/compare`, `go 1.25.0`, `replace github.com/vietpham102301/rice-http => ../..`, and pinned `github.com/gin-gonic/gin v1.12.0`, `github.com/labstack/echo/v5 v5.3.1`, `github.com/gofiber/fiber/v3 v3.5.0`. If `go mod tidy` resolves `github.com/valyala/fasthttp` to anything other than v1.73.0, stop and report — spec D1 then requires a note in the retrospective.
- No change to rice's production code in this milestone (spec non-goal). Only root test files, the Makefile and CI change at the root.
- Scenario names, in this order everywhere they are listed: `static`, `param`, `middleware5`, `notfound`, `githubapi`, `json`, `body64k`. Framework names, in this order: `rice`, `gin`, `echo`, `fiber`.
- No framework gets middleware or options the others lack, beyond each one's documented default. The GitHub API table is used as published: 203 routes, never reordered or trimmed.
- Every benchmark runs the equivalence check for its scenario and framework first and fails if it does not pass.
- Every wait in a test, the load generator, or a script is bounded and fails with a message; nothing may hang.
- Every new guard is broken on purpose once and watched to fail; record the failure text in the task report.
- `make lint`, `make test`, `make test-debug`, `make cover` and `make bench` stay green at the end of every task; from Task 7 on, `make compare` too. Root coverage must not drop below 98.8%.
- Commit messages follow the repo's style (`feat:`, `fix:`, `test:`, `docs:`, `bench:`, `build:`) and end with:
  `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`

## File map

| File | Status | Responsibility |
| --- | --- | --- |
| `alloc_test.go` (root) | modify | four budget tests for `Status`, `SetHeader`, `SetContentType`, `RequestCtx` |
| `bench/compare/go.mod`, `go.sum` | create | the comparison module |
| `bench/compare/scenarios.go` | create | `App`, `Scenario`, `Scenarios()`, `ScenarioByName`, the JSON payload, the 64 KiB body |
| `bench/compare/githubapi.go` | create (generated) | the 203-route GitHub API table, attributed |
| `bench/compare/targets.go` | create | `Server`, `Target`, `Targets()`, `TargetByName` |
| `bench/compare/check.go` | create | `Do` (one in-process request) and `Check` (the equivalence gate) |
| `bench/compare/{rice,gin,echo,fiber}.go` | create | one adapter each |
| `bench/compare/compare_test.go` | create | equivalence gate test, GitHub registration test, table test |
| `bench/compare/discardwriter_test.go` | create | reusable `http.ResponseWriter` and rewindable body for the benchmarks |
| `bench/compare/handler_bench_test.go` | create | `BenchmarkHandler` |
| `bench/compare/histogram.go` | create | allocation-free latency histogram |
| `bench/compare/loadgen.go` | create | `Load`, `WaitReady` |
| `bench/compare/summarize.go` | create | `Summarize`: raw end-to-end lines to the results table |
| `bench/compare/loadgen_test.go` | create | histogram, `Load`, `Summarize` tests |
| `bench/compare/cmd/server/main.go` | create | one binary serving one framework's app |
| `bench/compare/cmd/loadgen/main.go` | create | the load generator binary |
| `bench/compare/cmd/summarize/main.go` | create | stdin raw lines → stdout table |
| `bench/compare/scripts/header.sh`, `handler.sh`, `e2e.sh` | create | results headers and the two recordings |
| `Makefile`, `.github/workflows/ci.yml` | modify | `compare`, `compare-record`, the CI steps |
| `bench/results/M8-compare-handler.txt`, `M8-compare-e2e.txt` | create (generated) | recorded results |
| docs (Task 9) | modify/create | performance model, retrospectives, roadmap, README, journal |

---

### Task 1: Allocation budgets for the four unbudgeted `Ctx` methods

Spec D5 (root part).

**Files:**
- Modify: `alloc_test.go` (append after `TestAllocBudgetMethodAndPath`)

**Interfaces:**
- Consumes: `budget(t *testing.T, name string, want float64, fn func())` from `budget_test.go` (warms `fn` once, then `testing.AllocsPerRun(1000, fn)`); `Ctx.reset(a *App, fctx *fasthttp.RequestCtx)`.
- Produces: `TestAllocBudgetStatus`, `TestAllocBudgetSetHeader`, `TestAllocBudgetSetContentType`, `TestAllocBudgetRequestCtx` — Task 9 cites these names in the budget table.

- [ ] **Step 1: Write the tests**

Append to `alloc_test.go`:

```go
func TestAllocBudgetStatus(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.Status", 0, func() { _ = c.Status(201) })
}

func TestAllocBudgetSetHeader(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.SetHeader", 0, func() { c.SetHeader("X-Rice", "1") })
}

func TestAllocBudgetSetContentType(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.SetContentType", 0, func() { c.SetContentType("application/json") })
}

func TestAllocBudgetRequestCtx(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.RequestCtx", 0, func() { _ = c.RequestCtx() })
}
```

- [ ] **Step 2: Run them**

Run: `go test . -count=1 -run 'TestAllocBudget(Status|SetHeader|SetContentType|RequestCtx)$' -v`
Expected: all four PASS. These pin existing behaviour, so they are not red first; Step 3 proves each can fail. If any fails here, stop and report it — spec D5 says a non-zero result is a finding to record, not to hide.

- [ ] **Step 3: Fault injection, one per test**

For each of the four methods in `ctx_response.go` / `ctx.go`, temporarily add an allocating line at the top of the method body — `sink = fmt.Sprint(code)` for `Status` (declare `var sink string` in the same file for the injection only), `sink = fmt.Sprint(key)` for `SetHeader`, `sink = fmt.Sprint(value)` for `SetContentType`, `sink = fmt.Sprint(c.fctx)` for `RequestCtx` — run the matching test, record the failure text, and restore the file. Confirm with `git diff --stat` that only `alloc_test.go` changed afterwards.

- [ ] **Step 4: Full gate**

Run: `make lint && make test && make test-debug && make cover`
Expected: all exit 0; coverage ≥ 98.8%. (`alloc_test.go` is `!ricedebug`, so `make test-debug` skips these tests.)

- [ ] **Step 5: Commit**

```bash
git add alloc_test.go
git commit -m "test: budget Status, SetHeader, SetContentType and RequestCtx at zero allocations

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: The comparison module, scenarios, the GitHub table, rice's adapter and the equivalence gate

Spec D1, D2.

**Files:**
- Create: `bench/compare/go.mod`, `bench/compare/go.sum`, `bench/compare/scenarios.go`, `bench/compare/githubapi.go`, `bench/compare/targets.go`, `bench/compare/check.go`, `bench/compare/rice.go`, `bench/compare/compare_test.go`

**Interfaces:**
- Consumes: rice's public API only (`rice.New`, `GET`, `POST`, `Handle`, `FasthttpHandler`, `Serve`, `Ctx.Param`, `Ctx.String`, `Ctx.Bytes`, `Ctx.SetContentType`, `Ctx.RequestCtx`).
- Produces (package `compare`, used by every later task):
  - `type App int` with constants `Small`, `GitHub`
  - `type Scenario struct { Name string; App App; Method, Path string; Body []byte; Status int; Want string; StatusOnly bool }`
  - `func Scenarios() []Scenario`, `func ScenarioByName(name string) (Scenario, bool)`
  - `type JSONPayload struct { ID int; Name string; Tags []string }` (json tags `id`, `name`, `tags`) and the package variable `jsonPayload`
  - `type route struct { Method, Path string }`, `var githubAPI []route` (203 entries)
  - `type Server struct { HTTP http.Handler; Fasthttp fasthttp.RequestHandler; Serve func(ln net.Listener) error }`
  - `type Target struct { Name string; Build func(App) Server }`, `func Targets() []Target`, `func TargetByName(name string) (Target, bool)`
  - `func Do(srv Server, method, path string, body []byte) (status int, respBody string)`
  - `func Check(t Target, s Scenario) error`
  - `func Rice() Target`

- [ ] **Step 1: Create the module**

```bash
mkdir -p bench/compare
cat > bench/compare/go.mod <<'EOF'
module github.com/vietpham102301/rice-http/bench/compare

go 1.25.0

require github.com/vietpham102301/rice-http v0.0.0

replace github.com/vietpham102301/rice-http => ../..
EOF
```

- [ ] **Step 2: Write `scenarios.go`**

```go
// Package compare measures rice against Gin, Echo and Fiber on identical
// scenarios, at the handler level and end to end.
//
// It is a module of its own so that rice's go.mod keeps fasthttp as its only
// dependency: Gin, Echo and Fiber are required here and nowhere else. See
// docs/superpowers/specs/2026-09-19-m8-benchmark-suite-design.md.
package compare

import "bytes"

// App names which of a framework's two apps serves a scenario. The GitHub API
// table declares /users/:user, which conflicts with the small app's /users/:id
// in routers that reject two parameter names at one position, so the table is
// served by an app of its own. It also keeps the other scenarios from being
// measured inside a 203-route tree.
type App int

const (
	// Small serves every scenario except githubapi.
	Small App = iota
	// GitHub serves the 203-route GitHub API table and nothing else.
	GitHub
)

// Scenario is one request every framework must answer the same way.
type Scenario struct {
	Name   string
	App    App
	Method string
	Path   string
	Body   []byte
	Status int
	// Want is the expected response body. It is ignored when StatusOnly is set.
	Want string
	// StatusOnly marks a scenario whose body is each framework's own: the
	// default 404 differs by design between frameworks.
	StatusOnly bool
}

// JSONPayload is what the json scenario encodes.
type JSONPayload struct {
	ID   int      `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

var jsonPayload = JSONPayload{ID: 42, Name: "rice", Tags: []string{"a", "b"}}

// body64k is the body64k scenario's request body.
var body64k = bytes.Repeat([]byte("r"), 64<<10)

// githubTarget is the route the githubapi scenario requests: three parameters
// deep in a table of 203.
const githubTarget = "/repos/julienschmidt/httprouter/pulls/42/comments"

// Scenarios returns every scenario, in the order results are reported.
func Scenarios() []Scenario {
	return []Scenario{
		{Name: "static", App: Small, Method: "GET", Path: "/hello", Status: 200, Want: "hello"},
		{Name: "param", App: Small, Method: "GET", Path: "/users/42", Status: 200, Want: "42"},
		{Name: "middleware5", App: Small, Method: "GET", Path: "/mw", Status: 200, Want: "ok"},
		{Name: "notfound", App: Small, Method: "GET", Path: "/nope", Status: 404, StatusOnly: true},
		{Name: "githubapi", App: GitHub, Method: "GET", Path: githubTarget, Status: 200, Want: "ok"},
		{Name: "json", App: Small, Method: "GET", Path: "/json", Status: 200, Want: `{"id":42,"name":"rice","tags":["a","b"]}`},
		{Name: "body64k", App: Small, Method: "POST", Path: "/echo-len", Body: body64k, Status: 200, Want: "65536"},
	}
}

// ScenarioByName returns the scenario with the given name.
func ScenarioByName(name string) (Scenario, bool) {
	for _, s := range Scenarios() {
		if s.Name == name {
			return s, true
		}
	}
	return Scenario{}, false
}
```

- [ ] **Step 3: Generate `githubapi.go` from the published table**

The table is copied verbatim from julienschmidt/go-http-routing-benchmark at a pinned commit, commented-out routes excluded, order kept:

```bash
SRCVER=v0.0.0-20200726193010-d8f3b8589958
( cd "$(mktemp -d)" && go mod init tmp >/dev/null 2>&1 && go mod download github.com/julienschmidt/go-http-routing-benchmark@$SRCVER )
SRC=$(go env GOMODCACHE)/github.com/julienschmidt/go-http-routing-benchmark@$SRCVER
{
  cat <<'EOF'
package compare

// githubAPI is the GitHub API route table from
// github.com/julienschmidt/go-http-routing-benchmark (github_test.go, commit
// d8f3b8589958, 2020-07-26), copyright (c) 2013 Julien Schmidt, used under its
// BSD 3-Clause License. It is copied as published — the routes that file
// comments out are left out, and nothing is reordered or trimmed — so that no
// router's tree shape is favoured. Regenerate with the command in
// docs/superpowers/plans/2026-09-19-m8-benchmark-suite.md, Task 2.

// route is one entry of the table.
type route struct {
	Method string
	Path   string
}

var githubAPI = []route{
EOF
  awk '/githubAPI = \[\]route\{/,/^}/' "$SRC/github_test.go" | grep -E '^[[:space:]]*\{"' | sed -E 's/^[[:space:]]*/\t/'
  echo '}'
} > bench/compare/githubapi.go
gofmt -l bench/compare/githubapi.go   # expect no output
grep -c '^	{"' bench/compare/githubapi.go   # expect 203
```

- [ ] **Step 4: Write `targets.go`**

```go
package compare

import (
	"net"
	"net/http"

	"github.com/valyala/fasthttp"
)

// Server is one app of one framework, reachable in-process through exactly one
// of HTTP and Fasthttp, or over a listener through Serve.
type Server struct {
	HTTP     http.Handler
	Fasthttp fasthttp.RequestHandler
	// Serve serves on ln until the process exits.
	Serve func(ln net.Listener) error
}

// Target is one framework under comparison.
type Target struct {
	Name  string
	Build func(App) Server
}

// Targets returns every framework, in the order results are reported.
func Targets() []Target {
	return []Target{Rice()}
}

// TargetByName returns the framework with the given name.
func TargetByName(name string) (Target, bool) {
	for _, t := range Targets() {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}
```

- [ ] **Step 5: Write `check.go`**

```go
package compare

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"strings"

	"github.com/valyala/fasthttp"
)

// Do sends one request to srv in-process and returns the status and body.
func Do(srv Server, method, path string, body []byte) (int, string) {
	if srv.HTTP != nil {
		w := httptest.NewRecorder()
		srv.HTTP.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(body)))
		return w.Code, w.Body.String()
	}
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(path)
	if body != nil {
		ctx.Request.SetBody(body)
	}
	srv.Fasthttp(&ctx)
	return ctx.Response.StatusCode(), string(ctx.Response.Body())
}

// Check is the equivalence gate: it sends s to a fresh app of t and reports how
// the answer differs from what s expects. Every benchmark calls it first, so a
// broken adapter fails the run instead of producing a number for different
// work.
//
// One trailing newline is ignored: Echo's JSON encoder ends its output with
// one and the others do not, which is not a difference in the work done.
func Check(t Target, s Scenario) error {
	status, body := Do(t.Build(s.App), s.Method, s.Path, s.Body)
	if status != s.Status {
		return fmt.Errorf("%s %s: status %d, want %d", t.Name, s.Name, status, s.Status)
	}
	if s.StatusOnly {
		return nil
	}
	if got := strings.TrimSuffix(body, "\n"); got != s.Want {
		return fmt.Errorf("%s %s: body %q, want %q", t.Name, s.Name, got, s.Want)
	}
	return nil
}
```

- [ ] **Step 6: Write the failing tests, `compare_test.go`**

```go
package compare

import (
	"strings"
	"testing"
)

func TestTheGitHubTableIsThePublishedOne(t *testing.T) {
	if len(githubAPI) != 203 {
		t.Fatalf("githubAPI has %d routes, want 203", len(githubAPI))
	}
	if first := githubAPI[0]; first != (route{"GET", "/authorizations"}) {
		t.Errorf("first route = %v, want GET /authorizations", first)
	}
	if last := githubAPI[len(githubAPI)-1]; last != (route{"DELETE", "/user/keys/:id"}) {
		t.Errorf("last route = %v, want DELETE /user/keys/:id", last)
	}
}

func TestEveryFrameworkAnswersEveryScenarioAlike(t *testing.T) {
	for _, tg := range Targets() {
		for _, s := range Scenarios() {
			t.Run(tg.Name+"/"+s.Name, func(t *testing.T) {
				if err := Check(tg, s); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// TestEveryFrameworkServesTheWholeGitHubTable sends one request to every route,
// so that a route one framework silently failed to register cannot hide behind
// the githubapi scenario's single lookup.
func TestEveryFrameworkServesTheWholeGitHubTable(t *testing.T) {
	for _, tg := range Targets() {
		t.Run(tg.Name, func(t *testing.T) {
			srv := tg.Build(GitHub)
			for _, rt := range githubAPI {
				path := fillParams(rt.Path)
				if status, body := Do(srv, rt.Method, path, nil); status != 200 || body != "ok" {
					t.Errorf("%s %s: got %d %q, want 200 %q", rt.Method, path, status, body, "ok")
				}
			}
		})
	}
}

// fillParams replaces each :name segment with a concrete value.
func fillParams(pattern string) string {
	parts := strings.Split(pattern, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			parts[i] = "v" + p[1:]
		}
	}
	return strings.Join(parts, "/")
}
```

Run: `cd bench/compare && go mod tidy && go test ./... -count=1`
Expected: FAIL to compile — `undefined: Rice`.

- [ ] **Step 7: Write `rice.go`**

```go
package compare

import (
	"encoding/json"
	"strconv"

	rice "github.com/vietpham102301/rice-http"
)

// Rice is rice, used the way its documentation shows.
func Rice() Target { return Target{Name: "rice", Build: buildRice} }

func buildRice(app App) Server {
	r := rice.New()
	switch app {
	case Small:
		r.GET("/hello", func(c *rice.Ctx) error { return c.String(200, "hello") })
		r.GET("/users/:id", func(c *rice.Ctx) error { return c.Bytes(200, c.Param("id")) })
		r.GET("/mw", func(c *rice.Ctx) error { return c.String(200, "ok") },
			riceNoop, riceNoop, riceNoop, riceNoop, riceNoop)
		// rice has no JSON helper (not added in M8): the handler encodes with the
		// standard library, as a rice user would today.
		r.GET("/json", func(c *rice.Ctx) error {
			b, err := json.Marshal(jsonPayload)
			if err != nil {
				return err
			}
			c.SetContentType("application/json")
			return c.Bytes(200, b)
		})
		// rice has no body accessor: RequestCtx is the documented escape hatch.
		r.POST("/echo-len", func(c *rice.Ctx) error {
			return c.String(200, strconv.Itoa(len(c.RequestCtx().PostBody())))
		})
	case GitHub:
		for _, rt := range githubAPI {
			r.Handle(rt.Method, rt.Path, func(c *rice.Ctx) error { return c.String(200, "ok") })
		}
	}
	return Server{Fasthttp: r.FasthttpHandler(), Serve: r.Serve}
}

func riceNoop(next rice.Handler) rice.Handler {
	return func(c *rice.Ctx) error { return next(c) }
}
```

- [ ] **Step 8: Run the tests**

Run: `cd bench/compare && go mod tidy && go vet ./... && go test ./... -count=1 -v -run 'TestTheGitHubTable|TestEveryFramework' 2>&1 | tail -15`
Expected: PASS, with sub-tests `rice/static` … `rice/body64k` and `rice` for the GitHub table.

- [ ] **Step 9: Fault injection**

1. In `rice.go`, change the `/users/:id` handler to write `"43"`. Run `go test ./... -run TestEveryFrameworkAnswers`. Expected: FAIL naming `rice param: body "43", want "42"`. Restore.
2. In the `GitHub` case, skip the last route (`githubAPI[:len(githubAPI)-1]`). Run `-run TestEveryFrameworkServesTheWholeGitHubTable`. Expected: FAIL naming `DELETE /user/keys/vid`. Restore.

- [ ] **Step 10: Confirm the root module is untouched**

Run from the repo root: `git diff --exit-code go.mod go.sum && go mod tidy && git diff --exit-code go.mod go.sum && go vet ./... && make test`
Expected: exit 0 — the nested module is invisible to the root's `./...` and to `go mod tidy`.

- [ ] **Step 11: Commit**

```bash
git add bench/compare
git commit -m "bench: add the comparison module with scenarios, the GitHub table and rice's adapter

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Gin, Echo and Fiber adapters

Spec D2.

**Files:**
- Create: `bench/compare/gin.go`, `bench/compare/echo.go`, `bench/compare/fiber.go`
- Modify: `bench/compare/targets.go` (`Targets`)

**Interfaces:**
- Consumes: `App`, `Small`, `GitHub`, `Server`, `Target`, `githubAPI`, `jsonPayload` (Task 2).
- Produces: `func Gin() Target`, `func Echo() Target`, `func Fiber() Target`; `Targets()` returns `Rice(), Gin(), Echo(), Fiber()` in that order.

- [ ] **Step 1: Extend `Targets` first, so the tests fail**

In `targets.go`, change the body of `Targets` to:

```go
	return []Target{Rice(), Gin(), Echo(), Fiber()}
```

Run: `cd bench/compare && go test ./... -count=1`
Expected: FAIL to compile — `undefined: Gin`, `Echo`, `Fiber`.

- [ ] **Step 2: Write `gin.go`**

```go
package compare

import (
	"net"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Gin is Gin in release mode with no middleware: gin.New, not gin.Default,
// which would add Logger and Recovery that the others do not run.
func Gin() Target { return Target{Name: "gin", Build: buildGin} }

func buildGin(app App) Server {
	gin.SetMode(gin.ReleaseMode)
	g := gin.New()
	switch app {
	case Small:
		g.GET("/hello", func(c *gin.Context) { c.String(200, "hello") })
		// c.String with no format arguments writes the string as is.
		g.GET("/users/:id", func(c *gin.Context) { c.String(200, c.Param("id")) })
		g.GET("/mw", ginNoop, ginNoop, ginNoop, ginNoop, ginNoop,
			func(c *gin.Context) { c.String(200, "ok") })
		g.GET("/json", func(c *gin.Context) { c.JSON(200, jsonPayload) })
		g.POST("/echo-len", func(c *gin.Context) {
			b, err := c.GetRawData()
			if err != nil {
				c.AbortWithStatus(500)
				return
			}
			c.String(200, strconv.Itoa(len(b)))
		})
	case GitHub:
		for _, rt := range githubAPI {
			g.Handle(rt.Method, rt.Path, func(c *gin.Context) { c.String(200, "ok") })
		}
	}
	return Server{
		HTTP:  g,
		Serve: func(ln net.Listener) error { return (&http.Server{Handler: g}).Serve(ln) },
	}
}

func ginNoop(c *gin.Context) { c.Next() }
```

- [ ] **Step 3: Write `echo.go`**

```go
package compare

import (
	"io"
	"net"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
)

// Echo is Echo v5 with no middleware added.
func Echo() Target { return Target{Name: "echo", Build: buildEcho} }

func buildEcho(app App) Server {
	e := echo.New()
	switch app {
	case Small:
		e.GET("/hello", func(c *echo.Context) error { return c.String(200, "hello") })
		e.GET("/users/:id", func(c *echo.Context) error { return c.String(200, c.Param("id")) })
		e.GET("/mw", func(c *echo.Context) error { return c.String(200, "ok") },
			echoNoop, echoNoop, echoNoop, echoNoop, echoNoop)
		e.GET("/json", func(c *echo.Context) error { return c.JSON(200, jsonPayload) })
		e.POST("/echo-len", func(c *echo.Context) error {
			b, err := io.ReadAll(c.Request().Body)
			if err != nil {
				return err
			}
			return c.String(200, strconv.Itoa(len(b)))
		})
	case GitHub:
		for _, rt := range githubAPI {
			e.Add(rt.Method, rt.Path, func(c *echo.Context) error { return c.String(200, "ok") })
		}
	}
	return Server{
		HTTP:  e,
		Serve: func(ln net.Listener) error { return (&http.Server{Handler: e}).Serve(ln) },
	}
}

func echoNoop(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error { return next(c) }
}
```

- [ ] **Step 4: Write `fiber.go`**

```go
package compare

import (
	"net"
	"strconv"

	"github.com/gofiber/fiber/v3"
)

// Fiber is Fiber v3 with its default configuration.
func Fiber() Target { return Target{Name: "fiber", Build: buildFiber} }

func buildFiber(app App) Server {
	f := fiber.New()
	switch app {
	case Small:
		f.Get("/hello", func(c fiber.Ctx) error { return c.SendString("hello") })
		f.Get("/users/:id", func(c fiber.Ctx) error { return c.SendString(c.Params("id")) })
		// Fiber runs a route's handlers in order: the five no-ops, then the handler.
		f.Get("/mw", fiberNoop, fiberNoop, fiberNoop, fiberNoop, fiberNoop,
			func(c fiber.Ctx) error { return c.SendString("ok") })
		f.Get("/json", func(c fiber.Ctx) error { return c.JSON(jsonPayload) })
		f.Post("/echo-len", func(c fiber.Ctx) error {
			return c.SendString(strconv.Itoa(len(c.Body())))
		})
	case GitHub:
		for _, rt := range githubAPI {
			f.Add([]string{rt.Method}, rt.Path, func(c fiber.Ctx) error { return c.SendString("ok") })
		}
	}
	return Server{
		Fasthttp: f.Handler(),
		Serve: func(ln net.Listener) error {
			return f.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
		},
	}
}

func fiberNoop(c fiber.Ctx) error { return c.Next() }
```

- [ ] **Step 5: Pin the versions and run the tests**

```bash
cd bench/compare
go get github.com/gin-gonic/gin@v1.12.0 github.com/labstack/echo/v5@v5.3.1 github.com/gofiber/fiber/v3@v3.5.0
go mod tidy
grep 'github.com/valyala/fasthttp ' go.mod   # must show v1.73.0 — otherwise stop and report (Global Constraints)
go vet ./... && go test ./... -count=1 -v -run 'TestEveryFramework' 2>&1 | grep -E '^(=== RUN|--- FAIL|ok|FAIL)' | tail -40
```

Expected: PASS for all 28 `<framework>/<scenario>` sub-tests and the 4 GitHub-table sub-tests.

- [ ] **Step 6: Fault injection**

1. In `echo.go`, change `/json` to `c.JSON(200, map[string]int{"id": 42})`. Run `-run TestEveryFrameworkAnswers`. Expected: FAIL naming `echo json`. Restore.
2. In `fiber.go`, remove one `fiberNoop` from `/mw` — the test still passes (a no-op changes no output). Record that: the equivalence gate checks answers, not the amount of work, so the middleware count is guarded by code review of these adapters, not by a test. (No restore needed if you revert immediately; restore anyway.)
3. In `gin.go`, register the GitHub table with `rt.Path + "x"`. Run `-run TestEveryFrameworkServesTheWholeGitHubTable`. Expected: FAIL for `gin`. Restore.

- [ ] **Step 7: Root gate**

Run from the root: `git diff --exit-code go.mod go.sum && make lint && make test`
Expected: exit 0.

- [ ] **Step 8: Commit**

```bash
git add bench/compare
git commit -m "bench: add Gin, Echo and Fiber adapters to the comparison

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Handler-level benchmarks and their recording script

Spec D3, D6 (handler part).

**Files:**
- Create: `bench/compare/discardwriter_test.go`, `bench/compare/handler_bench_test.go`, `bench/compare/scripts/header.sh`, `bench/compare/scripts/handler.sh`

**Interfaces:**
- Consumes: `Scenarios`, `Targets`, `Check`, `Server` (Tasks 2–3).
- Produces: `BenchmarkHandler/<scenario>/<framework>`; `scripts/header.sh <label>` (prints the results header, used again by Task 6); `scripts/handler.sh` (writes `bench/results/M8-compare-handler.txt`).

- [ ] **Step 1: Write `discardwriter_test.go`**

```go
package compare

import (
	"bytes"
	"net/http"
)

// discardWriter is an http.ResponseWriter that keeps a reusable header map and
// the status, and throws the body away. httptest.ResponseRecorder is not used
// in benchmarks: it grows a buffer per response, and those allocations would
// be charged to the framework.
type discardWriter struct {
	header http.Header
	status int
	n      int
}

func newDiscardWriter() *discardWriter { return &discardWriter{header: http.Header{}} }

func (w *discardWriter) Header() http.Header { return w.header }

func (w *discardWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *discardWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.n += len(b)
	return len(b), nil
}

// reset prepares w for the next request without allocating: clear keeps the
// map's buckets.
func (w *discardWriter) reset() {
	clear(w.header)
	w.status = 0
	w.n = 0
}

// rewindBody is a request body that can be re-armed without allocating.
type rewindBody struct{ bytes.Reader }

func (*rewindBody) Close() error { return nil }
```

- [ ] **Step 2: Write `handler_bench_test.go`**

```go
package compare

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/valyala/fasthttp"
)

// BenchmarkHandler measures each framework's own cost: routing, context and
// response code on top of an already-parsed request, through the framework's
// in-process entry point. net/http frameworks (Gin, Echo) and fasthttp ones
// (Fiber, rice) are driven through different request objects, so this level
// excludes parsing for both and compares the rest; the end-to-end level exists
// because of that.
func BenchmarkHandler(b *testing.B) {
	for _, s := range Scenarios() {
		for _, tg := range Targets() {
			b.Run(s.Name+"/"+tg.Name, func(b *testing.B) {
				if err := Check(tg, s); err != nil {
					b.Fatalf("equivalence gate: %v", err)
				}
				srv := tg.Build(s.App)
				if srv.HTTP != nil {
					benchHTTP(b, srv.HTTP, s)
				} else {
					benchFasthttp(b, srv.Fasthttp, s)
				}
			})
		}
	}
}

func benchHTTP(b *testing.B, h http.Handler, s Scenario) {
	body := &rewindBody{}
	req := httptest.NewRequest(s.Method, s.Path, nil)
	if s.Body != nil {
		req.Body = body
		req.ContentLength = int64(len(s.Body))
	}
	w := newDiscardWriter()
	serve := func() {
		if s.Body != nil {
			body.Reset(s.Body)
		}
		w.reset()
		h.ServeHTTP(w, req)
	}

	serve() // warm
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		serve()
	}
	b.StopTimer()
	if w.status != s.Status {
		b.Fatalf("last response: status %d, want %d", w.status, s.Status)
	}
}

func benchFasthttp(b *testing.B, h fasthttp.RequestHandler, s Scenario) {
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(s.Method)
	ctx.Request.SetRequestURI(s.Path)
	if s.Body != nil {
		ctx.Request.SetBody(s.Body)
	}

	h(&ctx) // warm
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(&ctx)
	}
	b.StopTimer()
	if got := ctx.Response.StatusCode(); got != s.Status {
		b.Fatalf("last response: status %d, want %d", got, s.Status)
	}
}
```

- [ ] **Step 3: Smoke-run it**

Run: `cd bench/compare && go test . -run '^$' -bench '^BenchmarkHandler$' -benchmem -benchtime=100ms -count=1 2>&1 | tail -32`
Expected: 28 result lines, `BenchmarkHandler/static/rice-N` through `BenchmarkHandler/body64k/fiber-N`, each with ns/op, B/op, allocs/op. `static/rice`, `param/rice`, `middleware5/rice` and `notfound/rice` report `0 allocs/op` (rice's measured budgets). If any rice row in those four shows an allocation, stop and report.

- [ ] **Step 4: Fault injection — the gate guards the benchmark**

Temporarily change `Want` of the `static` scenario to `"hellO"`. Run the smoke command filtered to `-bench 'BenchmarkHandler/static'`. Expected: every `static/<framework>` benchmark FAILS with `equivalence gate: … body "hello", want "hellO"`. Restore.

- [ ] **Step 5: Write `scripts/header.sh`**

```bash
#!/usr/bin/env bash
# Prints the header of a comparison results file: the same fields as
# scripts/bench.sh at the repo root, plus every compared module's version.
# usage: scripts/header.sh <label>   (run from anywhere)
set -euo pipefail

label="${1:?usage: scripts/header.sh <label>}"
cd "$(dirname "$0")/.."

echo "# rice benchmark results: ${label}"
echo "# date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "# go:   $(go version)"
echo "# os:   $(uname -srm)"
if [ "$(uname -s)" = "Darwin" ]; then
  echo "# cpu:  $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
else
  echo "# cpu:  $(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2- | sed 's/^ *//' || echo unknown)"
fi
echo "# rice: $(git -C ../.. rev-parse --short HEAD) (local, via replace)"
for m in github.com/gin-gonic/gin github.com/labstack/echo/v5 github.com/gofiber/fiber/v3 github.com/valyala/fasthttp; do
  echo "# $(go list -m "$m")"
done
```

- [ ] **Step 6: Write `scripts/handler.sh`**

```bash
#!/usr/bin/env bash
# Records the handler-level comparison to bench/results/M8-compare-handler.txt.
# usage: bench/compare/scripts/handler.sh
set -euo pipefail

cd "$(dirname "$0")/.."
out=../results/M8-compare-handler.txt

{
  ./scripts/header.sh M8-compare-handler
  echo
  go test . -run '^$' -bench '^BenchmarkHandler$' -benchmem -count=10
} | tee "${out}"

echo "wrote bench/results/M8-compare-handler.txt"
```

Run: `chmod +x bench/compare/scripts/*.sh && bench/compare/scripts/header.sh smoke`
Expected: eight `#` lines; the module lines read `github.com/gin-gonic/gin v1.12.0`, `github.com/labstack/echo/v5 v5.3.1`, `github.com/gofiber/fiber/v3 v3.5.0`, `github.com/valyala/fasthttp v1.73.0`. Do **not** run `handler.sh` yet — Task 8 records.

- [ ] **Step 7: Root gate and commit**

Run: `git diff --exit-code go.mod go.sum && make lint && make test`

```bash
git add bench/compare
git commit -m "bench: add handler-level comparison benchmarks and their recording script

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: The load generator

Spec D4 (client side).

**Files:**
- Create: `bench/compare/histogram.go`, `bench/compare/loadgen.go`, `bench/compare/loadgen_test.go`, `bench/compare/cmd/loadgen/main.go`

**Interfaces:**
- Consumes: `Scenario`, `ScenarioByName`, `Scenarios`, `Rice`, `Small` (Task 2).
- Produces:
  - `type Histogram struct` with `Record(d time.Duration)`, `Merge(o *Histogram)`, `Count() uint64`, `Quantile(q float64) time.Duration`
  - `type LoadConfig struct { Addr string; Scenario Scenario; Conns int; Warmup, Duration time.Duration }`
  - `type LoadResult struct { Requests, Mismatches, Errors uint64; RPS float64; P50, P99 time.Duration }`
  - `func Load(cfg LoadConfig) (LoadResult, error)` — error when any response mismatched or failed
  - `func WaitReady(addr string, timeout time.Duration) error`
  - binary `cmd/loadgen`, flags `-addr -scenario -framework -round -conns -warmup -duration -list`; on success prints one line `<framework> <scenario> <round> <rps> <p50µs> <p99µs>`; `-list` prints scenario names one per line.

- [ ] **Step 1: Write the failing tests, `loadgen_test.go`**

```go
package compare

import (
	"net"
	"strings"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

func TestHistogramQuantilesAreWithinOneSixteenth(t *testing.T) {
	var h Histogram
	for i := 1; i <= 1000; i++ {
		h.Record(time.Duration(i) * time.Microsecond)
	}
	if h.Count() != 1000 {
		t.Fatalf("Count() = %d, want 1000", h.Count())
	}
	for _, c := range []struct {
		q    float64
		want time.Duration
	}{{0.50, 500 * time.Microsecond}, {0.99, 990 * time.Microsecond}} {
		got := h.Quantile(c.q)
		if got > c.want || got < c.want-c.want/16 {
			t.Errorf("Quantile(%v) = %v, want within 1/16 below %v", c.q, got, c.want)
		}
	}
}

func TestHistogramRecordDoesNotAllocate(t *testing.T) {
	var h Histogram
	if got := testing.AllocsPerRun(1000, func() { h.Record(123 * time.Microsecond) }); got != 0 {
		t.Errorf("Record allocates %v times, want 0", got)
	}
}

func TestHistogramMerge(t *testing.T) {
	var a, b Histogram
	a.Record(time.Millisecond)
	b.Record(time.Millisecond)
	b.Record(time.Millisecond)
	a.Merge(&b)
	if a.Count() != 3 {
		t.Errorf("Count() after Merge = %d, want 3", a.Count())
	}
}

// serveRice starts rice's small app on an ephemeral port for the load tests.
func serveRice(t *testing.T, status int) string {
	t.Helper()
	r := rice.New()
	r.GET("/hello", func(c *rice.Ctx) error { return c.String(status, "hello") })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = r.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	if err := WaitReady(ln.Addr().String(), 2*time.Second); err != nil {
		t.Fatal(err)
	}
	return ln.Addr().String()
}

func TestLoadCountsRequestsAndLatencies(t *testing.T) {
	addr := serveRice(t, 200)
	s, _ := ScenarioByName("static")
	res, err := Load(LoadConfig{Addr: addr, Scenario: s, Conns: 4, Warmup: 50 * time.Millisecond, Duration: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.Requests == 0 || res.RPS <= 0 {
		t.Errorf("Requests = %d, RPS = %v, want both positive", res.Requests, res.RPS)
	}
	if res.P50 <= 0 || res.P99 < res.P50 {
		t.Errorf("P50 = %v, P99 = %v, want 0 < P50 <= P99", res.P50, res.P99)
	}
}

func TestLoadFailsOnAStatusMismatch(t *testing.T) {
	addr := serveRice(t, 500)
	s, _ := ScenarioByName("static")
	res, err := Load(LoadConfig{Addr: addr, Scenario: s, Conns: 2, Warmup: 0, Duration: 100 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("Load returned %v, want a mismatch error", err)
	}
	if res.Mismatches == 0 {
		t.Error("Mismatches = 0, want the 500s counted")
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens there now
	start := time.Now()
	if err := WaitReady(addr, 200*time.Millisecond); err == nil {
		t.Fatal("WaitReady returned nil for an address nothing listens on")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("WaitReady took %v with a 200ms timeout", elapsed)
	}
}
```

Run: `cd bench/compare && go test . -count=1 -run 'Histogram|Load|WaitReady'`
Expected: FAIL to compile — `undefined: Histogram`.

- [ ] **Step 2: Write `histogram.go`**

```go
package compare

import (
	"math"
	"math/bits"
	"time"
)

// Histogram counts durations in log-linear buckets: below 16ns one bucket per
// nanosecond, above that 16 buckets per power of two, so a recorded value is
// reported within 1/16 (6.25%) below its true value. Record never allocates,
// so the load generator's measuring path adds no garbage to the machine it is
// measuring.
type Histogram struct {
	counts [1024]uint64
	n      uint64
}

func bucketOf(ns uint64) int {
	if ns < 16 {
		return int(ns)
	}
	e := bits.Len64(ns) - 1 // e >= 4
	sub := (ns >> (e - 4)) & 15
	return (e-3)*16 + int(sub)
}

// lowerBound is the smallest value in bucket i.
func lowerBound(i int) uint64 {
	if i < 16 {
		return uint64(i)
	}
	e := i/16 + 3
	sub := uint64(i % 16)
	return (16 + sub) << (e - 4)
}

// Record adds one duration. Negative durations count as zero.
func (h *Histogram) Record(d time.Duration) {
	if d < 0 {
		d = 0
	}
	h.counts[bucketOf(uint64(d))]++
	h.n++
}

// Merge adds every count of o to h.
func (h *Histogram) Merge(o *Histogram) {
	for i, c := range o.counts {
		h.counts[i] += c
	}
	h.n += o.n
}

// Count is the number of recorded durations.
func (h *Histogram) Count() uint64 { return h.n }

// Quantile returns the lower bound of the bucket holding the q-th quantile, or
// zero for an empty histogram.
func (h *Histogram) Quantile(q float64) time.Duration {
	if h.n == 0 {
		return 0
	}
	target := uint64(math.Ceil(q * float64(h.n)))
	if target == 0 {
		target = 1
	}
	var seen uint64
	for i, c := range h.counts {
		seen += c
		if seen >= target {
			return time.Duration(lowerBound(i))
		}
	}
	return time.Duration(lowerBound(len(h.counts) - 1))
}
```

- [ ] **Step 3: Write `loadgen.go`**

```go
package compare

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

// LoadConfig is one closed-loop run against one server.
type LoadConfig struct {
	Addr     string
	Scenario Scenario
	// Conns keep-alive connections, each sending one request at a time.
	Conns int
	// Warmup is run and discarded before Duration is measured.
	Warmup   time.Duration
	Duration time.Duration
}

// LoadResult is what a run measured. Requests, RPS and the percentiles cover
// the measured window only; Mismatches and Errors cover the whole run.
type LoadResult struct {
	Requests   uint64
	Mismatches uint64
	Errors     uint64
	RPS        float64
	P50        time.Duration
	P99        time.Duration
}

type worker struct {
	hist       Histogram
	requests   uint64
	mismatches uint64
	errors     uint64
}

// Load drives cfg.Addr with cfg.Conns closed-loop connections: each sends a
// request, waits for the response, and sends the next. It returns an error if
// any response had a status other than the scenario's or failed outright, so
// that a run which measured different work is never reported as a number.
func Load(cfg LoadConfig) (LoadResult, error) {
	if cfg.Conns < 1 {
		return LoadResult{}, fmt.Errorf("conns must be at least 1, got %d", cfg.Conns)
	}
	hc := &fasthttp.HostClient{
		Addr:                cfg.Addr,
		MaxConns:            cfg.Conns,
		ReadTimeout:         10 * time.Second,
		WriteTimeout:        10 * time.Second,
		MaxIdleConnDuration: time.Minute,
	}
	s := cfg.Scenario
	measureFrom := time.Now().Add(cfg.Warmup)
	end := measureFrom.Add(cfg.Duration)

	workers := make([]worker, cfg.Conns)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(w *worker) {
			defer wg.Done()
			req := fasthttp.AcquireRequest()
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseRequest(req)
			defer fasthttp.ReleaseResponse(resp)
			req.Header.SetMethod(s.Method)
			req.SetRequestURI("http://" + cfg.Addr + s.Path)
			if s.Body != nil {
				req.SetBody(s.Body)
			}
			for {
				t0 := time.Now()
				if !t0.Before(end) {
					return
				}
				err := hc.Do(req, resp)
				t1 := time.Now()
				if err != nil {
					w.errors++
					continue
				}
				if resp.StatusCode() != s.Status {
					w.mismatches++
				}
				if !t0.Before(measureFrom) {
					w.hist.Record(t1.Sub(t0))
					w.requests++
				}
			}
		}(&workers[i])
	}
	wg.Wait()

	var res LoadResult
	var all Histogram
	for i := range workers {
		all.Merge(&workers[i].hist)
		res.Requests += workers[i].requests
		res.Mismatches += workers[i].mismatches
		res.Errors += workers[i].errors
	}
	if cfg.Duration > 0 {
		res.RPS = float64(res.Requests) / cfg.Duration.Seconds()
	}
	res.P50 = all.Quantile(0.50)
	res.P99 = all.Quantile(0.99)
	if res.Mismatches > 0 || res.Errors > 0 {
		return res, fmt.Errorf("%s: %d status mismatches, %d errors", s.Name, res.Mismatches, res.Errors)
	}
	return res, nil
}

// WaitReady dials addr until it accepts a connection or timeout passes.
func WaitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			return conn.Close()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not accept a connection within %v: %w", addr, timeout, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `cd bench/compare && go test . -race -count=1 -run 'Histogram|Load|WaitReady' -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: all six PASS.

- [ ] **Step 5: Fault injection**

1. In `Load`, delete the `if res.Mismatches > 0 || res.Errors > 0 { … }` block. Run `-run TestLoadFailsOnAStatusMismatch`. Expected: FAIL `want a mismatch error`. Restore.
2. In `bucketOf`, change `e - 4` to `e - 3` in the `sub` line. Run `-run TestHistogramQuantiles`. Expected: FAIL. Restore.

- [ ] **Step 6: Write `cmd/loadgen/main.go`**

```go
// Command loadgen drives one server with one scenario and prints one result
// line: framework, scenario, round, requests per second, p50 and p99 in
// microseconds. It exits non-zero if the server is not ready or any response
// differs from the scenario's status.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/vietpham102301/rice-http/bench/compare"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "server address")
	scenario := flag.String("scenario", "static", "scenario name")
	framework := flag.String("framework", "", "framework label for the result line")
	round := flag.Int("round", 1, "round number for the result line")
	conns := flag.Int("conns", 64, "keep-alive connections")
	warmup := flag.Duration("warmup", 5*time.Second, "warm-up, not measured")
	duration := flag.Duration("duration", 10*time.Second, "measured duration")
	list := flag.Bool("list", false, "print scenario names and exit")
	flag.Parse()

	if *list {
		for _, s := range compare.Scenarios() {
			fmt.Println(s.Name)
		}
		return
	}
	s, ok := compare.ScenarioByName(*scenario)
	if !ok {
		fmt.Fprintf(os.Stderr, "loadgen: unknown scenario %q\n", *scenario)
		os.Exit(2)
	}
	if err := compare.WaitReady(*addr, 10*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "loadgen: %v\n", err)
		os.Exit(1)
	}
	res, err := compare.Load(compare.LoadConfig{Addr: *addr, Scenario: s, Conns: *conns, Warmup: *warmup, Duration: *duration})
	if err != nil {
		fmt.Fprintf(os.Stderr, "loadgen: %s %s: %v\n", *framework, *scenario, err)
		os.Exit(1)
	}
	fmt.Printf("%s %s %d %.0f %d %d\n", *framework, s.Name, *round, res.RPS, res.P50.Microseconds(), res.P99.Microseconds())
}
```

Run: `cd bench/compare && go vet ./... && go build ./cmd/loadgen && ./loadgen -list && rm loadgen`
Expected: the seven scenario names, one per line.

- [ ] **Step 7: Commit**

```bash
git add bench/compare
git commit -m "bench: add the comparison's closed-loop load generator

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: The server binary, the summariser and the end-to-end script

Spec D4 (server side, rounds, results file).

**Files:**
- Create: `bench/compare/cmd/server/main.go`, `bench/compare/summarize.go`, `bench/compare/cmd/summarize/main.go`, `bench/compare/scripts/e2e.sh`
- Modify: `bench/compare/loadgen_test.go` (append the `Summarize` test)

**Interfaces:**
- Consumes: `TargetByName`, `ScenarioByName`, `Scenarios`, `Targets`, `Server.Serve` (Tasks 2–3); `cmd/loadgen` and `scripts/header.sh` (Tasks 4–5).
- Produces:
  - binary `cmd/server`, flags `-framework -scenario -addr`: serves the scenario's app of that framework until killed
  - `func Summarize(r io.Reader, w io.Writer) error` — input lines `<framework> <scenario> <round> <rps> <p50µs> <p99µs>`; output one table per scenario (in `Scenarios()` order), one row per framework (in `Targets()` order): median req/s, min–max req/s, median p50 µs, median p99 µs
  - `scripts/e2e.sh` — writes `bench/results/M8-compare-e2e.txt`

- [ ] **Step 1: Write the failing `Summarize` test**

Append to `loadgen_test.go` (add `"bytes"` to its imports):

```go
func TestSummarizeTakesTheMedianPerFrameworkAndScenario(t *testing.T) {
	in := strings.Join([]string{
		"rice static 1 100 10 20",
		"rice static 2 300 30 60",
		"rice static 3 200 20 40",
		"gin static 1 50 5 9",
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Summarize(strings.NewReader(in), &out); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"## static",
		"| rice | 200 | 100–300 | 20 | 40 |",
		"| gin | 50 | 50–50 | 5 | 9 |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "## param") {
		t.Errorf("output has a table for a scenario with no samples:\n%s", got)
	}
}

func TestSummarizeRejectsAMalformedLine(t *testing.T) {
	if err := Summarize(strings.NewReader("rice static one 100 10 20\n"), &bytes.Buffer{}); err == nil {
		t.Error("Summarize accepted a round that is not a number")
	}
}
```

Run: `cd bench/compare && go test . -count=1 -run Summarize`
Expected: FAIL to compile — `undefined: Summarize`.

- [ ] **Step 2: Write `summarize.go`**

```go
package compare

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

type sample struct {
	rps      float64
	p50, p99 float64
}

// Summarize reads end-to-end result lines, one per round:
//
//	<framework> <scenario> <round> <rps> <p50µs> <p99µs>
//
// and writes one Markdown table per scenario that has samples, in Scenarios()
// order, with one row per framework in Targets() order: the median req/s, its
// min–max across rounds, and the median p50 and p99 in microseconds.
func Summarize(r io.Reader, w io.Writer) error {
	samples := map[string]map[string][]sample{} // scenario → framework → rounds
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) != 6 {
			return fmt.Errorf("line %d: want 6 fields, got %d: %q", line, len(f), sc.Text())
		}
		var nums [4]float64
		if _, err := strconv.Atoi(f[2]); err != nil {
			return fmt.Errorf("line %d: round %q: %w", line, f[2], err)
		}
		for i, s := range f[3:] {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return fmt.Errorf("line %d: field %d %q: %w", line, i+4, s, err)
			}
			nums[i] = v
		}
		fw, scen := f[0], f[1]
		if samples[scen] == nil {
			samples[scen] = map[string][]sample{}
		}
		samples[scen][fw] = append(samples[scen][fw], sample{rps: nums[0], p50: nums[1], p99: nums[2]})
	}
	if err := sc.Err(); err != nil {
		return err
	}

	for _, s := range Scenarios() {
		byFw := samples[s.Name]
		if len(byFw) == 0 {
			continue
		}
		fmt.Fprintf(w, "## %s\n\n", s.Name)
		fmt.Fprintln(w, "| framework | req/s (median) | req/s (min–max) | p50 µs (median) | p99 µs (median) |")
		fmt.Fprintln(w, "| --- | ---: | --- | ---: | ---: |")
		for _, t := range Targets() {
			rounds := byFw[t.Name]
			if len(rounds) == 0 {
				continue
			}
			rps := pick(rounds, func(x sample) float64 { return x.rps })
			fmt.Fprintf(w, "| %s | %.0f | %.0f–%.0f | %.0f | %.0f |\n", t.Name,
				median(rps), rps[0], rps[len(rps)-1],
				median(pick(rounds, func(x sample) float64 { return x.p50 })),
				median(pick(rounds, func(x sample) float64 { return x.p99 })))
		}
		fmt.Fprintln(w)
	}
	return nil
}

// pick returns field of every sample, sorted ascending.
func pick(xs []sample, field func(sample) float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = field(x)
	}
	sort.Float64s(out)
	return out
}

// median of a sorted, non-empty slice.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
```

Run: `cd bench/compare && go test . -count=1 -run Summarize -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: both PASS.

- [ ] **Step 3: Write `cmd/summarize/main.go`**

```go
// Command summarize turns loadgen result lines on stdin into the Markdown
// tables of bench/results/M8-compare-e2e.txt on stdout.
package main

import (
	"fmt"
	"os"

	"github.com/vietpham102301/rice-http/bench/compare"
)

func main() {
	if err := compare.Summarize(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "summarize: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Write `cmd/server/main.go`**

```go
// Command server serves one framework's app for one scenario over TCP until it
// is killed. Every framework is served by its own server: rice's Serve, Gin's
// and Echo's net/http.Server, Fiber's Listener.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/vietpham102301/rice-http/bench/compare"
)

func main() {
	framework := flag.String("framework", "rice", "rice, gin, echo or fiber")
	scenario := flag.String("scenario", "static", "scenario whose app to serve")
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	flag.Parse()

	t, ok := compare.TargetByName(*framework)
	if !ok {
		fmt.Fprintf(os.Stderr, "server: unknown framework %q\n", *framework)
		os.Exit(2)
	}
	s, ok := compare.ScenarioByName(*scenario)
	if !ok {
		fmt.Fprintf(os.Stderr, "server: unknown scenario %q\n", *scenario)
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
	if err := t.Build(s.App).Serve(ln); err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Write `scripts/e2e.sh`**

```bash
#!/usr/bin/env bash
# Records the end-to-end comparison to bench/results/M8-compare-e2e.txt.
#
# Each framework × scenario runs in its own server process with half the CPUs;
# the load generator gets the other half. Frameworks are interleaved within each
# round, and the order rotates between rounds, so drift over the run hits all
# four alike. Server and client still share memory bandwidth and caches.
#
# usage: bench/compare/scripts/e2e.sh
#   env: ROUNDS (5), WARMUP (5s), DURATION (10s), CONNS (64)
# With the defaults this takes about 7 × 4 × 5 × 15s ≈ 35 minutes.
set -euo pipefail

cd "$(dirname "$0")/.."
out=../results/M8-compare-e2e.txt
rounds="${ROUNDS:-5}"
warmup="${WARMUP:-5s}"
duration="${DURATION:-10s}"
conns="${CONNS:-64}"

bin="$(mktemp -d)"
trap 'rm -rf "${bin}"' EXIT
go build -o "${bin}/server" ./cmd/server
go build -o "${bin}/loadgen" ./cmd/loadgen
go build -o "${bin}/summarize" ./cmd/summarize

ncpu="$(getconf _NPROCESSORS_ONLN)"
half=$(( ncpu / 2 ))
[ "${half}" -ge 1 ] || half=1

frameworks=(rice gin echo fiber)
raw="${bin}/raw.txt"
: > "${raw}"
port=18000

for (( round = 1; round <= rounds; round++ )); do
  for scenario in $("${bin}/loadgen" -list); do
    for (( k = 0; k < ${#frameworks[@]}; k++ )); do
      fw="${frameworks[$(( (k + round - 1) % ${#frameworks[@]} ))]}"
      port=$(( port + 1 ))
      addr="127.0.0.1:${port}"
      GOMAXPROCS="${half}" "${bin}/server" -framework "${fw}" -scenario "${scenario}" -addr "${addr}" &
      pid=$!
      if ! GOMAXPROCS="${half}" "${bin}/loadgen" -addr "${addr}" -scenario "${scenario}" -framework "${fw}" \
          -round "${round}" -conns "${conns}" -warmup "${warmup}" -duration "${duration}" >> "${raw}"; then
        kill "${pid}" 2>/dev/null || true
        echo "e2e: ${fw} ${scenario} round ${round} failed" >&2
        exit 1
      fi
      kill "${pid}" 2>/dev/null || true
      wait "${pid}" 2>/dev/null || true
      echo "e2e: round ${round} ${scenario} ${fw} done" >&2
    done
  done
done

{
  ./scripts/header.sh M8-compare-e2e
  echo "# cpus: ${ncpu} (server GOMAXPROCS=${half}, loadgen GOMAXPROCS=${half})"
  echo "# load: ${conns} keep-alive connections, closed loop, warmup ${warmup}, measured ${duration}, ${rounds} rounds"
  echo
  "${bin}/summarize" < "${raw}"
  echo "## raw"
  echo
  echo '```'
  cat "${raw}"
  echo '```'
} | tee "${out}"

echo "wrote bench/results/M8-compare-e2e.txt" >&2
```

- [ ] **Step 6: Short dry run of the script**

Run: `chmod +x bench/compare/scripts/e2e.sh && ROUNDS=1 WARMUP=200ms DURATION=300ms CONNS=8 bench/compare/scripts/e2e.sh > /dev/null && head -20 bench/results/M8-compare-e2e.txt; rm -f bench/results/M8-compare-e2e.txt`
Expected: finishes in well under a minute with 28 `done` lines on stderr; the file has the header, a `## static` table with four rows, and a raw section. The dry-run file is deleted — Task 8 records the real one.

- [ ] **Step 7: Fault injection — the script fails loudly**

Temporarily change `rice.go`'s `/hello` to answer status 201. Run the dry run from Step 6. Expected: exits non-zero with `e2e: rice static round 1 failed` and `loadgen: rice static: … status mismatches`. Restore, and confirm no server process is left running (`pgrep -f 'bench/compare.*server|/server -framework'` prints nothing, or check `ps`).

- [ ] **Step 8: Gates and commit**

Run: `cd bench/compare && go vet ./... && go test ./... -count=1 && cd ../.. && git diff --exit-code go.mod go.sum && make lint && make test`

```bash
git add bench/compare
git commit -m "bench: add the comparison server, summariser and end-to-end script

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Make targets and CI

Spec D6.

**Files:**
- Modify: `Makefile`, `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `bench/compare` tests and benchmarks, `scripts/handler.sh`, `scripts/e2e.sh`.
- Produces: `make compare`, `make compare-record`.

- [ ] **Step 1: Add the targets**

In `Makefile`, change the `.PHONY` line to include the new targets:

```make
.PHONY: test test-debug bench bench-record cover lint tidy compare compare-record
```

and add after the `bench-record` target:

```make
## compare: the framework comparison's equivalence gate, vet, and a handler-level smoke run
compare:
	cd bench/compare && $(GO) vet ./... && $(GO) test ./... -count=1 -bench . -benchtime=100ms

## compare-record: record both comparison results files (about 40 minutes)
compare-record:
	./bench/compare/scripts/handler.sh
	./bench/compare/scripts/e2e.sh
```

Run: `make compare 2>&1 | tail -5`
Expected: exit 0, ending in `ok  	github.com/vietpham102301/rice-http/bench/compare`.

- [ ] **Step 2: Add the CI steps**

In `.github/workflows/ci.yml`, after the `Benchmark smoke run` step, add:

```yaml
      - name: Verify bench/compare go.mod is tidy
        working-directory: bench/compare
        run: |
          go mod tidy
          git diff --exit-code go.mod go.sum

      - name: Framework comparison (equivalence gate and smoke run)
        run: make compare
```

- [ ] **Step 3: Check the CI steps locally**

Run: `(cd bench/compare && go mod tidy && git diff --exit-code go.mod go.sum) && make lint && make test && make test-debug && make cover && make bench > /dev/null && make compare > /dev/null && echo ALL-OK`
Expected: `ALL-OK`; coverage ≥ 98.8%.

- [ ] **Step 4: Commit**

```bash
git add Makefile .github/workflows/ci.yml
git commit -m "build: add make compare and compare-record, and run the comparison gate in CI

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Record the comparison

Spec Benchmarks, exit criteria 1–3.

**Files:**
- Create (generated): `bench/results/M8-compare-handler.txt`, `bench/results/M8-compare-e2e.txt`

**Interfaces:**
- Consumes: `make compare-record` (Task 7).
- Produces: the two results files; a task report with the numbers Task 9 quotes (per scenario: benchstat-style median ns/op, B/op, allocs/op per framework; median req/s, p50, p99 per framework).

- [ ] **Step 1: Prepare the machine**

Close other CPU-heavy programs. Plug in power (laptops throttle on battery). Note the machine state in the report. Do not run anything else while recording.

- [ ] **Step 2: Record**

Run: `make compare-record` with a timeout of at least 60 minutes (run it in the background and wait on it; do not poll with short sleeps).
Expected: both files written; exit 0.

- [ ] **Step 3: Check the files**

- `M8-compare-handler.txt`: header with the four module versions; 280 `BenchmarkHandler/...` lines (7 scenarios × 4 frameworks × 10).
- `M8-compare-e2e.txt`: header with CPU split and load settings; seven `## <scenario>` tables with four rows each; a raw section with 140 lines.
- Run `benchstat bench/results/M8-compare-handler.txt` (benchstat is at `$(go env GOPATH)/bin/benchstat`) and put the full output in the task report.

If any rice row of `static`, `param`, `middleware5` or `notfound` shows a non-zero allocs/op, or any e2e line is missing, stop and report instead of committing.

- [ ] **Step 4: Commit**

```bash
git add bench/results/M8-compare-handler.txt bench/results/M8-compare-e2e.txt
git commit -m "bench: record rice against Gin, Echo and Fiber, handler level and end to end

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Documentation, the M8 retrospective and the project retrospective

Spec D5 (document part), D7. Read first: `docs/milestones/M7-lifecycle.md`, `docs/milestones/TEMPLATE.md`, the top two entries of `docs/progress.md`, every file in `docs/milestones/`, and `docs/adr/README.md` — match their structure and voice.

**Files:**
- Modify: `docs/05-performance-model.md`, `docs/04-roadmap.md`, `README.md`, `docs/README.md`, `bench/results/README.md`, `docs/progress.md`
- Create: `docs/milestones/M8-benchmark-suite.md`, `docs/07-retrospective.md`

**Interfaces:**
- Consumes: the Task 8 report and both results files; the Task 1 test names; the task reports of Tasks 1–7 (fault-injection texts).
- Produces: documentation only.

- [ ] **Step 1: The performance model**

In `docs/05-performance-model.md`:

1. Preamble: replace "Numbers marked TARGET are design commitments not yet verified…" with a sentence saying every number is now measured and none is a target, and that "not implemented" marks accessors that do not exist.
2. Replace the "Allocation budgets" table with three tables (spec D5):
   - **Request path — `Ctx` methods**: one row per exported `Ctx` method — `Method`, `Path`, `Param`, `ParamString`, `Status`, `SetHeader`, `SetContentType`, `String`, `Bytes`, `RequestCtx`, `Set` (pointer value), `Set` (non-constant string), `Get` — with budget, status (`MEASURED Mn`) and the enforcing test name, plus rows for `c.Query`, `c.Header`, `c.JSON` marked "not implemented — see the roadmap's Explicitly deferred". Get each existing test name with `grep -n "func TestAllocBudget" alloc_test.go lifecycle_internal_test.go method_test.go`.
   - **Request path — dispatch**: the existing dispatch, pool, lookup, chain, recovery, `ConnState`, 404, `HTTPError`, panic and end-to-end rows, unchanged in value.
   - **Not on the request path**: one row listing registration (`Handle`, the verb helpers, `Use`, `Group` and its methods), `Build`, lifecycle (`Run`, `Serve`, `RunContext`, `Shutdown`, `Addr`, `OnStart`, `OnShutdown`) and the options, marked "not budgeted — runs once per App".
   Keep the paragraphs below the old table; update "most recently `bench/results/M7-lifecycle.txt`" to say the root suite was not re-recorded in M8 because no root code changed.
3. Add a section **"Where rice stands"** after "What the lifecycle costs": both comparison tables (handler: median ns/op, B/op, allocs/op per framework per scenario, from the benchstat output; end to end: median req/s and p99 per framework per scenario), the fairness caveats of spec D3 and D4 in plain words, the framework and fasthttp versions, and per scenario one short explanation of rice's position tied to an ADR or the transport (for example ADR-0004 for `githubapi`, ADR-0005 for allocation counts, ADR-0001 for the fasthttp/`net/http` split, ADR-0003 for `middleware5`) — or "not explained" where the numbers do not support an explanation. Include the losses as prominently as the wins.

- [ ] **Step 2: M8 milestone file**

Create `docs/milestones/M8-benchmark-suite.md` from `TEMPLATE.md`: goal, the question and its answer, design notes (the separate module, the two apps per framework and why, the equivalence gate and the one-newline rule, the handler-level fairness limit, the load generator's design), measurements (both tables or a pointer to the performance model plus the headline numbers), every fault injection from Tasks 1–7 with its recorded failure text, anything the numbers contradicted in the spec or in earlier docs, and the retrospective section in the voice of M7's.

- [ ] **Step 3: The project retrospective**

Create `docs/07-retrospective.md`: what the project set out to learn (from `docs/00-overview.md`), what M0–M8 each answered (one short paragraph per milestone, linking its file), what would be done differently, and **at least three things that surprised the author**, each tied to a milestone file or ADR (candidates, to confirm against the files: the M5 panic claim that was false, ADR-0005's poisoning mechanism that would not have worked, fasthttp resetting its stop flag on a timed-out shutdown, the `ConnState` cost that was not noise, whatever M8's numbers show). Draw on the milestone retrospectives and `docs/progress.md`; do not restate them. Add it to `docs/README.md`'s index.

- [ ] **Step 4: Roadmap, README, results README, journal**

- `docs/04-roadmap.md`: M8 `☑`.
- `README.md`: in "Where it stands, measured", add a short comparison summary (median ns/op and allocs/op per framework for `static` and `githubapi`, and end-to-end median req/s for `static`) with a link to the performance model's "Where rice stands"; move the status line to M8 / all milestones done.
- `bench/results/README.md`: describe the two new files and `make compare-record`.
- `docs/progress.md`: a new top entry for M8 (`Did`, `Learned`, `Measured`, `Next`); `Next:` names whatever the retrospective says comes after the roadmap, or "none scheduled".

- [ ] **Step 5: Check every claim**

For every number in the files this task changed, find the line of a results file, test or source file that supports it; fix or remove any without one. `grep -n "TARGET" docs/05-performance-model.md` must print nothing.

- [ ] **Step 6: Gates and commit**

Run: `make lint && make test && make test-debug && make cover && make bench > /dev/null && make compare > /dev/null && git diff --exit-code main -- go.mod go.sum && echo ALL-OK`
Expected: `ALL-OK`.

```bash
git add docs README.md bench/results/README.md
git commit -m "docs: close M8 and the project with the comparison, the budget table and the retrospectives

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```
