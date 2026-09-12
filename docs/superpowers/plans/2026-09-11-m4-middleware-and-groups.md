# M4 Implementation Plan — Middleware and Groups

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `Middleware`, groups, and the one-time build phase that folds a route's middleware into a single closure before the socket opens, so a request costs exactly what it cost in M3.

**Architecture:** A generic `internal/chain.Compile` folds a middleware slice around a handler. Package `rice` gains a `Middleware` type, `App.Use`, a `Group` that holds a parent pointer rather than a snapshot, and an exported `Build` under `sync.Once`. Registration keeps inserting raw handlers eagerly so bad configuration still panics at the call site that caused it; `Build` then discards those trees and rebuilds them from a recorded route list with compiled chains.

**Tech Stack:** Go 1.25, `github.com/valyala/fasthttp` (the only runtime dependency), GNU make.

**Spec:** [`docs/superpowers/specs/2026-09-11-m4-middleware-and-groups-design.md`](../specs/2026-09-11-m4-middleware-and-groups-design.md). Decision record: [ADR-0003](../../adr/0003-middleware-as-prebuilt-closure-chain.md). Milestone definition in [`docs/04-roadmap.md`](../../04-roadmap.md). Budgets in [`docs/05-performance-model.md`](../../05-performance-model.md).

## Global Constraints

Every task's requirements implicitly include this section.

- Module path is `github.com/vietpham102301/rice-http`; root package is `rice`. Test files outside the package use the alias `rice "github.com/vietpham102301/rice-http"`.
- Nothing under `internal/` may import package `rice` (`docs/02-architecture.md`). This is why the chain compiler is generic.
- `github.com/valyala/fasthttp` is the only permitted **runtime** dependency. The standard library is fine everywhere.
- Package `rice` must not import `reflect` or `encoding/json` (ADR-0006).
- Every value handed out by `Ctx` is borrowed and dies when the handler returns (ADR-0005).
- Middleware order is fixed: **app, then outer group, then inner group, then route, then handler.** `chain[0]` is outermost — it runs first on the way in and last on the way out.
- Allocation budgets M4 must meet, asserted with `testing.AllocsPerRun` rather than observed in a benchmark: chain call with 0 middleware = 0, chain call with 5 middleware = 0, and a full dispatch on a five-middleware route still costs exactly 1 allocation (the `Ctx`).
- End-to-end per-request allocation stays at 1. Do not try to reduce it; M6 owns that.
- Code belonging to a later milestone carries a comment naming that milestone.
- Commit after every task, using Conventional Commits (`feat:`, `test:`, `chore:`, `docs:`, `bench:`).
- **Task boundaries follow compilation units.** Every task must end with `go build ./...` succeeding. M1 learned this the hard way and M2 and M3 both applied it.
- **Do not assert what a compiler does; measure it.** Six times across M2 and M3 a comment or test named as a guard turned out not to guard what it claimed, every one asserted from memory. Any comment claiming something allocates or does not allocate must be backed by `go build -gcflags=-m` output or an `AllocsPerRun` test actually run, and any test named as a guard must be shown to go red when the guarded property is broken.

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `internal/chain/chain.go` | `Compile` — fold a middleware slice around a handler | 1 |
| `internal/chain/chain_test.go` | Order, unwind order, empty and single-element cases | 1 |
| `middleware.go` | The `Middleware` type and its doc | 2 |
| `build.go` | `Build`, `build`, `middlewareFor`, `groupChain` | 2 |
| `build_test.go` | Build idempotence, after-build panics, order through the App | 2 |
| `app.go` | Modify: `App` gains `mws`, `routes`, `buildOnce`, `built`; `FasthttpHandler` builds | 2 |
| `route.go` | Modify: registration gains `mw ...Middleware`, records routes, routes through one `register` | 2 |
| `app_test.go`, `alloc_test.go`, `ctx_param_test.go` | Modify: the 14 direct `handle` call sites call `Build` first | 2 |
| `server.go` | Modify: `Run` and `Serve` call `Build` | 2 |
| `group.go` | Task 2 declares the `Group` struct, which `build.go` needs to walk a route's group chain; Task 3 adds the constructors, prefix validation and every registration method | 2, 3 |
| `group_test.go` | Prefix rules, nesting, inheritance, slice aliasing | 3 |
| `alloc_test.go` | Modify: chain budgets | 4 |
| `middleware_behaviour_test.go` | Short-circuit, error through the funnel | 4 |
| `server_test.go` | Modify: a grouped, middlewared route over a real socket | 4 |
| `docs/05-performance-model.md` | Modify: both `Chain call` rows become measured | 4 |
| `bench/chain_bench_test.go` | Dispatch at 0, 1, 5 middleware; 5 through groups; the build phase | 5 |
| `bench/results/M4-middleware-and-groups.txt` | Generated, then committed | 5 |
| `docs/milestones/M4-middleware-and-groups.md` | Retrospective | 6 |
| `docs/04-roadmap.md`, `docs/progress.md` | Modify: status marker, journal entry | 6 |

---

### Task 1: The chain compiler

**Files:**
- Create: `internal/chain/chain.go`
- Test: `internal/chain/chain_test.go`

**Interfaces:**
- Consumes: nothing. A new package that compiles and tests entirely on its own.
- Produces: `func Compile[H any, M ~func(H) H](h H, mws []M) H`

This package is six lines of code. It earns its own package by being the single
place the fold direction is written down. Folding from the wrong end reverses
execution order, produces a chain that still runs, and passes any test that does
not assert order — so it gets exhaustive order tests here, with no HTTP anywhere
near them.

- [ ] **Step 1: Write the failing tests**

Create `internal/chain/chain_test.go`:

```go
package chain

import (
	"strings"
	"testing"
)

// The test types stand in for rice.Handler and rice.Middleware without importing
// them, which internal/ may not do. They have the same shape, which is the point:
// if Compile works here it works there.
type handler func(*[]string)

type middleware func(handler) handler

// mark returns a middleware that records its name on the way in and again on the
// way out, so a single trace shows both orders at once.
func mark(name string) middleware {
	return func(next handler) handler {
		return func(log *[]string) {
			*log = append(*log, name+"-in")
			next(log)
			*log = append(*log, name+"-out")
		}
	}
}

func run(h handler, mws ...middleware) string {
	var log []string
	Compile(h, mws)(&log)
	return strings.Join(log, " ")
}

func base() handler {
	return func(log *[]string) { *log = append(*log, "handler") }
}

func TestCompileWithNoMiddleware(t *testing.T) {
	if got := run(base()); got != "handler" {
		t.Errorf("run() = %q, want %q", got, "handler")
	}
}

// TestCompileWithNoMiddlewareReturnsTheHandlerItself checks that an empty chain
// does not wrap. A wrapper that only forwards would be invisible to the trace
// above but would cost a closure call on every request of every route that has
// no middleware, which is most of them.
func TestCompileWithNoMiddlewareReturnsTheHandlerItself(t *testing.T) {
	called := false
	h := handler(func(log *[]string) { called = true })

	got := Compile(h, nil)
	got(nil)

	if !called {
		t.Fatal("the compiled handler did not call the original")
	}
	if &got == &h {
		t.Skip("cannot compare func values directly; the behavioural check above is the real assertion")
	}
}

func TestCompileWithOneMiddleware(t *testing.T) {
	if got := run(base(), mark("A")); got != "A-in handler A-out" {
		t.Errorf("run() = %q, want %q", got, "A-in handler A-out")
	}
}

// TestCompileOrder is the test this package exists for. The first middleware
// given is outermost: it runs first on the way in and last on the way out. A
// fold from the wrong end produces "B-in A-in handler A-out B-out", which is
// still a working chain and still passes any test that does not look at order.
func TestCompileOrder(t *testing.T) {
	got := run(base(), mark("A"), mark("B"), mark("C"))
	want := "A-in B-in C-in handler C-out B-out A-out"

	if got != want {
		t.Errorf("run() = %q, want %q", got, want)
	}
}

func TestCompileFiveMiddleware(t *testing.T) {
	got := run(base(), mark("1"), mark("2"), mark("3"), mark("4"), mark("5"))
	want := "1-in 2-in 3-in 4-in 5-in handler 5-out 4-out 3-out 2-out 1-out"

	if got != want {
		t.Errorf("run() = %q, want %q", got, want)
	}
}

// TestCompileShortCircuit records that stopping the chain is an ordinary return,
// which is ADR-0003's reason for choosing the decorator over an index walk: there
// is no rule to remember, because not calling next is visibly not calling next.
func TestCompileShortCircuit(t *testing.T) {
	stop := middleware(func(next handler) handler {
		return func(log *[]string) {
			*log = append(*log, "stopped")
			// next is deliberately not called
		}
	})

	got := run(base(), mark("A"), stop, mark("C"))
	want := "A-in stopped A-out"

	if got != want {
		t.Errorf("run() = %q, want %q; a middleware that does not call next must stop the chain", got, want)
	}
}

// TestCompileDoesNotRetainTheSlice guards against Compile keeping a reference to
// the caller's slice: the chain is fixed at compile time, and a later change to
// the slice must not reach the compiled handler.
func TestCompileDoesNotRetainTheSlice(t *testing.T) {
	mws := []middleware{mark("A"), mark("B")}

	compiled := Compile(base(), mws)

	mws[0] = mark("REPLACED")
	mws[1] = mark("ALSO-REPLACED")

	var log []string
	compiled(&log)

	if got := strings.Join(log, " "); got != "A-in B-in handler B-out A-out" {
		t.Errorf("after mutating the slice, the compiled chain ran %q; it should have been fixed at compile time", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/chain/ -v`

