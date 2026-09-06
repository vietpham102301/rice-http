# M2 Implementation Plan — Static Router

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `App.SetHandler` with per-method route registration backed by a deliberately naive `map[string]Handler` per verb, and record the lookup numbers at 10, 100 and 1000 routes that M3's radix tree must beat.

**Architecture:** A generic `internal/router.Tree[H]` owns exact-match path storage and knows nothing about rice. Package `rice` holds one tree per common verb in a fixed-size array indexed by a `method` constant, with a lazily created map for uncommon verbs. `App.handle` looks up before it allocates a `Ctx`, and a miss travels the existing error funnel as the package-level sentinel `ErrNotFound`.

**Tech Stack:** Go 1.25, `github.com/valyala/fasthttp` (the only runtime dependency), GNU make.

**Spec:** [`docs/superpowers/specs/2026-09-06-m2-static-router-design.md`](../specs/2026-09-06-m2-static-router-design.md). Milestone definition and exit criteria in [`docs/04-roadmap.md`](../../04-roadmap.md). Allocation budgets in [`docs/05-performance-model.md`](../../05-performance-model.md).

## Global Constraints

Every task's requirements implicitly include this section.

- Module path is `github.com/vietpham102301/rice-http`; root package is `rice`. Imports in test files outside the package use the alias `rice "github.com/vietpham102301/rice-http"`.
- Nothing under `internal/` may import package `rice` (`docs/02-architecture.md`). This is why the router is generic.
- `github.com/valyala/fasthttp` stays the only runtime dependency. Package `rice` must not import `reflect` or `encoding/json` (ADR-0006).
- Every value handed out by `Ctx` is borrowed and dies when the handler returns (ADR-0005).
- Allocation budgets M2 must meet, asserted with `testing.AllocsPerRun` rather than merely observed: `methodIndex` = 0, `App.lookup` on a hit = 0, `App.lookup` on a miss = 0.
- End-to-end per-request allocation stays at 1, the `Ctx`. Do not optimise it. M6 removes it.
- Code belonging to a later milestone carries a comment naming that milestone.
- Commit after every task, using Conventional Commits (`feat:`, `test:`, `chore:`, `docs:`, `bench:`).
- **Task boundaries follow compilation units.** Every task must end with `go build ./...` succeeding. This is the lesson M1 recorded the hard way: its Task 5 declared a field whose type arrived two tasks later, and three tasks had to be merged during execution.

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `internal/router/router.go` | `Tree[H]`, `Insert`, `Lookup`, `Len`, `ErrDuplicate` | 1 |
| `internal/router/router_test.go` | Router unit tests, no reference to rice | 1 |
| `method.go` | `method` type, verb constants, `methodIndex` | 2 |
| `method_test.go` | Verb mapping and its allocation budget | 2 |
| `route.go` | `Handle`, seven verb helpers, `treeFor`, `App.lookup` | 3 |
| `route_test.go` | Registration, validation panics, per-verb isolation | 3 |
| `errors.go` | `ErrNotFound` | 4 |
| `app.go` | Modify: tree fields, `handle` rewrite, `handleError` branch, remove `SetHandler` | 4 |
| `app_test.go` | Modify: migrate 4 `SetHandler` call sites, add 404 funnel tests | 4 |
| `server_test.go` | Modify: migrate 4 `SetHandler` call sites, add a 404-over-a-socket test | 4 |
| `alloc_test.go` | Modify: add lookup budgets | 5 |
| `bench/rice_bench_test.go` | Modify: migrate 2 `SetHandler` call sites | 4 |
| `bench/router_bench_test.go` | Lookup benchmarks at 10, 100, 1000, plus the miss path | 5 |
| `bench/results/M2-static-router.txt` | Generated, then committed | 5 |
| `docs/05-performance-model.md` | Modify: one row moves to `MEASURED M2` | 5 |
| `docs/milestones/M2-static-router.md` | Retrospective | 6 |
| `docs/04-roadmap.md`, `docs/progress.md` | Modify: status marker, journal entry | 6 |

---

### Task 1: The generic router

**Files:**
- Create: `internal/router/router.go`
- Test: `internal/router/router_test.go`

**Interfaces:**
- Consumes: nothing. This package compiles and tests entirely on its own.
- Produces:
  - `type Tree[H any] struct{...}` — zero value ready to use
  - `func (t *Tree[H]) Insert(path string, h H) error`
  - `func (t *Tree[H]) Lookup(path []byte) (H, bool)`
  - `func (t *Tree[H]) Len() int`
  - `var ErrDuplicate error`

- [ ] **Step 1: Write the failing tests**

Create `internal/router/router_test.go`:

```go
package router

import (
	"errors"
	"testing"
)

func TestInsertAndLookup(t *testing.T) {
	var tr Tree[string]

	if err := tr.Insert("/users", "handler-a"); err != nil {
		t.Fatalf("Insert returned %v, want nil", err)
	}

	got, ok := tr.Lookup([]byte("/users"))
	if !ok {
		t.Fatal("Lookup did not find a route that was just inserted")
	}
	if got != "handler-a" {
		t.Errorf("Lookup returned %q, want %q", got, "handler-a")
	}
}

func TestLookupMissReturnsTheZeroValue(t *testing.T) {
	var tr Tree[string]
	if err := tr.Insert("/users", "handler-a"); err != nil {
		t.Fatalf("Insert returned %v, want nil", err)
	}

	got, ok := tr.Lookup([]byte("/absent"))
	if ok {
		t.Error("Lookup found a route that was never inserted")
	}
	if got != "" {
		t.Errorf("Lookup returned %q on a miss, want the zero value", got)
	}
}

// TestLookupOnAnEmptyTree exercises the nil-map read path. A Tree is usable as
// its zero value, so the map does not exist until the first Insert.
func TestLookupOnAnEmptyTree(t *testing.T) {
	var tr Tree[string]

	if _, ok := tr.Lookup([]byte("/anything")); ok {
		t.Error("Lookup found a route in an empty tree")
	}
}

func TestInsertRejectsADuplicate(t *testing.T) {
	var tr Tree[string]
	if err := tr.Insert("/users", "first"); err != nil {
		t.Fatalf("first Insert returned %v, want nil", err)
	}

	err := tr.Insert("/users", "second")
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Insert returned %v, want ErrDuplicate", err)
	}

	got, _ := tr.Lookup([]byte("/users"))
	if got != "first" {
		t.Errorf("a rejected duplicate overwrote the original: got %q, want %q", got, "first")
	}
}

func TestLenReflectsInsertCount(t *testing.T) {
	var tr Tree[string]

	if got := tr.Len(); got != 0 {
		t.Errorf("Len() = %d on a new tree, want 0", got)
	}

	_ = tr.Insert("/a", "a")
	_ = tr.Insert("/b", "b")
	if got := tr.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}

	_ = tr.Insert("/a", "duplicate")
	if got := tr.Len(); got != 2 {
		t.Errorf("Len() = %d after a rejected duplicate, want 2", got)
	}
}

// TestLookupDoesNotRetainThePathSlice is the borrow contract applied to the
// router. fasthttp reuses the buffer a request path points into for the next
// request on the same connection, so Lookup must not keep a reference to it.
func TestLookupDoesNotRetainThePathSlice(t *testing.T) {
	var tr Tree[string]
	_ = tr.Insert("/aaa", "route-a")
	_ = tr.Insert("/bbb", "route-b")

	buf := []byte("/aaa")

	if got, _ := tr.Lookup(buf); got != "route-a" {
		t.Fatalf("first lookup returned %q, want %q", got, "route-a")
	}

	copy(buf, "/bbb") // overwrite in place, as fasthttp would

	if got, _ := tr.Lookup(buf); got != "route-b" {
		t.Errorf("after overwriting the buffer, lookup returned %q, want %q", got, "route-b")
	}
	if got, _ := tr.Lookup([]byte("/aaa")); got != "route-a" {
		t.Errorf("overwriting a caller buffer corrupted the tree: /aaa returned %q", got)
	}
}

// TestTreeWorksWithAFuncType pins the shape rice actually instantiates.
// Handler is a func type, and func types are not comparable, so an
// implementation that ever compared two handlers would fail to build here.
func TestTreeWorksWithAFuncType(t *testing.T) {
	type handler func() string

	var tr Tree[handler]
	if err := tr.Insert("/x", func() string { return "called" }); err != nil {
		t.Fatalf("Insert returned %v, want nil", err)
	}

	h, ok := tr.Lookup([]byte("/x"))
	if !ok {
		t.Fatal("Lookup did not find the handler")
	}
	if got := h(); got != "called" {
		t.Errorf("handler returned %q, want %q", got, "called")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/... -v`

Expected: FAIL to compile, with `undefined: Tree` and `undefined: ErrDuplicate`.

- [ ] **Step 3: Write the router**

Create `internal/router/router.go`:

```go
// Package router maps request paths to handlers.
//
// It is generic over the handler type because docs/02-architecture.md forbids
// anything under internal/ from importing package rice, so the router cannot
// name rice.Handler.
//
// M2 backs Tree with a map, which is exact-match only: no parameters, no
// wildcards, no trailing-slash handling. M3 replaces the internals with a radix
// tree without changing this API.
package router

import "errors"

// ErrDuplicate is returned by Insert when a path is already registered.
var ErrDuplicate = errors.New("router: duplicate route")

// Tree maps request paths to handlers.
//
// The zero value is ready to use; the underlying map is created on first Insert.
type Tree[H any] struct {
	routes map[string]H
}

// Insert registers h at path.
//
// It returns ErrDuplicate if path is already registered, leaving the existing
// handler in place. Deciding whether that is fatal belongs to the caller.
func (t *Tree[H]) Insert(path string, h H) error {
	if _, exists := t.routes[path]; exists {
		return ErrDuplicate
	}
	if t.routes == nil {
		t.routes = make(map[string]H)
	}
	t.routes[path] = h
	return nil
}

// Lookup returns the handler registered at path.
//
// The path is borrowed: Lookup does not retain it, so the caller may reuse the
// backing array as soon as the call returns.
//
// The map probe is deliberately written as t.routes[string(path)] on one line.
// The compiler special-cases that exact form and does not copy the bytes.
// Hoisting it into a variable — key := string(path) — loses the optimisation
// and costs one allocation on every request. alloc_test.go pins this down,
// because the regression would be invisible to every other test.
func (t *Tree[H]) Lookup(path []byte) (H, bool) {
	h, ok := t.routes[string(path)]
	return h, ok
}

// Len returns the number of registered routes.
func (t *Tree[H]) Len() int { return len(t.routes) }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/... -v`

Expected: PASS for all seven tests.

- [ ] **Step 5: Verify the package builds and is lint-clean**

Run: `go build ./... && make lint`

Expected: both exit 0.

- [ ] **Step 6: Commit**

```bash
git add internal/router
git commit -m "feat: add generic internal/router with exact-match lookup"
```

---

### Task 2: Method dispatch

**Files:**
- Create: `method.go`
- Test: `method_test.go`

**Interfaces:**
- Consumes: nothing from Task 1. This task touches package `rice` only and compiles on its own.
- Produces:
  - `type method uint8`
  - constants `mGET`, `mPOST`, `mPUT`, `mPATCH`, `mDELETE`, `mHEAD`, `mOPTIONS`, `methodCount`
  - `func methodIndex(m []byte) (method, bool)`

- [ ] **Step 1: Write the failing tests**

Create `method_test.go`:

```go
package rice

import "testing"

func TestMethodIndexMapsEveryCommonVerb(t *testing.T) {
	cases := []struct {
		verb string
		want method
	}{
		{"GET", mGET},
		{"POST", mPOST},
		{"PUT", mPUT},
		{"PATCH", mPATCH},
		{"DELETE", mDELETE},
		{"HEAD", mHEAD},
		{"OPTIONS", mOPTIONS},
	}

	for _, c := range cases {
		got, ok := methodIndex([]byte(c.verb))
		if !ok {
			t.Errorf("methodIndex(%q) reported the verb as uncommon", c.verb)
			continue
		}
		if got != c.want {
			t.Errorf("methodIndex(%q) = %d, want %d", c.verb, got, c.want)
		}
	}
}

func TestMethodIndexRejectsUncommonVerbs(t *testing.T) {
	for _, verb := range []string{"PROPFIND", "TRACE", "CONNECT", "", "get", "GETX", "GE"} {
		if _, ok := methodIndex([]byte(verb)); ok {
			t.Errorf("methodIndex(%q) claimed the verb is common, want false", verb)
		}
	}
}

// TestEveryCommonVerbHasADistinctIndex guards the fixed-size array: two verbs
// sharing an index would silently route one verb's traffic to the other's tree.
func TestEveryCommonVerbHasADistinctIndex(t *testing.T) {
	seen := make(map[method]string)

	for _, verb := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		i, ok := methodIndex([]byte(verb))
		if !ok {
			t.Fatalf("methodIndex(%q) reported the verb as uncommon", verb)
		}
		if other, clash := seen[i]; clash {
			t.Errorf("%q and %q both map to index %d", verb, other, i)
		}
		seen[i] = verb

		if i >= methodCount {
			t.Errorf("methodIndex(%q) = %d, which is outside the trees array of size %d", verb, i, methodCount)
		}
	}

	if len(seen) != int(methodCount) {
		t.Errorf("%d verbs map to indices but methodCount is %d", len(seen), methodCount)
	}
}

func TestAllocBudgetMethodIndex(t *testing.T) {
	verbs := [][]byte{
		[]byte("GET"), []byte("POST"), []byte("PUT"), []byte("PATCH"),
		[]byte("DELETE"), []byte("HEAD"), []byte("OPTIONS"), []byte("PROPFIND"),
	}

	budget(t, "methodIndex", 0, func() {
		for _, v := range verbs {
			_, _ = methodIndex(v)
		}
	})
}
```

Note: `budget` already exists in `alloc_test.go` from M1 and is reused here.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -run 'TestMethodIndex|TestEveryCommonVerb|TestAllocBudgetMethodIndex' -v`

Expected: FAIL to compile, with `undefined: mGET` and `undefined: methodIndex`.

- [ ] **Step 3: Write the method mapping**

Create `method.go`:

```go
package rice

// method indexes App's per-verb route trees.
//
// Dispatch on a common verb is therefore an array index, not a map lookup and
// not a string comparison. See docs/02-architecture.md.
type method uint8

const (
	mGET method = iota
	mPOST
	mPUT
	mPATCH
	mDELETE
	mHEAD
	mOPTIONS

	// methodCount is the size of App's trees array. It must stay last.
	methodCount
)

// methodIndex maps an HTTP verb to its tree index, reporting false for verbs
// that do not have a reserved slot.
//
// It switches on length first so that most verbs are separated before any byte
// is compared. Each comparison is written as string(m) == "LITERAL", which the
// compiler evaluates without copying the bytes, so the whole function allocates
// nothing. TestAllocBudgetMethodIndex pins that down rather than assuming it.
func methodIndex(m []byte) (method, bool) {
	switch len(m) {
	case 3:
		if string(m) == "GET" {
			return mGET, true
		}
		if string(m) == "PUT" {
			return mPUT, true
		}
	case 4:
		if string(m) == "POST" {
			return mPOST, true
		}
		if string(m) == "HEAD" {
			return mHEAD, true
		}
	case 5:
		if string(m) == "PATCH" {
			return mPATCH, true
		}
	case 6:
		if string(m) == "DELETE" {
			return mDELETE, true
		}
	case 7:
		if string(m) == "OPTIONS" {
			return mOPTIONS, true
		}
	}
	return 0, false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -run 'TestMethodIndex|TestEveryCommonVerb|TestAllocBudgetMethodIndex' -v`

Expected: PASS for all four tests.

If `TestAllocBudgetMethodIndex` fails, the compiler did not apply the `string(b) == "literal"` optimisation. Do not raise the budget. Find out what allocated:

```bash
go test -run TestAllocBudgetMethodIndex -memprofile mem.out .
go tool pprof -top -alloc_objects mem.out
```

- [ ] **Step 5: Commit**

```bash
git add method.go method_test.go
git commit -m "feat: add non-allocating HTTP verb to tree index mapping"
```

---

### Task 3: Route registration

**Files:**
- Create: `route.go`
- Modify: `app.go` (add the two tree fields to the `App` struct; nothing else changes yet)
- Test: `route_test.go`

**Interfaces:**
- Consumes: `Tree[H]` from Task 1, `method`/`methodIndex` from Task 2, `Handler` and `App` from M1.
- Produces:
  - `func (a *App) Handle(method, path string, h Handler)`
  - `func (a *App) GET(path string, h Handler)` and the same for `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD`, `OPTIONS`
  - `func (a *App) lookup(method, path []byte) (Handler, bool)` (unexported, used by Task 4)

`SetHandler` and `handle` are deliberately untouched in this task. Registration lands first and the package keeps building and passing its existing tests; Task 4 switches dispatch over and removes the old entry point in one move, because removing it breaks ten call sites at once.

- [ ] **Step 1: Add the tree fields to App**

In `app.go`, add the import and extend the struct. The `h Handler` field stays for now — Task 4 removes it.

```go
import (
	"net"
	"sync"

	"github.com/valyala/fasthttp"

	"github.com/vietpham102301/rice-http/internal/router"
)

// App is the root of a rice application. It owns the routes, the fasthttp
// server, and the listener.
type App struct {
	// h is M1 scaffolding, removed in this milestone's dispatch task.
	h Handler

	// trees holds one route tree per common verb, indexed by a method constant.
	// It is an array of values, so every element starts as a zero Tree whose
	// inner map is nil until its first Insert.
	trees [methodCount]router.Tree[Handler]

	// rare holds trees for verbs without a reserved slot. It stays nil for
	// applications that never register one.
	rare map[string]*router.Tree[Handler]

	srv *fasthttp.Server

	mu sync.Mutex
	ln net.Listener
}
```

- [ ] **Step 2: Write the failing tests**

Create `route_test.go`:

```go
package rice

import (
	"strings"
	"testing"
)

// registeredVerbs pairs each helper with the verb it must register under, so
// that a copy-paste error in one helper is caught rather than assumed absent.
func registeredVerbs() []struct {
	verb string
	call func(a *App, path string, h Handler)
} {
	return []struct {
		verb string
		call func(a *App, path string, h Handler)
	}{
		{"GET", (*App).GET},
		{"POST", (*App).POST},
		{"PUT", (*App).PUT},
		{"PATCH", (*App).PATCH},
		{"DELETE", (*App).DELETE},
		{"HEAD", (*App).HEAD},
		{"OPTIONS", (*App).OPTIONS},
	}
}

func TestEachVerbHelperRegistersUnderItsOwnVerb(t *testing.T) {
	for _, c := range registeredVerbs() {
		app := New()
		c.call(app, "/x", func(ctx *Ctx) error { return nil })

		if _, ok := app.lookup([]byte(c.verb), []byte("/x")); !ok {
			t.Errorf("%s helper did not register a route reachable by %s", c.verb, c.verb)
		}

		for _, other := range registeredVerbs() {
			if other.verb == c.verb {
				continue
			}
			if _, ok := app.lookup([]byte(other.verb), []byte("/x")); ok {
				t.Errorf("%s helper also registered the route under %s", c.verb, other.verb)
			}
		}
	}
}

func TestLookupMissesAnUnregisteredPath(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })

	if _, ok := app.lookup([]byte("GET"), []byte("/absent")); ok {
		t.Error("lookup found a path that was never registered")
	}
}

