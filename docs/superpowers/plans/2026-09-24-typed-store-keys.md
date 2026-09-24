# Typed Store Keys Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `Ctx.Set(string, any)` and `Ctx.Get(string)` with `rice.Key[T]`, a key that fixes its value's type and can never collide with another key.

**Architecture:** `NewKey[T](name)` allocates a `keyID` and returns a `Key[T]` holding a pointer to it; that pointer is the key's identity, the name is for people. `Key.Set(c, v)` and `Key.Get(c)` scan the same pre-sized per-request slice as today, whose entries now hold `*keyID` instead of `string`. `Ctx.Set`/`Ctx.Get` are removed and every caller moves to keys in the same commit, since removing them breaks the build of every package that calls them.

**Tech Stack:** Go 1.25 generics. No new dependency.

**Spec:** [docs/superpowers/specs/2026-09-24-typed-store-keys-design.md](../specs/2026-09-24-typed-store-keys-design.md)

## Global Constraints

- **Branch:** `typed-store-keys`, already created, already holding the spec commit. Do not commit to `main`.
- **Exported surface added:** exactly `Key[T]`, `NewKey`, and the methods `Key.Set`, `Key.Get`, `Key.String`. **Removed:** `Ctx.Set`, `Ctx.Get`. Nothing else exported changes.
- **Panics carry the prefix `rice: `**: `NewKey("")`, and `Set`/`Get` on a zero `Key[T]{}`.
- **`Key.Set` and `Key.Get` call `c.poison.check()` first**, before any other check, so under `-tags ricedebug` a released `Ctx` panics with the use-after-release panic even when the key is zero.
- **Semantics carried over:** `Get` with nothing stored returns the zero `T` and `false`; a second `Set` replaces the first; a nil value is a value (`Get` returns it and `true`).
- **`resetStore` still zeroes every entry before truncating.**
- **Budgets:** declared typed (`const want float64 = …` where a test declares one); no figure is written into a document until measured on darwin and Linux, with and without `-race`.
- **`BenchmarkCtxSetGet` keeps its name**: it appears in `bench/results/M6-context-pooling.txt` and `M7-lifecycle.txt`, and a rename breaks comparison across milestones.
- **Run `make test` and `make test-debug` before each commit.** Commit messages: subject, blank line, then the session's `Co-Authored-By` trailer on its own line.

## Review Focus

Inputs the spec implies but does not enumerate. Each has a test in the task that owns the code.

1. **`T` is an interface type and the stored value is nil** — `NewKey[error]("err").Set(c, nil)`. The slot holds a nil `any`, and a bare `v.(T)` on a nil interface panics. `Get` must return `(nil, true)`. (Task 1)
2. **Two keys with the same name and different types** — `NewKey[string]("x")` and `NewKey[int]("x")`. Both values must survive. (Task 1)
3. **A zero `Key` on a released `Ctx` under `ricedebug`** — must panic with the use-after-release panic, not the "use NewKey" panic, so the borrow contract is reported first. (Task 1)
4. **A package-level key used by concurrent requests** — the pool race test, moved to a package-level key, must still pass under `-race`. (Task 1)
5. **A grown store after release** — every backing slot's `key` must be nil and `val` nil, not only the first `storeCapacity`. (Task 1)

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `ctx_store.go` | `keyID`, `Key[T]`, `NewKey`, the methods, unexported `set`/`get`, `resetStore` | 1 |
| `ctx_store_test.go` | behaviour, rewritten for keys | 1 |
| `pool_test.go`, `pool_race_test.go`, `ricedebug_test.go` | move to keys; one new `ricedebug` test | 1 |
| `alloc_test.go` | the store budgets, renamed | 1, 2 |
| `middleware/requestid.go` | `requestIDKey` becomes a `Key[string]` | 1 |
| `bench/rice_bench_test.go` | `BenchmarkCtxSetGet` moves to a key | 1 |
| `docs/adr/0016-typed-store-keys.md` (new), `docs/adr/README.md`, `docs/03-core-concepts.md`, `docs/05-performance-model.md`, `docs/04-roadmap.md`, `docs/02-architecture.md`, `docs/progress.md`, and any of `README.md`, `middleware/doc.go`, `docs/06-glossary.md` that name `Set`/`Get` | documentation | 3 |