Expected: FAIL to compile, with `undefined: Compile`.

- [ ] **Step 3: Write the compiler**

Create `internal/chain/chain.go`:

```go
// Package chain compiles a middleware slice into a single handler.
//
// It is generic because docs/02-architecture.md forbids anything under internal/
// from importing package rice, so it cannot name rice.Handler or rice.Middleware.
// The ~func(H) H constraint is what lets a []rice.Middleware be passed without
// converting the slice, which would copy it.
package chain

// Compile folds mws around h so that mws[0] ends up outermost: it runs first on
// the way in and last on the way out.
//
// The fold runs backwards for that reason. Folding forwards produces a chain that
// still works and still runs every middleware, in exactly the wrong order — which
// is why this package exists separately and why its tests assert the order rather
// than only the effect.
//
// An empty slice returns h unchanged rather than wrapping it, so a route with no
// middleware costs no extra closure call.
func Compile[H any, M ~func(H) H](h H, mws []M) H {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/chain/ -v`

Expected: PASS for all seven tests.

- [ ] **Step 5: Prove the order test can fail**

The order test is this package's whole reason for existing, so confirm it
actually guards. Temporarily reverse the fold:

```bash
# change the loop to: for i := 0; i < len(mws); i++ {
go test ./internal/chain/ -run TestCompileOrder -v
```

Expected: FAIL, reporting `"C-in B-in A-in handler A-out B-out C-out"` against the
wanted order. Restore the backwards loop and re-run to confirm it passes again.
Put both outputs in your report.

This project has shipped six guards that turned out not to guard. The habit that
closed the last three is this one: break the property deliberately, watch the
guard go red, put it back.

- [ ] **Step 6: Verify and commit**

```bash
go build ./... && make lint && make test
git add internal/chain
git commit -m "feat: add the middleware chain compiler"
```

---

### Task 2: Middleware, route recording, and the build phase

This is the milestone's core. It adds the `Middleware` type, `App.Use`, the
recorded route list, and `Build`, and it wires `Run`, `Serve` and
`FasthttpHandler` to build.

**Files:**
- Create: `middleware.go`
- Create: `build.go`
- Test: `build_test.go`
- Modify: `app.go` (new `App` fields; `FasthttpHandler` builds)
- Modify: `route.go` (registration takes `mw ...Middleware` and records; one shared `register`)
- Modify: `server.go` (`Run` and `Serve` build)
- Modify: `app_test.go`, `alloc_test.go`, `ctx_param_test.go` (direct `handle` calls build first)

**Interfaces:**
- Consumes: `chain.Compile` from Task 1; `router.Tree`, `router.Params` from M3.
- Produces:
  - `type Middleware func(next Handler) Handler`
  - `func (a *App) Use(mw ...Middleware)`
  - `func (a *App) Build()`
  - `func (a *App) Handle(method, path string, h Handler, mw ...Middleware)` and the seven verb helpers, each gaining the same trailing variadic
  - `func (a *App) register(method, path string, h Handler, g *Group, mw []Middleware)` (unexported, used by Task 3)
  - `type route struct{...}` (unexported, used by Task 3 through `register`)

**Why the 14 existing `handle` call sites must change.** Registration inserts raw
handlers, and `build` replaces the trees with compiled ones. So calling `handle`
before `Build` dispatches a handler with no middleware around it — silently.
Users cannot reach that state, because `handle` is unexported and every exported
entry point builds. In-package tests can, and a test that exercises a
configuration no user can reach is testing the wrong thing. They call `Build`.

- [ ] **Step 1: Write the Middleware type**

Create `middleware.go`:

```go
package rice

// Middleware wraps a Handler and returns one that runs around it.
//
// This is the decorator shape rather than an index walk with a cursor on the
// context. Stopping the chain is an ordinary return rather than a rule to
// remember, and no per-request state is needed to track position — which is why
// Ctx gained no field for middleware. See ADR-0003.
//
//	func Logging(next rice.Handler) rice.Handler {
//		return func(c *rice.Ctx) error {
//			started := time.Now()
//			err := next(c)
//			log.Printf("%s %s %s", c.Method(), c.Path(), time.Since(started))
//			return err
//		}
//	}
//
// Middleware runs in a fixed order: application middleware first, then each
// group from outermost to innermost, then the route's own, then the handler. The
// unwind runs in reverse.
type Middleware func(next Handler) Handler
```

- [ ] **Step 2: Extend the App struct**

In `app.go`, add the fields. Keep the existing ones and their comments exactly as
they are:

```go
type App struct {
	// trees holds one route tree per common verb, indexed by a method constant.
	// Registration inserts raw handlers here so that a bad configuration panics
	// at the call site that caused it; Build discards these and rebuilds from
	// routes with compiled chains. See the M4 design doc, D1.
	trees [methodCount]router.Tree[Handler]

	// rare holds trees for verbs without a reserved slot. It stays nil for
	// applications that never register one.
	rare map[string]*router.Tree[Handler]

	// mws is the application-level middleware, outermost in every chain.
	mws []Middleware

	// routes records every registration in the order it happened. It is what
	// Build compiles from; the registration-time trees are only a validator.
	routes []route

	buildOnce sync.Once

	// built is read by registration to reject a late route. It is written inside
	// buildOnce.Do, which establishes the happens-before that makes reading it
	// without a lock safe: every request is served after Build returns.
	built bool

	srv *fasthttp.Server

	mu sync.Mutex
	ln net.Listener
}

// route is one registration, recorded for Build to compile.
type route struct {
	method string
	path   string // already joined with any group prefix
	h      Handler
	group  *Group // nil when registered directly on the App
	mws    []Middleware
}
```

- [ ] **Step 3: Write the build phase**

Create `build.go`:

```go
package rice

import (
	"github.com/vietpham102301/rice-http/internal/chain"
	"github.com/vietpham102301/rice-http/internal/router"
)

// Build compiles every registered route's middleware chain and installs the
// result. It runs once; later calls do nothing.
//
// Run, Serve and FasthttpHandler all call it, so most programs never call it
// directly. Doing so is useful to reject a bad configuration before a socket is
// ever opened.
//
// It returns nothing. Every error a configuration can contain — a malformed
// pattern, a duplicate route, a conflicting parameter name — is rejected by the
// registration call that introduced it, so by the time Build runs there is
// nothing left to report. Configuration mistakes panic here as they do there.
func (a *App) Build() { a.buildOnce.Do(a.build) }

// build is the body of Build, run exactly once.
func (a *App) build() {
	// The registration-time trees have done their job: they rejected bad
	// configuration where the mistake was written. Discard them and build the
	// trees that will actually serve, holding compiled chains.
	a.trees = [methodCount]router.Tree[Handler]{}
	a.rare = nil

	for i := range a.routes {
		r := &a.routes[i]
		compiled := chain.Compile(r.h, a.middlewareFor(r))

		if err := a.treeFor(r.method).Insert(r.path, compiled); err != nil {
			// Unreachable: this exact insertion succeeded during registration,
			// against an identical route set. Panicking rather than ignoring it
			// means a wrong assumption here surfaces instead of silently
			// dropping a route.
			panic("rice: build: " + err.Error() + ": " + r.method + " " + r.path)
		}
	}

	a.built = true
}

// middlewareFor assembles the full chain for one route, outermost first:
// application, then each group from outermost to innermost, then the route's own.
//
// The result is a fresh slice sized exactly, so it aliases nothing a caller or a
// group can later append to.
func (a *App) middlewareFor(r *route) []Middleware {
	groups := groupChain(r.group)

	n := len(a.mws) + len(r.mws)
	for _, g := range groups {
		n += len(g.mws)
	}
	if n == 0 {
		return nil
	}

	mws := make([]Middleware, 0, n)
	mws = append(mws, a.mws...)
	for _, g := range groups {
		mws = append(mws, g.mws...)
	}
	return append(mws, r.mws...)
}

// groupChain returns g and its ancestors ordered outermost first.
//
// A Group points at its parent rather than holding a copy of the parent's
// middleware, so that a parent given middleware after the child was created still
// reaches the child's routes. That means the chain is only knowable here, at
// build time, by walking up and reversing. See the M4 design doc, D3.
func groupChain(g *Group) []*Group {
	if g == nil {
		return nil
	}

	var out []*Group
	for ; g != nil; g = g.parent {
		out = append(out, g)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
```

