# M3 Implementation Plan — Radix Tree Router

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace M2's exact-match map with a per-verb radix tree supporting named parameters and a trailing catch-all, resolving overlaps by a fixed total priority, rejecting ambiguous registrations at startup, and capturing parameters without allocating.

**Architecture:** `internal/router` gains three units built bottom-up: fixed-capacity `Params` storage, pattern parsing and validation, then the tree itself (prefix-splitting insertion and a backtracking lookup). The algorithm lands as an unexported `tree[H]` tested entirely inside its own package, and only then is `Tree[H]` swapped over and its new `Lookup(path, *Params)` signature threaded through `rice`. `Params` finally moves onto `Ctx`, which forces the `Ctx` allocation ahead of the lookup and ends M2's accidental zero-allocation 404 path.

**Tech Stack:** Go 1.25, `github.com/valyala/fasthttp` (the only runtime dependency), GNU make.

**Spec:** [`docs/superpowers/specs/2026-09-10-m3-radix-tree-router-design.md`](../specs/2026-09-10-m3-radix-tree-router-design.md). Decision record: [ADR-0004](../../adr/0004-radix-tree-router.md). Milestone definition in [`docs/04-roadmap.md`](../../04-roadmap.md). Budgets in [`docs/05-performance-model.md`](../../05-performance-model.md).

## Global Constraints

Every task's requirements implicitly include this section.

- Module path is `github.com/vietpham102301/rice-http`; root package is `rice`. Test files outside the package use the alias `rice "github.com/vietpham102301/rice-http"`.
- Nothing under `internal/` may import package `rice` (`docs/02-architecture.md`). This is why the tree is generic over its handler type.
- `github.com/valyala/fasthttp` is the only permitted **runtime** dependency. The standard library is fine everywhere, including `fmt`, `errors` and `net/url` inside `internal/router`.
- Package `rice` must not import `reflect` or `encoding/json` (ADR-0006).
- Every value handed out by `Ctx` is borrowed and dies when the handler returns (ADR-0005). Byte-returning accessors are free and borrowed; string-returning accessors copy.
- Match priority is fixed and total: **static > parameter > wildcard** (ADR-0004). Overlaps are ordered, not rejected.
- `MaxParams` is `8`. A pattern with more parameters is rejected at registration.
- Allocation budgets M3 must meet, asserted with `testing.AllocsPerRun` rather than observed in a benchmark: router lookup with parameters captured = 0, `c.Param` = 0, `c.ParamString` = 1.
- End-to-end per-request allocation stays at 1, the `Ctx`. Do not optimise it. M6 removes it.
- Code belonging to a later milestone carries a comment naming that milestone. Temporary scaffolding a later task in *this* plan will remove carries a comment naming that task.
- Commit after every task, using Conventional Commits (`feat:`, `test:`, `chore:`, `docs:`, `bench:`).
- **Task boundaries follow compilation units.** Every task must end with `go build ./...` succeeding. This is the lesson M1 recorded the hard way and M2 applied: M1's Task 5 declared a field whose type arrived two tasks later, and three tasks had to be merged mid-execution.
- **Do not assert what a compiler does; measure it.** M2 shipped two confident, wrong claims about escape analysis and allocation, both caught in final review, both asserted from memory. Any comment in this milestone that claims something allocates or does not allocate must be backed by `go build -gcflags=-m` output or an `AllocsPerRun` test you actually ran.

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `internal/router/params.go` | `MaxParams`, `Param`, `Params`, `Reset`, `Get`, `Len`, `At`, unexported `add`/`truncate` | 1 |
| `internal/router/params_test.go` | Capture, retrieval, overflow, rollback, zeroing | 1 |
| `internal/router/pattern.go` | `segment`, `parsePattern`, every registration-time rejection | 2 |
| `internal/router/pattern_test.go` | Parsing table plus one test per rejection | 2 |
| `internal/router/tree.go` | `node[H]`, unexported `tree[H]`, prefix-splitting insert, backtracking lookup | 3 |
| `internal/router/tree_test.go` | Splits, priority, backtracking, conflicts, depth | 3 |
| `internal/router/router.go` | Modify: `Tree[H]` delegates to `tree[H]`; `Lookup` gains `*Params`; the map goes | 4 |
| `internal/router/router_test.go` | Modify: M2's tests updated for the new `Lookup` signature | 4 |
| `route.go` | Modify: validation moves into `parsePattern`; `App.lookup` threads `*Params` | 4 |
| `route_test.go` | Modify: `app.lookup` call sites gain a `*Params`; parameter routing tests | 4 |
| `ctx.go` | Modify: `Ctx` gains `params router.Params`; `reset` clears it | 5 |
| `ctx_param.go` | `Ctx.Param`, `Ctx.ParamString` | 5 |
| `ctx_param_test.go` | Borrow-contract behaviour of both accessors | 5 |
| `app.go` | Modify: `handle` allocates the `Ctx` before the lookup (spec D4) | 5 |
| `docs/adr/0005-context-pooling-and-borrow-contract.md` | Modify: record that M3 ended the accidental zero-allocation 404 | 5 |
| `alloc_test.go` | Modify: parameter lookup, `Param`, `ParamString` budgets | 6 |
| `server_test.go` | Modify: parameterised and wildcard routes over a real socket | 6 |
| `docs/05-performance-model.md` | Modify: split the bundled accessor row; three rows become measured | 6 |
| `bench/mapbaseline_test.go` | M2's map, frozen for same-run comparison (spec D9) | 7 |
| `bench/router_bench_test.go` | Modify: tree benchmarks beside the frozen map | 7 |
| `bench/results/M3-radix-tree-router.txt` | Generated, then committed | 7 |
| `docs/adr/0007-no-trailing-slash-or-case-insensitive-matching.md` | Closes ADR-0004's open question (spec D7) | 8 |
| `docs/milestones/M3-radix-tree-router.md` | Retrospective | 8 |
| `docs/04-roadmap.md`, `docs/progress.md` | Modify: status marker, journal entry | 8 |

---

### Task 1: Parameter storage

**Files:**
- Create: `internal/router/params.go`
- Test: `internal/router/params_test.go`