func TestHandleSupportsAnUncommonVerb(t *testing.T) {
	app := New()
	app.Handle("PROPFIND", "/dav", func(c *Ctx) error { return nil })

	if _, ok := app.lookup([]byte("PROPFIND"), []byte("/dav")); !ok {
		t.Error("an uncommon verb registered with Handle was not reachable")
	}
	if _, ok := app.lookup([]byte("GET"), []byte("/dav")); ok {
		t.Error("an uncommon verb's route leaked into a common verb's tree")
	}
}

func TestLookupOnAnUncommonVerbWithNoneRegistered(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error { return nil })

	// rare is still nil here; lookup must not panic on it.
	if _, ok := app.lookup([]byte("PROPFIND"), []byte("/x")); ok {
		t.Error("lookup found a route for a verb that was never registered")
	}
}

// mustPanic runs fn and returns the panic value, failing the test if fn returns
// normally.
func mustPanic(t *testing.T, name string, fn func()) any {
	t.Helper()

	var got any
	func() {
		defer func() { got = recover() }()
		fn()
	}()

	if got == nil {
		t.Fatalf("%s did not panic, want a panic", name)
	}
	return got
}

func TestRegistrationPanicsOnAnEmptyPath(t *testing.T) {
	app := New()
	mustPanic(t, `GET("")`, func() {
		app.GET("", func(c *Ctx) error { return nil })
	})
}

func TestRegistrationPanicsOnAPathWithoutALeadingSlash(t *testing.T) {
	app := New()
	v := mustPanic(t, `GET("users")`, func() {
		app.GET("users", func(c *Ctx) error { return nil })
	})

	if msg, _ := v.(string); !strings.Contains(msg, "users") {
		t.Errorf("panic message %q does not name the offending path", v)
	}
}

func TestRegistrationPanicsOnANilHandler(t *testing.T) {
	app := New()
	mustPanic(t, "GET with nil handler", func() {
		app.GET("/x", nil)
	})
}

func TestRegistrationPanicsOnADuplicateRoute(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })

	v := mustPanic(t, "duplicate GET /users", func() {
		app.GET("/users", func(c *Ctx) error { return nil })
	})

	msg, _ := v.(string)
	if !strings.Contains(msg, "/users") || !strings.Contains(msg, "GET") {
		t.Errorf("panic message %q should name both the verb and the path", v)
	}
}

// TestTheSamePathUnderDifferentVerbsIsNotADuplicate is the case the duplicate
// check must not over-reach on: REST APIs register GET and POST on one path
// constantly.
func TestTheSamePathUnderDifferentVerbsIsNotADuplicate(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })
	app.POST("/users", func(c *Ctx) error { return nil })

	if _, ok := app.lookup([]byte("GET"), []byte("/users")); !ok {
		t.Error("GET /users disappeared after POST /users was registered")
	}
	if _, ok := app.lookup([]byte("POST"), []byte("/users")); !ok {
		t.Error("POST /users was not registered")
	}
}