This file references `Group` and its `parent` and `mws` fields, which Task 3
creates. **`go build` will fail until Task 3 lands**, which breaks this plan's own
compilation-unit rule — so Task 2 declares a minimal `Group` in `group.go` and
Task 3 fills it in. Add this to `group.go` now, and nothing else:

```go
package rice

// Group is a route prefix plus middleware. M4 Task 3 gives it its constructor and
// its registration methods; it is declared here because the build phase needs its
// shape to walk a route's group chain.
type Group struct {
	app    *App
	parent *Group
	prefix string
	mws    []Middleware
}
```

- [ ] **Step 4: Route every registration through one function**

In `route.go`, replace `Handle` and the seven verb helpers. Keep
`hasLowercaseByte` and `treeFor` exactly as they are.

```go
// Handle registers h for the given method and path, wrapped in mw.
//
// All routes must be registered before serving begins. The route trees and
// middleware lists are mutated here and read on every request without
// synchronisation, so registering after Build is a data race rather than merely a
// late change — and it panics rather than racing.
//
// It panics on a programmer error: an empty or lowercase method, a nil handler, a
// pattern that is malformed or could never match a request, or a route already
// registered for the same method and path.
//
// Pattern syntax: a segment beginning with ':' captures one path segment by name,
// and a final segment beginning with '*' captures the remainder. A wildcard must
// capture at least one byte, so /files/*path matches /files/a but not /files/ or
// /files.
func (a *App) Handle(method, path string, h Handler, mw ...Middleware) {
	a.register(method, path, h, nil, mw)
}

// GET registers h for GET requests to path, wrapped in mw.
func (a *App) GET(path string, h Handler, mw ...Middleware) {
	a.register("GET", path, h, nil, mw)
}

// POST registers h for POST requests to path, wrapped in mw.
func (a *App) POST(path string, h Handler, mw ...Middleware) {
	a.register("POST", path, h, nil, mw)
}

// PUT registers h for PUT requests to path, wrapped in mw.
func (a *App) PUT(path string, h Handler, mw ...Middleware) {
	a.register("PUT", path, h, nil, mw)
}

// PATCH registers h for PATCH requests to path, wrapped in mw.
func (a *App) PATCH(path string, h Handler, mw ...Middleware) {
	a.register("PATCH", path, h, nil, mw)
}

// DELETE registers h for DELETE requests to path, wrapped in mw.
func (a *App) DELETE(path string, h Handler, mw ...Middleware) {
	a.register("DELETE", path, h, nil, mw)
}

// HEAD registers h for HEAD requests to path, wrapped in mw.
func (a *App) HEAD(path string, h Handler, mw ...Middleware) {
	a.register("HEAD", path, h, nil, mw)
}

// OPTIONS registers h for OPTIONS requests to path, wrapped in mw.
func (a *App) OPTIONS(path string, h Handler, mw ...Middleware) {
	a.register("OPTIONS", path, h, nil, mw)
}

// Use adds application-level middleware, which wraps every route.
//
// Order of calls does not matter relative to route registration: chains are
// compiled in Build, so Use written after a route still applies to it. That is
// ADR-0003's central reason for having a build phase at all — the alternative
// silently drops middleware added late, and middleware added late is usually
// authentication.
func (a *App) Use(mw ...Middleware) {
	if a.built {
		panic("rice: cannot call Use after Build; all middleware must be registered before serving begins")
	}
	// append into the App's own slice rather than keeping mw, so the caller's
	// slice is never aliased and cannot be changed underneath us.
	a.mws = append(a.mws, mw...)
}

// register is the single path every registration takes, from the App and from any
// Group. g is nil for a route registered directly on the App.
func (a *App) register(method, path string, h Handler, g *Group, mw []Middleware) {
	if a.built {
		panic("rice: cannot register " + method + " " + path +
			" after Build; all routes must be registered before serving begins")
	}
	if method == "" {
		panic("rice: route method is empty for path " + path)
	}
	if hasLowercaseByte(method) {
		panic("rice: route method " + method + " for path " + path +
			" is not uppercase; HTTP methods are matched case-sensitively")
	}
	if h == nil {
		panic("rice: nil handler for " + method + " " + path)
	}

	// Insert the raw handler now, so a malformed pattern or a conflicting route
	// panics from the call that wrote it. Build discards this tree and rebuilds
	// with the compiled chain.
	if err := a.treeFor(method).Insert(path, h); err != nil {
		panic("rice: " + err.Error())
	}

	a.routes = append(a.routes, route{
		method: method,
		path:   path,
		h:      h,
		group:  g,
		// Copy rather than keep mw: it may be a caller's slice with spare
		// capacity, and an append on their side must not reach into ours.
		mws: append([]Middleware(nil), mw...),
	})
}
```

The existing `Handle` currently distinguishes `router.ErrDuplicate` from other
errors when formatting its panic. M3's final review unified every startup panic on
the `rice: route path ...` shape, so that distinction is gone — `err.Error()`
already carries the right message for both. Check the tests that assert on panic
text still pass, and report what they now see.

- [ ] **Step 5: Make the entry points build**

In `app.go`, `FasthttpHandler` must build — every benchmark drives dispatch through
it and never opens a socket, so without this they would measure uncompiled routes:

```go
// FasthttpHandler returns the request handler this App installs on its server.
//
// It calls Build, because a handler that dispatched uncompiled routes would skip
// every middleware silently. Use it to mount rice inside an existing fasthttp
// server, or to drive the dispatch path directly in benchmarks without opening a
// socket.
func (a *App) FasthttpHandler() fasthttp.RequestHandler {
	a.Build()
	return a.handle
}
```

In `server.go`, both `Run` and `Serve` build. `Run` delegates to `Serve`, so
building in `Serve` covers both — but build before binding in `Run` so a bad
configuration fails before a port is taken:

```go
func (a *App) Run(addr string) error {
	a.Build()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return a.Serve(ln)
}

func (a *App) Serve(ln net.Listener) error {
	a.Build()

	a.mu.Lock()
	a.ln = ln
	a.mu.Unlock()

	return a.srv.Serve(ln)
}
```

Keep both functions' existing doc comments and add one sentence to each saying it
builds.

- [ ] **Step 6: Migrate the direct `handle` call sites**

Every test that calls `app.handle(...)` directly must call `app.Build()` after its
registrations and before dispatching. There are 14 such calls across
`app_test.go`, `alloc_test.go` and `ctx_param_test.go`. Find them with:

```bash
grep -rn 'app\.handle(\|\.handle(fctx' --include='*_test.go' .
```

Add `app.Build()` immediately before the first `handle` call in each test. Do not
change any assertion. `Compile` returns the handler unchanged for an empty chain,
so a route with no middleware behaves identically before and after building — the
allocation budgets in `alloc_test.go` must still measure exactly what they did.

Tests that call `app.lookup(...)` rather than `handle` need no change: the trees
hold a handler either way.

- [ ] **Step 7: Write the build tests**

Create `build_test.go`:

```go
package rice

import (
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// traceMW returns a middleware that records its name on the way in and again on
// the way out, so one request produces the full order in both directions.
func traceMW(log *[]string, name string) Middleware {
	return func(next Handler) Handler {
		return func(c *Ctx) error {
			*log = append(*log, name+"-in")
			err := next(c)
			*log = append(*log, name+"-out")
			return err
		}
	}
}

func traceHandler(log *[]string) Handler {
	return func(c *Ctx) error {
		*log = append(*log, "handler")
		return c.String(200, "ok")
	}
}

// dispatch builds the app if needed and sends one GET to path, returning the
// status.
func dispatch(app *App, path string) int {
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI(path)
	app.handle(fctx)
	return fctx.Response.StatusCode()
}

func TestApplicationMiddlewareRunsAroundTheHandler(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "A"))
	app.GET("/x", traceHandler(&log))

	if got := dispatch(app, "/x"); got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}
	if got := strings.Join(log, " "); got != "A-in handler A-out" {
		t.Errorf("trace = %q, want %q", got, "A-in handler A-out")
	}
}

// TestUseAfterRegistrationStillApplies is the roadmap's named exit criterion, and
// the whole reason ADR-0003 puts compilation in a build phase.
func TestUseAfterRegistrationStillApplies(t *testing.T) {
	var log []string

	app := New()
	app.GET("/x", traceHandler(&log)) // route first
	app.Use(traceMW(&log, "late"))    // middleware second

	dispatch(app, "/x")

	if got := strings.Join(log, " "); got != "late-in handler late-out" {
		t.Errorf("trace = %q; middleware added after the route must still wrap it", got)
	}
}

func TestRouteMiddlewareRunsInsideApplicationMiddleware(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "app"))
	app.GET("/x", traceHandler(&log), traceMW(&log, "route"))

	dispatch(app, "/x")

	want := "app-in route-in handler route-out app-out"
	if got := strings.Join(log, " "); got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}

func TestUnwindOrderIsTheReverseOfEntryOrder(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "1"), traceMW(&log, "2"), traceMW(&log, "3"))
	app.GET("/x", traceHandler(&log))

	dispatch(app, "/x")

	want := "1-in 2-in 3-in handler 3-out 2-out 1-out"
	if got := strings.Join(log, " "); got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}

func TestBuildIsIdempotent(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "A"))
	app.GET("/x", traceHandler(&log))

	app.Build()
	app.Build()
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/x")
	app.handle(fctx)

	if got := strings.Join(log, " "); got != "A-in handler A-out" {
		t.Errorf("trace = %q after three Builds; middleware must not be applied more than once", got)
	}
}

func TestRegisteringAfterBuildPanics(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error { return nil })
	app.Build()

	v := mustPanic(t, "GET after Build", func() {
		app.GET("/y", func(c *Ctx) error { return nil })
	})

	msg, _ := v.(string)
	if !strings.Contains(msg, "after Build") {
		t.Errorf("panic %q should say the registration came after Build", v)
	}
	if !strings.Contains(msg, "/y") {
		t.Errorf("panic %q should name the route that was too late", v)
	}
}

func TestUseAfterBuildPanics(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error { return nil })
	app.Build()

	v := mustPanic(t, "Use after Build", func() {
		app.Use(func(next Handler) Handler { return next })
	})

	if msg, _ := v.(string); !strings.Contains(msg, "after Build") {
		t.Errorf("panic %q should say Use came after Build", v)
	}
}

// TestFasthttpHandlerBuilds matters because every benchmark reaches dispatch
// through it and never opens a socket. If it did not build, they would all
// measure routes with no middleware and report a number that means nothing.
func TestFasthttpHandlerBuilds(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "A"))
	app.GET("/x", traceHandler(&log))

	h := app.FasthttpHandler()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/x")
	h(fctx)

	if got := strings.Join(log, " "); got != "A-in handler A-out" {
		t.Errorf("trace = %q; FasthttpHandler must build before returning", got)
	}
}

// TestUseDoesNotAliasTheCallersSlice guards a Go hazard that a naive
// implementation walks straight into: keeping the variadic slice rather than
// copying into the App's own means a caller appending to theirs can change ours.
func TestUseDoesNotAliasTheCallersSlice(t *testing.T) {
	var log []string

	callerSlice := make([]Middleware, 0, 4) // spare capacity is the trap
	callerSlice = append(callerSlice, traceMW(&log, "A"))

	app := New()
	app.Use(callerSlice...)
	app.GET("/x", traceHandler(&log))

	// The caller keeps using their slice after registering.
	callerSlice = append(callerSlice, traceMW(&log, "INTRUDER"))
	_ = callerSlice

	dispatch(app, "/x")

	if got := strings.Join(log, " "); got != "A-in handler A-out" {
		t.Errorf("trace = %q; the caller's later append must not reach the App", got)
	}
}
```

`mustPanic` already exists in `route_test.go` in this package — reuse it, do not
redefine it.

- [ ] **Step 8: Run the tests**

Run: `go test . -run 'TestApplicationMiddleware|TestUse|TestRoute|TestUnwind|TestBuild|TestRegisteringAfter|TestFasthttpHandlerBuilds' -v`

Expected: PASS.

- [ ] **Step 9: Run the whole suite**

Run: `make test`

Expected: PASS. Every M1, M2 and M3 test still holds — a route with no middleware
compiles to itself, so behaviour is unchanged for everything that existed before
this task.

If a panic-message assertion fails, read it before changing it: the message shapes
were unified in M3 and this task removes a distinction that had already stopped
mattering. Report which test saw which message.

- [ ] **Step 10: Lint and commit**

```bash
make lint
git add middleware.go build.go build_test.go group.go app.go route.go server.go app_test.go alloc_test.go ctx_param_test.go
git commit -m "feat: add middleware, route recording, and the build phase"
```

---
### Task 3: Groups

**Files:**
- Modify: `group.go` (Task 2 declared the struct; this task gives it everything else)
- Test: `group_test.go`

**Interfaces:**
- Consumes: `App.register` and the `route` record from Task 2; `groupChain` and `middlewareFor` from Task 2's `build.go`.
- Produces:
  - `func (a *App) Group(prefix string, mw ...Middleware) *Group`
  - `func (g *Group) Group(prefix string, mw ...Middleware) *Group`
  - `func (g *Group) Use(mw ...Middleware)`
  - `func (g *Group) Handle(method, path string, h Handler, mw ...Middleware)`
  - `func (g *Group) GET(path string, h Handler, mw ...Middleware)` and the same for `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD`, `OPTIONS`

- [ ] **Step 1: Write the failing tests**

Create `group_test.go`:

```go
package rice

import (
	"strings"
	"testing"
)

func TestGroupPrefixIsJoinedToTheRoutePath(t *testing.T) {
	var log []string

	app := New()
	g := app.Group("/api")
	g.GET("/users", traceHandler(&log))

	if got := dispatch(app, "/api/users"); got != 200 {
		t.Errorf("GET /api/users = %d, want 200", got)
	}
	if got := dispatch(app, "/users"); got != 404 {
		t.Errorf("GET /users = %d, want 404; the group prefix must be required", got)
	}
}

func TestNestedGroupPrefixesConcatenate(t *testing.T) {
	var log []string

	app := New()
	v1 := app.Group("/api").Group("/v1")
	v1.GET("/users", traceHandler(&log))

	if got := dispatch(app, "/api/v1/users"); got != 200 {
		t.Errorf("GET /api/v1/users = %d, want 200", got)
	}
}

// TestEmptyGroupPrefixIsAllowed covers the middleware-only group, which is a
// common shape: a set of routes that share middleware but not a path.
func TestEmptyGroupPrefixIsAllowed(t *testing.T) {
	var log []string

	app := New()
	g := app.Group("", traceMW(&log, "guard"))
	g.GET("/users", traceHandler(&log))

	if got := dispatch(app, "/users"); got != 200 {
		t.Fatalf("GET /users = %d, want 200", got)
	}
	if got := strings.Join(log, " "); got != "guard-in handler guard-out" {
		t.Errorf("trace = %q, want %q", got, "guard-in handler guard-out")
	}
}

func TestGroupRejectsAPrefixThatWouldJoinBadly(t *testing.T) {
	cases := []struct {
		prefix string
		phrase string
	}{
		{"/api/", "must not end with"},
		{"api", "must begin with"},
		{"api/", "must begin with"},
	}

	for _, c := range cases {
		app := New()
		v := mustPanic(t, "Group("+c.prefix+")", func() {
			app.Group(c.prefix)
		})

		msg, _ := v.(string)
		if !strings.Contains(msg, c.phrase) {
			t.Errorf("Group(%q) panicked with %q, which does not mention %q", c.prefix, msg, c.phrase)
		}
		if !strings.Contains(msg, c.prefix) {
			t.Errorf("Group(%q) panicked with %q, which does not name the offending prefix", c.prefix, msg)
		}
	}
}

func TestNestedGroupRejectsABadPrefixToo(t *testing.T) {
	app := New()
	g := app.Group("/api")

	mustPanic(t, `g.Group("/v1/")`, func() { g.Group("/v1/") })
	mustPanic(t, `g.Group("v1")`, func() { g.Group("v1") })
}

// TestGroupRootRegistersWithATrailingSlash records the consequence of ADR-0007
// declining trailing-slash equivalence: inside a group, "/" is the prefix plus a
// slash, which is a different route from the bare prefix.
func TestGroupRootRegistersWithATrailingSlash(t *testing.T) {
	var log []string

	app := New()
	g := app.Group("/api")
	g.GET("/", traceHandler(&log))

	if got := dispatch(app, "/api/"); got != 200 {
		t.Errorf("GET /api/ = %d, want 200", got)
	}
	if got := dispatch(app, "/api"); got != 404 {
		t.Errorf("GET /api = %d, want 404; ADR-0007 declined trailing-slash equivalence", got)
	}
}

func TestGroupMiddlewareRunsInsideApplicationMiddleware(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "app"))
	g := app.Group("/api", traceMW(&log, "group"))
	g.GET("/x", traceHandler(&log), traceMW(&log, "route"))

	dispatch(app, "/api/x")

	want := "app-in group-in route-in handler route-out group-out app-out"
	if got := strings.Join(log, " "); got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}

// TestThreeLevelOrder is the roadmap's exit criterion for nested groups, entry
// and unwind both.
func TestThreeLevelOrder(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "app"))
	outer := app.Group("/a", traceMW(&log, "outer"))
	inner := outer.Group("/b", traceMW(&log, "inner"))
	inner.GET("/c", traceHandler(&log), traceMW(&log, "route"))

	if got := dispatch(app, "/a/b/c"); got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}

	want := "app-in outer-in inner-in route-in handler route-out inner-out outer-out app-out"
	if got := strings.Join(log, " "); got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}

// TestParentUseAfterChildCreationStillApplies is the reason a Group holds a
// parent pointer instead of copying its parent's middleware. If it snapshotted,
// this middleware would silently not wrap the child's routes — the same bug
// ADR-0003 exists to prevent, one level down, and the middleware people add late
// is usually authentication.
func TestParentUseAfterChildCreationStillApplies(t *testing.T) {
	var log []string

	app := New()
	parent := app.Group("/api")
	child := parent.Group("/v1")
	child.GET("/x", traceHandler(&log))

	parent.Use(traceMW(&log, "late")) // after the child exists and has routes

	dispatch(app, "/api/v1/x")

	if got := strings.Join(log, " "); got != "late-in handler late-out" {
		t.Errorf("trace = %q; middleware added to a parent after the child was created must still wrap the child's routes", got)
	}
}

// TestSiblingGroupsDoNotShareMiddleware is the slice-aliasing guard. Two children
// of one parent must not see each other's middleware, which a shared backing
// array would cause.
func TestSiblingGroupsDoNotShareMiddleware(t *testing.T) {
	var log []string

	app := New()
	parent := app.Group("/api")

	left := parent.Group("/left", traceMW(&log, "left"))
	right := parent.Group("/right", traceMW(&log, "right"))

	left.GET("/x", traceHandler(&log))
	right.GET("/x", traceHandler(&log))

	dispatch(app, "/api/left/x")
	if got := strings.Join(log, " "); got != "left-in handler left-out" {
		t.Errorf("left route trace = %q, want only left's middleware", got)
	}

	log = nil
	dispatch(app, "/api/right/x")
	if got := strings.Join(log, " "); got != "right-in handler right-out" {
		t.Errorf("right route trace = %q, want only right's middleware", got)
	}
}

// TestGroupDoesNotAliasTheCallersSlice is the same hazard as the App-level one:
// keeping the variadic rather than copying lets a caller's later append reach in.
func TestGroupDoesNotAliasTheCallersSlice(t *testing.T) {
	var log []string

	callerSlice := make([]Middleware, 0, 4) // spare capacity is the trap
	callerSlice = append(callerSlice, traceMW(&log, "A"))

	app := New()
	g := app.Group("/api", callerSlice...)
	g.GET("/x", traceHandler(&log))

	callerSlice = append(callerSlice, traceMW(&log, "INTRUDER"))
	_ = callerSlice

	dispatch(app, "/api/x")

	if got := strings.Join(log, " "); got != "A-in handler A-out" {
		t.Errorf("trace = %q; the caller's later append must not reach the group", got)
	}
}

func TestGroupRegistrationAfterBuildPanics(t *testing.T) {
	app := New()
	g := app.Group("/api")
	g.GET("/x", func(c *Ctx) error { return nil })
	app.Build()

	v := mustPanic(t, "group GET after Build", func() {
		g.GET("/y", func(c *Ctx) error { return nil })
	})

	if msg, _ := v.(string); !strings.Contains(msg, "after Build") {
		t.Errorf("panic %q should say the registration came after Build", v)
	}
}

func TestGroupUseAfterBuildPanics(t *testing.T) {
	app := New()
	g := app.Group("/api")
	g.GET("/x", func(c *Ctx) error { return nil })
	app.Build()

	mustPanic(t, "group Use after Build", func() {
		g.Use(func(next Handler) Handler { return next })
	})
}

func TestEveryGroupVerbHelperRegistersUnderItsOwnVerb(t *testing.T) {
	verbs := []struct {
		name string
		call func(g *Group, path string, h Handler)
	}{
		{"GET", func(g *Group, p string, h Handler) { g.GET(p, h) }},
		{"POST", func(g *Group, p string, h Handler) { g.POST(p, h) }},
		{"PUT", func(g *Group, p string, h Handler) { g.PUT(p, h) }},
		{"PATCH", func(g *Group, p string, h Handler) { g.PATCH(p, h) }},
		{"DELETE", func(g *Group, p string, h Handler) { g.DELETE(p, h) }},
		{"HEAD", func(g *Group, p string, h Handler) { g.HEAD(p, h) }},
		{"OPTIONS", func(g *Group, p string, h Handler) { g.OPTIONS(p, h) }},
	}

	for _, v := range verbs {
		app := New()
		g := app.Group("/api")
		v.call(g, "/x", func(c *Ctx) error { return nil })

		var p router.Params
		if _, ok := app.lookup([]byte(v.name), []byte("/api/x"), &p); !ok {
			t.Errorf("%s helper did not register a route reachable by %s at /api/x", v.name, v.name)
		}
		for _, other := range verbs {
			if other.name == v.name {
				continue
			}
			if _, ok := app.lookup([]byte(other.name), []byte("/api/x"), &p); ok {
				t.Errorf("%s helper also registered the route under %s", v.name, other.name)
			}
		}
	}
}
```

`group_test.go` needs `"github.com/vietpham102301/rice-http/internal/router"` in
its imports, for the `router.Params` the last test passes to `app.lookup`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -run 'TestGroup|TestNested|TestEmpty|TestThreeLevel|TestParentUse|TestSibling|TestEveryGroup' -v`

Expected: FAIL to compile, with `app.Group undefined`.

- [ ] **Step 3: Write the Group**

Replace `group.go` entirely. It keeps the struct Task 2 declared and adds the rest:

```go
package rice

// Group is a route prefix plus middleware, and a convenience that exists only at
// registration time. By the time a request arrives, every route holds one flat
// compiled chain and no group is involved.
//
// A Group holds a pointer to its parent rather than a copy of the parent's
// middleware, so that middleware added to a parent after the child was created
// still wraps the child's routes. Snapshotting instead would reintroduce, one
// level down, exactly the bug the build phase exists to prevent. See the M4
// design doc, D3.
type Group struct {
	app    *App
	parent *Group
	prefix string
	mws    []Middleware
}

// Group creates a route group under prefix, wrapped in mw.
//
// The prefix must be empty, or begin with "/" and not end with "/". An empty
// prefix is a group that shares middleware without sharing a path. The rule makes
// joining total: a valid prefix joined to a valid route path is always a valid
// pattern, so a bad prefix panics here rather than producing a malformed pattern
// that fails later naming a string nobody wrote.
func (a *App) Group(prefix string, mw ...Middleware) *Group {
	checkGroupPrefix(prefix)
	if a.built {
		panic("rice: cannot create a group after Build; all routes must be registered before serving begins")
	}

	return &Group{
		app:    a,
		prefix: prefix,
		// Copy rather than keep mw: it may be a caller's slice with spare
		// capacity, and an append on their side must not reach into ours.
		mws: append([]Middleware(nil), mw...),
	}
}

// Group creates a nested group, concatenating prefixes and inheriting middleware.
func (g *Group) Group(prefix string, mw ...Middleware) *Group {
	checkGroupPrefix(prefix)
	if g.app.built {
		panic("rice: cannot create a group after Build; all routes must be registered before serving begins")
	}

	return &Group{
		app:    g.app,
		parent: g,
		prefix: g.prefix + prefix,
		mws:    append([]Middleware(nil), mw...),
	}
}

// Use adds middleware to this group, wrapping every route registered on it or on
// any group nested inside it.
//
// Order does not matter: chains are compiled in Build, so Use written after a
// route — or after a nested group was created — still applies to it.
func (g *Group) Use(mw ...Middleware) {
	if g.app.built {
		panic("rice: cannot call Use after Build; all middleware must be registered before serving begins")
	}
	g.mws = append(g.mws, mw...)
}

