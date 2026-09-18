# M6 Context Pooling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the last per-request allocation on rice's dispatch path by pooling `*Ctx`, and make the resulting use-after-release bug class loud under a `ricedebug` build.

**Architecture:** Each `App` owns a `sync.Pool` created in `New`. `handle` acquires a `Ctx`, and releases it inside M5's existing deferred closure, after the ErrorHandler. Route parameters move from a fixed `[8]Param` array to an append-grown slice pre-sized from the largest route, and a pre-sized `[]entry` slice backs the new `Set`/`Get` store. Under `-tags ricedebug`, a released `Ctx` is poisoned and never returned to the pool, so every later use panics deterministically.

**Tech Stack:** Go 1.25, fasthttp v1.73.0, `sync.Pool`, build tags.

**Spec:** [docs/superpowers/specs/2026-09-18-m6-context-pooling-design.md](../specs/2026-09-18-m6-context-pooling-design.md)

## Global Constraints

- Nothing under `internal/` may import package `rice`.
- `github.com/valyala/fasthttp` is the only runtime dependency. Package `rice` production code must not import `reflect` or `encoding/json` (ADR-0006). Test files may import `reflect`.
- Every user-facing panic is prefixed `rice: `.
- Work on branch `m6-context-pooling` (already created; the spec is committed there).
- End state: dispatch to a static route, to a parameterised route read with `Param`, and a 404 all cost **0** allocations in the release build.
- `newCtx` allocates at most three objects (the `Ctx`, its parameter slice, its store slice). A fourth breaks every zero budget under `-race`; see the spec's "Budgets under `-race`".
- `release` runs after the ErrorHandler, inside the same deferred closure as M5's `recover()`. No second `defer` is added to `handle`.
- The `poison` field is the **first** field of `Ctx`. A zero-sized field placed last forces trailing padding in Go.
- `make lint`, `make test`, `make cover` must exit 0 at the end of every task; from Task 4 on, `make test-debug` too. Root package coverage must not drop below 98.8%.
- Every new guard is broken on purpose and watched to fail, with the failure text recorded in the task report. A guard nobody watched go red is not a guard.
- Commit messages follow the repo's style (`feat:`, `fix:`, `test:`, `docs:`, `bench:`, `refactor:`) and end with:
  `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`

---

### Task 1: Slice-backed `Params`, no parameter limit, `Tree.MaxParams`

**Files:**
- Modify: `internal/router/params.go` (whole file)
- Modify: `internal/router/pattern.go:104-106` (delete the limit)
- Modify: `internal/router/router.go:24-46` (`Tree` gains `maxParams`, `Insert` records it, new `MaxParams`)
- Modify: `internal/router/tree.go:188-219` (unconditional `add`)
- Modify: `internal/router/params_test.go`, `internal/router/pattern_test.go:109-138`
- Modify: `internal/router/router_test.go` (append new tests)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func MakeParams(capacity int) Params` — a `Params` whose storage is pre-sized.
  - `func (p *Params) Set(name string, value []byte)` — no longer returns `bool`.
  - `func (p *Params) add(key string, value []byte)` — no longer returns `bool`.
  - `func (t *Tree[H]) MaxParams() int` — largest parameter count (named + wildcard) of any pattern `Insert` accepted.
  - `router.MaxParams` is **removed**.
  - Unchanged: `Reset`, `Get`, `Len`, `At`, `truncate`.

- [ ] **Step 1: Rewrite the tests that encode the old limit**

In `internal/router/params_test.go`:

Replace `TestParamsAddAndGet`'s two `if !p.add(...) { t.Fatal(...) }` blocks with plain calls:

```go
	p.add("id", []byte("42"))
	p.add("slug", []byte("hello"))
```

Delete `TestParamsFillsExactlyMaxParams`, `TestParamsRejectsOverflow` and `TestSetReportsFalseWhenFull`. Add in their place:

```go
// TestParamsHasNoFixedLimit replaces the M3 overflow tests. M6 removed the
// eight-slot array; storage is a slice that grows, so a capture can never be
// refused.
func TestParamsHasNoFixedLimit(t *testing.T) {
	var p Params
	for i := 0; i < 64; i++ {
		p.add("k"+strconv.Itoa(i), []byte(strconv.Itoa(i)))
	}

	if got := p.Len(); got != 64 {
		t.Fatalf("Len() = %d, want 64", got)
	}
	if got := string(p.Get("k63")); got != "63" {
		t.Errorf("Get(\"k63\") = %q, want %q", got, "63")
	}
}

func TestMakeParamsPreSizesStorage(t *testing.T) {
	p := MakeParams(5)

	if got := cap(p.slots); got != 5 {
		t.Errorf("cap = %d, want 5", got)
	}
	if got := p.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
}

// TestAddWithinCapacityAllocatesNothing is the property the pool depends on:
// once a Ctx's parameter slice is sized, capture is free.
func TestAddWithinCapacityAllocatesNothing(t *testing.T) {
	p := MakeParams(3)
	value := []byte("v")

	got := testing.AllocsPerRun(1000, func() {
		p.Reset()
		p.add("a", value)
		p.add("b", value)
		p.add("c", value)
	})
	if got != 0 {
		t.Errorf("add within capacity allocated %.1f objects per call, want 0", got)
	}
}
```

Add `"strconv"` to the imports.

Rewrite `TestResetZeroesDiscardedSlots` to walk the backing array rather than `MaxParams` slots:

```go
func TestResetZeroesDiscardedSlots(t *testing.T) {
	var p Params
	p.add("id", []byte("42"))
	p.add("slug", []byte("hello"))

	p.Reset()

	backing := p.slots[:cap(p.slots)]
	for i := range backing {
		if backing[i].Key != "" || backing[i].Value != nil {
			t.Errorf("slot %d still holds %+v after Reset, want the zero Param", i, backing[i])
		}
	}
}
```

In `TestTruncateRollsBackAndZeroes`, replace the final `p.slots[1]` check with:

```go
	if s := p.slots[:2][1]; s.Key != "" || s.Value != nil {
		t.Errorf("slot 1 still holds %+v after truncate, want the zero Param", s)
	}
```

In `internal/router/pattern_test.go`, replace `TestParsePatternAllowsExactlyMaxParams` and `TestParsePatternCountsAWildcardTowardTheLimit` with:

```go
// TestParsePatternAcceptsManyParameters records M6's removal of the
// eight-parameter limit. The limit existed only because Params was a fixed
// array; it is a slice now.
func TestParsePatternAcceptsManyParameters(t *testing.T) {
	pattern := ""
	for i := 0; i < 20; i++ {
		pattern += "/a/:p" + strconv.Itoa(i)
	}
	pattern += "/*rest"

	if _, err := parsePattern(pattern); err != nil {
		t.Fatalf("parsePattern rejected 20 parameters and a wildcard: %v", err)
	}
}
```

Add `"strconv"` to that file's imports if absent.

Append to `internal/router/router_test.go`:

```go
func TestTreeMaxParamsTracksTheLargestPattern(t *testing.T) {
	var tr Tree[int]
	if got := tr.MaxParams(); got != 0 {
		t.Fatalf("MaxParams() on an empty tree = %d, want 0", got)
	}

	mustInsert := func(pattern string) {
		t.Helper()
		if err := tr.Insert(pattern, 1); err != nil {
			t.Fatalf("Insert(%q): %v", pattern, err)
		}
	}

	mustInsert("/static")
	mustInsert("/users/:id")
	mustInsert("/a/:x/b/:y/*rest") // wildcard counts: 3
	mustInsert("/users/:id/posts") // 1, must not lower the max

	if got := tr.MaxParams(); got != 3 {
		t.Errorf("MaxParams() = %d, want 3", got)
	}
}

// TestTreeMaxParamsIgnoresRejectedPatterns guards the order inside Insert: a
// pattern that fails must not raise the maximum.
func TestTreeMaxParamsIgnoresRejectedPatterns(t *testing.T) {
	var tr Tree[int]
	if err := tr.Insert("/users/:id", 1); err != nil {
		t.Fatal(err)
	}
	if err := tr.Insert("/users/:name/:a/:b", 1); err == nil {
		t.Fatal("conflicting parameter name was accepted; this test needs a rejected insert")
	}

	if got := tr.MaxParams(); got != 1 {
		t.Errorf("MaxParams() = %d after a rejected insert, want 1", got)
	}
}

