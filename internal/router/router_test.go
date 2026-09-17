package router

import (
	"errors"
	"strconv"
	"testing"
)

func TestInsertAndLookup(t *testing.T) {
	var tr Tree[string]
	var p Params

	if err := tr.Insert("/users", "handler-a"); err != nil {
		t.Fatalf("Insert returned %v, want nil", err)
	}

	got, ok := tr.Lookup([]byte("/users"), &p)
	if !ok {
		t.Fatal("Lookup did not find a route that was just inserted")
	}
	if got != "handler-a" {
		t.Errorf("Lookup returned %q, want %q", got, "handler-a")
	}
}

func TestLookupMissReturnsTheZeroValue(t *testing.T) {
	var tr Tree[string]
	var p Params
	if err := tr.Insert("/users", "handler-a"); err != nil {
		t.Fatalf("Insert returned %v, want nil", err)
	}

	got, ok := tr.Lookup([]byte("/absent"), &p)
	if ok {
		t.Error("Lookup found a route that was never inserted")
	}
	if got != "" {
		t.Errorf("Lookup returned %q on a miss, want the zero value", got)
	}
}

// TestLookupOnAnEmptyTree exercises the nil-root read path. A Tree is usable
// as its zero value, so the root node does not exist until the first Insert.
func TestLookupOnAnEmptyTree(t *testing.T) {
	var tr Tree[string]
	var p Params

	if _, ok := tr.Lookup([]byte("/anything"), &p); ok {
		t.Error("Lookup found a route in an empty tree")
	}
}

func TestInsertRejectsADuplicate(t *testing.T) {
	var tr Tree[string]
	var p Params
	if err := tr.Insert("/users", "first"); err != nil {
		t.Fatalf("first Insert returned %v, want nil", err)
	}

	err := tr.Insert("/users", "second")
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Insert returned %v, want ErrDuplicate", err)
	}

	got, _ := tr.Lookup([]byte("/users"), &p)
	if got != "first" {
		t.Errorf("a rejected duplicate overwrote the original: got %q, want %q", got, "first")
	}
}

func TestInsertRejectsAnUnmatchablePattern(t *testing.T) {
	var tr Tree[string]

	if err := tr.Insert("/a//b", "h"); err == nil {
		t.Error("Insert accepted a pattern with an empty segment, want a rejection")
	}
	if got := tr.Len(); got != 0 {
		t.Errorf("Len() = %d after a rejected insert, want 0", got)
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
	var p Params
	_ = tr.Insert("/aaa", "route-a")
	_ = tr.Insert("/bbb", "route-b")

	buf := []byte("/aaa")

	if got, _ := tr.Lookup(buf, &p); got != "route-a" {
		t.Fatalf("first lookup returned %q, want %q", got, "route-a")
	}

	copy(buf, "/bbb") // overwrite in place, as fasthttp would

	if got, _ := tr.Lookup(buf, &p); got != "route-b" {
		t.Errorf("after overwriting the buffer, lookup returned %q, want %q", got, "route-b")
	}
	if got, _ := tr.Lookup([]byte("/aaa"), &p); got != "route-a" {
		t.Errorf("overwriting a caller buffer corrupted the tree: /aaa returned %q", got)
	}
}

// TestTreeWorksWithAFuncType pins the shape rice actually instantiates.
// Handler is a func type, and func types are not comparable, so an
// implementation that ever compared two handlers would fail to build here.
func TestTreeWorksWithAFuncType(t *testing.T) {
	type handler func() string

	var tr Tree[handler]
	var p Params
	if err := tr.Insert("/x", func() string { return "called" }); err != nil {
		t.Fatalf("Insert returned %v, want nil", err)
	}

	h, ok := tr.Lookup([]byte("/x"), &p)
	if !ok {
		t.Fatal("Lookup did not find the handler")
	}
	if got := h(); got != "called" {
		t.Errorf("handler returned %q, want %q", got, "called")
	}
}

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