// Handle registers h for method at the group's prefix joined with path.
func (g *Group) Handle(method, path string, h Handler, mw ...Middleware) {
	g.app.register(method, g.prefix+path, h, g, mw)
}

// GET registers h for GET requests to the group's prefix joined with path.
func (g *Group) GET(path string, h Handler, mw ...Middleware) {
	g.app.register("GET", g.prefix+path, h, g, mw)
}

// POST registers h for POST requests to the group's prefix joined with path.
func (g *Group) POST(path string, h Handler, mw ...Middleware) {
	g.app.register("POST", g.prefix+path, h, g, mw)
}

// PUT registers h for PUT requests to the group's prefix joined with path.
func (g *Group) PUT(path string, h Handler, mw ...Middleware) {
	g.app.register("PUT", g.prefix+path, h, g, mw)
}

// PATCH registers h for PATCH requests to the group's prefix joined with path.
func (g *Group) PATCH(path string, h Handler, mw ...Middleware) {
	g.app.register("PATCH", g.prefix+path, h, g, mw)
}

// DELETE registers h for DELETE requests to the group's prefix joined with path.
func (g *Group) DELETE(path string, h Handler, mw ...Middleware) {
	g.app.register("DELETE", g.prefix+path, h, g, mw)
}

// HEAD registers h for HEAD requests to the group's prefix joined with path.
func (g *Group) HEAD(path string, h Handler, mw ...Middleware) {
	g.app.register("HEAD", g.prefix+path, h, g, mw)
}

// OPTIONS registers h for OPTIONS requests to the group's prefix joined with path.
func (g *Group) OPTIONS(path string, h Handler, mw ...Middleware) {
	g.app.register("OPTIONS", g.prefix+path, h, g, mw)
}

// checkGroupPrefix rejects a prefix that would not join cleanly onto a route path.
func checkGroupPrefix(prefix string) {
	if prefix == "" {
		return
	}
	if prefix[0] != '/' {
		panic("rice: group prefix " + prefix + " must begin with /")
	}
	if prefix[len(prefix)-1] == '/' {
		panic("rice: group prefix " + prefix + " must not end with /; write " +
			strings.TrimRight(prefix, "/"))
	}
}
```

`group.go` needs `"strings"` in its imports for `checkGroupPrefix`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -run 'TestGroup|TestNested|TestEmpty|TestThreeLevel|TestParentUse|TestSibling|TestEveryGroup' -v`

Expected: PASS.

If `TestParentUseAfterChildCreationStillApplies` fails, the child is snapshotting
its parent's middleware somewhere instead of relying on the parent pointer. If
`TestSiblingGroupsDoNotShareMiddleware` fails, two groups share a backing array —
check that every place a group's `mws` is set uses `append` into a fresh slice
rather than assigning a slice it was handed.

- [ ] **Step 5: Prove the aliasing guards can fail**

Both aliasing tests assert an absence, which is the shape most likely to pass for
the wrong reason. Break each and confirm it goes red:

```bash
# in Group (the App-level constructor), replace
#     mws: append([]Middleware(nil), mw...),
# with
#     mws: mw,
go test . -run TestGroupDoesNotAliasTheCallersSlice -v
```

Expected: FAIL, with the trace showing `INTRUDER`. Restore the copy. Do the same
for `App.Use` against `TestUseDoesNotAliasTheCallersSlice` by replacing
`a.mws = append(a.mws, mw...)` with `a.mws = mw`. Put both outputs in your report.

- [ ] **Step 6: Run the whole suite, lint, and commit**

```bash
make test
make lint
git add group.go group_test.go
git commit -m "feat: add route groups with prefix joining and inherited middleware"
```

---

### Task 4: Allocation budgets, behaviour, and the performance model

**Files:**
- Modify: `alloc_test.go`
- Create: `middleware_behaviour_test.go`
- Modify: `server_test.go`
- Modify: `docs/05-performance-model.md`

**Interfaces:**
- Consumes: everything from Tasks 1 to 3.
- Produces: the enforced budgets M4's exit criteria require.

- [ ] **Step 1: Write the allocation budget tests**

Append to `alloc_test.go`. It already has the `budget(t, name, want, fn)` helper
and imports `strconv`, `fasthttp` and `internal/router`; check the existing import
block before adding anything.

**The hazard specific to these budgets.** A chain built from middleware that do
nothing can be optimised into nothing, and then the budget measures an empty
chain and passes for the wrong reason. Every test below therefore uses middleware
with an observable side effect and asserts that every one of them ran before
measuring.

```go
// chainSink counts middleware invocations. It is a package-level variable so the
// compiler cannot discard the increments as dead code, which would let these
// budgets measure a chain that was optimised away.
var chainSink int

// countingMW returns a middleware that increments chainSink on the way through.
func countingMW() Middleware {
	return func(next Handler) Handler {
		return func(c *Ctx) error {
			chainSink++
			return next(c)
		}
	}
}

func TestAllocBudgetDispatchNoMiddleware(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error { return c.String(200, "ok") })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/x")

	app.handle(fctx)
	if fctx.Response.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200; this budget would be measuring the 404 path", fctx.Response.StatusCode())
	}

	budget(t, "App.handle with no middleware", 1, func() {
		app.handle(fctx)
	})
}

// TestAllocBudgetDispatchFiveMiddleware is the milestone's central claim as a
// test: five middleware cost no allocations beyond the one Ctx.
func TestAllocBudgetDispatchFiveMiddleware(t *testing.T) {
	app := New()
	app.Use(countingMW(), countingMW(), countingMW())
	g := app.Group("/api", countingMW())
	g.GET("/x", func(c *Ctx) error { return c.String(200, "ok") }, countingMW())
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/api/x")

	chainSink = 0
	app.handle(fctx)

	if fctx.Response.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", fctx.Response.StatusCode())
	}
	if chainSink != 5 {
		t.Fatalf("%d middleware ran, want 5; this budget would be measuring a shorter chain than it claims", chainSink)
	}

	budget(t, "App.handle with five middleware", 1, func() {
		app.handle(fctx)
	})
}

// TestAllocBudgetChainCompile pins the compiler itself, separately from dispatch.
func TestAllocBudgetChainCompile(t *testing.T) {
	h := Handler(func(c *Ctx) error { return nil })
	mws := []Middleware{countingMW(), countingMW(), countingMW(), countingMW(), countingMW()}

	// Compiling allocates — it builds closures. What must not allocate is calling
	// the result, which is what a request does.
	compiled := chainCompileForTest(h, mws)

	chainSink = 0
	if err := compiled(nil); err != nil {
		t.Fatalf("compiled chain returned %v, want nil", err)
	}
	if chainSink != 5 {
		t.Fatalf("%d middleware ran, want 5", chainSink)
	}

	budget(t, "compiled five-middleware chain call", 0, func() {
		_ = compiled(nil)
	})
}
```

`chainCompileForTest` does not exist. Add it to `build.go` as a thin, unexported
wrapper so the test can reach the compiler without `alloc_test.go` importing
`internal/chain` directly:

```go
// chainCompileForTest exposes the chain compiler to this package's tests. It is
// not used by production code; the build phase calls chain.Compile directly.
func chainCompileForTest(h Handler, mws []Middleware) Handler {
	return chain.Compile(h, mws)
}
```

- [ ] **Step 2: Run the budget tests**

Run: `go test . -run TestAllocBudget -v`

Expected: PASS for every budget test, the M1 to M3 ones included.

If a budget fails, do not raise it — a budget is a design contract and raising one
is a design change this task is not authorised to make. Investigate:

```bash
go test -run TestAllocBudgetDispatchFiveMiddleware -memprofile mem.out .
go tool pprof -top -alloc_objects mem.out
```

The first suspects are a closure capturing something that escapes, and
`middlewareFor` being called per request rather than only in `build`.

- [ ] **Step 3: Prove the five-middleware budget can fail**

The budget's precondition check is what stops it measuring a shorter chain, so
confirm the whole test goes red when the property breaks. Temporarily make
`middlewareFor` return only the application-level middleware:

```bash
# in build.go's middlewareFor, return early with just a.mws
go test . -run TestAllocBudgetDispatchFiveMiddleware -v
```

Expected: FAIL at the precondition, reporting `3 middleware ran, want 5` — not a
passing budget over a chain that lost two middleware. Restore and re-run. Put both
outputs in your report.

- [ ] **Step 4: Write the behaviour tests**

Create `middleware_behaviour_test.go`:

```go
package rice

import (
	"errors"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// TestMiddlewareCanShortCircuit records ADR-0003's reason for the decorator
// shape: stopping the chain is an ordinary return, not a rule about calling Next.
func TestMiddlewareCanShortCircuit(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "outer"))
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			log = append(log, "gate")
			return c.String(403, "denied")
		}
	})
	app.GET("/x", traceHandler(&log))

	status := dispatch(app, "/x")

	if status != 403 {
		t.Errorf("status = %d, want 403", status)
	}
	if strings.Contains(strings.Join(log, " "), "handler") {
		t.Errorf("trace = %q; the handler must not run when a middleware short-circuits", log)
	}
	if got := strings.Join(log, " "); got != "outer-in gate outer-out" {
		t.Errorf("trace = %q, want %q", got, "outer-in gate outer-out")
	}
}

func TestMiddlewareErrorReachesTheErrorFunnel(t *testing.T) {
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error { return errors.New("middleware failed") }
	})
	app.GET("/x", func(c *Ctx) error { return c.String(200, "unreachable") })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/x")
	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want 500", got)
	}
	if got := string(fctx.Response.Body()); strings.Contains(got, "middleware failed") {
		t.Errorf("body %q leaked the error cause", got)
	}
}

// TestMiddlewareSeesAnErrorFromTheHandler is the other direction: a middleware
// wrapping a failing handler observes the error on the way out, which is what the
// decorator shape buys over an index walk.
func TestMiddlewareSeesAnErrorFromTheHandler(t *testing.T) {
	var seen error

	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			err := next(c)
			seen = err
			return err
		}
	})
	app.GET("/x", func(c *Ctx) error { return errors.New("handler failed") })

	dispatch(app, "/x")

	if seen == nil {
		t.Fatal("the middleware did not observe the handler's error")
	}
	if seen.Error() != "handler failed" {
		t.Errorf("middleware saw %q, want %q", seen, "handler failed")
	}
}

// TestMiddlewareCanReadRouteParameters checks the two features compose: a chain
// wraps a parameterised route and the parameters are already captured.
func TestMiddlewareCanReadRouteParameters(t *testing.T) {
	var seen string

	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			seen = c.ParamString("id")
			return next(c)
		}
	})
	app.GET("/users/:id", func(c *Ctx) error { return c.String(200, "ok") })

	if got := dispatch(app, "/users/42"); got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}
	if seen != "42" {
		t.Errorf("middleware read id = %q, want %q", seen, "42")
	}
}
```

- [ ] **Step 5: Add the integration test**

Append to `server_test.go`:

```go
func TestServeAnswersAGroupedMiddlewaredRoute(t *testing.T) {
	app := rice.New()

	app.Use(func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			c.SetHeader("X-App", "1")
			return next(c)
		}
	})

	g := app.Group("/api", func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			c.SetHeader("X-Group", "1")
			return next(c)
		}
	})
	g.GET("/users/:id", func(c *rice.Ctx) error {
		return c.String(200, "user "+c.ParamString("id"))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	resp, err := http.Get("http://" + addr + "/api/users/42")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := string(body); got != "user 42" {
		t.Errorf("body = %q, want %q", got, "user 42")
	}
	if got := resp.Header.Get("X-App"); got != "1" {
		t.Errorf("X-App = %q; application middleware did not run over a socket", got)
	}
	if got := resp.Header.Get("X-Group"); got != "1" {
		t.Errorf("X-Group = %q; group middleware did not run over a socket", got)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}
```

- [ ] **Step 6: Run the full suite with the race detector**

Run: `make test`

Expected: PASS.

- [ ] **Step 7: Update the performance model**

In `docs/05-performance-model.md`, change these two rows:

```markdown
| Chain call, 0 middleware | 0 | MEASURED M4 |
| Chain call, 5 middleware | 0 | MEASURED M4 |
```

Leave every other row alone. The end-to-end row stays at 1 allocation: middleware
adds nothing to it, and `TestAllocBudgetDispatchFiveMiddleware` is what proves
that rather than asserts it.

- [ ] **Step 8: Verify and commit**

```bash
make lint
make test
make bench
git add alloc_test.go middleware_behaviour_test.go server_test.go build.go docs/05-performance-model.md
git commit -m "test: enforce M4 chain allocation budgets and middleware behaviour"
```

---
### Task 5: Benchmarks

**Files:**
- Create: `bench/chain_bench_test.go`
- Create: `bench/results/M4-middleware-and-groups.txt` (generated, then committed)

**Interfaces:**
- Consumes: rice's exported API only — `New`, `Use`, `Group`, `GET`, `FasthttpHandler`, `Build`.
- Produces: the recorded numbers Task 6 writes into the retrospective.

This task answers the milestone's question. Every behavioural test in this plan
would still pass if a five-middleware chain cost forty nanoseconds more than a
bare handler, so the benchmark is the only thing that can catch it.

- [ ] **Step 1: Write the benchmarks**

Create `bench/chain_bench_test.go`:

```go
package bench

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
)

// benchSink counts middleware invocations. It is a package-level variable so the
// compiler cannot discard the increments and optimise an entire chain away, which
// would make these benchmarks measure a bare handler while claiming otherwise.
var benchSink int

// passthroughMW does the least work a middleware can do while still being
// impossible to elide: one increment of a package-level counter, then next.
func passthroughMW() rice.Middleware {
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			benchSink++
			return next(c)
		}
	}
}

// chainApp builds an app with n application-level middleware on one static route,
// and returns its handler plus a warmed request context.
func chainApp(b *testing.B, n int) (fasthttp.RequestHandler, *fasthttp.RequestCtx) {
	b.Helper()

	app := rice.New()
	for i := 0; i < n; i++ {
		app.Use(passthroughMW())
	}
	app.GET("/x", func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "ok") })

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/x")

	benchSink = 0
	h(fctx)

	if fctx.Response.StatusCode() != fasthttp.StatusOK {
		b.Fatalf("probe returned %d; this would measure the 404 path", fctx.Response.StatusCode())
	}
	if benchSink != n {
		b.Fatalf("%d middleware ran, want %d; this benchmark would measure a shorter chain than it claims", benchSink, n)
	}
	return h, fctx
}

func benchmarkChainDispatch(b *testing.B, n int) {
	h, fctx := chainApp(b, n)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkChainDispatch0 is the control: the same dispatch with no middleware.
// Every other figure in this file is read against it.
func BenchmarkChainDispatch0(b *testing.B) { benchmarkChainDispatch(b, 0) }

func BenchmarkChainDispatch1(b *testing.B) { benchmarkChainDispatch(b, 1) }

// BenchmarkChainDispatch5 is the roadmap's stated case. Against
// BenchmarkChainDispatch0 it answers the milestone's question: whether compiling
// at build time removes the per-request cost, or whether the closure indirection
// eats the gain.
func BenchmarkChainDispatch5(b *testing.B) { benchmarkChainDispatch(b, 5) }

// BenchmarkChainDispatch5Grouped runs the same five middleware, arriving through
// three nested groups instead of five Use calls. Groups exist only at registration
// time, so this should be indistinguishable from BenchmarkChainDispatch5 — and if
// it is not, groups are costing something at request time that they should not.
func BenchmarkChainDispatch5Grouped(b *testing.B) {
	app := rice.New()
	app.Use(passthroughMW())

	outer := app.Group("/a", passthroughMW())
	inner := outer.Group("/b", passthroughMW(), passthroughMW())
	inner.GET("/c", func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "ok") }, passthroughMW())

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/a/b/c")

	benchSink = 0
	h(fctx)

	if fctx.Response.StatusCode() != fasthttp.StatusOK {
		b.Fatalf("probe returned %d; this would measure the 404 path", fctx.Response.StatusCode())
	}
	if benchSink != 5 {
		b.Fatalf("%d middleware ran, want 5", benchSink)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkBuild1000Routes measures the build phase itself. The design inserts
// every route twice on purpose — once at registration to reject bad configuration
// where it was written, once in build with the compiled chain — so the startup
// cost of that choice should be a number rather than a shrug.
func BenchmarkBuild1000Routes(b *testing.B) {
	paths := make([]string, 1000)
	for i := range paths {
		paths[i] = "/route/" + strconv.Itoa(i)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		app := rice.New()
		app.Use(passthroughMW(), passthroughMW(), passthroughMW())
		for _, p := range paths {
			app.GET(p, func(c *rice.Ctx) error { return nil })
		}
		b.StartTimer()

		app.Build()
	}
}
```

- [ ] **Step 2: Run the benchmarks and read the numbers**

```bash
go test ./bench/... -run '^$' -bench 'BenchmarkChain|BenchmarkBuild' -benchmem -count=1
```

Expected: every `ChainDispatch` figure reports `1 allocs/op` — the `Ctx`, and
nothing from the chain. `BenchmarkChainDispatch5Grouped` should sit within noise of
`BenchmarkChainDispatch5`.