func TestLookupCapturesMoreThanEightParameters(t *testing.T) {
	var tr Tree[int]
	pattern, path := "", ""
	for i := 0; i < 12; i++ {
		pattern += "/:p" + strconv.Itoa(i)
		path += "/" + strconv.Itoa(i)
	}
	if err := tr.Insert(pattern, 7); err != nil {
		t.Fatal(err)
	}

	var p Params
	h, ok := tr.Lookup([]byte(path), &p)
	if !ok || h != 7 {
		t.Fatalf("Lookup(%q) = %d, %v; want 7, true", path, h, ok)
	}
	if got := string(p.Get("p11")); got != "11" {
		t.Errorf("p11 = %q, want %q", got, "11")
	}
}
```

Add `"strconv"` to that file's imports if absent. Check first that `/users/:name/:a/:b` is in fact rejected after `/users/:id` (M3's conflicting-name rule); if the tree accepts it, pick a pattern `tree.insert` rejects and say so in the comment.

- [ ] **Step 2: Run the router tests to verify they fail to compile**

Run: `go test ./internal/router/`
Expected: FAIL — `undefined: MakeParams`, `tr.MaxParams undefined`, and `p.add(...) (no value) used as value` or similar from the unchanged production signatures.

- [ ] **Step 3: Rewrite `internal/router/params.go`**

```go
package router

// Param is one captured route parameter.
//
// Key is owned: it comes from the registered pattern and lives as long as the
// tree does. Value is borrowed: it points into fasthttp's request buffer and
// dies when the handler returns.
type Param struct {
	Key   string
	Value []byte
}

// Params holds the parameters captured by one lookup.
//
// It lives inside a pooled Ctx by value, so that capture costs no allocation of
// its own. That is the whole reason Lookup takes a *Params instead of returning a
// slice — see ADR-0005.
//
// Storage is a slice rather than a fixed array. Package rice pre-sizes it with
// MakeParams from the largest route registered, so on a served App capture never
// grows it. If a Params is too small — a Ctx built before a larger route was
// registered — add grows it once and the pooled Ctx keeps the larger slice. A
// wrong size therefore costs one allocation, never a missed match. See the M6
// design doc, D3.
//
// The zero value is ready to use.
type Params struct {
	slots []Param
}

// MakeParams returns a Params with room for capacity captures before it grows.
func MakeParams(capacity int) Params {
	return Params{slots: make([]Param, 0, capacity)}
}

// Reset discards every captured parameter.
//
// It is exported because package rice calls it from Ctx.reset, across the
// internal/ package boundary where an unexported method would be unreachable.
func (p *Params) Reset() { p.truncate(0) }

// truncate rolls capture back to n entries.
//
// The discarded slots are zeroed rather than merely sliced away: a stale Value
// in the backing array keeps fasthttp's request buffer reachable, and a pooled
// Params outlives the request that filled it.
func (p *Params) truncate(n int) {
	clear(p.slots[n:])
	p.slots = p.slots[:n]
}

// add appends a captured parameter.
//
// The slice is stored, not copied: Value is a view into the request path.
func (p *Params) add(key string, value []byte) {
	p.slots = append(p.slots, Param{Key: key, Value: value})
}

// Set records a captured parameter, replacing any earlier capture of the same
// name.
//
// The tree fills Params through the unexported add, which is append-only because
// a lookup never revisits a name. Set exists for package rice, which cannot reach
// add across the internal/ boundary, and for tests that need to stage a Ctx
// without running a lookup.
func (p *Params) Set(name string, value []byte) {
	for i := range p.slots {
		if p.slots[i].Key == name {
			p.slots[i].Value = value
			return
		}
	}
	p.add(name, value)
}

// Get returns the value captured for name, or nil if there is none.
//
// A linear scan over a route's few parameters beats a map decisively and
// allocates nothing. See TestGetAllocatesNothing.
func (p *Params) Get(name string) []byte {
	for i := range p.slots {
		if p.slots[i].Key == name {
			return p.slots[i].Value
		}
	}
	return nil
}

// Len returns the number of captured parameters.
func (p *Params) Len() int { return len(p.slots) }

// At returns the i'th captured parameter in the order the lookup captured them.
// It panics if i is out of range.
func (p *Params) At(i int) Param {
	if i < 0 || i >= len(p.slots) {
		panic("router: Params.At index out of range")
	}
	return p.slots[i]
}
```

- [ ] **Step 4: Remove the limit from `parsePattern`**

Delete these lines from `internal/router/pattern.go`:

```go
	if len(names) > MaxParams {
		return nil, fmt.Errorf("route path %s declares %d parameters; at most %d are supported", pattern, len(names), MaxParams)
	}
```

If `fmt` becomes unused, `go build` will say so; it is still used by the other errors in the function, so it should not.

- [ ] **Step 5: Make `add` unconditional in `tree.go`**

Replace the parameter branch body (currently `if params.add(n.param.name, path[:i]) { ... }`) with:

```go
		if i > 0 {
			saved := params.Len()
			params.add(n.param.name, path[:i])
			rest := path[i:]
			if len(rest) == 0 {
				if n.param.hasHandler {
					return n.param.handler, true
				}
			} else if h, ok := n.param.lookup(rest, params); ok {
				return h, true
			}
			params.truncate(saved)
		}
```

and the wildcard branch with:

```go
	if n.wildcard != nil {
		saved := params.Len()
		params.add(n.wildcard.name, path)
		if n.wildcard.hasHandler {
			return n.wildcard.handler, true
		}
		params.truncate(saved)
	}
```

- [ ] **Step 6: Record `maxParams` in `Tree`**

In `internal/router/router.go`:

```go
// Tree maps route patterns to handlers.
//
// The zero value is ready to use.
type Tree[H any] struct {
	t tree[H]

	// maxParams is the largest parameter count of any accepted pattern. Package
	// rice sizes pooled parameter storage from it.
	maxParams int
}

// Insert registers h at pattern.
//
// It returns an error for a pattern that is malformed, ambiguous, or unable to
// match any request — see parsePattern — and ErrDuplicate if the pattern is
// already registered, leaving the existing handler in place. Deciding whether
// that is fatal belongs to the caller.
func (t *Tree[H]) Insert(pattern string, h H) error {
	segs, err := parsePattern(pattern)
	if err != nil {
		return err
	}
	if err := t.t.insert(pattern, segs, h); err != nil {
		return err
	}

	// Counted only after a successful insert, so a rejected pattern cannot size
	// storage for a route that does not exist.
	n := 0
	for _, s := range segs {
		if s.kind != segStatic {
			n++
		}
	}
	t.maxParams = max(t.maxParams, n)
	return nil
}

// MaxParams returns the largest number of parameters, a wildcard included, that
// any registered pattern captures.
func (t *Tree[H]) MaxParams() int { return t.maxParams }
```

Also delete the now-false sentence in the package doc comment only if one mentions `MaxParams`; the current one does not.

- [ ] **Step 7: Run the router tests**

Run: `go test ./internal/router/ -race -count=1`
Expected: PASS.

- [ ] **Step 8: Fix package `rice` compile errors and stale comments**

Run: `go build ./... && go vet ./...`

Nothing in package `rice` uses the `Set` return value (verified with `grep -rn "params.Set" --include=*.go .`: every call discards it), so it should compile. Then update the comment on `Ctx.params` in `ctx.go`:

```go
	// params is held by value, not by pointer, so that capturing route
	// parameters needs no allocation of its own. This is what ADR-0005's
	// Lookup(path, *Params) signature exists to make possible, and it is why the
	// Ctx must be constructed before the lookup runs.
	params router.Params
```

(Delete the paragraph about `MaxParams` fixed slots and "M6's pool makes that irrelevant".)

- [ ] **Step 9: Run the full suite**

Run: `make lint && make test && make cover`
Expected: all exit 0; coverage for the root package ≥ 98.8%, `internal/router` ≥ 98.9%.

- [ ] **Step 10: Fault injection**

1. In `truncate`, delete `clear(p.slots[n:])`. Run `go test ./internal/router/ -run 'TestResetZeroesDiscardedSlots|TestTruncateRollsBackAndZeroes'`. Expected: FAIL. Restore.
2. In `Insert`, move the counting loop above `t.t.insert`. Run `go test ./internal/router/ -run TestTreeMaxParamsIgnoresRejectedPatterns`. Expected: FAIL. Restore.
3. In `MakeParams`, change `make([]Param, 0, capacity)` to `make([]Param, 0)`. Run `go test ./internal/router/ -run 'TestMakeParamsPreSizesStorage|TestAddWithinCapacityAllocatesNothing'`. Expected: the first FAILs; record whether the second does (it may not, because `Reset` keeps the capacity grown on the warm-up call — if it stays green, say so in the report and explain why rather than moving on). Restore.

Record each failure message in the task report.

- [ ] **Step 11: Commit**

```bash
git add internal/router ctx.go
git commit -m "refactor: back Params with a growable slice and drop the eight-parameter limit

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: The pool — `newCtx`, `acquire`, `release`

**Files:**
- Create: `pool.go`
- Create: `pool_test.go`
- Modify: `app.go` (`App` fields, `New`, `handle`)
- Modify: `route.go:102-116` (`register` updates `maxParams`)
- Modify: `ctx.go` (`reset` comment)
- Modify: `alloc_test.go` (budgets 1 → 0, helper comment)