**Test helpers already in package `rice` that this plan uses:** `dispatchCtx(app, method, path) *fasthttp.RequestCtx` (`panic_test.go`), `budget(t, name, want, fn)` (`budget_test.go`, an upper bound), `releasedCtx(t) *Ctx` and `errUseAfterRelease` (`ricedebug_test.go` / `poison_debug.go`).

---

### Task 1: `Key[T]` replaces `Set` and `Get`, and every caller moves

**Files:**
- Modify: `ctx_store.go` (whole file)
- Modify: `ctx_store_test.go` (whole file)
- Modify: `pool_test.go:26-60`, `pool_race_test.go:17-27`, `ricedebug_test.go` (append), `alloc_test.go:506-545`
- Modify: `middleware/requestid.go:10-16, 46, 55-62`
- Modify: `bench/rice_bench_test.go:91-102`

**Interfaces:**
- Produces: `type Key[T any] struct{ id *keyID }`; `func NewKey[T any](name string) Key[T]`; `func (k Key[T]) Set(c *Ctx, v T)`; `func (k Key[T]) Get(c *Ctx) (T, bool)`; `func (k Key[T]) String() string`; unexported `type keyID struct{ name string }`, `func (c *Ctx) set(id *keyID, v any)`, `func (c *Ctx) get(id *keyID) (any, bool)`, `entry{key *keyID; val any}`.

- [ ] **Step 1: Write the failing behaviour tests**

Replace `ctx_store_test.go` entirely:

```go
package rice

import (
	"strconv"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestKeyGetWithNothingStored(t *testing.T) {
	c := &Ctx{}
	k := NewKey[string]("missing")

	v, ok := k.Get(c)
	if v != "" || ok {
		t.Errorf("Get = %q, %v; want \"\", false", v, ok)
	}
}

func TestKeySetThenGet(t *testing.T) {
	c := &Ctx{}
	k := NewKey[string]("user")
	k.Set(c, "alice")

	v, ok := k.Get(c) // v is a string: no assertion at the call site
	if !ok || v != "alice" {
		t.Errorf("Get = %q, %v; want alice, true", v, ok)
	}
}

// TestTwoKeysWithTheSameNameNeverCollide is the reason keys exist. Two
// middleware that both choose "user" get two slots, whatever the types.
func TestTwoKeysWithTheSameNameNeverCollide(t *testing.T) {
	c := &Ctx{}
	a := NewKey[string]("user")
	b := NewKey[string]("user")
	n := NewKey[int]("user")
	a.Set(c, "alice")
	b.Set(c, "bob")
	n.Set(c, 7)

	if v, _ := a.Get(c); v != "alice" {
		t.Errorf("first key = %q, want alice", v)
	}
	if v, _ := b.Get(c); v != "bob" {
		t.Errorf("second key with the same name = %q, want bob", v)
	}
	if v, _ := n.Get(c); v != 7 {
		t.Errorf("key with the same name and another type = %d, want 7", v)
	}
}

func TestACopiedKeyIsTheSameKey(t *testing.T) {
	c := &Ctx{}
	k := NewKey[int]("n")
	copied := k
	k.Set(c, 7)

	if v, ok := copied.Get(c); !ok || v != 7 {
		t.Errorf("copy.Get = %d, %v; want 7, true", v, ok)
	}
}

func TestKeySetReplacesAnEarlierValue(t *testing.T) {
	c := &Ctx{}
	k := NewKey[int]("k")
	k.Set(c, 1)
	k.Set(c, 2)

	if v, _ := k.Get(c); v != 2 {
		t.Errorf("Get = %d, want 2", v)
	}
	if n := len(c.store); n != 1 {
		t.Errorf("store holds %d entries after setting one key twice, want 1", n)
	}
}

func TestKeyStoresANilPointer(t *testing.T) {
	c := &Ctx{}
	k := NewKey[*int]("p")
	k.Set(c, nil)

	if v, ok := k.Get(c); v != nil || !ok {
		t.Errorf("Get = %v, %v; want nil, true", v, ok)
	}
}

// TestKeyStoresANilInterfaceValue is the case a bare v.(T) gets wrong: a nil
// error stored in an any is a nil any, and asserting a nil any to an interface
// type panics.
func TestKeyStoresANilInterfaceValue(t *testing.T) {
	c := &Ctx{}
	k := NewKey[error]("err")
	k.Set(c, nil)

	if v, ok := k.Get(c); v != nil || !ok {
		t.Errorf("Get = %v, %v; want nil, true", v, ok)
	}
}

// TestTheStoreHoldsMoreThanItsInitialCapacity crosses storeCapacity, the
// documented cliff, and checks nothing is lost on the way over it.
func TestTheStoreHoldsMoreThanItsInitialCapacity(t *testing.T) {
	c := &Ctx{}
	keys := make([]Key[int], storeCapacity+2)
	for i := range keys {
		keys[i] = NewKey[int]("k" + strconv.Itoa(i))
		keys[i].Set(c, i)
	}

	for i, k := range keys {
		if v, ok := k.Get(c); !ok || v != i {
			t.Errorf("key %d = %d, %v; want %d, true", i, v, ok, i)
		}
	}
}

// TestMiddlewareCanPassAValueToTheHandler is the use the store exists for.
func TestMiddlewareCanPassAValueToTheHandler(t *testing.T) {
	requestID := NewKey[string]("request_id")
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			requestID.Set(c, "abc")
			return next(c)
		}
	})
	app.GET("/x", func(c *Ctx) error {
		v, _ := requestID.Get(c)
		return c.String(200, v)
	})

	fctx := dispatchCtx(app, "GET", "/x")

	if got := string(fctx.Response.Body()); got != "abc" {
		t.Errorf("body = %q, want %q", got, "abc")
	}
}

func TestARequestSeesNoStoreEntriesFromThePreviousOne(t *testing.T) {
	k := NewKey[string]("k")
	var leaked bool
	app := New()
	app.GET("/set", func(c *Ctx) error { k.Set(c, "v"); return nil })
	app.GET("/get", func(c *Ctx) error { _, leaked = k.Get(c); return nil })
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

func TestKeyStringIsItsName(t *testing.T) {
	if got := NewKey[int]("user").String(); got != "user" {
		t.Errorf("String() = %q, want user", got)
	}
}

// wantKeyPanic runs fn and checks it panics with a rice: message naming NewKey.
func wantKeyPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		msg, _ := recover().(string)
		if !strings.HasPrefix(msg, "rice: ") || !strings.Contains(msg, "NewKey") {
			t.Errorf("panic = %q, want a rice: message naming NewKey", msg)
		}
	}()
	fn()
}

func TestNewKeyPanicsOnAnEmptyName(t *testing.T) {
	wantKeyPanic(t, func() { NewKey[int]("") })
}

// TestAZeroKeyPanics: every zero Key has a nil id, so allowing one would give
// all of them one shared slot — the collision keys exist to remove.
func TestAZeroKeyPanics(t *testing.T) {
	c := &Ctx{}
	var k Key[int]
	t.Run("Set", func(t *testing.T) { wantKeyPanic(t, func() { k.Set(c, 1) }) })
	t.Run("Get", func(t *testing.T) { wantKeyPanic(t, func() { k.Get(c) }) })
}
```

