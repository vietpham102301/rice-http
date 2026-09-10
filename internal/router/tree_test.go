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

func TestTreeLookupOnAnEmptyTree(t *testing.T) {
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
	mustInsert(t, &tr, "/abc", "short")  // split at an interior byte
	mustInsert(t, &tr, "/abcxyz", "sib") // split again below the first split
	mustInsert(t, &tr, "/a", "shortest") // split near the first byte
	mustInsert(t, &tr, "/b", "other")    // no common prefix beyond "/"

	cases := map[string]string{
		"/abcdef": "full",
		"/abc":    "short",
		"/abcxyz": "sib",
		"/a":      "shortest",
		"/b":      "other",
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
// into the match that eventually succeeds.
//
// The two routes put a parameter child and a wildcard child on the same node,
// which is legal — they are separate pointers with separate names — unlike two
// parameter names at one position, which the conflict rule rejects.
func TestBacktrackingUnwindsCapturedParameters(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/a/:id/b", "deep")
	mustInsert(t, &tr, "/a/*rest", "wildcard")

	// /a/x/c: the parameter child captures id=x and descends looking for "/b",
	// meets "/c" instead, and fails. That capture must be rolled back before the
	// wildcard captures the remainder, or the successful match carries a
	// parameter no route asked for.
	h, ok, params := find(&tr, "/a/x/c")
	if !ok {
		t.Fatal("lookup(/a/x/c) did not match")
	}
	if h != "wildcard" {
		t.Errorf("lookup(/a/x/c) = %q, want %q", h, "wildcard")
	}
	if params != "rest=x/c" {
		t.Errorf("lookup(/a/x/c) captured %q, want exactly %q — a failed branch leaked its capture", params, "rest=x/c")
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

func TestTreeInsertRejectsADuplicate(t *testing.T) {
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

// TestTreeLookupBorrowsTheCapturedValue is the borrow contract at the tree
// level. ADR-0005 requires a captured parameter to be a view into the caller's
// path, not a copy of it: Ctx.Param hands that view straight to a handler and
// Ctx.ParamString is the accessor that copies. An implementation that defensively
// copied here would be safer and wrong, and would break the zero-allocation
// budget the milestone is built on.
//
// It drives tr.lookup directly. The find helper converts its argument with
// []byte(path), which would hand the tree a fresh copy and make any aliasing
// assertion vacuous — that is exactly how the test this replaces came to be
// unfailable.
func TestTreeLookupBorrowsTheCapturedValue(t *testing.T) {
	var tr tree[string]
	mustInsert(t, &tr, "/users/:id", "user")

	buf := []byte("/users/aaa")

	var p Params
	h, ok := tr.lookup(buf, &p)
	if !ok {
		t.Fatal("lookup did not match")
	}
	if h != "user" {
		t.Fatalf("lookup = %q, want %q", h, "user")
	}
	if got := string(p.Get("id")); got != "aaa" {
		t.Fatalf("captured id = %q, want %q", got, "aaa")
	}

	// Overwrite the tail of the path in place, the way fasthttp reuses its
	// request buffer for the next request on the same connection.
	copy(buf[len("/users/"):], "bbb")

	if got := string(p.Get("id")); got != "bbb" {
		t.Errorf("captured id = %q after the buffer changed, want %q; the value is expected to alias the caller's path, not copy it", got, "bbb")
	}
}

// TestTreeGenericOverAFuncType pins the shape rice instantiates. Handler is a func
// type, and func types are not comparable, so an implementation that compared
// two handlers would fail to build here.
func TestTreeGenericOverAFuncType(t *testing.T) {
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