**Interfaces:**
- Consumes: `router.MakeParams(capacity int) router.Params`, `(*router.Tree[H]).MaxParams() int` from Task 1.
- Produces:
  - `App.pool sync.Pool`, `App.maxParams int`
  - `func (a *App) newCtx() *Ctx`
  - `func (a *App) acquire(fctx *fasthttp.RequestCtx) *Ctx`
  - `func (a *App) release(c *Ctx)`
  - Task 3 extends `newCtx` and `reset`; Task 4 extends `release`.

- [ ] **Step 1: Write the failing tests**

Create `pool_test.go`:

```go
package rice

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestRegisterTracksTheLargestParameterCount(t *testing.T) {
	app := New()
	app.GET("/a", func(c *Ctx) error { return nil })
	app.GET("/users/:id", func(c *Ctx) error { return nil })
	app.POST("/x/:a/:b/*rest", func(c *Ctx) error { return nil })
	app.Handle("PURGE", "/cache/:key", func(c *Ctx) error { return nil })

	if app.maxParams != 3 {
		t.Errorf("maxParams = %d, want 3", app.maxParams)
	}
}

// TestReleaseDropsEveryReference is the reason release exists as more than a
// Put. A pooled Ctx that kept its fctx, its App or its parameter values would
// keep a finished request's memory reachable for as long as it sat in the pool.
func TestReleaseDropsEveryReference(t *testing.T) {
	var retained *Ctx
	app := New()
	app.GET("/users/:id", func(c *Ctx) error {
		retained = c
		return c.String(200, "ok")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")
	app.handle(fctx)

	if retained == nil {
		t.Fatal("handler did not run")
	}
	if retained.fctx != nil {
		t.Error("released Ctx still holds its *fasthttp.RequestCtx")
	}
	if retained.app != nil {
		t.Error("released Ctx still holds its *App")
	}
	if n := retained.params.Len(); n != 0 {
		t.Errorf("released Ctx still holds %d parameters", n)
	}
}

func TestReleaseRunsWhenTheHandlerPanics(t *testing.T) {
	var retained *Ctx
	app := New(WithErrorHandler(func(c *Ctx, err error) { respond(c, 500, "x") }))
	app.GET("/boom", func(c *Ctx) error {
		retained = c
		panic("boom")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/boom")
	app.handle(fctx)

	if retained == nil || retained.fctx != nil {
		t.Error("a panicking handler's Ctx was not released")
	}
}

// TestErrorHandlerReceivesALiveCtx pins the ordering in handle's defer: release
// runs after the ErrorHandler, never before it.
func TestErrorHandlerReceivesALiveCtx(t *testing.T) {
	var path string
	app := New(WithErrorHandler(func(c *Ctx, err error) {
		path = string(c.Path())
		respond(c, 500, "x")
	}))
	app.GET("/fail", func(c *Ctx) error { return NewHTTPError(500, "x") })
	app.GET("/boom", func(c *Ctx) error { panic("boom") })

	for _, uri := range []string{"/fail", "/boom", "/missing"} {
		path = ""
		fctx := &fasthttp.RequestCtx{}
		fctx.Request.Header.SetMethod("GET")
		fctx.Request.SetRequestURI(uri)
		app.handle(fctx)

		if path != uri {
			t.Errorf("%s: ErrorHandler saw Path() = %q, want %q", uri, path, uri)
		}
	}
}

func TestARequestSeesNoParametersFromThePreviousOne(t *testing.T) {
	var seen []string
	app := New()
	app.GET("/a/:x/:y", func(c *Ctx) error { return nil })
	app.GET("/b/:z", func(c *Ctx) error {
		seen = []string{c.ParamString("x"), c.ParamString("y"), c.ParamString("z")}
		return nil
	})

	for _, uri := range []string{"/a/1/2", "/b/3"} {
		fctx := &fasthttp.RequestCtx{}
		fctx.Request.Header.SetMethod("GET")
		fctx.Request.SetRequestURI(uri)
		app.handle(fctx)
	}

	if seen[0] != "" || seen[1] != "" || seen[2] != "3" {
		t.Errorf("second request saw x=%q y=%q z=%q; want only z=3", seen[0], seen[1], seen[2])
	}
}

func TestNewCtxIsSizedForTheLargestRoute(t *testing.T) {
	app := New()
	app.GET("/a/:p1/:p2/:p3", func(c *Ctx) error { return nil })

	c := app.newCtx()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/a/1/2/3")
	c.reset(app, fctx)

	got := testing.AllocsPerRun(100, func() {
		c.params.Reset()
		_, _ = app.lookup(fctx.Method(), fctx.Path(), &c.params)
	})
	if got != 0 {
		t.Errorf("lookup into a fresh newCtx allocated %.1f objects, want 0; newCtx is not pre-sizing params", got)
	}
}

// TestACtxBuiltBeforeALargerRouteStillCaptures is D3's reason for append: a
// Ctx sized for an earlier, smaller route set must grow rather than fail to match.
func TestACtxBuiltBeforeALargerRouteStillCaptures(t *testing.T) {
	app := New()
	app.GET("/a/:x", func(c *Ctx) error { return nil })
	c := app.newCtx() // sized for one parameter

	app.GET("/b/:p1/:p2/:p3", func(c *Ctx) error { return nil })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/b/1/2/3")
	c.reset(app, fctx)

	if _, ok := app.lookup(fctx.Method(), fctx.Path(), &c.params); !ok {
		t.Fatal("lookup missed; an undersized Ctx must still match")
	}
	if got := string(c.Param("p3")); got != "3" {
		t.Errorf("p3 = %q, want %q", got, "3")
	}
}

func TestARouteWithMoreThanEightParametersDispatches(t *testing.T) {
	pattern, uri := "", ""
	for i := 0; i < 10; i++ {
		pattern += "/:p" + strconv.Itoa(i)
		uri += "/" + strconv.Itoa(i)
	}

	app := New()
	app.GET(pattern, func(c *Ctx) error { return c.String(200, c.ParamString("p9")) })

	fctx := dispatchCtx(app, "GET", uri)

	if got := string(fctx.Response.Body()); got != "9" {
		t.Errorf("body = %q, want %q", got, "9")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -run 'TestRegisterTracks|TestRelease|TestErrorHandlerReceivesALiveCtx|TestARequestSeesNo|TestNewCtx|TestACtxBuilt|TestARouteWithMore' -count=1`
Expected: FAIL to compile — `app.maxParams undefined`, `app.newCtx undefined`.

- [ ] **Step 3: Create `pool.go`**

```go
package rice

import (
	"github.com/valyala/fasthttp"

	"github.com/vietpham102301/rice-http/internal/router"
)

// newCtx builds a Ctx for the pool.
//
// It reads maxParams when it runs rather than when the pool was created, so a
// Ctx built after more routes were registered is sized for them. A Ctx built
// before that is not, and grows on its first oversized capture — see the M6
// design doc, D3.
//
// It must allocate no more than three objects. Under -race, sync.Pool drops one
// Put in four, so every dispatch budget of zero in alloc_test.go absorbs a
// quarter of this function's cost; at four objects that reaches a whole
// allocation and every zero budget fails under make test.
func (a *App) newCtx() *Ctx {
	return &Ctx{params: router.MakeParams(a.maxParams)}
}

// acquire takes a Ctx from the pool and binds it to fctx.
func (a *App) acquire(fctx *fasthttp.RequestCtx) *Ctx {
	c := a.pool.Get().(*Ctx)
	c.reset(a, fctx)
	return c
}

// release unbinds c and returns it to the pool.
//
// It drops every reference c holds before the Put, so a pooled Ctx never keeps a
// finished request's memory reachable. After this call, c belongs to whichever
// request gets it next — which is the borrow contract, stated from the other
// side.
func (a *App) release(c *Ctx) {
	c.reset(nil, nil)
	a.pool.Put(c)
}
```

- [ ] **Step 4: Wire the pool into `App`**

In `app.go`, add to the `App` struct directly after `errorHandler`:

```go
	// pool holds idle contexts. It is created in New, not Build, because the
	// dispatch path is reachable before Build: package tests call handle on unbuilt
	// Apps throughout. See the M6 design doc, D1.
	pool sync.Pool

	// maxParams is the largest parameter count of any registered route. newCtx
	// sizes parameter storage from it.
	//
	// It is written during registration and read by newCtx while serving, with no
	// lock, on the same argument as built above: registration is a
	// single-goroutine phase that ends before serving begins.
	maxParams int
```

In `New`, after `a := &App{errorHandler: DefaultErrorHandler}`:

```go
	a.pool.New = func() any { return a.newCtx() }
```

Replace the start of `handle` — from the comment "The Ctx is constructed before the lookup" through the closing `}()` of the deferred function — with:

```go
func (a *App) handle(fctx *fasthttp.RequestCtx) {
	// The Ctx is acquired before the lookup because the lookup fills c.params in
	// place. That ordering is ADR-0005's design; see the Ctx.params comment.
	c := a.acquire(fctx)

	// fasthttp has no panic hook. Its only recover() on the request path guards
	// body-stream writes — a second one exists in fasthttpadaptor/adaptor.go,
	// which rice does not use; server.go calls the handler bare from a
	// worker-pool goroutine, so an unrecovered panic here takes the whole
	// process down, not just this connection. Recovering is therefore core
	// behaviour rather than opt-in middleware, which is a deliberate exception
	// to design principle 7 — see ADR-0008.
	//
	// release shares this closure rather than taking a defer of its own, so the
	// recovery's measured cost remains the only defer cost on the path. It runs
	// after the ErrorHandler, which may still use c, and it runs whether the
	// handler returned, errored or panicked — the property that makes the pool
	// safe.
	//
	// One hole is accepted. If respond panicked inside callErrorHandler's
	// last-resort recover, nothing would catch it and release would not run.
	// That is unreachable today (see callErrorHandler), and if it became
	// reachable the cost is one Ctx lost to the pool, not a corrupted one.
	defer func() {
		if r := recover(); r != nil {
			a.callErrorHandler(c, &PanicError{Value: r, Stack: debug.Stack()})
		}
		a.release(c)
	}()
```

Leave the remainder of `handle` (lookup, `ErrNotFound`, handler call) unchanged.

- [ ] **Step 5: Update `maxParams` on registration**

In `route.go`, `register`, replace:

```go
	if err := a.treeFor(method).Insert(path, h); err != nil {
```

with:

```go
	t := a.treeFor(method)
	if err := t.Insert(path, h); err != nil {
```

and immediately after that `if` block's closing brace add:

```go
	a.maxParams = max(a.maxParams, t.MaxParams())
```

`build` discards and rebuilds the trees from the same route set, so the maximum it would compute is identical; it does not need to touch `maxParams`.

- [ ] **Step 6: Update the `reset` comment in `ctx.go`**

```go
// reset binds the context to a request, or unbinds it when app and fctx are nil.
//
// acquire calls it to bind a pooled Ctx and release calls it to unbind one, so
// it clears everything a previous request could have left behind rather than
// trusting it to be empty.
```

- [ ] **Step 7: Run the new tests**

Run: `go test . -run 'TestRegisterTracks|TestRelease|TestErrorHandlerReceivesALiveCtx|TestARequestSeesNo|TestNewCtx|TestACtxBuilt|TestARouteWithMore' -race -count=1 -v`
Expected: PASS.

- [ ] **Step 8: Lower the dispatch budgets to zero**

In `alloc_test.go`, change the budget argument from `1` to `0` for: `"App.handle dispatch (hit)"`, `"App.handle on a parameterised route"`, `"App.handle with no middleware"`, `"App.handle with five middleware"`, `"dispatch with recovery installed"`, `"404 through the funnel"`. Change `"handler returning a fresh HTTPError"` from `2` to `1`.

Rewrite each affected test's doc comment so it no longer says the `Ctx` is allocated. For example, `TestAllocBudgetHandleDispatch`:

```go
// TestAllocBudgetHandleDispatch pins the headline claim of M6: end-to-end
// dispatch on a warm server allocates nothing. Until M6 the budget was 1, the
// unpooled Ctx that M1 recorded as the baseline.
```

`TestAllocBudget404`:

```go
// TestAllocBudget404 pins D2 of the M5 design with the pool in place: a miss
// costs what a hit costs, and both cost nothing.
```

`TestAllocBudgetHTTPErrorReturn`:

```go
// TestAllocBudgetHTTPErrorReturn records the cost of a handler constructing an
// error: the HTTPError, and nothing else now that the Ctx is pooled. It is 1 and
// it is meant to be 1 — a budget that documents a cost rather than forbidding one.
```

Extend the `budget` helper's comment:

```go
// budget asserts that fn allocates no more than want objects per call.
//
// The response buffers are warmed before measuring, because a live server is
// warm. Measuring a cold buffer would measure one-time setup, not steady state.
//
// Under -race, sync.Pool drops one Put in four, so a pooled dispatch really does
// allocate a fraction of a Ctx per call there. AllocsPerRun divides integer
// counts and reports that fraction as 0, while a genuine per-request allocation
// still reads as at least 1. The zero budgets are meaningful in both modes; see
// newCtx for why that holds only while newCtx allocates three objects or fewer.
```

- [ ] **Step 9: Run the full suite**

Run: `make lint && make test && make cover`
Expected: all exit 0; root coverage ≥ 98.8%.

- [ ] **Step 10: Fault injection**

1. Remove `a.release(c)` from the defer. Run `go test . -run 'TestReleaseDropsEveryReference|TestAllocBudgetHandleDispatch$' -count=1`. Expected: both FAIL. Restore.
2. Move `a.release(c)` above the `if r := recover()` block. Run `go test . -run TestErrorHandlerReceivesALiveCtx -count=1`. Expected: FAIL (nil `fctx` panic caught by the last-resort net, or a wrong path). Restore.
3. In `release`, replace `c.reset(nil, nil)` with nothing. Run `go test . -run TestReleaseDropsEveryReference -count=1`. Expected: FAIL. Restore.
4. In `newCtx`, return `&Ctx{}`. Run `go test . -run TestNewCtxIsSizedForTheLargestRoute -count=1`. Expected: FAIL. Restore.

- [ ] **Step 11: Commit**

```bash
git add pool.go pool_test.go app.go route.go ctx.go alloc_test.go
git commit -m "feat: pool Ctx per App and release it after the ErrorHandler

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: The per-request store — `Set` and `Get`

**Files:**
- Create: `ctx_store.go`
- Create: `ctx_store_test.go`
- Modify: `ctx.go` (`Ctx.store` field, `reset`)
- Modify: `pool.go` (`newCtx` sizes the store)
- Modify: `pool_test.go` (store assertions)
- Modify: `alloc_test.go` (store budgets)

**Interfaces:**
- Consumes: `newCtx`, `reset`, `release` from Task 2.
- Produces:
  - `func (c *Ctx) Set(key string, v any)`
  - `func (c *Ctx) Get(key string) (any, bool)`
  - `type entry struct { key string; val any }`, `const storeCapacity = 4`
  - `func (c *Ctx) resetStore()`

- [ ] **Step 1: Write the failing tests**

Create `ctx_store_test.go`:

```go
package rice

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestGetOnAnAbsentKey(t *testing.T) {
	c := &Ctx{}

	v, ok := c.Get("missing")
	if v != nil || ok {
		t.Errorf("Get(\"missing\") = %v, %v; want nil, false", v, ok)
	}
}

func TestSetThenGet(t *testing.T) {
	c := &Ctx{}
	c.Set("user", "alice")

	v, ok := c.Get("user")
	if !ok || v != "alice" {
		t.Errorf("Get(\"user\") = %v, %v; want alice, true", v, ok)
	}
}

func TestSetReplacesAnExistingKey(t *testing.T) {
	c := &Ctx{}
	c.Set("k", 1)
	c.Set("k", 2)

	if v, _ := c.Get("k"); v != 2 {
		t.Errorf("Get(\"k\") = %v, want 2", v)
	}
	if n := len(c.store); n != 1 {
		t.Errorf("store holds %d entries after setting one key twice, want 1", n)
	}
}

// TestTheStoreHoldsMoreThanItsInitialCapacity crosses storeCapacity, the
// documented cliff, and checks nothing is lost on the way over it.
func TestTheStoreHoldsMoreThanItsInitialCapacity(t *testing.T) {
	c := &Ctx{}
	for i := 0; i < storeCapacity+2; i++ {
		c.Set("k"+strconv.Itoa(i), i)
	}

	for i := 0; i < storeCapacity+2; i++ {
		if v, ok := c.Get("k" + strconv.Itoa(i)); !ok || v != i {
			t.Errorf("Get(k%d) = %v, %v; want %d, true", i, v, ok, i)
		}
	}
}

// TestMiddlewareCanPassAValueToTheHandler is the use the store exists for.
func TestMiddlewareCanPassAValueToTheHandler(t *testing.T) {
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			c.Set("request_id", "abc")
			return next(c)
		}
	})
	app.GET("/x", func(c *Ctx) error {
		v, _ := c.Get("request_id")
		return c.String(200, v.(string))
	})

	fctx := dispatchCtx(app, "GET", "/x")

	if got := string(fctx.Response.Body()); got != "abc" {
		t.Errorf("body = %q, want %q", got, "abc")
	}
}