- [ ] **Step 2: Run them to see the build fail**

Run: `go test . -run 'Key|Store' -count=1`
Expected: build failure, `undefined: NewKey` and `undefined: Key`.

- [ ] **Step 3: Replace `ctx_store.go`**

```go
package rice

// storeCapacity is how many keys a pooled Ctx holds before its store grows.
//
// Middleware typically stores one or two values. Past four, the slice grows —
// once per pooled Ctx, since the grown slice is kept. That is the documented
// cliff in docs/03-core-concepts.md.
const storeCapacity = 4

// keyID is a key's identity. Only its address matters; the name is for people.
type keyID struct{ name string }

// entry is one key/value pair in the per-request store.
type entry struct {
	key *keyID
	val any
}

// Key identifies one value in the per-request store and fixes its type.
//
// Create keys once, at package level, with NewKey:
//
//	var userKey = rice.NewKey[*User]("user")
//
//	userKey.Set(c, u)
//	u, ok := userKey.Get(c) // u is a *User
//
// A key's identity is made by NewKey, not by its name: two NewKey calls give
// two keys that never share a value, even with the same name and type. A copy
// of a Key is the same key. The zero Key is not usable; Set and Get on it
// panic.
//
// Borrowed: a value lives as long as the Ctx it was stored on, and cannot be
// read back through that Ctx once the handler has returned.
type Key[T any] struct{ id *keyID }

// NewKey returns a new key for values of type T. name appears in String and
// in nothing else; it must not be empty.
//
// It allocates once, for the key's identity. Creating a key per request pays
// that each time and gives a key the next request cannot read back: declare
// keys as package-level variables.
func NewKey[T any](name string) Key[T] {
	if name == "" {
		panic("rice: NewKey: name must not be empty")
	}
	return Key[T]{id: &keyID{name: name}}
}

// Set stores v for the rest of the request, replacing any earlier value for k.
//
// It allocates nothing itself. A non-pointer T can allocate at the call site,
// where Go boxes v into the store's any; a pointer T does not.
func (k Key[T]) Set(c *Ctx, v T) {
	c.poison.check()
	k.mustBeMade()
	c.set(k.id, v)
}

// Get returns the value stored for k, and whether there was one. With nothing
// stored it returns the zero T and false.
func (k Key[T]) Get(c *Ctx) (T, bool) {
	c.poison.check()
	k.mustBeMade()
	v, ok := c.get(k.id)
	if !ok || v == nil {
		// A nil v is a stored nil of an interface type T: asserting a nil any
		// to an interface type would panic, and the zero T is that nil.
		var zero T
		return zero, ok
	}
	return v.(T), true
}

// String returns the name the key was made with.
func (k Key[T]) String() string {
	if k.id == nil {
		return ""
	}
	return k.id.name
}

// mustBeMade panics on a zero Key. Every zero Key has a nil id, so letting one
// through would give all of them a single shared slot.
func (k Key[T]) mustBeMade() {
	if k.id == nil {
		panic("rice: Key used without NewKey; create keys with rice.NewKey")
	}
}

// set stores v under id, replacing an earlier value. A linear scan beats a map
// at the handful of keys a request carries, and allocates nothing.
func (c *Ctx) set(id *keyID, v any) {
	for i := range c.store {
		if c.store[i].key == id {
			c.store[i].val = v
			return
		}
	}
	c.store = append(c.store, entry{key: id, val: v})
}

// get returns the value stored under id, and whether there was one.
func (c *Ctx) get(id *keyID) (any, bool) {
	for i := range c.store {
		if c.store[i].key == id {
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

- [ ] **Step 4: Move every other caller**

`pool_test.go`, in `TestReleaseDropsEveryReference`: replace `c.Set("k", &struct{}{})` with `NewKey[*struct{}]("k").Set(c, &struct{}{})`, and in the backing-slot loop replace `e.key != ""` with `e.key != nil`.

`pool_race_test.go`: add above the test

```go
// raceIDKey is package-level, as keys are meant to be, so every concurrent
// request shares one key and only the Ctx separates their values.
var raceIDKey = NewKey[string]("id")
```

and replace the two store lines: `c.Set("id", c.ParamString("id"))` becomes `raceIDKey.Set(c, c.ParamString("id"))`; `stored, _ := c.Get("id")` becomes `stored, _ := raceIDKey.Get(c)`; `stored.(string)` becomes `stored`.

`alloc_test.go`, lines 506–545: replace the three store budgets with

```go
// storeSink keeps Get's result reachable so the compiler cannot elide the call.
var storeSink any

