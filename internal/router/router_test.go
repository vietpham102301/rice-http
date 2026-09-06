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