func TestARequestSeesNoStoreEntriesFromThePreviousOne(t *testing.T) {
	var leaked bool
	app := New()
	app.GET("/set", func(c *Ctx) error { c.Set("k", "v"); return nil })
	app.GET("/get", func(c *Ctx) error { _, leaked = c.Get("k"); return nil })
	app.Build()

	for _, uri := range []string{"/set", "/get"} {
		fctx := &fasthttp.RequestCtx{}
		fctx.Request.Header.SetMethod("GET")
		fctx.Request.SetRequestURI(uri)
		app.handle(fctx)
	}

	if leaked {
		t.Error("a store entry set by one request was visible to the next")
	}
}
```

Append to `TestReleaseDropsEveryReference` in `pool_test.go` — change its handler to also call `c.Set("k", &struct{}{})`, and add after the parameter assertion:

```go
	if n := len(retained.store); n != 0 {
		t.Errorf("released Ctx still holds %d store entries", n)
	}
	// Truncating is not enough: the backing array would still hold the value and
	// the pool would keep it alive.
	for i, e := range retained.store[:cap(retained.store)] {
		if e.key != "" || e.val != nil {
			t.Errorf("store backing slot %d still holds %+v after release", i, e)
		}
	}
```

Append to `alloc_test.go`:

```go
// storeSink keeps Get's result reachable so the compiler cannot elide the call.
var storeSink any

func TestAllocBudgetCtxSetPointer(t *testing.T) {
	c := New().newCtx()
	v := &struct{ n int }{}

	budget(t, "Ctx.Set with a pointer value", 0, func() {
		c.resetStore()
		c.Set("k", v)
	})
}

func TestAllocBudgetCtxGet(t *testing.T) {
	c := New().newCtx()
	c.Set("k", &struct{ n int }{})

	budget(t, "Ctx.Get", 0, func() {
		storeSink, _ = c.Get("k")
	})
}