func TestAllocBudgetKeySetPointer(t *testing.T) {
	c := New().newCtx()
	k := NewKey[*struct{ n int }]("k")
	v := &struct{ n int }{}

	budget(t, "Key.Set with a pointer value", 0, func() {
		c.resetStore()
		k.Set(c, v)
	})
}

func TestAllocBudgetKeyGet(t *testing.T) {
	c := New().newCtx()
	k := NewKey[*struct{ n int }]("k")
	k.Set(c, &struct{ n int }{})

	budget(t, "Key.Get with a pointer value", 0, func() {
		storeSink, _ = k.Get(c)
	})
}

// TestAllocBudgetKeySetString asserts exactly one allocation, and the
// allocation is not rice's. Converting a non-constant string to the store's
// any boxes it; Set itself allocates nothing, as TestAllocBudgetKeySetPointer
// shows. The budget is exact rather than an upper bound so that a change to
// Go's boxing rules shows up here instead of silently changing what the
// documentation says.
func TestAllocBudgetKeySetString(t *testing.T) {
	c := New().newCtx()
	k := NewKey[string]("k")
	s := strconv.Itoa(123456)

	got := testing.AllocsPerRun(1000, func() {
		c.resetStore()
		k.Set(c, s)
	})
	if got != 1 {
		t.Errorf("Key.Set with a non-constant string allocated %.1f objects per call, want exactly 1 (the caller's boxing)", got)
	}
}
```

`ricedebug_test.go`: append

```go
// TestKeyMethodsPanicAfterRelease covers what the reflection walk above cannot:
// Key's methods are methods on Key, not on *Ctx. A zero Key is included so the
// use-after-release panic is shown to come before the zero-key check.
func TestKeyMethodsPanicAfterRelease(t *testing.T) {
	c := releasedCtx(t)
	k := NewKey[string]("k")
	var zero Key[string]
	for name, call := range map[string]func(){
		"Set":          func() { k.Set(c, "v") },
		"Get":          func() { k.Get(c) },
		"zero key Set": func() { zero.Set(c, "v") },
		"zero key Get": func() { zero.Get(c) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != errUseAfterRelease {
					t.Errorf("%s on a released Ctx: recovered %v, want the use-after-release panic", name, r)
				}
			}()
			call()
		})
	}
}
```

`middleware/requestid.go`: replace the `const (...)` block with

```go
const requestIDHeader = "X-Request-Id"