func TestLookupReturnsTheHandlerThatWasRegistered(t *testing.T) {
	app := New()

	marker := "not called"
	app.GET("/x", func(c *Ctx) error {
		marker = "called"
		return nil
	})

	h, ok := app.lookup([]byte("GET"), []byte("/x"))
	if !ok {
		t.Fatal("lookup did not find the route")
	}
	if err := h(nil); err != nil {
		t.Fatalf("handler returned %v, want nil", err)
	}
	if marker != "called" {
		t.Error("lookup returned a different handler than the one registered")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test . -run 'TestEachVerbHelper|TestLookup|TestHandleSupports|TestRegistrationPanics|TestTheSamePath' -v`

Expected: FAIL to compile, with `app.GET undefined` and `app.lookup undefined`.

- [ ] **Step 4: Write the registration API**

Create `route.go`:

```go
package rice

import (
	"fmt"

	"github.com/vietpham102301/rice-http/internal/router"
)

// Handle registers h for the given method and path.
//
// It panics on a programmer error: an empty path, a path without a leading
// slash, a nil handler, or a route already registered for the same method and
// path. These are mistakes discovered at startup rather than runtime
// conditions, and a duplicate in particular means one of the two handlers can
// never run — silent, and expensive to debug. Panicking at the call site puts
// the mistake in the stack trace.
//
// M4 appends a variadic mw ...Middleware parameter. Doing so does not break
// existing calls.
func (a *App) Handle(method, path string, h Handler) {
	if path == "" {
		panic("rice: route path is empty for method " + method)
	}
	if path[0] != '/' {
		panic("rice: route path " + path + " does not begin with /")
	}
	if h == nil {
		panic("rice: nil handler for " + method + " " + path)
	}

	if err := a.treeFor(method).Insert(path, h); err != nil {
		panic(fmt.Sprintf("rice: %v: %s %s", err, method, path))
	}
}

// GET registers h for GET requests to path.
func (a *App) GET(path string, h Handler) { a.Handle("GET", path, h) }

// POST registers h for POST requests to path.
func (a *App) POST(path string, h Handler) { a.Handle("POST", path, h) }

// PUT registers h for PUT requests to path.
func (a *App) PUT(path string, h Handler) { a.Handle("PUT", path, h) }

// PATCH registers h for PATCH requests to path.
func (a *App) PATCH(path string, h Handler) { a.Handle("PATCH", path, h) }

// DELETE registers h for DELETE requests to path.
func (a *App) DELETE(path string, h Handler) { a.Handle("DELETE", path, h) }

// HEAD registers h for HEAD requests to path.
func (a *App) HEAD(path string, h Handler) { a.Handle("HEAD", path, h) }

// OPTIONS registers h for OPTIONS requests to path.
func (a *App) OPTIONS(path string, h Handler) { a.Handle("OPTIONS", path, h) }

// treeFor returns the tree for method, creating one for an uncommon verb.
//
// Registration time, not request time: the []byte conversion below allocates,
// and that is fine here. The request path through lookup does not convert.
func (a *App) treeFor(method string) *router.Tree[Handler] {
	if i, ok := methodIndex([]byte(method)); ok {
		return &a.trees[i]
	}

	if a.rare == nil {
		a.rare = make(map[string]*router.Tree[Handler])
	}
	t, ok := a.rare[method]
	if !ok {
		t = &router.Tree[Handler]{}
		a.rare[method] = t
	}
	return t
}

// lookup finds the handler for a request.
//
// This is the hot path. For a common verb it costs one array index and one map
// probe, and it allocates nothing: both method and path stay as borrowed byte
// slices throughout.
func (a *App) lookup(method, path []byte) (Handler, bool) {
	if i, ok := methodIndex(method); ok {
		return a.trees[i].Lookup(path)
	}

	if a.rare == nil {
		return nil, false
	}
	t, ok := a.rare[string(method)]
	if !ok {
		return nil, false
	}
	return t.Lookup(path)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test . -run 'TestEachVerbHelper|TestLookup|TestHandleSupports|TestRegistrationPanics|TestTheSamePath' -v`

Expected: PASS for all ten tests.

- [ ] **Step 6: Run the whole suite to confirm nothing regressed**

Run: `make test`

Expected: PASS. Nothing dispatches through the router yet, so every M1 test still passes unchanged.

- [ ] **Step 7: Commit**

```bash
git add route.go route_test.go app.go
git commit -m "feat: add per-verb route registration"
```

---

### Task 4: Dispatch through the router

This task changes the dispatch path and removes `SetHandler` in one move. `SetHandler` has ten call sites across four files, and the package does not compile between removing it and migrating them, so they belong together.

**Files:**
- Create: `errors.go`
- Modify: `app.go` (remove `h` and `SetHandler`, rewrite `handle`, extend `handleError`)
- Modify: `app_test.go` (migrate 4 call sites, add funnel tests)
- Modify: `server_test.go` (migrate 4 call sites, add a 404-over-a-socket test)
- Modify: `bench/rice_bench_test.go` (migrate 2 call sites)

**Interfaces:**
- Consumes: `App.lookup` from Task 3.
- Produces: `var ErrNotFound error`.

- [ ] **Step 1: Write the sentinel**

Create `errors.go`:

```go
package rice

import "errors"

// ErrNotFound is passed into the error funnel when no route matches the
// request. The error handler turns it into a 404.
//
// It is a package-level value created once at init, so returning it costs no
// allocation — which matters, because it is returned on the path that mistaken
// and hostile traffic hits hardest.
//
// M5 generalises the funnel to an HTTPError type. errors.Is keeps working
// against this sentinel, so checks written against it today keep working then.
var ErrNotFound = errors.New("rice: not found")
```

- [ ] **Step 2: Write the failing dispatch tests**

In `app_test.go`, first migrate the four existing `SetHandler` call sites. Each becomes a `GET` registration on the path the test already requests:

| Test | Was | Becomes |
| --- | --- | --- |
| `TestHandleInvokesTheRegisteredHandler` | `app.SetHandler(...)` | `app.GET("/anything", ...)` |
| `TestHandleConvertsAReturnedErrorInto500` | `app.SetHandler(...)` | `app.GET("/boom", ...)` |
| `TestHandleDiscardsAPartialBodyWhenTheHandlerErrors` | `app.SetHandler(...)` | `app.GET("/partial", ...)` |
| `TestFasthttpHandlerDispatchesLikeHandle` | `app.SetHandler(...)` | `app.GET("/x", ...)` |

`TestHandleWithNoRegisteredHandlerReturns404` keeps its body exactly as it is: an `App` with no routes must still answer 404, and now it does so through the router rather than through a nil check.

Then append these tests to `app_test.go`:

```go
func TestHandleReturns404ForAnUnregisteredPath(t *testing.T) {
	app := New()
	app.GET("/registered", func(c *Ctx) error { return c.String(200, "ok") })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/not-registered")

	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestHandleReturns404ForTheRightPathUnderTheWrongVerb(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return c.String(200, "ok") })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("POST")
	fctx.Request.SetRequestURI("/users")

	app.handle(fctx)

	// M2 deliberately answers 404 rather than 405: computing Allow would cost a
	// scan of every other verb's tree on the miss path. See the M2 design doc.
	if got := fctx.Response.StatusCode(); got != fasthttp.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestThe404BodyDoesNotLeakTheSentinelMessage(t *testing.T) {
	app := New()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/missing")

	app.handle(fctx)

	body := string(fctx.Response.Body())
	if body != "Not Found" {
		t.Errorf("body = %q, want %q", body, "Not Found")
	}
	if strings.Contains(body, "rice:") {
		t.Errorf("the sentinel's internal message leaked into the response: %q", body)
	}
}

func TestAHandlerErrorStillProduces500AfterTheFunnelLearnedAbout404(t *testing.T) {
	app := New()
	app.GET("/boom2", func(c *Ctx) error { return errors.New("unrelated failure") })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/boom2")

	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want 500", got)
	}
	if got := string(fctx.Response.Body()); got != "Internal Server Error" {
		t.Errorf("body = %q, want %q", got, "Internal Server Error")
	}
}

// TestAHandlerMayReturnErrNotFoundToGetA404 records that the funnel switches on
// the error rather than on where it came from, so a handler can opt into a 404.
func TestAHandlerMayReturnErrNotFoundToGetA404(t *testing.T) {
	app := New()
	app.GET("/maybe", func(c *Ctx) error { return ErrNotFound })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/maybe")

	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}
```

`app_test.go` needs `"strings"` added to its imports for `TestThe404BodyDoesNotLeakTheSentinelMessage`.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test . -run 'TestHandle|TestThe404|TestAHandler' -v`

Expected: FAIL. `TestHandleReturns404ForAnUnregisteredPath` and the wrong-verb test fail with status 200, because `handle` still dispatches to the M1 single handler and ignores routes entirely. `TestThe404BodyDoesNotLeakTheSentinelMessage` fails because the M1 404 path writes no body at all.

- [ ] **Step 4: Rewrite dispatch**

In `app.go`: delete the `h Handler` field and the whole `SetHandler` method, add `"errors"` to the imports, and replace `handle` and `handleError` with these:

```go
// handle is the dispatch path: one request in, one response out.
func (a *App) handle(fctx *fasthttp.RequestCtx) {
	h, ok := a.lookup(fctx.Method(), fctx.Path())
	if !ok {
		// M1 allocated the Ctx before deciding whether it had a handler, so a
		// miss paid for a context nobody read. M2 looks up first. The miss path
		// still allocates one Ctx, because the funnel takes a *Ctx and M5 wants
		// a real one to build a custom 404 from; M6's pool removes both.
		c := &Ctx{}
		c.reset(a, fctx)
		a.handleError(c, ErrNotFound)
		return
	}

	// M2 allocates a Ctx per request on purpose. This is the baseline M6's
	// sync.Pool is measured against. Do not optimise it here.
	c := &Ctx{}
	c.reset(a, fctx)

	if err := h(c); err != nil {
		a.handleError(c, err)
	}
}

// handleError is M2's error funnel.
//
// It discards any partially written body and never writes the cause to the
// response: leaking internal error strings to clients is how databases end up
// described in HTTP responses. ErrNotFound is the one error it recognises.
//
// M5 replaces this with a configurable ErrorHandler and the HTTPError type,
// at which point the errors.Is check below generalises rather than disappears.
func (a *App) handleError(c *Ctx, err error) {
	status := fasthttp.StatusInternalServerError
	body := "Internal Server Error"

	if errors.Is(err, ErrNotFound) {
		status = fasthttp.StatusNotFound
		body = "Not Found"
	}

	c.fctx.ResetBody()
	c.fctx.SetStatusCode(status)
	c.fctx.SetContentType(MIMETextPlainUTF8)
	c.fctx.SetBodyString(body)
}
```

Note that `err` is now read. The M1 doc comment saying the parameter is unused must go, and it is replaced above.

- [ ] **Step 5: Migrate the remaining call sites**

In `server_test.go`, replace each `app.SetHandler(h)` with a registration on the path that test requests:

| Test | Path requested | Registration |
| --- | --- | --- |
| `TestServeAnswersARealRequestOnAnEphemeralPort` | `/world` | `app.GET("/world", ...)` |
| `TestRunBindsTheGivenAddress` | `/` | `app.GET("/", ...)` |
| `TestShutdownStopsAcceptingNewConnections` | `/` | `app.GET("/", ...)` |
| `TestShutdownReturnsErrShutdownTimeoutWhenTheDeadlinePasses` | `/slow` | `app.GET("/slow", ...)` |

In `bench/rice_bench_test.go`, replace `app.SetHandler(...)` with `app.GET("/hello", ...)` in `BenchmarkRiceDispatch` and `app.GET("/hdr", ...)` in `BenchmarkCtxSetHeader`, matching the URIs those benchmarks already build. `BenchmarkRiceDispatchNotFound` registers nothing and needs no change: it was measuring the no-handler path and now measures the no-route path, which is the same 404 funnel.

- [ ] **Step 6: Run the full suite**

Run: `make test`

Expected: PASS. If anything reports `app.SetHandler undefined`, a call site was missed — find them all with `grep -rn SetHandler .`

- [ ] **Step 7: Confirm SetHandler is gone**

```bash
grep -rn "SetHandler" --include="*.go" . ; echo "exit=$?"
```

Expected: no matches, exit 1.

- [ ] **Step 8: Run lint and vet**

Run: `make lint`

Expected: exit 0.

- [ ] **Step 9: Commit**

```bash
git add errors.go app.go app_test.go server_test.go bench/rice_bench_test.go
git commit -m "feat: dispatch through the router and route 404 through the error funnel"
```

---

### Task 5: Allocation budgets and benchmarks

**Files:**
- Modify: `alloc_test.go`
- Modify: `server_test.go` (add one integration test)
- Create: `bench/router_bench_test.go`
- Create: `bench/results/M2-static-router.txt` (generated, then committed)
- Modify: `docs/05-performance-model.md`

**Interfaces:**
- Consumes: everything from Tasks 1 to 4.
- Produces: the recorded numbers Task 6 writes into the retrospective, and that M3 is compared against.

- [ ] **Step 1: Write the allocation budget tests**

Append to `alloc_test.go`:

```go
func TestAllocBudgetLookupHit(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/users")

	if _, ok := app.lookup(method, path); !ok {
		t.Fatal("route not registered; the budget below would be measuring the miss path")
	}

	budget(t, "App.lookup hit", 0, func() {
		_, _ = app.lookup(method, path)
	})
}

func TestAllocBudgetLookupMiss(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/absent")

	if _, ok := app.lookup(method, path); ok {
		t.Fatal("route unexpectedly found; the budget below would be measuring a hit")
	}

	budget(t, "App.lookup miss", 0, func() {
		_, _ = app.lookup(method, path)
	})
}

// TestAllocBudgetLookupAtScale guards the map probe specifically. If someone
// rewrites Tree.Lookup as key := string(path) the optimisation is lost and this
// fails, while every behavioural test keeps passing.
func TestAllocBudgetLookupAtScale(t *testing.T) {
	app := New()
	for i := 0; i < 1000; i++ {
		app.GET("/route/"+strconv.Itoa(i), func(c *Ctx) error { return nil })
	}

	method := []byte("GET")
	path := []byte("/route/500")

	if _, ok := app.lookup(method, path); !ok {
		t.Fatal("route not registered")
	}

	budget(t, "App.lookup with 1000 routes", 0, func() {
		_, _ = app.lookup(method, path)
	})
}
```

`alloc_test.go` needs `"strconv"` added to its imports.

- [ ] **Step 2: Run the budget tests**

Run: `go test . -run TestAllocBudget -v`

Expected: PASS for all six budget tests, the three from M1 and the three new ones.

If a lookup budget fails, do not raise it. Raising a budget is a design change (principle 1). Find the allocation:

```bash
go test -run TestAllocBudgetLookupHit -memprofile mem.out .
go tool pprof -top -alloc_objects mem.out
```

The first thing to check is whether `Tree.Lookup` still probes the map with `t.routes[string(path)]` on one line.

- [ ] **Step 3: Add the integration test**

Append to `server_test.go`:

```go
func TestServeReturns404ForAnUnregisteredPath(t *testing.T) {
	app := rice.New()
	app.GET("/known", func(c *rice.Ctx) error { return c.String(200, "known") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	if status, body := get(t, addr, "/known"); status != 200 || body != "known" {
		t.Errorf("registered route: got %d %q, want 200 %q", status, body, "known")
	}

	status, body := get(t, addr, "/unknown")
	if status != 404 {
		t.Errorf("unregistered route: status = %d, want 404", status)
	}
	if body != "Not Found" {
		t.Errorf("unregistered route: body = %q, want %q", body, "Not Found")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}
```

- [ ] **Step 4: Run the full suite with the race detector**

Run: `make test`

Expected: PASS.

- [ ] **Step 5: Write the router benchmarks**

Create `bench/router_bench_test.go`:

```go
package bench

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
)

// newRoutedApp registers n distinct static GET routes and returns the app
// alongside a path that is guaranteed to be registered, taken from the middle
// of the set so a lookup is not accidentally measuring a best or worst case.
func newRoutedApp(n int) (*rice.App, string) {
	app := rice.New()
	for i := 0; i < n; i++ {
		app.GET("/route/"+strconv.Itoa(i), func(c *rice.Ctx) error {
			return c.String(fasthttp.StatusOK, "ok")
		})
	}
	return app, "/route/" + strconv.Itoa(n/2)
}

// benchmarkLookup measures a full dispatch against an app with n routes.
//
// It includes the Ctx allocation and the response write, not only the lookup,
// because a socket-free dispatch is the smallest thing package bench can reach
// through the exported API. Everything except the lookup is constant across n,
// so the difference between the 10, 100 and 1000 figures is lookup scaling and
// nothing else. That difference is what M3's radix tree is judged on.
func benchmarkLookup(b *testing.B, n int) {
	app, path := newRoutedApp(n)

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", path)
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

func BenchmarkStaticRouterLookup10(b *testing.B)   { benchmarkLookup(b, 10) }
func BenchmarkStaticRouterLookup100(b *testing.B)  { benchmarkLookup(b, 100) }
func BenchmarkStaticRouterLookup1000(b *testing.B) { benchmarkLookup(b, 1000) }

// BenchmarkStaticRouterMiss measures the 404 path with 1000 routes registered.
// M1's retrospective flagged the miss path as the one hostile traffic hits
// hardest, so it gets its own number rather than being inferred.
func BenchmarkStaticRouterMiss(b *testing.B) {
	app, _ := newRoutedApp(1000)

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/not-registered")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkStaticRouterWrongVerb measures a request whose path exists under a
// different verb. In M2 this is an ordinary miss; the benchmark exists so that
// the cost is already recorded if 405 is ever added.
func BenchmarkStaticRouterWrongVerb(b *testing.B) {
	app, path := newRoutedApp(1000)

	h := app.FasthttpHandler()
	fctx := newRequestCtx("POST", path)
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}
```

- [ ] **Step 6: Run the benchmarks and read the numbers**

```bash
go test ./bench/... -run '^$' -bench . -benchmem -count=1
```

Expected: every rice benchmark reports `1 allocs/op`, the `Ctx`. `BenchmarkStaticRouterLookup10`, `100` and `1000` should be close to each other — that flatness is the finding, not a mistake.

If any reports more than 1 alloc/op, stop and find it before recording. A number nobody can explain is not a baseline:

```bash
go test ./bench/... -run '^$' -bench BenchmarkStaticRouterLookup1000 -memprofile mem.out -count=1
go tool pprof -top -alloc_objects mem.out
```

- [ ] **Step 7: Record the results**

```bash
make bench-record LABEL=M2-static-router
```

The M1 benchmarks are in the same package and run in the same recording, so the comparison against M1 is same-machine and same-session, per the rule in `bench/results/README.md`.

- [ ] **Step 8: Update the performance model**

In `docs/05-performance-model.md`, change exactly this row:

```markdown
| Router lookup, static route | 0 | TARGET (M3) |
```

to:

```markdown
| Router lookup, static route | 0 | MEASURED M2 |
```

Every other row keeps its label. The row describes routing generally rather than the radix tree specifically, and M2 is what first makes it true.

- [ ] **Step 9: Verify**

```bash
make lint
make test
make bench
```

Expected: all exit 0.

- [ ] **Step 10: Commit**

```bash
git add alloc_test.go server_test.go bench docs/05-performance-model.md
git commit -m "bench: record M2 static router lookup at 10, 100 and 1000 routes"
```

---

### Task 6: Close out M2

**Files:**
- Create: `docs/milestones/M2-static-router.md`
- Modify: `docs/04-roadmap.md` (M2 status marker)
- Modify: `docs/progress.md` (prepend an entry)

**Interfaces:**
- Consumes: the recorded numbers from Task 5.
- Produces: nothing other tasks depend on.

- [ ] **Step 1: Write the milestone retrospective**

Create `docs/milestones/M2-static-router.md` from `docs/milestones/TEMPLATE.md`, filling in every section.

The Measurements table takes the four `BenchmarkStaticRouter*` figures plus `BenchmarkRiceDispatch` from `bench/results/M2-static-router.txt`, with the hardware line copied from that file's stamp header.

The Design notes section must record what the numbers actually said about the map, because that is the milestone's output. State plainly whether lookup cost grew between 10 and 1000 routes and by how much. The design doc predicted it would be close to flat and that a radix tree may therefore lose on static routes; say whether that held, and resist rewriting the prediction to match the result.

The Retrospective section must be written honestly rather than filled with plausible text. If nothing surprised you, write that nothing did and say what you expected to be harder.

- [ ] **Step 2: Mark M2 done in the roadmap**

In `docs/04-roadmap.md`, change the M2 heading marker from `☐` to `☑`:

```
### ☑ M2 — Static router
```

- [ ] **Step 3: Prepend a journal entry**

In `docs/progress.md`, insert a new M2 entry at the top of the entry list, above the M1 entry, using the four-field shape: Did, Learned, Measured, Next. The journal is append-only; do not edit existing entries.

The Measured field carries the lookup figures at 10, 100 and 1000 routes and the miss-path figure. The Next field points at M3 and states, in one sentence, what these numbers mean the radix tree now has to prove.

- [ ] **Step 4: Verify the links**

```bash
grep -n "M2" docs/04-roadmap.md docs/progress.md docs/milestones/M2-static-router.md
```

Expected: the roadmap shows `☑ M2`, and both the journal and the milestone doc reference `bench/results/M2-static-router.txt`.

- [ ] **Step 5: Final verification**

```bash
make lint
make test
make cover
make bench
git status --short
```

Expected: lint, test and bench exit 0, and `git status --short` is empty. Confirm by eye that the roadmap shows `☑` for M0, M1 and M2 and `☐` for M3 through M8.

- [ ] **Step 6: Commit**

```bash
git add docs
git commit -m "docs: close out M2 with retrospective, lookup numbers, and journal entry"
```

---

## Plan Self-Review

**Spec coverage:**

| Spec item | Task |
| --- | --- |
| D1 generic `Tree[H]`, `Insert`/`Lookup`/`Len`, error not panic | 1 |
| D2 `Lookup([]byte)` non-allocating, pinned by a test | 1 (implementation), 5 (budget test) |
| D3 fixed array plus lazy `rare` map, nil inner map on first Insert | 2 (`methodIndex`), 3 (fields and `treeFor`) |
| D4 wrong method returns 404 | 4 (`TestHandleReturns404ForTheRightPathUnderTheWrongVerb`) |
| D5 `ErrNotFound` sentinel through the funnel | 4 |
| D6 lookup before `Ctx` allocation | 4 |
| D7 registration without the middleware parameter, seven verbs plus `Handle` | 3 |
| D8 panics on bad registration, `SetHandler` removed | 3 (panics), 4 (removal) |
| Router tests including the non-retention property | 1 |
| Registration and dispatch tests | 3, 4 |
| Allocation budget tests | 2 (`methodIndex`), 5 (lookup) |
| Integration test over a socket | 5 |
| Benchmarks at 10, 100, 1000, plus miss | 5 |
| Performance model row moves to `MEASURED M2` | 5 |
| Retrospective, roadmap marker, journal entry | 6 |

Every spec item maps to a task. The two open questions the spec carries forward are answered by M2's numbers (map versus tree) and deferred by D4 (405), and neither needs a task.

**Placeholder scan:** no `TBD`, no `TODO`, no "add appropriate error handling", no "similar to Task N". Every code step contains the code.

**Type consistency:** `Tree[H]`, `Insert(path string, h H) error`, `Lookup(path []byte) (H, bool)`, `Len() int` and `ErrDuplicate` are used identically in Tasks 1, 3 and 5. `method`, `methodCount` and `methodIndex(m []byte) (method, bool)` are used identically in Tasks 2 and 3. `App.lookup(method, path []byte) (Handler, bool)` is defined in Task 3 and used in Tasks 4 and 5. `ErrNotFound` is defined in Task 4 and used only there. `budget(t, name, want, fn)` is reused from M1's `alloc_test.go` in Tasks 2 and 5.

**Compilation-unit check:** Task 1 is a standalone package. Task 2 adds only new files to `rice`. Task 3 adds new files plus struct fields, leaving `SetHandler` in place so existing tests keep compiling. Task 4 removes `SetHandler` and migrates all ten call sites together. Task 5 adds tests and benchmarks only. Each task ends with `go build ./...` succeeding.