// TestAllocBudgetCtxSetString asserts exactly one allocation, and the allocation
// is not rice's. Converting a non-constant string to any at the call site boxes
// it; Set itself allocates nothing, as TestAllocBudgetCtxSetPointer shows. The
// budget is exact rather than an upper bound so that a change to Go's boxing
// rules shows up here instead of silently changing what the documentation says.
func TestAllocBudgetCtxSetString(t *testing.T) {
	c := New().newCtx()
	s := strconv.Itoa(123456)

	got := testing.AllocsPerRun(1000, func() {
		c.resetStore()
		c.Set("k", s)
	})
	if got != 1 {
		t.Errorf("Ctx.Set with a non-constant string allocated %.1f objects per call, want exactly 1 (the caller's boxing)", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -count=1 2>&1 | head`
Expected: FAIL to compile — `c.Set undefined`, `c.store undefined`, `storeCapacity undefined`.

- [ ] **Step 3: Implement the store**

Create `ctx_store.go`:

```go
package rice

// storeCapacity is how many Set keys a pooled Ctx holds before its store grows.
//
// Middleware typically stores one or two values. Past four, the slice grows —
// once per pooled Ctx, since the grown slice is kept. That is the documented
// cliff in docs/03-core-concepts.md.
const storeCapacity = 4

// entry is one key/value pair in the per-request store.
type entry struct {
	key string
	val any
}

// Set stores a value for the rest of the request, replacing any earlier value for
// key.
//
// It allocates nothing itself. Passing a non-pointer value can allocate at the
// call site, where Go boxes it into an any; pass a pointer to avoid that.
//
// Borrowed: the store dies with the Ctx when the handler returns. The value is
// yours, but it cannot be read back through this Ctx afterwards.
func (c *Ctx) Set(key string, v any) {
	for i := range c.store {
		if c.store[i].key == key {
			c.store[i].val = v
			return
		}
	}
	c.store = append(c.store, entry{key: key, val: v})
}

// Get returns the value stored for key, and whether there was one.
//
// A linear scan beats a map at the handful of keys a request carries, and
// allocates nothing.
func (c *Ctx) Get(key string) (any, bool) {
	for i := range c.store {
		if c.store[i].key == key {
			return c.store[i].val, true
		}
	}
	return nil, false
}

// resetStore empties the store and zeroes the entries it held.
//
// Zeroing is not optional. A truncated slice keeps its old values in the backing
// array, and a pooled Ctx would keep every value a previous request stored alive
// for as long as it sat in the pool.
func (c *Ctx) resetStore() {
	clear(c.store)
	c.store = c.store[:0]
}
```

In `ctx.go`, add to `Ctx` after `params`:

```go
	// store backs Set and Get. newCtx pre-sizes it to storeCapacity.
	store []entry
```

and in `reset`, after `c.params.Reset()`:

```go
	c.resetStore()
```

In `pool.go`, `newCtx`:

```go
func (a *App) newCtx() *Ctx {
	return &Ctx{
		params: router.MakeParams(a.maxParams),
		store:  make([]entry, 0, storeCapacity),
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test . -race -count=1`
Expected: PASS.

Also run once without `-race` to see the exact budgets rather than their race-floored form:
Run: `go test . -run 'TestAllocBudget' -count=1 -v 2>&1 | grep -E '^(---|ok|FAIL)'`
Expected: all PASS.

- [ ] **Step 5: Full suite**

Run: `make lint && make test && make cover`
Expected: all exit 0; root coverage ≥ 98.8%.

- [ ] **Step 6: Fault injection**

1. In `resetStore`, delete `clear(c.store)`. Run `go test . -run TestReleaseDropsEveryReference -count=1`. Expected: FAIL naming a backing slot. Restore.
2. In `reset`, delete `c.resetStore()`. Run `go test . -run TestARequestSeesNoStoreEntriesFromThePreviousOne -count=1`. Expected: FAIL. Restore.
3. In `Set`, delete the replace loop (always append). Run `go test . -run TestSetReplacesAnExistingKey -count=1`. Expected: FAIL. Restore.
4. In `newCtx`, drop the `store:` line. Run `go test . -run TestAllocBudgetCtxSetPointer -count=1`. Record whether it fails. It should not — `budget` warms once and `resetStore` keeps the grown capacity — and if it stays green, write that down: this budget pins `Set`, not pre-sizing, and pre-sizing is pinned by the dispatch budgets instead. Then run `go test . -run 'TestAllocBudgetHandle' -count=1` with a handler-less check to confirm nothing else regressed. Restore.

- [ ] **Step 7: Commit**

```bash
git add ctx_store.go ctx_store_test.go ctx.go pool.go pool_test.go alloc_test.go
git commit -m "feat: add the per-request Set/Get store to Ctx

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: `ricedebug` — poisoning, the exhaustive guard, CI

**Files:**
- Create: `poison_debug.go`, `poison_release.go`
- Create: `ricedebug_test.go` (tagged `ricedebug`)
- Modify: `ctx.go`, `ctx_param.go`, `ctx_response.go`, `ctx_store.go` (a `check()` at the top of every exported method)
- Modify: `pool.go` (`release` marks and conditionally `Put`s)
- Modify: `alloc_test.go` (build tag)
- Modify: `Makefile` (`test-debug`), `.github/workflows/ci.yml` (step)

**Interfaces:**
- Consumes: `release`, `Ctx`, every exported `Ctx` method from Tasks 2–3.
- Produces:
  - `type poison` with `mark()` and `check()`, embedded as field `poison` (first field of `Ctx`)
  - `const poolReuse bool`
  - `const errUseAfterRelease = "rice: *Ctx used after its handler returned; see the borrow contract in the package documentation"` (in `poison_debug.go` only)
  - `make test-debug`

- [ ] **Step 1: Record the pre-check baseline**

Before touching any accessor, in the release build:

Run: `go test ./bench/ -run '^$' -bench 'BenchmarkRiceDispatch$|BenchmarkCtxSetHeader' -benchmem -count=10 | tee /tmp/claude-m6-precheck.txt`

Keep the file; Step 9 compares against it. (Use the session scratchpad directory if `/tmp` is not writable.)

- [ ] **Step 2: Write the failing debug tests**

Create `ricedebug_test.go`:

```go
//go:build ricedebug

package rice

import (
	"reflect"
	"testing"

	"github.com/valyala/fasthttp"
)

// releasedCtx dispatches one request whose handler retains its Ctx, and returns
// that Ctx after release.
func releasedCtx(t *testing.T) *Ctx {
	t.Helper()
	var retained *Ctx
	app := New()
	app.GET("/users/:id", func(c *Ctx) error {
		retained = c
		return nil
	})
	dispatchCtx(app, "GET", "/users/42")
	if retained == nil {
		t.Fatal("handler did not run")
	}
	return retained
}

// TestEveryCtxMethodPanicsAfterRelease walks the exported method set by
// reflection, so a method added later without a check() fails here without
// anyone remembering to extend a list. reflect is confined to this test file;
// ADR-0006 governs production code.
func TestEveryCtxMethodPanicsAfterRelease(t *testing.T) {
	c := releasedCtx(t)
	v := reflect.ValueOf(c)
	typ := v.Type()

	if typ.NumMethod() == 0 {
		t.Fatal("*Ctx has no exported methods; this test is measuring nothing")
	}

	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		args := make([]reflect.Value, m.Type.NumIn()-1) // In(0) is the receiver
		for j := range args {
			args[j] = reflect.Zero(m.Type.In(j + 1))
		}

		t.Run(m.Name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r != errUseAfterRelease {
					t.Errorf("%s on a released Ctx: recovered %v, want the use-after-release panic", m.Name, r)
				}
			}()
			v.Method(i).Call(args)
		})
	}
}

// TestARetainedCtxPanicsEvenAfterAnotherRequest is the sequence ADR-0005's
// original mechanism missed: a retained Ctx used after a later request has run.
// With a poisoned Ctx returned to the pool, the second request would un-poison
// it and this call would return the second request's parameter instead of
// panicking.
func TestARetainedCtxPanicsEvenAfterAnotherRequest(t *testing.T) {
	var retained *Ctx
	app := New()
	app.GET("/users/:id", func(c *Ctx) error {
		if retained == nil {
			retained = c
		}
		return nil
	})
	app.Build()

	for _, uri := range []string{"/users/1", "/users/2"} {
		fctx := &fasthttp.RequestCtx{}
		fctx.Request.Header.SetMethod("GET")
		fctx.Request.SetRequestURI(uri)
		app.handle(fctx)
	}

	defer func() {
		if r := recover(); r != errUseAfterRelease {
			t.Errorf("retained Ctx after a second request: recovered %v, want the use-after-release panic", r)
		}
	}()
	_ = retained.Param("id")
}

func TestTheDebugBuildNeverReusesAContext(t *testing.T) {
	if poolReuse {
		t.Error("poolReuse is true under ricedebug; a poisoned Ctx would be un-poisoned by its next acquire")
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test . -tags ricedebug -run 'TestEveryCtxMethod|TestARetainedCtx|TestTheDebugBuild' -count=1`
Expected: FAIL to compile — `undefined: errUseAfterRelease`, `undefined: poolReuse`.

- [ ] **Step 4: Create the poison pair**

`poison_debug.go`:

```go
//go:build ricedebug

package rice

// poolReuse is false in the debug build. A poisoned Ctx that went back into the
// pool would be un-poisoned by the next acquire, and a stale reference to it
// would silently read another request's data instead of panicking — the exact
// bug this build exists to catch. See the M6 design doc, D5, and ADR-0005.
const poolReuse = false

// errUseAfterRelease is the panic value for any use of a released Ctx.
const errUseAfterRelease = "rice: *Ctx used after its handler returned; see the borrow contract in the package documentation"

// poison marks a Ctx as released.
type poison struct{ released bool }

func (p *poison) mark() { p.released = true }

func (p *poison) check() {
	if p.released {
		panic(errUseAfterRelease)
	}
}
```

`poison_release.go`:

```go
//go:build !ricedebug

package rice

// poolReuse is true in release builds: a released Ctx goes back to the pool.
const poolReuse = true

// poison is zero-sized in release builds and its methods are empty, so every
// check() call on the hot path compiles to nothing. Build with -tags ricedebug to
// make use-after-release panic. See poison_debug.go.
type poison struct{}

func (*poison) mark()  {}
func (*poison) check() {}
```

- [ ] **Step 5: Embed `poison` and call `check()` everywhere**

In `ctx.go`, make `poison` the first field of `Ctx`:

```go
type Ctx struct {
	// poison is first on purpose. It is zero-sized in release builds, and Go pads
	// a zero-sized final field so a pointer to it cannot point past the struct.
	poison poison

	fctx *fasthttp.RequestCtx
	...
```

Add `c.poison.check()` as the first statement of every exported method. One-line methods become:

```go
func (c *Ctx) RequestCtx() *fasthttp.RequestCtx {
	c.poison.check()
	return c.fctx
}

func (c *Ctx) Method() []byte {
	c.poison.check()
	return c.fctx.Method()
}

func (c *Ctx) Path() []byte {
	c.poison.check()
	return c.fctx.Path()
}
```

`ctx_param.go`:

```go
func (c *Ctx) Param(name string) []byte {
	c.poison.check()
	return c.params.Get(name)
}

func (c *Ctx) ParamString(name string) string {
	c.poison.check()
	return string(c.params.Get(name))
}
```

`ctx_response.go`: prepend `c.poison.check()` to `Status`, `SetHeader`, `SetContentType`, `String`, `Bytes`.
`ctx_store.go`: prepend `c.poison.check()` to `Set` and `Get` (not `resetStore`, which `release` calls).

Keep every existing doc comment above its method unchanged.

- [ ] **Step 6: Poison in `release`**

```go
// release unbinds c and hands it back.
//
// It drops every reference c holds, so a pooled Ctx never keeps a finished
// request's memory reachable. In the debug build it then poisons c and keeps it
// out of the pool, so every later use panics; in the release build the poison is
// a no-op and c returns to the pool. poolReuse is a constant, so the branch not
// taken is compiled out.
func (a *App) release(c *Ctx) {
	c.reset(nil, nil)
	c.poison.mark()
	if poolReuse {
		a.pool.Put(c)
	}
}
```

- [ ] **Step 7: Tag the allocation budgets out of the debug build**

First line of `alloc_test.go`, followed by a blank line:

```go
//go:build !ricedebug
```

Then add, directly above `func budget`:

```go
// This file is excluded from the ricedebug build, which allocates a fresh Ctx
// per request on purpose: a poisoned Ctx is never returned to the pool.
```

- [ ] **Step 8: Makefile and CI**

`Makefile` — add to `.PHONY` and below `test`:

```make
## test-debug: run all tests under the ricedebug build, which panics on use-after-release
test-debug:
	$(GO) test ./... -race -count=1 -tags ricedebug
```

`.github/workflows/ci.yml` — after the `Test` step:

```yaml
      - name: Test (ricedebug)
        run: make test-debug
```

Also `.github/workflows/` may hold more than one file; add the step to the one containing `make test`.

- [ ] **Step 9: Run everything**

Run: `make lint && make test && make test-debug && make cover`
Expected: all exit 0.

Run: `go test ./bench/ -run '^$' -bench 'BenchmarkRiceDispatch$|BenchmarkCtxSetHeader' -benchmem -count=10 | tee /tmp/claude-m6-postcheck.txt`
Then: `benchstat /tmp/claude-m6-precheck.txt /tmp/claude-m6-postcheck.txt` (install with `go install golang.org/x/perf/cmd/benchstat@latest` if absent).
Expected: no statistically significant difference, 0 allocs/op in both. Paste the benchstat table into the task report — Task 7 cites it.

Confirm the check is gone from the release binary rather than assuming it:
Run: `go build -gcflags=-m . 2>&1 | grep -E 'inlining call to \(\*poison\)\.check' | head -3`
Expected: at least one line. Paste it into the report.

- [ ] **Step 10: Fault injection**

1. Remove `c.poison.check()` from `Get` only. Run `make test-debug`. Expected: `TestEveryCtxMethodPanicsAfterRelease/Get` FAILs. Restore.
2. Change `poison_debug.go` to `const poolReuse = true`. Run `go test . -tags ricedebug -run 'TestARetainedCtx|TestTheDebugBuild' -count=1`. Expected: both FAIL; note in the report that `TestARetainedCtxPanicsEvenAfterAnotherRequest` failing is the empirical proof that ADR-0005's original mechanism missed this case. Under `-race` the pool may drop the Put and let the test pass by luck, which is why this step runs without `-race`. Restore.
3. Remove `c.poison.mark()` from `release`. Run `go test . -tags ricedebug -run TestEveryCtxMethod -count=1`. Expected: FAIL. Restore.

- [ ] **Step 11: Commit**

```bash
git add poison_debug.go poison_release.go ricedebug_test.go ctx.go ctx_param.go ctx_response.go ctx_store.go pool.go alloc_test.go Makefile .github/workflows
git commit -m "feat: poison released contexts under ricedebug and keep them out of the pool

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Concurrency — hammer the pool under `-race`

**Files:**
- Create: `pool_race_test.go`

**Interfaces:**
- Consumes: `FasthttpHandler`, `Use`, `Set`/`Get`, `Param` from earlier tasks.
- Produces: nothing new.

- [ ] **Step 1: Write the test**

```go
package rice

import (
	"strconv"
	"sync"
	"testing"

	"github.com/valyala/fasthttp"
)

// TestConcurrentRequestsNeverSeeEachOthersState hammers the pool from many
// goroutines. Each request carries a distinct id in its path; a middleware copies
// it into the store; the handler echoes both. A Ctx shared between two in-flight
// requests, or one not fully reset between them, shows up as a response carrying
// someone else's id. make test and make test-debug both run this under -race.
func TestConcurrentRequestsNeverSeeEachOthersState(t *testing.T) {
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			c.Set("id", c.ParamString("id"))
			return next(c)
		}
	})
	app.GET("/users/:id", func(c *Ctx) error {
		stored, _ := c.Get("id")
		return c.String(200, string(c.Param("id"))+"|"+stored.(string))
	})
	h := app.FasthttpHandler()

	const workers, perWorker = 16, 500
	var wg sync.WaitGroup
	errs := make(chan string, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			fctx := &fasthttp.RequestCtx{}
			for i := 0; i < perWorker; i++ {
				id := strconv.Itoa(w*perWorker + i)
				fctx.Request.Reset()
				fctx.Response.Reset()
				fctx.Request.Header.SetMethod("GET")
				fctx.Request.SetRequestURI("/users/" + id)

				h(fctx)

				if got, want := string(fctx.Response.Body()), id+"|"+id; got != want {
					errs <- "body = " + got + ", want " + want
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)

	for e := range errs {
		t.Error(e)
	}
}
```

- [ ] **Step 2: Run it in both builds**

Run: `go test . -run TestConcurrentRequestsNeverSeeEachOthersState -race -count=3`
Run: `go test . -run TestConcurrentRequestsNeverSeeEachOthersState -race -count=3 -tags ricedebug`
Expected: both PASS.

- [ ] **Step 3: Fault injection**

1. In `release`, move `a.pool.Put(c)` to run **before** `c.reset(nil, nil)` (Put, then reset a Ctx another goroutine may already hold). Run the release-build command from Step 2. Expected: FAIL with a data race report or a mismatched body. Restore.
2. In `pool.go`, replace the body of `acquire` with a package-level shared `Ctx` (`var shared Ctx` / `c := &shared`). Run the release-build command. Expected: FAIL. Restore.

If either stays green, stop and report: the test is not exercising concurrency the way it claims.

- [ ] **Step 4: Full suite and commit**

Run: `make lint && make test && make test-debug`
Expected: all exit 0.

```bash
git add pool_race_test.go
git commit -m "test: hammer the Ctx pool concurrently in both builds

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Benchmarks

**Files:**
- Create: `pool_bench_test.go` (package `rice`)
- Modify: `bench/rice_bench_test.go` (comments; new parameterised, store and parallel benchmarks)
- Modify: `scripts/bench.sh`, `Makefile` (`bench` target) — include the root package
- Create: `bench/results/M6-context-pooling.txt` (generated)

**Interfaces:**
- Consumes: `handle`, `acquire`, `release`, `lookup`, `callErrorHandler` from package `rice`.
- Produces: `BenchmarkDispatchPooledVsUnpooled` with sub-benchmarks `pooled` and `unpooled`; `BenchmarkDispatchParameterised`; `BenchmarkCtxSetGet`; `BenchmarkDispatchParallel`.

- [ ] **Step 1: Write the same-session comparison**

`pool_bench_test.go`:

```go
//go:build !ricedebug

package rice

import (
	"runtime/debug"
	"testing"

	"github.com/valyala/fasthttp"
)

// handleUnpooled is handle as it was before M6: a fresh Ctx per request. It
// exists only so the pooled path can be measured against it in one session —
// comparing against M1's recorded file compares two sessions, and between-session
// drift has misled this project before. Keep it in step with handle apart from
// acquire and release.
func (a *App) handleUnpooled(fctx *fasthttp.RequestCtx) {
	c := a.newCtx()
	c.reset(a, fctx)

	defer func() {
		if r := recover(); r != nil {
			a.callErrorHandler(c, &PanicError{Value: r, Stack: debug.Stack()})
		}
	}()

	h, ok := a.lookup(fctx.Method(), fctx.Path(), &c.params)
	if !ok {
		a.callErrorHandler(c, ErrNotFound)
		return
	}
	if err := h(c); err != nil {
		a.callErrorHandler(c, err)
	}
}

func BenchmarkDispatchPooledVsUnpooled(b *testing.B) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return c.Bytes(200, c.Param("id")) })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")

	b.Run("pooled", func(b *testing.B) {
		app.handle(fctx)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			app.handle(fctx)
		}
	})

	b.Run("unpooled", func(b *testing.B) {
		app.handleUnpooled(fctx)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			app.handleUnpooled(fctx)
		}
	})
}
```

Note that the unpooled arm now allocates the pre-sized slices too (3 objects), which is the true cost of not pooling a Ctx with this shape. Say so when reporting the number; do not compare it to M1's single allocation as if they were the same object.

- [ ] **Step 2: Add public-API benchmarks**

Append to `bench/rice_bench_test.go`:

```go
// BenchmarkDispatchParameterised is the full dispatch path for a route read with
// Param — the second zero M6's exit criteria claim. Until M6 only tree-level
// parameter lookups were benchmarked.
func BenchmarkDispatchParameterised(b *testing.B) {
	app := rice.New()
	app.GET("/users/:id", func(c *rice.Ctx) error {
		return c.Bytes(fasthttp.StatusOK, c.Param("id"))
	})

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/users/42")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

type benchUser struct{ id int }

// BenchmarkCtxSetGet measures one Set and one Get with a pointer value, the
// allocation-free way to use the store.
func BenchmarkCtxSetGet(b *testing.B) {
	u := &benchUser{id: 1}
	app := rice.New()
	app.GET("/x", func(c *rice.Ctx) error {
		c.Set("user", u)
		v, _ := c.Get("user")
		if v != u {
			return rice.NewHTTPError(500, "store lost the value")
		}
		return nil
	})

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/x")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkDispatchParallel exercises sync.Pool's per-P caches, which a
// single-goroutine benchmark cannot show.
func BenchmarkDispatchParallel(b *testing.B) {
	app := rice.New()
	app.GET("/users/:id", func(c *rice.Ctx) error {
		return c.Bytes(fasthttp.StatusOK, c.Param("id"))
	})
	h := app.FasthttpHandler()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		fctx := newRequestCtx("GET", "/users/42")
		for pb.Next() {
			h(fctx)
		}
	})
}
```

Update `BenchmarkRiceDispatch`'s comment:

```go
// BenchmarkRiceDispatch measures the full dispatch path: acquire a pooled Ctx,
// bind it, call the handler, write a plaintext body, release.
//
// It is expected to report zero allocations. Until M6 it reported one, the
// unpooled Ctx; bench/results/M1-minimal-server.txt holds that baseline. Compare
// against BenchmarkFasthttpBaseline, which is the same work with no framework.
```

Grep `bench/` for any other comment claiming one allocation per request (`grep -rn "one allocation\|1 alloc\|allocat" bench/*.go`) and correct each.

- [ ] **Step 3: Include the root package in benchmark runs**

`scripts/bench.sh`: change `go test ./bench/... -run '^$' -bench . -benchmem -count=10` to `go test . ./bench/... -run '^$' -bench . -benchmem -count=10`.

`Makefile` `bench` target: change `$(GO) test ./bench/... -run '^$$' -bench . -benchmem -count=1` to `$(GO) test . ./bench/... -run '^$$' -bench . -benchmem -count=1`.

- [ ] **Step 4: Smoke-run and verify zeros**

Run: `make bench 2>&1 | grep -E 'RiceDispatch|Dispatch404|DispatchParameterised|CtxSetGet|DispatchParallel|PooledVsUnpooled'`
Expected: `0 allocs/op` on every line except `PooledVsUnpooled/unpooled` (3 allocs/op). If any other line is not 0, stop and report.

- [ ] **Step 5: Record**

Run: `make bench-record LABEL=M6-context-pooling`
Expected: `wrote bench/results/M6-context-pooling.txt`. Close other heavy processes first; the run takes several minutes.

Then: `benchstat bench/results/M5-error-handling.txt bench/results/M6-context-pooling.txt > /tmp/claude-m6-benchstat.txt` and paste the dispatch rows into the task report, noting both files' stamps.

- [ ] **Step 6: Commit**

```bash
git add pool_bench_test.go bench/rice_bench_test.go scripts/bench.sh Makefile bench/results/M6-context-pooling.txt
git commit -m "bench: record pooled dispatch against an unpooled arm in one session

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Documentation, the ADR-0005 correction, and the retrospective

**Files:**
- Modify: `docs/adr/0005-context-pooling-and-borrow-contract.md`
- Modify: `doc.go`
- Modify: `docs/03-core-concepts.md` (§2 per-request store and Lifetime)
- Modify: `docs/05-performance-model.md`
- Modify: `docs/02-architecture.md`
- Modify: `docs/04-roadmap.md`
- Modify: `README.md`
- Modify: `app.go`, `internal/router/params.go` (any stale comment Tasks 1–2 missed)
- Create: `docs/milestones/M6-context-pooling.md`
- Modify: `docs/progress.md`

**Interfaces:**
- Consumes: the benchstat tables from Task 4 Step 9 and Task 6 Step 5; the fault-injection reports from Tasks 1–5.
- Produces: documentation only.

- [ ] **Step 1: Correct ADR-0005**

In the "Decision" section, replace the second paragraph with:

```markdown
Add a `ricedebug` build tag under which a released `Ctx` is poisoned and **never returned
to the pool**, so any later method call panics with a message naming the contract instead
of returning plausible data from another request. The check compiles out entirely in
release builds.
```

Append at the end of the file:

```markdown
**Corrected in M6.** This ADR originally decided to poison a released `Ctx` by "clearing its
pointers and setting a generation counter" and did not say whether the poisoned object went
back into the pool. If it does, the mechanism fails at the one moment it is needed: the next
request acquires the same object, `reset` clears the poison, and a stale reference to it reads
that request's data without panicking. A generation counter cannot help, because the code
holding the stale pointer has no generation of its own to compare — the stale pointer and the
reused object are the same pointer. The debug build now keeps poisoned contexts out of the pool
entirely. Its price is a fresh `Ctx` for every request — up to three objects through `newCtx`:
the `Ctx`, its store slice, and its parameter slice when the App has a parameterised route at all
— paid in that build only.
`TestARetainedCtxPanicsEvenAfterAnotherRequest` fails when the poisoned `Ctx` is returned to the
pool, which is the empirical form of this correction. See the M6 design doc, D5.
```

- [ ] **Step 2: `doc.go`**

Replace the package comment with:

```go
// Package rice is a small HTTP framework built on fasthttp.
//
// # Borrow contract
//
// Every value reachable from a *Ctx is borrowed, not owned. It is valid only
// until the handler returns. That includes the *Ctx itself, the per-request store
// behind Set and Get, and every []byte it hands out: route parameters, the path,
// and the method. The *Ctx is returned to a pool and handed to another request;
// the memory behind those slices belongs to fasthttp and is reused for the next
// request on the same connection.
//
// Accessors that return []byte are free and borrowed. Accessors that return
// string copy, cost one allocation, and are safe to keep. The naming makes the
// expensive choice the longer one to type:
//
//	id := c.Param("id")        // borrowed: valid until the handler returns
//	id := c.ParamString("id")  // owned: safe to keep, costs one allocation
//
// # Debug build
//
// Build or test with -tags ricedebug to make any use of a *Ctx after its handler
// returns panic. The check costs nothing in a normal build.
//
// It catches misuse of the *Ctx only. A []byte kept from Param or Path, or the
// *fasthttp.RequestCtx kept from RequestCtx, points into fasthttp's memory, which
// rice cannot poison; keeping one past the handler is still a bug and the debug
// build will not report it.
//
// See docs/adr/0005-context-pooling-and-borrow-contract.md.
package rice
```

- [ ] **Step 3: `docs/03-core-concepts.md` §2**

Replace the "Per-request store" paragraph under the code block with:

```markdown
Backed by a small slice of key/value pairs, not a map, pre-sized to four entries on each
pooled `Ctx`. Middleware typically stores one or two values, and a linear scan over a few
entries beats a map allocation. A fifth key grows the slice — once per pooled `Ctx`, since
the grown slice is kept. That is a documented cliff, not a secret.

`Set` allocates nothing itself, but a non-pointer value is boxed into `any` at the call
site, and that can allocate: `c.Set("id", someString)` costs one allocation, and it is the
caller's. Store a pointer to avoid it.
```

In "Lifetime", replace the final paragraph with:

```markdown
Under `-tags ricedebug`, a released `Ctx` is poisoned and never reused, and any later method
call panics with a message pointing at this section. That check is compiled out of release
builds entirely, so it costs nothing in production.

The debug build catches misuse of the `*Ctx`. It does **not** catch a retained `[]byte`: in
the example above, `go audit(c.Param("id"))` reads a reused buffer and the debug build stays
silent, because that slice points into fasthttp's memory rather than into anything rice can
poison. See [ADR-0005](adr/0005-context-pooling-and-borrow-contract.md).
```

Add `Set` and `Get` to the read/write tables only if §2 lists them there; the store has its own subsection, so it likely needs no table change.

- [ ] **Step 4: `docs/05-performance-model.md`**

- Keep the "End to end: single handler, unpooled Ctx (M1 baseline)" row as history.
- Add rows, status `MEASURED M6`, with numbers from `bench/results/M6-context-pooling.txt`: end to end static route (0), end to end parameterised route read with `Param` (0), 404 through the funnel (0), `Ctx.Set` pointer (0), `Ctx.Get` (0), `Ctx.Set` non-constant string (1, caller's boxing).
- Change any M5 row that recorded 1 for the `Ctx` to its M6 value.
- Replace the "Inline arrays before heap slices" technique with:

```markdown
**Pre-sized slices on the pooled `Ctx`.** Route parameters and the per-request store are
slices allocated once when the pool builds a `Ctx`, sized from the largest registered route
and to four store entries. Buys: the common case entirely, and no fixed limit on parameters.
Costs: a cliff at the store's capacity, paid once per pooled `Ctx` rather than per request,
and a `Ctx` built before a larger route was registered grows once on first use.
```

- In "Caller-supplied parameter slices", replace "a maximum parameter count that must be computed at build time" with "a maximum parameter count tracked during registration to size the pool's contexts".
- Add a short subsection "Pooled versus unpooled, same session" quoting the `BenchmarkDispatchPooledVsUnpooled` numbers, and noting that the unpooled arm allocates three objects because it builds a pre-sized `Ctx`.
- Add a sentence quoting Task 4 Step 9's benchstat: the `check()` calls cost nothing measurable in the release build.

- [ ] **Step 5: `docs/02-architecture.md`, `docs/04-roadmap.md`, `README.md`**

- Architecture: mark `pool.go` as existing (drop `(M6)`), add `poison_debug.go` / `poison_release.go` and `ctx_store.go` to the package layout.
- Roadmap: `### ☐ M6` → `### ☑ M6`. Under "Explicitly deferred", add:
  `- Typed per-request store keys (`rice.Key[T]` with a `Get` returning `T`), safer than `Set(string, any)` and immune to key collisions between middleware. Rejected for M6 because the documented API was already `string`/`any`.`
- README: update the status section so it no longer describes the `Ctx` as unpooled or M6 as upcoming; mention `make test-debug`.

- [ ] **Step 6: Sweep for stale comments**

Run: `grep -rn "M6\|MaxParams\|unpooled\|per request on purpose\|Do not optimise" --include=*.go .`
Every production-code hit that describes M6 as future work, or says a `Ctx` is allocated per request, is rewritten to the present tense or deleted. `pool_bench_test.go`'s `handleUnpooled` is expected to match and stays.

- [ ] **Step 7: Retrospective**

Create `docs/milestones/M6-context-pooling.md` from `docs/milestones/TEMPLATE.md`. It must contain:
- The answer to the milestone's question, with the same-session pooled/unpooled numbers.
- The ADR-0005 correction as a finding: a documented safety mechanism that would not have worked, found while designing rather than in production, and the fault injection that proves it (Task 4 Step 10.2).
- The `-race` interaction with `sync.Pool` and the three-object ceiling it imposes on `newCtx`.
- Every fault injection from Tasks 1–5 that stayed green when expected red, and what that meant. If none did, say so.
- What `ricedebug` still cannot catch.

- [ ] **Step 8: Journal entry**

Prepend to `docs/progress.md`, directly below the `---` that follows the template block, in the required shape:

```markdown
## 2026-MM-DD — M6 — Context pooling: zero allocations, and a poisoning mechanism that would not have worked

**Did:** ...
**Learned:** ...
**Measured:** ...
**Next:** M7 — graceful shutdown with a deadline, OnStart/OnShutdown hooks, server timeouts as options.
```

Use the actual date. Fill every field from the task reports; no placeholder text may remain.

- [ ] **Step 9: Final verification**

Run: `make lint && make test && make test-debug && make cover && make bench`
Expected: all exit 0; root coverage ≥ 98.8%.

Run: `grep -rn "TODO\|TBD\|MM-DD" docs/milestones/M6-context-pooling.md docs/progress.md`
Expected: no output.

- [ ] **Step 10: Commit**

```bash
git add docs doc.go README.md app.go internal/router/params.go
git commit -m "docs: close M6 with the ADR-0005 correction, the retrospective, and measured budgets

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Notes for the executor

**On ADR-0005.** The design found that the ADR's poisoning mechanism would not have caught the bug it was written for. Task 4 Step 10.2 is where that stops being an argument and becomes a failing test. Do not skip it, and report its output verbatim.

**On `-race` and the pool.** `sync.Pool` behaves differently under the race detector — it drops a quarter of `Put`s on purpose. The zero budgets survive that only because `AllocsPerRun` floors, and only while `newCtx` makes three allocations or fewer. If a budget flakes under `make test` but passes under plain `go test`, that is the cause; do not "fix" it by raising the budget.

**On fault injection.** Same rule as every milestone since M4: when a deliberately broken guard stays green, that is the finding. Tasks 1 and 3 each contain an injection that is *expected* to stay green, with the reason written next to it; confirm the reason rather than taking it on faith.

**On tests that call `handle` without `Build`.** Many do. The pool is created in `New` precisely so they keep working; if one breaks with a nil pool or a nil `New` func, the wiring in Task 2 Step 4 is wrong, not the test.