**Interfaces:**
- Consumes: nothing. Compiles and tests entirely on its own.
- Produces:
  - `const MaxParams = 8`
  - `type Param struct { Key string; Value []byte }`
  - `type Params struct{ ... }` — zero value ready to use
  - `func (p *Params) Reset()`
  - `func (p *Params) Get(name string) []byte`
  - `func (p *Params) Len() int`
  - `func (p *Params) At(i int) Param`
  - `func (p *Params) add(key string, value []byte) bool` (unexported, used by Task 3)
  - `func (p *Params) truncate(n int)` (unexported, used by Task 3's backtracking)

- [ ] **Step 1: Write the failing tests**

Create `internal/router/params_test.go`:

```go
package router

import (
	"bytes"
	"testing"
)

func TestParamsAddAndGet(t *testing.T) {
	var p Params

	if !p.add("id", []byte("42")) {
		t.Fatal("add reported storage full on an empty Params")
	}
	if !p.add("slug", []byte("hello")) {
		t.Fatal("add reported storage full after one entry")
	}

	if got := p.Get("id"); !bytes.Equal(got, []byte("42")) {
		t.Errorf("Get(\"id\") = %q, want %q", got, "42")
	}
	if got := p.Get("slug"); !bytes.Equal(got, []byte("hello")) {
		t.Errorf("Get(\"slug\") = %q, want %q", got, "hello")
	}
	if got := p.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}
}

func TestParamsGetAbsentReturnsNil(t *testing.T) {
	var p Params
	p.add("id", []byte("42"))

	if got := p.Get("missing"); got != nil {
		t.Errorf("Get on an absent name = %q, want nil", got)
	}
}

func TestParamsGetOnEmptyParams(t *testing.T) {
	var p Params

	if got := p.Get("anything"); got != nil {
		t.Errorf("Get on empty Params = %q, want nil", got)
	}
	if got := p.Len(); got != 0 {
		t.Errorf("Len() = %d on empty Params, want 0", got)
	}
}

func TestParamsAtReturnsInsertionOrder(t *testing.T) {
	var p Params
	p.add("a", []byte("1"))
	p.add("b", []byte("2"))

	if got := p.At(0); got.Key != "a" || !bytes.Equal(got.Value, []byte("1")) {
		t.Errorf("At(0) = %+v, want {a 1}", got)
	}
	if got := p.At(1); got.Key != "b" || !bytes.Equal(got.Value, []byte("2")) {
		t.Errorf("At(1) = %+v, want {b 2}", got)
	}
}

func TestParamsAtPanicsOutOfRange(t *testing.T) {
	var p Params
	p.add("a", []byte("1"))

	for _, i := range []int{-1, 1, 99} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("At(%d) did not panic on a Params of length 1", i)
				}
			}()
			_ = p.At(i)
		}()
	}
}

func TestParamsFillsExactlyMaxParams(t *testing.T) {
	var p Params

	for i := 0; i < MaxParams; i++ {
		if !p.add("k", []byte("v")) {
			t.Fatalf("add reported storage full at entry %d, want room for %d", i, MaxParams)
		}
	}
	if got := p.Len(); got != MaxParams {
		t.Errorf("Len() = %d, want %d", got, MaxParams)
	}
}

func TestParamsRejectsOverflow(t *testing.T) {
	var p Params
	for i := 0; i < MaxParams; i++ {
		p.add("k", []byte("v"))
	}

	if p.add("one-too-many", []byte("v")) {
		t.Error("add accepted an entry past MaxParams, want false")
	}
	if got := p.Len(); got != MaxParams {
		t.Errorf("a rejected add changed Len to %d, want %d", got, MaxParams)
	}
}

func TestResetClearsEverything(t *testing.T) {
	var p Params
	p.add("id", []byte("42"))
	p.add("slug", []byte("hello"))

	p.Reset()

	if got := p.Len(); got != 0 {
		t.Errorf("Len() = %d after Reset, want 0", got)
	}
	if got := p.Get("id"); got != nil {
		t.Errorf("Get(\"id\") = %q after Reset, want nil", got)
	}
}

// TestResetZeroesDiscardedSlots reaches into the struct on purpose. A stale
// Value in a slot past n keeps fasthttp's request buffer reachable, and under
// M6's pool a Params outlives the request that filled it, so "forgotten" is not
// good enough — the slots must actually be cleared.
func TestResetZeroesDiscardedSlots(t *testing.T) {
	var p Params
	p.add("id", []byte("42"))
	p.add("slug", []byte("hello"))

	p.Reset()

	for i := 0; i < MaxParams; i++ {
		if p.slots[i].Key != "" || p.slots[i].Value != nil {
			t.Errorf("slot %d still holds %+v after Reset, want the zero Param", i, p.slots[i])
		}
	}
}

// TestTruncateRollsBackAndZeroes is what makes lookup backtracking safe: a
// parameter captured on a branch that then fails must not survive the unwind.
func TestTruncateRollsBackAndZeroes(t *testing.T) {
	var p Params
	p.add("kept", []byte("yes"))
	saved := p.Len()
	p.add("speculative", []byte("no"))

	p.truncate(saved)

	if got := p.Len(); got != 1 {
		t.Errorf("Len() = %d after truncate(1), want 1", got)
	}
	if got := p.Get("speculative"); got != nil {
		t.Errorf("the rolled-back capture is still visible: %q", got)
	}
	if got := p.Get("kept"); !bytes.Equal(got, []byte("yes")) {
		t.Errorf("truncate discarded a kept capture: Get(\"kept\") = %q", got)
	}
	if p.slots[1].Key != "" || p.slots[1].Value != nil {
		t.Errorf("slot 1 still holds %+v after truncate, want the zero Param", p.slots[1])
	}
}

func TestParamsValueAliasesTheCallerSlice(t *testing.T) {
	var p Params

	buf := []byte("original")
	p.add("k", buf)

	copy(buf, "OVERWRIT")

	// Params stores the slice, it does not copy it. That is the borrow contract:
	// the value is only valid while the request buffer behind it is.
	if got := p.Get("k"); !bytes.Equal(got, []byte("OVERWRIT")) {
		t.Errorf("Get returned %q; Params is expected to alias the caller's slice, not copy it", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/router/ -run 'TestParams|TestReset|TestTruncate' -v`

Expected: FAIL to compile, with `undefined: Params` and `undefined: MaxParams`.

- [ ] **Step 3: Write the implementation**

Create `internal/router/params.go`:

```go
package router

// MaxParams is the number of route parameters one route may capture.
//
// Storage is a fixed-size array inside Params, so this is a hard limit: a
// pattern declaring more parameters is rejected at registration rather than
// discovered at request time. Eight is generous —
// /org/:org/repo/:repo/pull/:pull/comment/:comment uses four.
//
// M6 sizes parameter storage from the maximum actually observed across
// registered routes, which removes the limit. Until a pool exists to own that
// storage, a fixed array is the only way to capture parameters without
// allocating.
const MaxParams = 8

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
// The zero value is ready to use. It is meant to live inside a Ctx by value, so
// that capture costs no allocation of its own. That is the whole reason Lookup
// takes a *Params instead of returning a slice — see ADR-0005.
type Params struct {
	slots [MaxParams]Param
	n     int
}

// Reset discards every captured parameter.
//
// It is exported because package rice calls it from Ctx.reset, across the
// internal/ package boundary where an unexported method would be unreachable.
func (p *Params) Reset() { p.truncate(0) }

// truncate rolls capture back to n entries.
//
// The discarded slots are zeroed rather than merely skipped: a stale Value keeps
// fasthttp's request buffer reachable, and under M6's pool a Params outlives the
// request that filled it.
func (p *Params) truncate(n int) {
	for i := n; i < p.n; i++ {
		p.slots[i] = Param{}
	}
	p.n = n
}

// add appends a captured parameter, reporting false if storage is full.
//
// The slice is stored, not copied: Value is a view into the request path. A full
// Params means a pattern with more than MaxParams parameters was registered,
// which parsePattern rejects, so false here indicates a bug in the tree rather
// than anything a caller can provoke.
func (p *Params) add(key string, value []byte) bool {
	if p.n >= MaxParams {
		return false
	}
	p.slots[p.n] = Param{Key: key, Value: value}
	p.n++
	return true
}

// Get returns the value captured for name, or nil if there is none.
//
// A linear scan over at most MaxParams entries beats a map decisively at this
// size and allocates nothing.
func (p *Params) Get(name string) []byte {
	for i := 0; i < p.n; i++ {
		if p.slots[i].Key == name {
			return p.slots[i].Value
		}
	}
	return nil
}

// Len returns the number of captured parameters.
func (p *Params) Len() int { return p.n }

// At returns the i'th captured parameter in the order the lookup captured them.
// It panics if i is out of range.
func (p *Params) At(i int) Param {
	if i < 0 || i >= p.n {
		panic("router: Params.At index out of range")
	}
	return p.slots[i]
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/router/ -run 'TestParams|TestReset|TestTruncate' -v`

Expected: PASS for all eleven tests.

- [ ] **Step 5: Verify the build and lint**

Run: `go build ./... && make lint`

Expected: both exit 0. `add` and `truncate` are unexported and currently unused outside tests; that is legal Go and `go vet` is silent about it. Do not delete them — Task 3 uses both.

- [ ] **Step 6: Commit**

```bash
git add internal/router/params.go internal/router/params_test.go
git commit -m "feat: add fixed-capacity route parameter storage"
```

---

### Task 2: Pattern parsing and validation

**Files:**
- Create: `internal/router/pattern.go`
- Test: `internal/router/pattern_test.go`

**Interfaces:**
- Consumes: `MaxParams` from Task 1.
- Produces:
  - `type segKind uint8` with `segStatic`, `segParam`, `segWildcard`
  - `type segment struct { kind segKind; text string }`
  - `func parsePattern(pattern string) ([]segment, error)`

Static segments carry their raw text including slashes. `/users/:id/posts/:pid`
parses to `{static "/users/"} {param "id"} {static "/posts/"} {param "pid"}`.
Parameter and wildcard segments carry the bare name, without the `:` or `*`.

- [ ] **Step 1: Write the failing tests**

Create `internal/router/pattern_test.go`:

```go
package router

import (
	"strings"
	"testing"
)

func TestParsePatternSplitsIntoSegments(t *testing.T) {
	cases := []struct {
		pattern string
		want    []segment
	}{
		{"/", []segment{{segStatic, "/"}}},
		{"/users", []segment{{segStatic, "/users"}}},
		{"/users/", []segment{{segStatic, "/users/"}}},
		{"/users/:id", []segment{{segStatic, "/users/"}, {segParam, "id"}}},
		{
			"/users/:id/posts/:pid",
			[]segment{{segStatic, "/users/"}, {segParam, "id"}, {segStatic, "/posts/"}, {segParam, "pid"}},
		},
		{"/users/:id/edit", []segment{{segStatic, "/users/"}, {segParam, "id"}, {segStatic, "/edit"}}},
		{"/files/*path", []segment{{segStatic, "/files/"}, {segWildcard, "path"}}},
		{"/*all", []segment{{segStatic, "/"}, {segWildcard, "all"}}},
	}

	for _, c := range cases {
		got, err := parsePattern(c.pattern)
		if err != nil {
			t.Errorf("parsePattern(%q) returned %v, want nil", c.pattern, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("parsePattern(%q) produced %d segments, want %d: %+v", c.pattern, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parsePattern(%q) segment %d = %+v, want %+v", c.pattern, i, got[i], c.want[i])
			}
		}
	}
}

// rejected pairs a pattern with a phrase its error must contain, so the message
// is asserted to be useful rather than merely present.
func TestParsePatternRejects(t *testing.T) {
	cases := []struct {
		pattern string
		phrase  string
	}{
		{"", "empty"},
		{"users", "does not begin with /"},
		{"/a//b", "empty segment"},
		{"//", "empty segment"},
		{"/a/./b", "dot segment"},
		{"/a/../b", "dot segment"},
		{"/a/.", "dot segment"},
		{"/a/..", "dot segment"},
		{"/caf%C3%A9", "percent-encoded"},
		{"/users/:", "empty parameter name"},
		{"/users/:/edit", "empty parameter name"},
		{"/files/*", "empty wildcard name"},
		{"/files/*path/edit", "must be the last"},
		{"/a/:id/b/:id", "twice"},
		{"/a/:x1/:x2/:x3/:x4/:x5/:x6/:x7/:x8/:x9", "at most"},
	}

	for _, c := range cases {
		_, err := parsePattern(c.pattern)
		if err == nil {
			t.Errorf("parsePattern(%q) returned nil error, want a rejection", c.pattern)
			continue
		}
		if !strings.Contains(err.Error(), c.phrase) {
			t.Errorf("parsePattern(%q) error %q does not mention %q", c.pattern, err, c.phrase)
		}
		if !strings.Contains(err.Error(), c.pattern) && c.pattern != "" {
			t.Errorf("parsePattern(%q) error %q does not name the offending pattern", c.pattern, err)
		}
	}
}

// TestParsePatternSuggestsTheDecodedForm checks the one error that has to do
// work to be useful: telling the author what to write instead.
func TestParsePatternSuggestsTheDecodedForm(t *testing.T) {
	_, err := parsePattern("/caf%C3%A9")
	if err == nil {
		t.Fatal("parsePattern accepted a percent-encoded pattern")
	}
	if !strings.Contains(err.Error(), "/café") {
		t.Errorf("error %q should suggest the decoded form /café", err)
	}
}

// TestParsePatternAllowsABarePercent guards against over-rejecting: a literal
// percent sign that is not an escape sequence is a legitimate path character.
func TestParsePatternAllowsABarePercent(t *testing.T) {
	if _, err := parsePattern("/discount/100%"); err != nil {
		t.Errorf("parsePattern rejected a bare percent sign: %v", err)
	}
}

func TestParsePatternAllowsExactlyMaxParams(t *testing.T) {
	pattern := ""
	for i := 0; i < MaxParams; i++ {
		pattern += "/a/:p" + string(rune('0'+i))
	}

	segs, err := parsePattern(pattern)
	if err != nil {
		t.Fatalf("parsePattern(%q) returned %v, want nil for exactly MaxParams parameters", pattern, err)
	}

	params := 0
	for _, s := range segs {
		if s.kind == segParam {
			params++
		}
	}
	if params != MaxParams {
		t.Errorf("parsed %d parameters, want %d", params, MaxParams)
	}
}

// TestParsePatternCountsAWildcardTowardTheLimit records the rule: a wildcard
// occupies a Params slot exactly as a named parameter does.
func TestParsePatternCountsAWildcardTowardTheLimit(t *testing.T) {
	pattern := "/a/:p0/:p1/:p2/:p3/:p4/:p5/:p6/:p7/*rest"

	if _, err := parsePattern(pattern); err == nil {
		t.Error("parsePattern accepted MaxParams parameters plus a wildcard, want a rejection")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/router/ -run TestParsePattern -v`

Expected: FAIL to compile, with `undefined: parsePattern` and `undefined: segStatic`.

- [ ] **Step 3: Write the implementation**

Create `internal/router/pattern.go`:

```go
package router

import (
	"fmt"
	"net/url"
	"strings"
)

// segKind distinguishes the three things a pattern can contain.
type segKind uint8

const (
	// segStatic is literal text, including any slashes around it.
	segStatic segKind = iota
	// segParam is a ":name" placeholder matching one path segment.
	segParam
	// segWildcard is a "*name" catch-all matching the remainder of the path.
	segWildcard
)

// segment is one piece of a parsed pattern. For segStatic, text is the literal
// run including slashes; for the other two it is the bare name, without ':' or
// '*'.
type segment struct {
	kind segKind
	text string
}

// parsePattern splits a route pattern into segments, rejecting anything
// malformed, ambiguous, or unable to match.
//
// The last category is the subtle one. fasthttp collapses empty segments,
// resolves dot segments and percent-decodes the path before rice sees it, so
// matching happens in normalised decoded space. A pattern given in any other
// form would register cleanly and then never match a single request. Rejecting
// it here puts the failure at the registration that caused it, which is the same
// reasoning that makes a duplicate route fatal. See the M3 design doc, D8.
func parsePattern(pattern string) ([]segment, error) {
	if pattern == "" {
		return nil, fmt.Errorf("route path is empty")
	}
	if pattern[0] != '/' {
		return nil, fmt.Errorf("route path %s does not begin with /", pattern)
	}
	if err := checkNormalised(pattern); err != nil {
		return nil, err
	}

	var (
		segs  []segment
		names []string
		start int
	)

	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c != ':' && c != '*' {
			continue
		}

		// Flush the static run preceding this marker. It is never empty: a
		// pattern begins with '/', and checkNormalised has already rejected an
		// empty segment, so a marker is always preceded by at least a slash.
		segs = append(segs, segment{segStatic, pattern[start:i]})

		name, next := scanName(pattern, i+1)

		if c == ':' {
			if name == "" {
				return nil, fmt.Errorf("route path %s has an empty parameter name", pattern)
			}
			segs = append(segs, segment{segParam, name})
		} else {
			if name == "" {
				return nil, fmt.Errorf("route path %s has an empty wildcard name", pattern)
			}
			if next != len(pattern) {
				return nil, fmt.Errorf("route path %s has a wildcard that is not at the end; a wildcard must be the last segment", pattern)
			}
			segs = append(segs, segment{segWildcard, name})
		}

		for _, prior := range names {
			if prior == name {
				return nil, fmt.Errorf("route path %s declares the name %s twice; each parameter name may appear only once", pattern, name)
			}
		}
		names = append(names, name)

		start = next
		i = next - 1
	}

	if start < len(pattern) {
		segs = append(segs, segment{segStatic, pattern[start:]})
	}

	if len(names) > MaxParams {
		return nil, fmt.Errorf("route path %s declares %d parameters; at most %d are supported", pattern, len(names), MaxParams)
	}

	return segs, nil
}

// scanName reads a parameter or wildcard name starting at i, stopping at the
// next '/' or the end of the pattern. It returns the name and the index just
// past it.
func scanName(pattern string, i int) (string, int) {
	j := i
	for j < len(pattern) && pattern[j] != '/' {
		j++
	}
	return pattern[i:j], j
}

// checkNormalised rejects patterns that fasthttp's own normalisation would
// prevent from ever matching.
func checkNormalised(pattern string) error {
	if strings.Contains(pattern, "//") {
		return fmt.Errorf("route path %s has an empty segment; register %s", pattern, collapseSlashes(pattern))
	}
	for _, seg := range strings.Split(pattern, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("route path %s has a dot segment; register the resolved path", pattern)
		}
	}
	if hasPercentEscape(pattern) {
		if decoded, err := url.PathUnescape(pattern); err == nil && decoded != pattern {
			return fmt.Errorf("route path %s is percent-encoded; register the decoded form %s", pattern, decoded)
		}
		return fmt.Errorf("route path %s is percent-encoded; register the path in decoded form", pattern)
	}
	return nil
}

// hasPercentEscape reports whether s contains a complete percent escape. A bare
// '%' is a legitimate path character and is left alone.
func hasPercentEscape(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == '%' && isHex(s[i+1]) && isHex(s[i+2]) {
			return true
		}
	}
	return false
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// collapseSlashes squeezes runs of '/' down to one, so the error message can
// show the caller what to write instead.
func collapseSlashes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '/' && i+1 < len(s) && s[i+1] == '/' {
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/router/ -run TestParsePattern -v`

Expected: PASS for all six tests.

Two things to check if `TestParsePatternRejects` fails on a specific row. The
duplicate-name check runs after the segment is appended, so a duplicate is
reported even when it is the last segment. The `MaxParams` check runs at the end,
after every name is known, which is why a pattern with nine parameters is
rejected rather than truncated.

- [ ] **Step 5: Run the whole package and lint**

Run: `go test ./internal/router/ && go build ./... && make lint`

Expected: all exit 0. Task 1's tests still pass unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/router/pattern.go internal/router/pattern_test.go
git commit -m "feat: parse and validate route patterns"
```

---
### Task 3: The tree — insertion with splitting, lookup with backtracking

This is the hardest code in the project, in ADR-0004's own words. It lands as an
unexported `tree[H]` tested entirely inside `internal/router`, so nothing outside
the package changes and the algorithm gets reviewed without integration churn
alongside it. Task 4 swaps it in.

**Files:**
- Create: `internal/router/tree.go`
- Test: `internal/router/tree_test.go`

**Interfaces:**
- Consumes: `Params`, `add`, `truncate` from Task 1; `segment`, `parsePattern`, `segStatic`, `segParam`, `segWildcard` from Task 2.
- Produces (all unexported, used by Task 4):
  - `type node[H any] struct{ ... }`
  - `type tree[H any] struct{ ... }` — zero value ready to use
  - `func (t *tree[H]) insert(pattern string, segs []segment, h H) error`
  - `func (t *tree[H]) lookup(path []byte, params *Params) (H, bool)`
  - `func (t *tree[H]) len() int`

- [ ] **Step 1: Write the failing tests**

Create `internal/router/tree_test.go`:

```go
package router

import (
	"errors"
	"strings"
	"testing"
)

// insert is a test helper that parses and inserts in one step, failing the test
// on a pattern the parser rejects — the parser has its own tests.
func mustInsert(t *testing.T, tr *tree[string], pattern, handler string) {
	t.Helper()
	segs, err := parsePattern(pattern)
	if err != nil {
		t.Fatalf("parsePattern(%q) returned %v", pattern, err)
	}
	if err := tr.insert(pattern, segs, handler); err != nil {
		t.Fatalf("insert(%q) returned %v, want nil", pattern, err)
	}
}

// tryInsert parses and inserts, returning the insert error for tests that expect one.
func tryInsert(t *testing.T, tr *tree[string], pattern, handler string) error {
	t.Helper()
	segs, err := parsePattern(pattern)
	if err != nil {
		t.Fatalf("parsePattern(%q) returned %v", pattern, err)
	}
	return tr.insert(pattern, segs, handler)
}

// find looks up path and returns the handler, whether it matched, and the
// captured parameters as a "k=v,k=v" string for compact assertions.
func find(tr *tree[string], path string) (string, bool, string) {
	var p Params
	h, ok := tr.lookup([]byte(path), &p)

	var b strings.Builder
	for i := 0; i < p.Len(); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(p.At(i).Key)
		b.WriteByte('=')
		b.Write(p.At(i).Value)
	}
	return h, ok, b.String()
}