Read `ChainDispatch5` against `ChainDispatch0` and write the difference down. That
difference is five closure calls and nothing else. If it is larger than a few
nanoseconds, stop and find out why before recording — the design claims M4 adds
nothing per request, and a number that contradicts it is the finding, not an
inconvenience.

If any benchmark reports more than 1 alloc/op, stop and investigate rather than
recording. A recorded number nobody can explain is worse than no number, and this
project has spent six review rounds on claims that turned out to have nothing
behind them.

- [ ] **Step 3: Record the results**

```bash
make bench-record LABEL=M4-middleware-and-groups
```

This runs every benchmark ten times and takes several minutes — there are now
around twenty. Let it finish. The M3 benchmarks run in the same recording, so the
whole table stays comparable on one machine in one session.

- [ ] **Step 4: Verify**

```bash
make lint
make test
make bench
git status --short
```

Expected: the three make targets exit 0, and only the new results file is
untracked.

- [ ] **Step 5: Commit**

```bash
git add bench
git commit -m "bench: measure the cost of a compiled middleware chain"
```

---

### Task 6: Close out M4

**Files:**
- Create: `docs/milestones/M4-middleware-and-groups.md`
- Modify: `docs/04-roadmap.md` (M4 status marker)
- Modify: `docs/progress.md` (prepend an entry)

**Interfaces:**
- Consumes: the recorded numbers from Task 5.
- Produces: nothing other tasks depend on.

- [ ] **Step 1: Write the milestone retrospective**

Create `docs/milestones/M4-middleware-and-groups.md` from
`docs/milestones/TEMPLATE.md`, filling in every section.

Read `docs/milestones/M3-radix-tree-router.md` first and match its voice and its
candour. That document refuses a comparison its numbers do not support, separates
two figures that are both correct, and retracts a prediction an earlier milestone
made. This one is held to the same standard.

The Measurements table takes the figures from
`bench/results/M4-middleware-and-groups.txt`, with the hardware line copied from
that file's stamp header. Report `ChainDispatch0`, `1`, `5`, `5Grouped` and
`Build1000Routes`.

Four things the retrospective must address directly:

1. **Did compiling at build time remove the per-request cost?** That is the
   milestone's question. Answer it with the `ChainDispatch5` minus
   `ChainDispatch0` difference and say what the difference consists of. If five
   closure calls are lost in the noise above M3's dispatch cost, say so plainly —
   that is the answer, not a disappointment.

2. **Did groups cost anything at request time?** `ChainDispatch5Grouped` against
   `ChainDispatch5` is the test. The design says groups exist only at registration
   time; state whether the numbers agree.

3. **What did inserting every route twice cost?** `BenchmarkBuild1000Routes` is
   that number. D1 traded startup time for panics at the call site that caused the
   mistake. Say what the trade actually cost, so the next person deciding a similar
   question has a figure rather than an intuition.

4. **The per-request allocation is still one, and M4 is not why it is large.** M3
   grew the `Ctx` to 344 bytes; M4 adds nothing to it. Do not take credit for a
   flat line that a previous milestone already paid for, and do not repeat M3's
   cost as though it were new.

- [ ] **Step 2: Mark M4 done in the roadmap**

In `docs/04-roadmap.md`, change the M4 heading marker from `☐` to `☑`:

```
### ☑ M4 — Middleware and groups
```

- [ ] **Step 3: Prepend a journal entry**

In `docs/progress.md`, insert a new M4 entry at the top of the entry list, above
the M3 entry, using the four-field shape: Did, Learned, Measured, Next. The journal
is append-only; do not edit any existing entry.

The Measured field carries the `ChainDispatch5` against `ChainDispatch0`
comparison. The Next field points at M5 and says in one sentence what M5 has to
prove.

Watch out for a shell quoting hazard this project has hit: apostrophes inside a
single-quoted heredoc or an awk program can silently emit backticks instead. Prefer
writing prose with the file-editing tools rather than a heredoc, and check the
result for stray backticks where apostrophes belong.

- [ ] **Step 4: Verify the links**

```bash
grep -n "M4" docs/04-roadmap.md docs/progress.md docs/milestones/M4-middleware-and-groups.md
```

Expected: the roadmap shows `☑ M4`, and both the journal and the milestone doc
reference `bench/results/M4-middleware-and-groups.txt`. Confirm the relative links
resolve from the file each lives in — `docs/progress.md` reaches the results file
as `../bench/results/...`, while `docs/milestones/M4-middleware-and-groups.md`
needs `../../bench/results/...`.

- [ ] **Step 5: Final verification**

```bash
make lint
make test
make cover
make bench
git status --short
```

Expected: lint, test and bench exit 0, and `git status --short` is empty. Confirm
by eye that the roadmap shows `☑` for M0 through M4 and `☐` for M5 through M8.

- [ ] **Step 6: Commit**

```bash
git add docs
git commit -m "docs: close out M4 with retrospective, chain numbers, and journal entry"
```

---

## Plan Self-Review

**Spec coverage:**

| Spec item | Task |
| --- | --- |
| D1 registration inserts eagerly; build constructs fresh trees; `route` record shape | 2 |
| D2 `Build` exported, returns nothing, panics; `Run`/`Serve`/`FasthttpHandler` trigger it under `sync.Once` | 2 |
| D3 `Group` holds a parent pointer, resolves middleware at build; the slice-aliasing hazard | 2 (`groupChain`, `middlewareFor`), 3 (the `Group` itself and both aliasing tests) |
| D4 group prefix constrained so joining is total; empty prefix allowed | 3 |
| D5 `internal/chain.Compile`, generic, empty slice returns the handler unwrapped | 1 |
| D6 order app → outer group → inner group → route → handler, and the unwind | 1 (compiler level), 2 (app level), 3 (three-level nesting) |
| D7 the hot path does not change; `Ctx` gains no field | 2 (no `Ctx` change is made), 4 (the budgets that prove it), 5 (the benchmarks) |
| D8 registration and `Use` after `Build` panic; `Build` idempotent | 2 (App level), 3 (Group level) |
| Public API: `Middleware`, `App.Use`, `App.Group`, `App.Build`, `Group` and its methods | 2, 3 |
| Both `Chain call` budget rows move to measured | 4 |
| Short-circuit, error through the funnel, middleware reading parameters | 4 |
| Integration over a real socket | 4 |
| Benchmarks including the grouped variant and the build phase | 5 |
| Retrospective, roadmap marker, journal entry | 6 |

Every spec item maps to a task. The spec's two open questions are answered by
Task 5's numbers (whether five closure calls are measurable) and deferred with a
recorded reason (whether the double insertion needs more measurement than
`BenchmarkBuild1000Routes`).

**Placeholder scan:** no `TBD`, no `TODO`, no "add appropriate error handling", no
"similar to Task N". Every code step carries its code. Task 6's three writing steps
give the required content and the standard to meet rather than the prose itself,
which is deliberate: a retrospective written from a template is the one thing
`docs/milestones/TEMPLATE.md` explicitly forbids.

**Type consistency:** `chain.Compile[H any, M ~func(H) H](h H, mws []M) H` is
defined in Task 1 and used in Tasks 2 and 4. `Middleware`, `route`, `App.register`,
`App.middlewareFor`, `groupChain` and `chainCompileForTest` are defined in Task 2
and used in Tasks 2, 3 and 4. `Group` and its `app`/`parent`/`prefix`/`mws` fields
are declared in Task 2 and completed in Task 3. `checkGroupPrefix` is Task 3 only.
`traceMW`, `traceHandler` and `dispatch` are defined in Task 2's `build_test.go`
and reused in Tasks 3 and 4. `budget(t, name, want, fn)` is reused from M1's
`alloc_test.go`; `mustPanic` from M2's `route_test.go`; `newRequestCtx` from M0's
`bench/helpers_test.go`; `waitForAddr` and `get` from M1's `server_test.go`.

**Compilation-unit check:** Task 1 is a new standalone package. Task 2 is the large
one, and it is large for a reason — `build.go` needs `Group`'s shape to walk a
route's group chain, so Task 2 declares the struct and Task 3 fills it in. Splitting
that any further would leave a task that does not build. Task 3 replaces `group.go`
wholesale. Tasks 4, 5 and 6 add tests, benchmarks and documents. Each task ends with
`go build ./...` succeeding.

**A note on Task 2's size.** It touches nine files, which is more than any other
task in this plan or the last two. The reason is the signature change: adding
`mw ...Middleware` to eight registration methods, routing them through one
`register`, and adding `Build` are one atomic change, and the fourteen direct
`handle` call sites must migrate in the same commit or the suite tests a state no
user can reach. The alternative was a task that leaves middleware silently
unapplied in the test suite, which is the exact defect ADR-0003 exists to prevent.