// requestIDKey is the store key. A Key is identified by the value NewKey made,
// not by its name, so no other middleware can read or overwrite this slot; the
// namespaced name is only what String prints. Read the id with RequestIDFrom.
var requestIDKey = rice.NewKey[string]("rice/middleware.request-id")
```

replace `c.Set(requestIDKey, id)` with `requestIDKey.Set(c, id)`, and replace the body of `RequestIDFrom` with

```go
	id, _ := requestIDKey.Get(c)
	return id
```

`bench/rice_bench_test.go`: above `BenchmarkCtxSetGet` add `var benchUserKey = rice.NewKey[*benchUser]("user")`; in the handler replace the two store lines with `benchUserKey.Set(c, u)` and `v, _ := benchUserKey.Get(c)`. Update the doc comment's "one Set and one Get" to "one Key.Set and one Key.Get". **Keep the benchmark's name.**

- [ ] **Step 5: Run everything**

Run: `go test . -run 'Key|Store|Release|Concurrent' -count=1 -v`, then `make test`, `make test-debug`, `make lint`, and `go test ./bench/ -run '^$' -bench BenchmarkCtxSetGet -benchmem -count=1`.
Expected: all green; the benchmark reports `0 allocs/op`. `grep -rn 'c\.Set(\|c\.Get(' --include='*.go' .` returns nothing.

- [ ] **Step 6: Commit**

```bash
git add ctx_store.go ctx_store_test.go pool_test.go pool_race_test.go ricedebug_test.go alloc_test.go middleware/requestid.go bench/rice_bench_test.go
git commit -F - <<'EOF'
rice: Key[T] replaces Set and Get, and every caller moves to it

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 2: Measure the store budgets and add the string `Get` row