func TestLookupOnAnEmptyTree(t *testing.T) {
	var tr tree[string]

	if _, ok, _ := find(&tr, "/anything"); ok {
		t.Error("lookup matched in an empty tree")
	}
}

func TestStaticRoutes(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/", "root")
	mustInsert(t, &tr, "/users", "users")
	mustInsert(t, &tr, "/users/new", "new")
	mustInsert(t, &tr, "/health", "health")

	cases := []struct {
		path string
		want string
	}{
		{"/", "root"},
		{"/users", "users"},
		{"/users/new", "new"},
		{"/health", "health"},
	}
	for _, c := range cases {
		h, ok, _ := find(&tr, c.path)
		if !ok {
			t.Errorf("lookup(%q) did not match", c.path)
			continue
		}
		if h != c.want {
			t.Errorf("lookup(%q) = %q, want %q", c.path, h, c.want)
		}
	}

	for _, path := range []string{"/user", "/userss", "/users/", "/nope", ""} {
		if _, ok, _ := find(&tr, path); ok {
			t.Errorf("lookup(%q) matched, want a miss", path)
		}
	}
}

// TestPrefixSplitting exercises a split at each interesting position. The tree
// starts with one long prefix and every subsequent insert forces a split of it.
func TestPrefixSplitting(t *testing.T) {
	var tr tree[string]

	mustInsert(t, &tr, "/abcdef", "full")
	mustInsert(t, &tr, "/abc", "short")   // split at an interior byte
	mustInsert(t, &tr, "/abcxyz", "sib")  // split again below the first split
	mustInsert(t, &tr, "/a", "shortest")  // split near the first byte
	mustInsert(t, &tr, "/b", "other")     // no common prefix beyond "/"

	cases := map[string]string{
		"/abcdef":  "full",
		"/abc":     "short",
		"/abcxyz":  "sib",
		"/a":       "shortest",
		"/b":       "other",
	}
	for path, want := range cases {
		h, ok, _ := find(&tr, path)
		if !ok {
			t.Errorf("lookup(%q) did not match after splitting", path)
			continue
		}
		if h != want {
			t.Errorf("lookup(%q) = %q, want %q", path, h, want)
		}
	}

	for _, path := range []string{"/ab", "/abcd", "/abcx", "/c"} {
		if _, ok, _ := find(&tr, path); ok {
			t.Errorf("lookup(%q) matched, want a miss — splitting must not invent handlers", path)
		}
	}
}

// TestInsertionOrderDoesNotMatter is the strongest single guard on the splitting
// logic: the same route set inserted in different orders must behave identically.
func TestInsertionOrderDoesNotMatter(t *testing.T) {
	routes := []string{"/a", "/abc", "/abcdef", "/abcxyz", "/b", "/users/:id", "/users/new"}
	orders := [][]int{
		{0, 1, 2, 3, 4, 5, 6},
		{6, 5, 4, 3, 2, 1, 0},
		{2, 0, 4, 6, 1, 5, 3},
		{5, 6, 2, 1, 3, 0, 4},
	}

	probes := []string{"/a", "/abc", "/abcdef", "/abcxyz", "/b", "/users/42", "/users/new", "/ab", "/users"}

	var want []string
	for oi, order := range orders {
		var tr tree[string]
		for _, i := range order {
			mustInsert(t, &tr, routes[i], routes[i])
		}

		var got []string
		for _, p := range probes {
			h, ok, params := find(&tr, p)
			got = append(got, h+"|"+params+"|"+boolString(ok))
		}

		if oi == 0 {
			want = got
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("order %d: probe %q gave %q, order 0 gave %q", oi, probes[i], got[i], want[i])
			}
		}
	}
}

func boolString(b bool) string {
	if b {
		return "hit"
	}
	return "miss"
}

func TestParameterCapture(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/users/:id", "user")
	mustInsert(t, &tr, "/users/:id/posts/:pid", "post")
	mustInsert(t, &tr, "/users/:id/edit", "edit")

	cases := []struct {
		path   string
		want   string
		params string
	}{
		{"/users/42", "user", "id=42"},
		{"/users/42/edit", "edit", "id=42"},
		{"/users/42/posts/7", "post", "id=42,pid=7"},
	}
	for _, c := range cases {
		h, ok, params := find(&tr, c.path)
		if !ok {
			t.Errorf("lookup(%q) did not match", c.path)
			continue
		}
		if h != c.want {
			t.Errorf("lookup(%q) = %q, want %q", c.path, h, c.want)
		}
		if params != c.params {
			t.Errorf("lookup(%q) captured %q, want %q", c.path, params, c.params)
		}
	}

	// A parameter matches exactly one segment and cannot be empty.
	for _, path := range []string{"/users/", "/users", "/users/42/posts", "/users/42/posts/"} {
		if _, ok, _ := find(&tr, path); ok {
			t.Errorf("lookup(%q) matched, want a miss", path)
		}
	}
}

func TestStaticBeatsParameter(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/users/:id", "param")
	mustInsert(t, &tr, "/users/new", "static")

	if h, _, _ := find(&tr, "/users/new"); h != "static" {
		t.Errorf("lookup(/users/new) = %q, want the static route to win", h)
	}
	h, _, params := find(&tr, "/users/42")
	if h != "param" || params != "id=42" {
		t.Errorf("lookup(/users/42) = %q with %q, want param with id=42", h, params)
	}
}

// TestLookupBacktracksFromAPartialStaticMatch is the mandatory test from the
// design doc, D2. The static branch matches "new", strands "x", and lookup must
// unwind to try the parameter child. httprouter carried this bug and Gin
// inherited it.
func TestLookupBacktracksFromAPartialStaticMatch(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/users/new", "static")
	mustInsert(t, &tr, "/users/:id", "param")

	h, ok, params := find(&tr, "/users/newx")
	if !ok {
		t.Fatal("lookup(/users/newx) did not match; lookup must backtrack from the static branch to the parameter child")
	}
	if h != "param" {
		t.Errorf("lookup(/users/newx) = %q, want %q", h, "param")
	}
	if params != "id=newx" {
		t.Errorf("lookup(/users/newx) captured %q, want %q", params, "id=newx")
	}
}

// TestBacktrackingUnwindsCapturedParameters guards the other half of
// backtracking: a parameter captured on a branch that then fails must not leak
// into the successful match.
func TestBacktrackingUnwindsCapturedParameters(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/a/:first/b", "deep")
	mustInsert(t, &tr, "/a/:only", "shallow")

	// /a/x matches the shallow route. The deep route's :first would capture "x"
	// on the way to a "/b" that is not there, and that capture must be rolled
	// back before the shallow route captures :only.
	h, ok, params := find(&tr, "/a/x")
	if !ok {
		t.Fatal("lookup(/a/x) did not match")
	}
	if h != "shallow" {
		t.Errorf("lookup(/a/x) = %q, want %q", h, "shallow")
	}
	if params != "only=x" {
		t.Errorf("lookup(/a/x) captured %q, want exactly %q — a failed branch leaked a capture", params, "only=x")
	}
}