**Files:**
- Modify: `alloc_test.go` (the store budgets' doc comments; one new test)

**Interfaces:**
- Consumes: `NewKey`, `Key.Set`, `Key.Get`, `budget`, and the budgets Task 1 renamed.
- Produces: `TestAllocBudgetKeyGetString`; the measured figures, which Task 3 writes into the documents.

- [ ] **Step 1: Add the budget**

After `TestAllocBudgetKeySetString`:

```go
// stringSink keeps Get's string result reachable.
var stringSink string

// TestAllocBudgetKeyGetString pins that Get with a non-pointer T copies the
// value out of the store's any without allocating: the assertion v.(T) reads
// the boxed string's header, it does not build one.
func TestAllocBudgetKeyGetString(t *testing.T) {
	c := New().newCtx()
	k := NewKey[string]("k")
	k.Set(c, strconv.Itoa(123456))

	budget(t, "Key.Get with a string value", 0, func() {
		stringSink, _ = k.Get(c)
	})
}
```

- [ ] **Step 2: Measure four ways, and record every output verbatim in your report**

```bash
go test . -run 'TestAllocBudgetKey' -count=1 -v
go test -race . -run 'TestAllocBudgetKey' -count=1 -v
go test ./middleware/ -run 'TestAllocBudgetRequestIDGenerated|TestAllocBudgetLoggerWithRequestID' -count=1 -v
go test -race ./middleware/ -run 'TestAllocBudgetRequestIDGenerated|TestAllocBudgetLoggerWithRequestID' -count=1 -v
docker run --rm --user 1000:1000 -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath \
  -e GOFLAGS=-buildvcs=false -v "$PWD":/src -w /src golang:1.25.14 \
  sh -c "uname -m; go test . -run 'TestAllocBudgetKey' -count=1 -v; go test ./middleware/ -run 'TestAllocBudgetRequestIDGenerated|TestAllocBudgetLoggerWithRequestID' -count=1 -v"
docker run --rm --user 1000:1000 -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath \
  -e GOFLAGS=-buildvcs=false -v "$PWD":/src -w /src golang:1.25.14 \
  sh -c "uname -m; go test -race . -run 'TestAllocBudgetKey' -count=1 -v; go test -race ./middleware/ -run 'TestAllocBudgetRequestIDGenerated|TestAllocBudgetLoggerWithRequestID' -count=1 -v"
```

`budget` is an upper bound and prints nothing on a pass. To read the actual figure of each `budget` test, temporarily set its `want` to `-1` in a scratch edit, run once per platform without `-race`, record the printed figure, and restore it — edit each test's own line by hand, **never with a pattern across the file** (a `sed` over `const want float64 = …` changed unrelated budgets on the RealIP branch). Expected: `Key.Set` pointer 0, `Key.Get` pointer 0, `Key.Get` string 0, `Key.Set` string 1, `RequestID` 2 (at most 3 under `-race` on Linux), `Logger+RequestID` 6 (at most 9 under `-race`). **If any figure differs from the expected one, do not change a budget to match it; stop and report it with the output.**

- [ ] **Step 3: Record the measurements in the doc comments**

To each of the four `TestAllocBudgetKey*` doc comments add one sentence in the style of `middleware/alloc_test.go`'s `TestAllocBudgetTimeout`: where it was measured (darwin arm64 go1.25.6, Linux arm64 golang:1.25.14), with and without `-race`, and the figure.

- [ ] **Step 4: Run and commit**

Run: `git diff alloc_test.go` — the only changes are the new test and the doc-comment sentences. Then `make test && make test-debug && make lint`.

```bash
git add alloc_test.go
git commit -F - <<'EOF'
rice: measure the typed store's budgets and pin Get with a string

Co-Authored-By: <your session's trailer>
EOF
```

---

### Task 3: ADR-0016 and the documentation

**Files:**
- Create: `docs/adr/0016-typed-store-keys.md`
- Modify: `docs/adr/README.md`, `docs/03-core-concepts.md`, `docs/05-performance-model.md`, `docs/04-roadmap.md`, `docs/02-architecture.md`, `docs/progress.md`; and `README.md`, `middleware/doc.go`, `docs/06-glossary.md` wherever they name `Set` or `Get`

**Interfaces:**
- Consumes: Task 2's measured figures.
- Produces: nothing code depends on.

- [ ] **Step 1: Write ADR-0016, "Typed store keys replace Set and Get"**

Format of `docs/adr/README.md`: `Status: Accepted`, `Date` the day it lands, sections `Context`, `Decision`, `Alternatives`, `Consequences`. It must contain:

- **Context:** the two run-time failure modes of `Set(string, any)` — a collision between two middleware choosing the same string, silent; a wrong type assertion on `Get`'s `any`, found only when it panics or quietly returns false — and that `middleware.RequestID` avoided the first only by an unexported namespaced string. The M6 design deferred typed keys because "the documented API was already `string`/`any`"; this reverses that.
- **Decision:** the spec's D1–D6, stated flatly.
- **Alternatives**, each with why it lost: **package-level generic functions** `rice.Set(c, k, v)` / `rice.Get(c, k)` — the same mechanism, but `rice.Get` is a vague name at package level and reads unlike `c.Param`; **user-declared key types** in the `context.WithValue` style — prevent collisions but leave the value's type unchecked, so `Get` still needs the caller to name a type; **keeping `Set`/`Get` beside keys** — two ways to do one thing, and the string path still collides.
- **Consequences:** a breaking change — code calling `c.Set`/`c.Get` stops compiling, and the migration is one package-level `NewKey` per string key, then `k.Set(c, v)` and `k.Get(c)` without the assertion. The boxing cost of a non-pointer value does not change. A zero `Key` panics. `Key`'s methods are not `Ctx` methods, so the reflection test of `ricedebug` cannot see them and a dedicated test covers them.

Add the row `| [0016](0016-typed-store-keys.md) | Typed store keys replace Set and Get | Accepted |` to the index.

- [ ] **Step 2: Rewrite the store's documentation**

- `docs/03-core-concepts.md`, *Per-request store* (around line 168): replace the `Set`/`Get` signatures with the `Key[T]` API from the spec's D1, explain identity (D2), the panics (D3), the carried-over semantics (D4), keys as package-level variables, and the boxing cost with its one-allocation example rewritten for a key. The `RequestID` illustration (around line 221) uses a package-level key; the paragraph after it says `RequestIDFrom` is the only way to read the real middleware's id, because its key is unexported.
- `docs/05-performance-model.md`: rename the three store rows (`c.Set`/`c.Get` to `Key.Set`/`Key.Get`, test names to Task 1's), add the `Key.Get` string row, and reword the `c.Set("k", s)` sentence (around line 162) for a key. State the figures as Task 2 measured them.
- `docs/04-roadmap.md`: remove the typed-keys item from *Explicitly deferred*; add a *Done after M8* entry after the RealIP one, in the same voice, naming ADR-0016, the breaking change, and that the figures are unchanged.
- `docs/02-architecture.md` line 47: `ctx_store.go        Key[T], NewKey: the typed per-request store and its pre-sized slice`.
- `README.md`, `middleware/doc.go`, `docs/06-glossary.md`: run `grep -n 'Set\b\|Get\b\|store' <file>` on each and move every mention of the store API to keys; leave `SetHeader`, `SetContext` and the like alone.

- [ ] **Step 3: Write the progress entry**

Prepend to `docs/progress.md`, milestone `post-M8`, in the template's shape. **Learned** must carry: that a Go method cannot have its own type parameters, which is why the typed operation lives on the key; that a typed key buys safety and not speed, since boxing is the value's cost; and that `Key`'s methods escape the reflection walk of `TestEveryCtxMethodPanicsAfterRelease`, which is why they have their own `ricedebug` test. **Measured:** Task 2's figures by platform, and `make cover`. **Next:** none scheduled.

- [ ] **Step 4: Verify and commit**

Run: `make test && make test-debug && make lint && make cover`, then
`grep -rn 'c\.Set(\|c\.Get(\|Ctx\.Set\|Ctx\.Get' README.md docs/*.md docs/adr/0016-typed-store-keys.md middleware/doc.go` — it must return nothing but sentences in `docs/progress.md` entries older than this one and ADR-0016's own description of the old API.

```bash
git add docs/ README.md middleware/doc.go
git commit -F - <<'EOF'
docs: ADR-0016 and the docs for typed store keys

Co-Authored-By: <your session's trailer>
EOF
```

---

## Self-Review

**Spec coverage.** D1 (API) → Task 1 Step 3. D2 (identity; copy is the same key; `String`) → `TestTwoKeysWithTheSameNameNeverCollide`, `TestACopiedKeyIsTheSameKey`, `TestKeyStringIsItsName`. D3 (panics) → `TestNewKeyPanicsOnAnEmptyName`, `TestAZeroKeyPanics`. D4 (semantics; borrow; poison first) → `TestKeyGetWithNothingStored`, `TestKeySetReplacesAnEarlierValue`, `TestKeyStoresANilPointer`, `TestARequestSeesNoStoreEntriesFromThePreviousOne`, `TestKeyMethodsPanicAfterRelease`. D5 (store shape, zeroing) → Task 1 Step 3 and `TestReleaseDropsEveryReference`. D6 (callers) → Task 1 Step 4. Budgets → Tasks 1–2. Documentation → Task 3. Exit criterion 4's grep → Task 1 Step 5 and Task 3 Step 4.

**Placeholders.** None; each code step shows its code.

**Type consistency.** `Key[T]`, `NewKey[T](name string) Key[T]`, `Set(c *Ctx, v T)`, `Get(c *Ctx) (T, bool)`, `String() string`, `keyID`, `entry{key *keyID; val any}`, `set(id *keyID, v any)`, `get(id *keyID) (any, bool)` are named the same in every task. Test names in Task 2 and Task 3 match those Task 1 creates.

**Review Focus.** 1 → `TestKeyStoresANilInterfaceValue`, and `Get`'s `v == nil` branch. 2 → the `int` key in `TestTwoKeysWithTheSameNameNeverCollide`. 3 → the zero-key cases in `TestKeyMethodsPanicAfterRelease`. 4 → `raceIDKey` in `pool_race_test.go`. 5 → `e.key != nil` over the full capacity in `TestReleaseDropsEveryReference`.