func TestWildcardCapturesTheRemainder(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/files/*path", "files")

	cases := []struct {
		path   string
		params string
	}{
		{"/files/a", "path=a"},
		{"/files/a/b", "path=a/b"},
		{"/files/a/b/c.txt", "path=a/b/c.txt"},
	}
	for _, c := range cases {
		h, ok, params := find(&tr, c.path)
		if !ok {
			t.Errorf("lookup(%q) did not match", c.path)
			continue
		}
		if h != "files" {
			t.Errorf("lookup(%q) = %q, want %q", c.path, h, "files")
		}
		if params != c.params {
			t.Errorf("lookup(%q) captured %q, want %q", c.path, params, c.params)
		}
	}

	// A wildcard requires at least one byte, so the bare prefix does not match.
	for _, path := range []string{"/files/", "/files"} {
		if _, ok, _ := find(&tr, path); ok {
			t.Errorf("lookup(%q) matched; a wildcard must capture at least one byte", path)
		}
	}
}

func TestPriorityIsStaticThenParameterThenWildcard(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/f/*rest", "wildcard")
	mustInsert(t, &tr, "/f/:name", "param")
	mustInsert(t, &tr, "/f/exact", "static")

	cases := map[string]string{
		"/f/exact": "static",
		"/f/other": "param",
		"/f/a/b":   "wildcard",
	}
	for path, want := range cases {
		h, ok, _ := find(&tr, path)
		if !ok {
			t.Errorf("lookup(%q) did not match", path)
			continue
		}
		if h != want {
			t.Errorf("lookup(%q) = %q, want %q", path, h, want)
		}
	}
}

func TestStaticBeatsWildcardAtDepth(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/files/*path", "wildcard")
	mustInsert(t, &tr, "/files/a/b", "static")

	if h, _, _ := find(&tr, "/files/a/b"); h != "static" {
		t.Errorf("lookup(/files/a/b) = %q, want the static route to win over the wildcard", h)
	}
	if h, _, _ := find(&tr, "/files/a/c"); h != "wildcard" {
		t.Errorf("lookup(/files/a/c) = %q, want the wildcard", h)
	}
}

func TestInsertRejectsADuplicate(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/users", "first")

	err := tryInsert(t, &tr, "/users", "second")
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second insert returned %v, want ErrDuplicate", err)
	}
	if h, _, _ := find(&tr, "/users"); h != "first" {
		t.Errorf("a rejected duplicate replaced the original: %q", h)
	}
}

func TestInsertRejectsAConflictingParameterName(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/users/:id", "byID")

	err := tryInsert(t, &tr, "/users/:name", "byName")
	if err == nil {
		t.Fatal("insert accepted a second parameter name at the same position, want a rejection")
	}
	for _, want := range []string{"id", "name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name both parameters; missing %q", err, want)
		}
	}
}

func TestInsertRejectsAConflictingWildcardName(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/files/*path", "byPath")

	if err := tryInsert(t, &tr, "/files/*rest", "byRest"); err == nil {
		t.Fatal("insert accepted a second wildcard name at the same position, want a rejection")
	}
}

// TestTheSameParameterNameAtDifferentPositionsIsFine guards against
// over-rejecting: two routes reusing a name at different depths are unrelated.
func TestTheSameParameterNameAtDifferentPositionsIsFine(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/users/:id", "user")
	mustInsert(t, &tr, "/posts/:id", "post")

	if h, _, params := find(&tr, "/users/1"); h != "user" || params != "id=1" {
		t.Errorf("lookup(/users/1) = %q with %q", h, params)
	}
	if h, _, params := find(&tr, "/posts/2"); h != "post" || params != "id=2" {
		t.Errorf("lookup(/posts/2) = %q with %q", h, params)
	}
}

func TestLenCountsRegisteredRoutes(t *testing.T) {
	var tr tree[string]

	if got := tr.len(); got != 0 {
		t.Errorf("len() = %d on an empty tree, want 0", got)
	}

	mustInsert(t, &tr, "/a", "a")
	mustInsert(t, &tr, "/a/b", "ab")
	mustInsert(t, &tr, "/a/:id", "param")
	if got := tr.len(); got != 3 {
		t.Errorf("len() = %d, want 3", got)
	}

	_ = tryInsert(t, &tr, "/a", "duplicate")
	if got := tr.len(); got != 3 {
		t.Errorf("len() = %d after a rejected duplicate, want 3", got)
	}
}

// TestDeepNesting shows recursion depth follows the path, not the tree's size.
func TestDeepNesting(t *testing.T) {
	var tr tree[string]

	pattern := ""
	path := ""
	for i := 0; i < 50; i++ {
		pattern += "/seg"
		path += "/seg"
	}
	mustInsert(t, &tr, pattern, "deep")

	if h, ok, _ := find(&tr, path); !ok || h != "deep" {
		t.Errorf("lookup on a 50-segment path = %q, ok=%v", h, ok)
	}
}

func TestLookupDoesNotRetainThePathSlice(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/aaa", "a")
	mustInsert(t, &tr, "/bbb", "b")

	buf := []byte("/aaa")
	if h, _, _ := find(&tr, string(buf)); h != "a" {
		t.Fatalf("first lookup = %q, want %q", h, "a")
	}

	copy(buf, "/bbb")

	var p Params
	h, ok := tr.lookup(buf, &p)
	if !ok || h != "b" {
		t.Errorf("after overwriting the buffer, lookup = %q ok=%v, want b true", h, ok)
	}
}

// TestTreeWorksWithAFuncType pins the shape rice instantiates. Handler is a func
// type, and func types are not comparable, so an implementation that compared
// two handlers would fail to build here.
func TestTreeWorksWithAFuncType(t *testing.T) {
	type handler func() string

	var tr tree[handler]
	segs, err := parsePattern("/x")
	if err != nil {
		t.Fatalf("parsePattern returned %v", err)
	}
	if err := tr.insert("/x", segs, func() string { return "called" }); err != nil {
		t.Fatalf("insert returned %v", err)
	}

	var p Params
	h, ok := tr.lookup([]byte("/x"), &p)
	if !ok {
		t.Fatal("lookup did not find the handler")
	}
	if got := h(); got != "called" {
		t.Errorf("handler returned %q, want %q", got, "called")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/router/ -run 'TestStatic|TestPrefix|TestInsertion|TestParameter|TestLookupBacktracks|TestBacktracking|TestWildcard|TestPriority|TestInsertRejects|TestTheSame|TestLen|TestDeep|TestLookupDoesNot|TestTreeWorks|TestLookupOnAnEmpty' -v`

Expected: FAIL to compile, with `undefined: tree`.

- [ ] **Step 3: Write the tree**

Create `internal/router/tree.go`:

```go
package router

import "fmt"

// node is one node of the radix tree.
//
// A node owns a compressed static prefix and at most one parameter child and one
// wildcard child. Single pointers rather than slices are what make conflict
// detection simple: a second parameter name at the same position has nowhere to
// go, so it is rejected. That is forced rather than chosen — a request for
// /users/42 could not say whether to bind :id or :name.
//
// Static children have unique first bytes, which is an invariant of insertStatic
// and the reason lookup never has to try more than one of them.
type node[H any] struct {
	prefix     string
	handler    H
	hasHandler bool

	static   []*node[H]
	param    *node[H]
	wildcard *node[H]
	name     string // parameter or wildcard name; empty on static nodes
}

// tree is a radix tree mapping patterns to handlers.
//
// The zero value is ready to use; the root is created on first insert.
type tree[H any] struct {
	root *node[H]
	n    int
}

func (t *tree[H]) len() int { return t.n }

// insert adds h at the parsed pattern.
//
// pattern is carried only for error messages; segs is what drives the walk.
func (t *tree[H]) insert(pattern string, segs []segment, h H) error {
	if t.root == nil {
		t.root = &node[H]{}
	}

	cur := t.root
	for _, seg := range segs {
		switch seg.kind {
		case segStatic:
			cur = cur.insertStatic(seg.text)

		case segParam:
			if cur.param == nil {
				cur.param = &node[H]{name: seg.text}
			} else if cur.param.name != seg.text {
				return fmt.Errorf(
					"router: %s declares parameter :%s where :%s is already registered at the same position",
					pattern, seg.text, cur.param.name)
			}
			cur = cur.param

		case segWildcard:
			if cur.wildcard == nil {
				cur.wildcard = &node[H]{name: seg.text}
			} else if cur.wildcard.name != seg.text {
				return fmt.Errorf(
					"router: %s declares wildcard *%s where *%s is already registered at the same position",
					pattern, seg.text, cur.wildcard.name)
			}
			cur = cur.wildcard
		}
	}

	if cur.hasHandler {
		return ErrDuplicate
	}
	cur.handler = h
	cur.hasHandler = true
	t.n++
	return nil
}

// insertStatic descends into, or creates, the chain of static nodes spelling
// text, splitting an existing node wherever it shares only a prefix with it.
func (n *node[H]) insertStatic(text string) *node[H] {
	cur := n
	for len(text) > 0 {
		child := cur.staticChild(text[0])
		if child == nil {
			leaf := &node[H]{prefix: text}
			cur.static = append(cur.static, leaf)
			return leaf
		}

		cp := commonPrefixLen(child.prefix, text)

		if cp < len(child.prefix) {
			// The child shares only part of its prefix with text. Split it: the
			// child keeps the shared head, and a new node takes its tail along
			// with everything hanging off it.
			var zero H
			tail := &node[H]{
				prefix:     child.prefix[cp:],
				handler:    child.handler,
				hasHandler: child.hasHandler,
				static:     child.static,
				param:      child.param,
				wildcard:   child.wildcard,
			}
			child.prefix = child.prefix[:cp]
			child.handler = zero
			child.hasHandler = false
			child.static = []*node[H]{tail}
			child.param = nil
			child.wildcard = nil
		}

		text = text[cp:]
		cur = child
	}
	return cur
}

// staticChild returns the static child whose prefix begins with b.
//
// At most one can, because insertStatic only ever creates a new child when no
// existing one shares a first byte, and splitting preserves the first byte on
// the node that keeps the shared head.
func (n *node[H]) staticChild(b byte) *node[H] {
	for _, c := range n.static {
		if c.prefix[0] == b {
			return c
		}
	}
	return nil
}

func commonPrefixLen(a, b string) int {
	max := len(a)
	if len(b) < max {
		max = len(b)
	}
	i := 0
	for i < max && a[i] == b[i] {
		i++
	}
	return i
}

func (t *tree[H]) lookup(path []byte, params *Params) (H, bool) {
	if t.root == nil {
		var zero H
		return zero, false
	}
	return t.root.lookup(path, params)
}

// lookup walks the children of n against path, filling params.
//
// It tries the static child, then the parameter child, then the wildcard, which
// is the total priority ADR-0004 fixes. Each attempt that fails falls through to
// the next, and a failed attempt deeper in the tree returns here to do the same —
// that unwinding is the whole point. Without it, /users/newx would 404 while
// /users/:id sits registered and willing: the static branch consumes "new",
// strands "x", and has nowhere to go. See the M3 design doc, D2.
//
// Recursion costs no allocation — stack frames are not heap — and its depth is
// bounded by the path, not by the size of the tree.
func (n *node[H]) lookup(path []byte, params *Params) (H, bool) {
	var zero H

	if len(path) == 0 {
		return zero, false
	}

	// Static: at most one child can share the first byte.
	if child := n.staticChild(path[0]); child != nil {
		if len(path) >= len(child.prefix) && string(path[:len(child.prefix)]) == child.prefix {
			rest := path[len(child.prefix):]
			if len(rest) == 0 {
				if child.hasHandler {
					return child.handler, true
				}
			} else if h, ok := child.lookup(rest, params); ok {
				return h, true
			}
		}
	}

	// Parameter: consume one segment, which must be non-empty.
	if n.param != nil {
		i := 0
		for i < len(path) && path[i] != '/' {
			i++
		}
		if i > 0 {
			saved := params.Len()
			if params.add(n.param.name, path[:i]) {
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
		}
	}

	// Wildcard: consume the remainder, which must be non-empty.
	if n.wildcard != nil {
		saved := params.Len()
		if params.add(n.wildcard.name, path) {
			if n.wildcard.hasHandler {
				return n.wildcard.handler, true
			}
			params.truncate(saved)
		}
	}

	return zero, false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/router/ -v`

Expected: PASS for every test in the package, Tasks 1 and 2 included.

If `TestLookupBacktracksFromAPartialStaticMatch` fails, the static branch is
returning failure instead of falling through — check that the static block does
not `return` on a failed match. If `TestBacktrackingUnwindsCapturedParameters`
fails, `params.truncate(saved)` is missing or placed where a successful match
also reaches it.

- [ ] **Step 5: Verify the allocation claim before writing any comment about it**

The doc comment on `lookup` states that recursion costs no allocation. Verify it
rather than trusting it — this milestone's constraints require any allocation
claim to be measured:

```bash
cat > /tmp/alloc_probe_test.go <<'EOF'
package router

import "testing"

func TestProbeLookupAllocations(t *testing.T) {
	var tr tree[string]
	for _, p := range []string{"/users/new", "/users/:id", "/users/:id/posts/:pid", "/files/*path"} {
		segs, err := parsePattern(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := tr.insert(p, segs, p); err != nil {
			t.Fatal(err)
		}
	}

	var p Params
	path := []byte("/users/42/posts/7")

	tr.lookup(path, &p) // warm

	got := testing.AllocsPerRun(1000, func() {
		p.Reset()
		tr.lookup(path, &p)
	})
	if got != 0 {
		t.Errorf("lookup allocated %.1f objects per call, want 0", got)
	}
	t.Logf("lookup allocations per call: %.1f", got)
}
EOF
cp /tmp/alloc_probe_test.go internal/router/zz_probe_test.go
go test ./internal/router/ -run TestProbeLookupAllocations -v
rm internal/router/zz_probe_test.go
```

Expected: PASS, reporting 0. This is a throwaway probe — Task 6 adds the
permanent budget tests. If it reports more than 0, find the allocation before
continuing; the tree is the one place in this milestone where a stray allocation
would be invisible to every behavioural test.

- [ ] **Step 6: Run the whole suite and lint**

Run: `make test && make lint`

Expected: both exit 0. `tree` is unexported and used only by its own tests at
this point; nothing outside `internal/router` has changed.

- [ ] **Step 7: Commit**

```bash
git add internal/router/tree.go internal/router/tree_test.go
git commit -m "feat: add radix tree with prefix splitting and backtracking lookup"
```

---

### Task 4: Swap the tree in and thread Params through rice

`Tree[H]` stops being a map and starts delegating to `tree[H]`, and `Lookup` gains
a `*Params`. That signature change breaks `route.go` and both test files that call
it, so all of it lands together.

`Params` does **not** reach `Ctx` in this task. `handle` passes a stack-local, so
M2's lookup-before-allocate ordering survives one more task and this task's diff
stays about the router. Task 5 moves it onto `Ctx`.

**Files:**
- Modify: `internal/router/router.go` (the map goes; `Tree` delegates; `Insert` parses)
- Modify: `internal/router/router_test.go` (M2's tests, updated for the new signature)
- Modify: `route.go` (path validation moves into `parsePattern`; `lookup` threads `*Params`)
- Modify: `route_test.go` (call sites, plus parameter routing tests)
- Modify: `app.go` (`handle` passes a stack-local `Params`)

**Interfaces:**
- Consumes: everything from Tasks 1 to 3.
- Produces:
  - `func (t *Tree[H]) Insert(pattern string, h H) error` — now parses and validates
  - `func (t *Tree[H]) Lookup(path []byte, params *Params) (H, bool)`
  - `func (t *Tree[H]) Len() int`
  - `func (a *App) lookup(method, path []byte, params *router.Params) (Handler, bool)`

- [ ] **Step 1: Rewrite Tree as a wrapper**

Replace the body of `internal/router/router.go`. Keep the package doc comment at
the top, updating the sentence that says M2 backs it with a map:

```go
// Package router maps request paths to handlers.
//
// It is generic over the handler type because docs/02-architecture.md forbids
// anything under internal/ from importing package rice, so the router cannot
// name rice.Handler.
//
// Tree is a radix tree: it supports static segments, ":name" parameters and a
// trailing "*name" catch-all, and resolves overlaps by the total priority fixed
// in ADR-0004 — static, then parameter, then wildcard. Lookup fills a
// caller-supplied *Params so that parameter storage can live on a pooled Ctx,
// which is what keeps capture free of allocations.
package router

import "errors"

// ErrDuplicate is returned by Insert when a pattern is already registered.
var ErrDuplicate = errors.New("router: duplicate route")

// Tree maps route patterns to handlers.
//
// The zero value is ready to use.
type Tree[H any] struct {
	t tree[H]
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
	return t.t.insert(pattern, segs, h)
}

// Lookup returns the handler registered for path, filling params with whatever
// the matched route captured.
//
// The path is borrowed: Lookup does not retain it, so the caller may reuse the
// backing array as soon as the call returns. Captured parameter values are views
// into that same array and share its lifetime.
//
// params is filled only on a match. On a miss it is left as the caller passed it.
func (t *Tree[H]) Lookup(path []byte, params *Params) (H, bool) {
	return t.t.lookup(path, params)
}

// Len returns the number of registered routes.
func (t *Tree[H]) Len() int { return t.t.len() }
```

- [ ] **Step 2: Update M2's router tests for the new signature**

`internal/router/router_test.go` holds M2's map tests. They exercise `Tree`, which
still exists, so keep them and make three changes:

1. Every `tr.Lookup(path)` becomes `tr.Lookup(path, &p)` with `var p Params`
   declared in the test.
2. `TestLookupDoesNotRetainThePathSlice` stays — it is still a real property.
   Task 3 has its own version at the `tree` level; both are worth keeping,
   because this one asserts it through the exported API a user reaches.
3. Add one test proving `Insert` now rejects what `parsePattern` rejects, so the
   wiring between them is covered at this level and not only inside the parser:

```go
func TestInsertRejectsAnUnmatchablePattern(t *testing.T) {
	var tr Tree[string]

	if err := tr.Insert("/a//b", "h"); err == nil {
		t.Error("Insert accepted a pattern with an empty segment, want a rejection")
	}
	if got := tr.Len(); got != 0 {
		t.Errorf("Len() = %d after a rejected insert, want 0", got)
	}
}
```

- [ ] **Step 3: Move path validation out of Handle**

In `route.go`, `Handle` currently checks the path itself. `parsePattern` now owns
every path rule, so delete the duplicated checks and keep only what is about the
method and the handler. Replace `Handle` with:

```go
// Handle registers h for the given method and path.
//
// All routes must be registered before serving begins. The route trees are
// mutated here without synchronisation and read on every request, so registering
// a route after Run or Serve is a data race, not merely a late change.
//
// It panics on a programmer error: an empty or lowercase method, a nil handler,
// a pattern that is malformed or could never match a request, or a route already
// registered for the same method and path. These are mistakes discovered at
// startup rather than runtime conditions, and a duplicate in particular means one
// of the two handlers can never run — silent, and expensive to debug. Panicking
// at the call site puts the mistake in the stack trace.
//
// M4 appends a variadic mw ...Middleware parameter. Doing so does not break
// existing calls.
func (a *App) Handle(method, path string, h Handler) {
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

	if err := a.treeFor(method).Insert(path, h); err != nil {
		if errors.Is(err, router.ErrDuplicate) {
			panic(fmt.Sprintf("rice: %v: %s %s", err, method, path))
		}
		// parsePattern's errors already name the offending pattern and say what
		// to write instead, so they are surfaced verbatim.
		panic("rice: " + err.Error())
	}
}
```

`route.go` needs `"errors"` added to its imports. Keep `hasLowercaseByte` exactly
as it is.

- [ ] **Step 4: Thread Params through App.lookup**

Replace `App.lookup` in `route.go`:

```go
// lookup finds the handler for a request, filling params with whatever the
// matched route captured.
//
// This is the hot path. Both method and path stay borrowed byte slices
// throughout; params is storage the caller already owns, which is why a match
// costs no allocation. alloc_test.go pins that down.
func (a *App) lookup(method, path []byte, params *router.Params) (Handler, bool) {
	if i, ok := methodIndex(method); ok {
		return a.trees[i].Lookup(path, params)
	}

	if a.rare == nil {
		return nil, false
	}
	t, ok := a.rare[string(method)]
	if !ok {
		return nil, false
	}
	return t.Lookup(path, params)
}
```

- [ ] **Step 5: Give handle a stack-local Params**

In `app.go`, `handle` keeps M2's ordering for one more task. Add the local and
pass it:

```go
func (a *App) handle(fctx *fasthttp.RequestCtx) {
	// M3 Task 5 moves this onto the Ctx, which is where it belongs and where the
	// pool in M6 can reuse it. It is a stack local here only so that Task 4's
	// change stays confined to the router.
	var params router.Params

	h, ok := a.lookup(fctx.Method(), fctx.Path(), &params)
	if !ok {
		c := &Ctx{}
		c.reset(a, fctx)
		a.handleError(c, ErrNotFound)
		return
	}

	c := &Ctx{}
	c.reset(a, fctx)

	if err := h(c); err != nil {
		a.handleError(c, err)
	}
}
```

`app.go` needs the `internal/router` import added.

- [ ] **Step 6: Update route_test.go call sites and add parameter routing tests**

Every `app.lookup(...)` call in `route_test.go` gains a third argument. Add
`"github.com/vietpham102301/rice-http/internal/router"` to its imports and declare
a `var p router.Params` in each test that calls `lookup`.

Then append these tests, which cover routing through `App` rather than through the
tree directly:

```go
func TestAppRoutesAParameterisedPattern(t *testing.T) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return nil })

	var p router.Params
	if _, ok := app.lookup([]byte("GET"), []byte("/users/42"), &p); !ok {
		t.Fatal("lookup did not match a parameterised route")
	}
	if got := string(p.Get("id")); got != "42" {
		t.Errorf("captured id = %q, want %q", got, "42")
	}
}

func TestAppRoutesAWildcardPattern(t *testing.T) {
	app := New()
	app.GET("/files/*path", func(c *Ctx) error { return nil })

	var p router.Params
	if _, ok := app.lookup([]byte("GET"), []byte("/files/a/b.txt"), &p); !ok {
		t.Fatal("lookup did not match a wildcard route")
	}
	if got := string(p.Get("path")); got != "a/b.txt" {
		t.Errorf("captured path = %q, want %q", got, "a/b.txt")
	}
}

func TestRegistrationPanicsOnAnUnmatchablePattern(t *testing.T) {
	cases := []string{"/a//b", "/a/./b", "/caf%C3%A9", "/users/:", "/files/*p/edit", "/a/:id/b/:id"}

	for _, pattern := range cases {
		app := New()
		v := mustPanic(t, "GET("+pattern+")", func() {
			app.GET(pattern, func(c *Ctx) error { return nil })
		})
		if msg, _ := v.(string); !strings.Contains(msg, pattern) {
			t.Errorf("panic for %q does not name the pattern: %v", pattern, v)
		}
	}
}
```

- [ ] **Step 7: Run the tests**

Run: `make test`

Expected: PASS. Every M2 test still holds — the map is gone but exact-match
routing behaves identically, which is the point of keeping M2's tests.

If anything reports `not enough arguments in call to`, a `Lookup` or `lookup` call
site was missed. Find them with `grep -rn '\.lookup(\|\.Lookup(' --include='*.go' .`

- [ ] **Step 8: Lint**

Run: `make lint`

Expected: exit 0.

- [ ] **Step 9: Commit**

```bash
git add internal/router route.go route_test.go app.go
git commit -m "feat: replace the map with the radix tree and thread Params through dispatch"
```

---
### Task 5: Params onto the Ctx, and the ordering change it forces

**Files:**
- Modify: `ctx.go` (`Ctx` gains `params router.Params`; `reset` clears it)
- Create: `ctx_param.go`
- Test: `ctx_param_test.go`
- Modify: `app.go` (`handle` allocates the `Ctx` before the lookup)
- Modify: `docs/adr/0005-context-pooling-and-borrow-contract.md`

**Interfaces:**
- Consumes: `router.Params` from Task 1, `App.lookup` from Task 4.
- Produces:
  - `func (c *Ctx) Param(name string) []byte`
  - `func (c *Ctx) ParamString(name string) string`

- [ ] **Step 1: Write the failing tests**

Create `ctx_param_test.go`:

```go
package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// newParamCtx builds a Ctx with parameters already captured, the way a lookup
// would leave it.
func newParamCtx(t *testing.T, pairs ...string) (*Ctx, *fasthttp.RequestCtx) {
	t.Helper()
	if len(pairs)%2 != 0 {
		t.Fatal("newParamCtx needs key/value pairs")
	}

	app := New()
	app.GET("/x", func(c *Ctx) error { return nil })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/x")

	c := &Ctx{}
	c.reset(app, fctx)
	for i := 0; i < len(pairs); i += 2 {
		c.params.Set(pairs[i], []byte(pairs[i+1]))
	}
	return c, fctx
}

func TestParamReturnsTheCapturedValue(t *testing.T) {
	c, _ := newParamCtx(t, "id", "42")

	if got := string(c.Param("id")); got != "42" {
		t.Errorf("Param(\"id\") = %q, want %q", got, "42")
	}
}

func TestParamOnAnAbsentNameIsEmpty(t *testing.T) {
	c, _ := newParamCtx(t, "id", "42")

	if got := c.Param("missing"); len(got) != 0 {
		t.Errorf("Param on an absent name = %q, want empty", got)
	}
}

func TestParamOnARouteWithNoParametersIsEmpty(t *testing.T) {
	c, _ := newParamCtx(t)

	if got := c.Param("id"); len(got) != 0 {
		t.Errorf("Param = %q on a route with no parameters, want empty", got)
	}
}

func TestParamStringReturnsTheCapturedValue(t *testing.T) {
	c, _ := newParamCtx(t, "id", "42")

	if got := c.ParamString("id"); got != "42" {
		t.Errorf("ParamString(\"id\") = %q, want %q", got, "42")
	}
}

func TestParamStringOnAnAbsentNameIsTheEmptyString(t *testing.T) {
	c, _ := newParamCtx(t, "id", "42")

	if got := c.ParamString("missing"); got != "" {
		t.Errorf("ParamString on an absent name = %q, want the empty string", got)
	}
}

// TestParamIsBorrowedAndParamStringIsOwned is the borrow contract stated as a
// test. Param aliases the request buffer; ParamString copies out of it. This is
// the distinction docs/03-core-concepts.md promises and the reason the expensive
// accessor has the longer name.
func TestParamIsBorrowedAndParamStringIsOwned(t *testing.T) {
	backing := []byte("42")

	app := New()
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(app, fctx)
	c.params.Set("id", backing)

	borrowed := c.Param("id")
	owned := c.ParamString("id")

	copy(backing, "99")

	if got := string(borrowed); got != "99" {
		t.Errorf("Param returned %q after the buffer changed; it is expected to alias, not copy", got)
	}
	if owned != "42" {
		t.Errorf("ParamString returned %q after the buffer changed; it is expected to copy", owned)
	}
}

// TestResetClearsParameters matters because M6 reuses a Ctx across requests: a
// parameter from the previous request must not be visible to the next handler.
func TestResetClearsParameters(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}

	c := &Ctx{}
	c.reset(app, fctx)
	c.params.Set("id", []byte("42"))

	c.reset(app, fctx)

	if got := c.Param("id"); len(got) != 0 {
		t.Errorf("Param(\"id\") = %q after reset, want empty", got)
	}
}

func TestDispatchExposesCapturedParametersToTheHandler(t *testing.T) {
	app := New()

	var gotID, gotPID string
	app.GET("/users/:id/posts/:pid", func(c *Ctx) error {
		gotID = c.ParamString("id")
		gotPID = c.ParamString("pid")
		return c.String(200, "ok")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42/posts/7")

	app.handle(fctx)

	if fctx.Response.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", fctx.Response.StatusCode())
	}
	if gotID != "42" {
		t.Errorf("id = %q, want %q", gotID, "42")
	}
	if gotPID != "7" {
		t.Errorf("pid = %q, want %q", gotPID, "7")
	}
}

func TestDispatchExposesAWildcardToTheHandler(t *testing.T) {
	app := New()

	var got string
	app.GET("/files/*path", func(c *Ctx) error {
		got = c.ParamString("path")
		return c.String(200, "ok")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/files/a/b.txt")

	app.handle(fctx)

	if got != "a/b.txt" {
		t.Errorf("path = %q, want %q", got, "a/b.txt")
	}
}
```

Note the tests call `c.params.Set(...)`. `Params.add` is unexported *in package
router*, so package `rice` cannot reach it. Task 1 gave `Params` no exported
setter because nothing needed one; these tests do. Add it in Step 3.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -run 'TestParam|TestReset|TestDispatchExposes' -v`

Expected: FAIL to compile, with `c.params undefined` and `c.Param undefined`.

- [ ] **Step 3: Add an exported setter to Params**

In `internal/router/params.go`, add:

```go
// Set records a captured parameter, replacing any earlier capture of the same
// name. It reports false if storage is full.
//
// The tree fills Params through the unexported add, which is append-only because
// a lookup never revisits a name. Set exists for package rice, which cannot reach
// add across the internal/ boundary, and for tests that need to stage a Ctx
// without running a lookup.
func (p *Params) Set(name string, value []byte) bool {
	for i := 0; i < p.n; i++ {
		if p.slots[i].Key == name {
			p.slots[i].Value = value
			return true
		}
	}
	return p.add(name, value)
}
```

Add a test for it in `internal/router/params_test.go`:

```go
func TestSetReplacesAnExistingName(t *testing.T) {
	var p Params
	p.Set("id", []byte("first"))
	p.Set("id", []byte("second"))

	if got := p.Len(); got != 1 {
		t.Errorf("Len() = %d after setting the same name twice, want 1", got)
	}
	if got := p.Get("id"); !bytes.Equal(got, []byte("second")) {
		t.Errorf("Get(\"id\") = %q, want %q", got, "second")
	}
}

func TestSetReportsFalseWhenFull(t *testing.T) {
	var p Params
	for i := 0; i < MaxParams; i++ {
		p.Set("k"+string(rune('0'+i)), []byte("v"))
	}

	if p.Set("one-too-many", []byte("v")) {
		t.Error("Set accepted a new name past MaxParams, want false")
	}
	if !p.Set("k0", []byte("replaced")) {
		t.Error("Set refused to replace an existing name in a full Params, want true")
	}
}
```

- [ ] **Step 4: Put Params on the Ctx**

In `ctx.go`, add the import and the field, and clear it in `reset`:

```go
import (
	"github.com/valyala/fasthttp"

	"github.com/vietpham102301/rice-http/internal/router"
)

// Ctx is a borrowed handle to one in-flight request.
//
// It is valid only for the duration of the handler that received it, along with
// every []byte it hands out. See the borrow contract in doc.go.
type Ctx struct {
	fctx *fasthttp.RequestCtx
	app  *App

	// params is held by value, not by pointer, so that capturing route
	// parameters needs no allocation of its own. This is what ADR-0005's
	// Lookup(path, *Params) signature exists to make possible, and it is why the
	// Ctx must be constructed before the lookup runs.
	//
	// It costs size: Params is MaxParams fixed slots, so a Ctx is a few hundred
	// bytes rather than sixteen. M6's pool makes that irrelevant by reusing the
	// same Ctx across requests. Until then it is one larger allocation, not two.
	params router.Params
}

// reset rebinds the context to a new request.
//
// M3 constructs a fresh Ctx per request, so reset is called exactly once per
// instance. M6 introduces a sync.Pool, at which point reset becomes the point
// where a recycled Ctx drops every reference to the previous request — which is
// why it clears params rather than trusting them to be empty.
func (c *Ctx) reset(app *App, fctx *fasthttp.RequestCtx) {
	c.app = app
	c.fctx = fctx
	c.params.Reset()
}
```

- [ ] **Step 5: Add the accessors**

Create `ctx_param.go`:

```go
package rice

// Param returns the value captured for a named route parameter, or an empty
// slice if the route captured no such name.
//
// Borrowed: the returned slice points into fasthttp's request buffer and is
// valid only until the handler returns. Use ParamString to keep it.
func (c *Ctx) Param(name string) []byte { return c.params.Get(name) }

// ParamString returns the value captured for a named route parameter as a
// string, or the empty string if the route captured no such name.
//
// Owned: it copies, costs one allocation, and is safe to keep past the handler.
// The naming rule holds across the whole API — the byte-returning accessor is
// free and borrowed, the string-returning one is the longer name to type because
// it is the expensive choice.
func (c *Ctx) ParamString(name string) string { return string(c.params.Get(name)) }
```

- [ ] **Step 6: Reorder handle, and delete the stack-local**

In `app.go`, replace `handle`. This is spec D4, and the comment has to be honest
about reversing M2's ordering:

```go
// handle is the dispatch path: one request in, one response out.
func (a *App) handle(fctx *fasthttp.RequestCtx) {
	// The Ctx is constructed before the lookup, which reverses the ordering M2
	// chose. It is not a regression walked back: the lookup fills a *Params, and
	// Params lives on the Ctx so that capturing parameters costs no allocation
	// of its own. That is ADR-0005's design, and the price of it is that the
	// borrowed handle must exist before anything can fill it.
	//
	// The consequence is that a miss now pays for a Ctx again, and the
	// zero-allocation 404 path M2 measured is gone. M2 predicted M5's
	// configurable ErrorHandler would end it; M3 ended it first, for a different
	// reason. That is recorded in ADR-0005.
	c := &Ctx{}
	c.reset(a, fctx)

	// M3 allocates a Ctx per request on purpose. This is the baseline M6's
	// sync.Pool is measured against. Do not optimise it here.
	h, ok := a.lookup(fctx.Method(), fctx.Path(), &c.params)
	if !ok {
		a.handleError(c, ErrNotFound)
		return
	}

	if err := h(c); err != nil {
		a.handleError(c, err)
	}
}
```

The `internal/router` import Task 4 added to `app.go` is now unused — remove it,
or `go build` will fail.

- [ ] **Step 7: Run the tests**

Run: `make test`

Expected: PASS, including the two dispatch tests that read parameters inside a
handler.

- [ ] **Step 8: Record the ordering change in ADR-0005**

Append to the Consequences section of
`docs/adr/0005-context-pooling-and-borrow-contract.md`. This is an addition, not a
rewrite — ADRs are append-only, and M2's spec set the form for a dated correction:

```markdown
**Recorded in M3.** M2 measured the 404 path at zero allocations, because escape
analysis kept the miss-path `Ctx` on the stack, and M2's retrospective predicted
that M5's configurable `ErrorHandler` would eventually take that zero away. M3
took it away first, for a different reason: `Lookup` fills a `*Params`, `Params`
lives on the `Ctx`, so the `Ctx` must be constructed before the lookup and now
escapes on both paths. The prediction that the zero would not last was correct;
the mechanism named for it was not the one that arrived. This is the clearest
available argument for the rule M2 adopted — measure a zero, document why it
holds, and do not pin it with a test unless the design guarantees it.
```

- [ ] **Step 9: Verify the escape claim you just wrote down**

The ADR text above asserts the `Ctx` now escapes on both paths. This milestone's
constraints forbid asserting compiler behaviour from memory — M2 shipped two wrong
claims that way. Measure it:

```bash
go build -gcflags='-m' ./... 2>&1 | grep -n 'app.go.*&Ctx{}'
```

Expected: one line, reporting `escapes to heap`. There is now a single `&Ctx{}` in
`handle`, so one line is the whole answer. If it reports `does not escape`, the
ADR paragraph is wrong — fix the paragraph, not the code.

- [ ] **Step 10: Lint and commit**

```bash
make lint
git add ctx.go ctx_param.go ctx_param_test.go app.go internal/router/params.go internal/router/params_test.go docs/adr/0005-context-pooling-and-borrow-contract.md
git commit -m "feat: expose route parameters through Ctx.Param and Ctx.ParamString"
```

---

### Task 6: Allocation budgets, integration, and the performance model

**Files:**
- Modify: `alloc_test.go`
- Modify: `server_test.go`
- Modify: `docs/05-performance-model.md`

**Interfaces:**
- Consumes: everything from Tasks 1 to 5.
- Produces: the enforced budgets M3's exit criteria require.

- [ ] **Step 1: Write the allocation budget tests**

Append to `alloc_test.go`. It already has the `budget(t, name, want, fn)` helper
and imports `strconv` and `fasthttp`; add
`"github.com/vietpham102301/rice-http/internal/router"`.

Each test asserts its precondition before measuring, following the pattern M2
established — a mistyped path must fail loudly rather than quietly turn a hit
budget into a passing miss budget.

```go
func TestAllocBudgetLookupOneParameter(t *testing.T) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/users/42")

	var p router.Params
	if _, ok := app.lookup(method, path, &p); !ok {
		t.Fatal("route not registered; the budget below would be measuring the miss path")
	}
	if got := string(p.Get("id")); got != "42" {
		t.Fatalf("captured id = %q, want 42; the budget below would not be measuring capture", got)
	}

	budget(t, "App.lookup with one parameter", 0, func() {
		p.Reset()
		_, _ = app.lookup(method, path, &p)
	})
}

func TestAllocBudgetLookupFiveParameters(t *testing.T) {
	app := New()
	app.GET("/a/:p1/b/:p2/c/:p3/d/:p4/e/:p5", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/a/1/b/2/c/3/d/4/e/5")

	var p router.Params
	if _, ok := app.lookup(method, path, &p); !ok {
		t.Fatal("route not registered")
	}
	if p.Len() != 5 {
		t.Fatalf("captured %d parameters, want 5", p.Len())
	}

	budget(t, "App.lookup with five parameters", 0, func() {
		p.Reset()
		_, _ = app.lookup(method, path, &p)
	})
}

func TestAllocBudgetLookupWildcard(t *testing.T) {
	app := New()
	app.GET("/files/*path", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/files/a/b/c.txt")

	var p router.Params
	if _, ok := app.lookup(method, path, &p); !ok {
		t.Fatal("route not registered")
	}

	budget(t, "App.lookup with a wildcard", 0, func() {
		p.Reset()
		_, _ = app.lookup(method, path, &p)
	})
}

// TestAllocBudgetLookupBacktrack measures the worst case the design admits: a
// static branch that matches, strands a byte, and forces an unwind to the
// parameter child. It must still allocate nothing.
func TestAllocBudgetLookupBacktrack(t *testing.T) {
	app := New()
	app.GET("/users/new", func(c *Ctx) error { return nil })
	app.GET("/users/:id", func(c *Ctx) error { return nil })

	method := []byte("GET")
	path := []byte("/users/newx")

	var p router.Params
	if _, ok := app.lookup(method, path, &p); !ok {
		t.Fatal("the backtracking route did not match; this budget would measure a miss")
	}
	if got := string(p.Get("id")); got != "newx" {
		t.Fatalf("captured id = %q, want newx", got)
	}

	budget(t, "App.lookup with backtracking", 0, func() {
		p.Reset()
		_, _ = app.lookup(method, path, &p)
	})
}

func TestAllocBudgetCtxParam(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}

	c := &Ctx{}
	c.reset(app, fctx)
	c.params.Set("id", []byte("42"))

	if got := string(c.Param("id")); got != "42" {
		t.Fatalf("Param = %q, want 42", got)
	}

	budget(t, "Ctx.Param", 0, func() {
		_ = c.Param("id")
	})
}

func TestAllocBudgetCtxParamString(t *testing.T) {
	app := New()
	fctx := &fasthttp.RequestCtx{}

	c := &Ctx{}
	c.reset(app, fctx)
	c.params.Set("id", []byte("42"))

	if got := c.ParamString("id"); got != "42" {
		t.Fatalf("ParamString = %q, want 42", got)
	}

	// One allocation, on purpose: this accessor copies out of the request buffer
	// so the value outlives the handler. docs/03-core-concepts.md budgets it at 1.
	budget(t, "Ctx.ParamString", 1, func() {
		_ = c.ParamString("id")
	})
}

// TestAllocBudgetHandleDispatchParameterised keeps the end-to-end promise honest
// for a parameterised route: still exactly one allocation, the Ctx.
func TestAllocBudgetHandleDispatchParameterised(t *testing.T) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return c.String(200, "ok") })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")

	app.handle(fctx)
	if fctx.Response.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200; this budget would be measuring the 404 path", fctx.Response.StatusCode())
	}

	budget(t, "App.handle on a parameterised route", 1, func() {
		app.handle(fctx)
	})
}
```

- [ ] **Step 2: Run the budget tests**

Run: `go test . -run TestAllocBudget -v`

Expected: PASS for every budget test, M2's included.

If a lookup budget fails, do not raise it — raising a budget is a design change
this task is not authorised to make. Find the allocation:

```bash
go test -run TestAllocBudgetLookupOneParameter -memprofile mem.out .
go tool pprof -top -alloc_objects mem.out
```

The first suspects are a `string(...)` conversion that escapes inside the lookup
path, and `params.add` storing something it should be aliasing.

- [ ] **Step 3: Add the integration tests**

Append to `server_test.go`:

```go
func TestServeAnswersAParameterisedRoute(t *testing.T) {
	app := rice.New()
	app.GET("/users/:id", func(c *rice.Ctx) error {
		return c.String(200, "user "+c.ParamString("id"))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	status, body := get(t, addr, "/users/42")
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if body != "user 42" {
		t.Errorf("body = %q, want %q", body, "user 42")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}

func TestServeAnswersAWildcardRoute(t *testing.T) {
	app := rice.New()
	app.GET("/files/*path", func(c *rice.Ctx) error {
		return c.String(200, "file "+c.ParamString("path"))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	status, body := get(t, addr, "/files/a/b/c.txt")
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if body != "file a/b/c.txt" {
		t.Errorf("body = %q, want %q", body, "file a/b/c.txt")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}

// TestServeMatchesAPercentEncodedRequestAgainstADecodedPattern is the request-side
// half of the rule Task 2 enforces at registration: fasthttp decodes the path
// before rice sees it, so the decoded pattern is the one that matches.
func TestServeMatchesAPercentEncodedRequestAgainstADecodedPattern(t *testing.T) {
	app := rice.New()
	app.GET("/café", func(c *rice.Ctx) error { return c.String(200, "decoded") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	status, body := get(t, addr, "/caf%C3%A9")
	if status != 200 || body != "decoded" {
		t.Errorf("got %d %q, want 200 %q", status, body, "decoded")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}
```

- [ ] **Step 4: Run the full suite with the race detector**

Run: `make test`

Expected: PASS.

- [ ] **Step 5: Update the performance model**

`docs/05-performance-model.md` currently has this row, bundling three accessors:

```markdown
| `c.Param`, `c.Query`, `c.Header` | 0 | TARGET (M3) |
```

**Split it rather than relabelling it.** M3 delivers `c.Param` only; marking the
row MEASURED M3 would assert that `c.Query` and `c.Header` are measured when
neither exists yet. Replace that single row with two:

```markdown
| `c.Param` | 0 | MEASURED M3 |
| `c.Query`, `c.Header` | 0 | TARGET (unscheduled) |
```

Then change these two rows:

```markdown
| Router lookup, 1 parameter | 0 | MEASURED M3 |
| `c.ParamString` | 1 | MEASURED M3 |
```

Leave `Router lookup, 5 parameters` on `TARGET (M6, needs slot sizing)`. M3
measures five parameters and the budget holds, but the row is about sizing storage
from the observed maximum, which is M6's work and not what M3 built.

Finally, the sentence under the table names only M1's results file. Update it so a
reader can find both:

```markdown
Measured values come from the results files in `bench/results/`, most recently
`bench/results/M3-radix-tree-router.txt`. Re-run `make bench-record` on your own
machine before comparing.
```

- [ ] **Step 6: Note the unscheduled accessors**

`c.Query` and `c.Header` are committed in `docs/03-core-concepts.md` and budgeted
in the performance model, but no milestone in `docs/04-roadmap.md` claims them.
Add them to the "Explicitly deferred" list at the bottom of the roadmap so the gap
is recorded rather than rediscovered:

```markdown
- `c.Query` and `c.Header`, the remaining borrowed read accessors. Committed in
  [03-core-concepts.md](03-core-concepts.md) and budgeted in
  [05-performance-model.md](05-performance-model.md), but owned by no milestone.
  Noticed while writing M3.
```

- [ ] **Step 7: Verify**

```bash
make lint
make test
make bench
```

Expected: all exit 0.

- [ ] **Step 8: Commit**

```bash
git add alloc_test.go server_test.go docs/05-performance-model.md docs/04-roadmap.md
git commit -m "test: enforce M3 allocation budgets and route parameters over a socket"
```

---

### Task 7: Benchmarks, and the frozen map

**Files:**
- Create: `bench/mapbaseline_test.go`
- Modify: `bench/router_bench_test.go`
- Create: `bench/results/M3-radix-tree-router.txt` (generated, then committed)

**Interfaces:**
- Consumes: the router through rice's exported API.
- Produces: the recorded numbers Task 8 writes into the retrospective.

- [ ] **Step 1: Freeze M2's map inside the benchmark package**

`bench/results/README.md` says numbers from different stamps are not comparable
and the older label must be re-run on the current machine. Deleting M2's map would
make that impossible for every milestone after this one, so a copy lives here.

Create `bench/mapbaseline_test.go`:

```go
package bench

// M2's exact-match map router, frozen for comparison.
//
// This is a deliberate duplicate of code that no longer exists in rice. Its
// entire value is that it does not change: every recording from M3 onward can
// measure the tree against the map in the same binary, on the same machine, in
// the same session, which is what bench/results/README.md requires and what
// deleting the map would have made impossible.
//
// Do not "fix" it, extend it, or make it support parameters. It is a
// measurement instrument, not a component.

type mapTree struct {
	routes map[string]int
}

func (t *mapTree) insert(path string, h int) {
	if t.routes == nil {
		t.routes = make(map[string]int)
	}
	t.routes[path] = h
}

// lookup keeps M2's one-line map probe verbatim. The single-expression form is
// the one the compiler documents its map-index conversion optimisation for.
func (t *mapTree) lookup(path []byte) (int, bool) {
	h, ok := t.routes[string(path)]
	return h, ok
}
```

- [ ] **Step 2: Write the comparison benchmarks**

Rewrite `bench/router_bench_test.go`. It currently holds M2's
`BenchmarkStaticRouterLookup{10,100,1000}`, `BenchmarkStaticRouterMiss` and
`BenchmarkStaticRouterWrongVerb`, which measured full dispatch through
`FasthttpHandler`. Keep all five unchanged — they are the continuity between
milestones — and add the map-versus-tree pair plus the parameter cases:

```go
package bench

import (
	"strconv"
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
)

// staticPaths builds n distinct static route paths and the one to probe, taken
// from the middle of the set so a lookup is not measuring a best or worst case.
func staticPaths(n int) ([]string, string) {
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		paths[i] = "/route/" + strconv.Itoa(i)
	}
	return paths, "/route/" + strconv.Itoa(n/2)
}

// benchmarkMapLookup measures the frozen M2 map on nothing but the probe, with no
// framework around it. Paired with benchmarkTreeLookup below, which does the same
// for the tree, this is the comparison M3 exists to produce.
func benchmarkMapLookup(b *testing.B, n int) {
	paths, probe := staticPaths(n)

	var t mapTree
	for i, p := range paths {
		t.insert(p, i)
	}

	path := []byte(probe)
	if _, ok := t.lookup(path); !ok {
		b.Fatal("probe path is not registered; this would measure a miss")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.lookup(path)
	}
}

func BenchmarkMapLookup10(b *testing.B)   { benchmarkMapLookup(b, 10) }
func BenchmarkMapLookup100(b *testing.B)  { benchmarkMapLookup(b, 100) }
func BenchmarkMapLookup1000(b *testing.B) { benchmarkMapLookup(b, 1000) }

// benchmarkTreeLookup measures the radix tree on the same route set as
// benchmarkMapLookup, through rice's dispatch path, since the tree is not
// reachable from this package any other way.
//
// The two are therefore not measuring identical work: the tree figure includes a
// Ctx allocation and a response write that the map figure does not. Read the map
// numbers against each other and the tree numbers against each other for scaling,
// and read the pair only as "does route count matter" — not as a bare ratio.
func benchmarkTreeLookup(b *testing.B, n int) {
	paths, probe := staticPaths(n)

	app := rice.New()
	for _, p := range paths {
		app.GET(p, func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "ok") })
	}

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", probe)
	h(fctx)
	if fctx.Response.StatusCode() != fasthttp.StatusOK {
		b.Fatalf("probe returned %d; this would measure the 404 path", fctx.Response.StatusCode())
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

func BenchmarkTreeLookup10(b *testing.B)   { benchmarkTreeLookup(b, 10) }
func BenchmarkTreeLookup100(b *testing.B)  { benchmarkTreeLookup(b, 100) }
func BenchmarkTreeLookup1000(b *testing.B) { benchmarkTreeLookup(b, 1000) }

// benchmarkPattern dispatches one request against one registered pattern.
func benchmarkPattern(b *testing.B, pattern, path string) {
	app := rice.New()
	app.GET(pattern, func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "ok") })

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", path)
	h(fctx)
	if fctx.Response.StatusCode() != fasthttp.StatusOK {
		b.Fatalf("%s did not match %s; this would measure the 404 path", pattern, path)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkTreeLookup1Param measures the case the map cannot express at all.
func BenchmarkTreeLookup1Param(b *testing.B) {
	benchmarkPattern(b, "/users/:id", "/users/42")
}

func BenchmarkTreeLookup5Params(b *testing.B) {
	benchmarkPattern(b, "/a/:p1/b/:p2/c/:p3/d/:p4/e/:p5", "/a/1/b/2/c/3/d/4/e/5")
}

func BenchmarkTreeLookupWildcard(b *testing.B) {
	benchmarkPattern(b, "/files/*path", "/files/a/b/c.txt")
}

// BenchmarkTreeLookupBacktrack measures the worst case the design admits: the
// static branch matches, strands a byte, and lookup unwinds to the parameter
// child. A worst case nobody measured is a worst case nobody knows.
func BenchmarkTreeLookupBacktrack(b *testing.B) {
	app := rice.New()
	app.GET("/users/new", func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "static") })
	app.GET("/users/:id", func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "param") })

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/users/newx")
	h(fctx)
	if got := string(fctx.Response.Body()); got != "param" {
		b.Fatalf("body = %q, want param; the backtracking path is not being measured", got)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkTreeMiss measures the 404 path with 1000 routes registered.
func BenchmarkTreeMiss(b *testing.B) {
	paths, _ := staticPaths(1000)

	app := rice.New()
	for _, p := range paths {
		app.GET(p, func(c *rice.Ctx) error { return c.String(fasthttp.StatusOK, "ok") })
	}

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/not-registered")
	h(fctx)
	if fctx.Response.StatusCode() != fasthttp.StatusNotFound {
		b.Fatalf("probe returned %d, want 404", fctx.Response.StatusCode())
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}
```

M2's five `BenchmarkStaticRouter*` functions are superseded by the `Tree*` set
above, which measures the same dispatch path under clearer names. Delete them so
the results file does not carry two names for one measurement — and say so in the
commit message, because a disappearing benchmark name is otherwise a mystery to
whoever diffs the results files.

- [ ] **Step 3: Run the benchmarks and read the numbers**

```bash
go test ./bench/... -run '^$' -bench . -benchmem -count=1
```

Expected: `MapLookup*` at 0 allocs/op, every `Tree*` and the M1-era rice
benchmarks at 1 alloc/op (the `Ctx`), and `TreeMiss` now at 1 rather than M2's 0 —
that change is expected and Task 5's ADR note explains it.

The `MapLookup10/100/1000` figures should be flat, reproducing M2's finding. The
`TreeLookup10/100/1000` figures are the answer to the milestone's question. Note
the `Ctx` byte figure: it is now a few hundred bytes rather than sixteen, because
`Params` lives inline.

If any benchmark reports an unexpected allocation count, stop and find it before
recording. A recorded number nobody can explain is worse than no number.

- [ ] **Step 4: Record the results**

```bash
make bench-record LABEL=M3-radix-tree-router
```

This runs every benchmark ten times and will take several minutes. Let it finish.

- [ ] **Step 5: Verify**

```bash
make lint
make test
make bench
git status --short
```

Expected: the three make targets exit 0 and only the new results file is untracked.

- [ ] **Step 6: Commit**

```bash
git add bench
git commit -m "bench: compare the radix tree against M2's frozen map in one run"
```

---

### Task 8: Close out M3

**Files:**
- Create: `docs/adr/0007-no-trailing-slash-or-case-insensitive-matching.md`
- Modify: `docs/adr/0004-radix-tree-router.md` (its open question is now answered)
- Modify: `docs/adr/README.md` (index the new ADR)
- Create: `docs/milestones/M3-radix-tree-router.md`
- Modify: `docs/04-roadmap.md` (M3 status marker)
- Modify: `docs/progress.md` (prepend an entry)

**Interfaces:**
- Consumes: the recorded numbers from Task 7.
- Produces: nothing other tasks depend on.

- [ ] **Step 1: Write the follow-up ADR**

ADR-0004 ends with: *"Open question for M3: whether to support trailing-slash
redirection and case-insensitive fallback matching. Both are conveniences that add
branches to the lookup path. Decide with a measurement, and record the outcome
here as a follow-up ADR."*

Create `docs/adr/0007-no-trailing-slash-or-case-insensitive-matching.md`, following
the shape of the existing ADRs in that directory — read two of them first, and
match their sections exactly.

The decision is to support neither. The reasoning must be honest about one thing:
ADR-0004 asked for a measurement, and the decision is not being made on a
measurement. Both features sit entirely on the miss path and neither would touch a
matching request, so the cost argument ADR-0004 anticipated turned out not to
apply. The decision rests on scope instead — prefix splitting, parameter capture,
wildcards, backtracking and conflict detection are, in ADR-0004's own words, the
hardest code in the project, and redirect semantics bring their own questions
(301 versus 308, and whether a redirect fires for a request carrying a body)
without contributing to the measurement M3 existed to produce.

Record that both remain available as opt-in features, and that the `Option`
mechanism for them arrives in M7.

- [ ] **Step 2: Close the open question in ADR-0004**

Append to `docs/adr/0004-radix-tree-router.md`. ADRs are append-only, so this
answers the question rather than editing it away:

```markdown
**Answered in M3:** neither is supported. See
[ADR-0007](0007-no-trailing-slash-or-case-insensitive-matching.md). The
measurement this ADR asked for turned out not to be the deciding factor: both
features sit on the miss path and neither touches a matching request, so the cost
concern anticipated here did not apply. The decision was made on scope instead.
```

- [ ] **Step 3: Index the new ADR**

Add ADR-0007 to the table or list in `docs/adr/README.md`, matching the existing
rows exactly.

- [ ] **Step 4: Write the milestone retrospective**

Create `docs/milestones/M3-radix-tree-router.md` from
`docs/milestones/TEMPLATE.md`, filling in every section.

Read `docs/milestones/M2-static-router.md` first and match its voice and its level
of candour. That document states its costs plainly and admits an unexplained
number rather than glossing it; this one is held to the same standard.

The Measurements table takes the figures from
`bench/results/M3-radix-tree-router.txt`, with the hardware line copied from that
file's stamp header. Include the map and the tree side by side, since that pair is
the milestone's output.

Four things the retrospective must address directly:

1. **Did the tree beat the map?** The design doc predicted it would not, on static
   routes, and predicted the consequence: that a tree earns its keep on parameters
   and shared prefixes rather than on static lookup. State plainly what the numbers
   say. If the prediction held, say so; do not rewrite it to look more impressive,
   and do not soften what it implies.

2. **The `Ctx` grew from 16 bytes to a few hundred**, because `Params` is inline.
   Give the measured figure. Allocations stayed at one; bytes did not.

3. **The zero-allocation 404 path is gone**, and M3 ended it rather than M5 as
   predicted. This is the milestone's cleanest evidence for a rule the project
   adopted in M2 — measure a zero, document why it holds, do not pin it with a test
   unless the design guarantees it. Say that.

4. **M2 left roughly nine nanoseconds of routing cost unexplained.** M3 replaced
   the lookup path entirely, so that figure is not carried over — but the habit of
   recording an unexplained number is now two milestones old. Either account for
   M3's own cost or state explicitly that it was not accounted for. Do not let it
   pass in silence a third time.

- [ ] **Step 5: Mark M3 done in the roadmap**

In `docs/04-roadmap.md`, change the M3 heading marker from `☐` to `☑`:

```
### ☑ M3 — Radix tree router
```

- [ ] **Step 6: Prepend a journal entry**

In `docs/progress.md`, insert a new M3 entry at the top of the entry list, above
the M2 entry, using the four-field shape: Did, Learned, Measured, Next. The
journal is append-only; do not edit any existing entry.

The Measured field carries the map-versus-tree comparison and the parameter
figures. The Next field points at M4 and says in one sentence what M4 has to prove.

Watch out for a shell quoting hazard this project has hit before: writing
apostrophes inside a single-quoted heredoc or awk program can silently emit
backticks instead. Prefer a quoted heredoc (`<<'EOF'`) and check the result for
stray backticks where apostrophes belong.

- [ ] **Step 7: Verify the links**

```bash
grep -n "M3" docs/04-roadmap.md docs/progress.md docs/milestones/M3-radix-tree-router.md
grep -rn "0007" docs/adr/
```

Expected: the roadmap shows `☑ M3`; the journal and milestone doc both reference
`bench/results/M3-radix-tree-router.txt`; ADR-0004 and `docs/adr/README.md` both
link ADR-0007.

- [ ] **Step 8: Final verification**

```bash
make lint
make test
make cover
make bench
git status --short
```

Expected: lint, test and bench exit 0, and `git status --short` is empty. Confirm
by eye that the roadmap shows `☑` for M0 through M3 and `☐` for M4 through M8.

- [ ] **Step 9: Commit**

```bash
git add docs
git commit -m "docs: close out M3 with retrospective, the map comparison, and ADR-0007"
```

---

## Plan Self-Review

**Spec coverage:**

| Spec item | Task |
| --- | --- |
| D1 one parameter and one wildcard child per node; static children by first byte | 3 |
| D2 backtracking lookup, with the mandatory `/users/newx` test | 3 (behaviour), 6 (allocation budget) |
| D3 `Params` fixed inline array of eight, `MaxParams`, registration-time rejection | 1 (storage), 2 (rejection) |
| D3 the `Ctx` size cost, stated with a measured figure | 5 (the change), 8 (the figure) |
| D4 `Ctx` allocated before the lookup; the 404 zero ends | 5, recorded in ADR-0005 |
| D5 insert splits on the longest common prefix | 3 |
| D6 every rejection and every accepted overlap | 2 (pattern rejections), 3 (tree conflicts and overlaps), 4 (through `App`) |
| D7 no trailing-slash redirect, no case-insensitive fallback, recorded as an ADR | 8 |
| D8 patterns must be given in `fctx.Path()` form | 2 (rejection), 6 (the request-side half) |
| D9 M2's map frozen in `bench/` | 7 |
| Public API `Param` and `ParamString` | 5 |
| Budget rows: lookup with a parameter, `c.Param`, `c.ParamString` | 6 |
| The bundled accessor row must be split, not relabelled | 6 |
| `c.Query`/`c.Header` belong to no milestone — record it | 6 |
| Benchmarks including map-versus-tree and the backtracking worst case | 7 |
| Retrospective, roadmap marker, journal entry | 8 |

Every spec item maps to a task. The spec's three open questions are answered by
M3's own benchmarks (map versus tree), deferred with a recorded reason (the
`indices` layout, to M8), or made a required section of the retrospective (the
unexplained cost).

**Placeholder scan:** no `TBD`, no `TODO`, no "add appropriate error handling", no
"similar to Task N". Every code step carries its code. Task 8's three writing steps
give the required content and the standard to meet rather than the prose itself,
which is deliberate: a retrospective written from a template is the one thing
`docs/milestones/TEMPLATE.md` explicitly forbids.

**Type consistency:** `Params`, `Param`, `MaxParams`, `Reset`, `Get`, `Len`, `At`,
`add`, `truncate` and `Set` are used identically in Tasks 1, 3, 4, 5 and 6.
`segment`, `segKind`, `segStatic`, `segParam`, `segWildcard` and `parsePattern`
are consistent between Tasks 2, 3 and 4. `tree[H]`, `node[H]`, `insert`, `lookup`
and `len` are unexported and used only inside `internal/router` in Tasks 3 and 4.
`Tree[H].Insert(pattern string, h H) error`, `Tree[H].Lookup(path []byte, params *Params) (H, bool)`
and `Tree[H].Len() int` are defined in Task 4 and used in Tasks 4 and 6.
`App.lookup(method, path []byte, params *router.Params) (Handler, bool)` is defined
in Task 4 and used in Tasks 4, 5 and 6. `Ctx.Param` and `Ctx.ParamString` are
defined in Task 5 and used in Tasks 5, 6 and 7. `budget(t, name, want, fn)` is
reused from M1's `alloc_test.go`. `mustPanic` and `hasLowercaseByte` are reused
from M2.

**Compilation-unit check:** Task 1 adds one new file to `internal/router`. Task 2
adds another. Task 3 adds a third, all unexported and used only by its own tests —
nothing outside the package changes. Task 4 is where `Tree.Lookup`'s signature
changes, and it updates every call site in the same commit, because nothing
compiles in between. Task 5 moves `Params` onto `Ctx` and deletes Task 4's
deliberate stack-local scaffolding. Tasks 6, 7 and 8 add tests, benchmarks and
documents. Each task ends with `go build ./...` succeeding.

**One deliberate piece of throwaway work:** Task 4's stack-local `Params` in
`handle` exists for exactly one task and Task 5 removes it. The alternative was
merging Tasks 4 and 5, which would have put the hardest integration change and the
public API addition in one reviewer's lap. Two lines of scaffolding, carrying a
comment that names the task which removes them, is the cheaper trade.
