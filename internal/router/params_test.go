package router

import (
	"bytes"
	"strconv"
	"testing"
)

func TestParamsAddAndGet(t *testing.T) {
	var p Params

	p.add("id", []byte("42"))
	p.add("slug", []byte("hello"))

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
	if got := p.Cap(); got != 5 {
		t.Errorf("Cap() = %d, want 5", got)
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

	backing := p.slots[:cap(p.slots)]
	for i := range backing {
		if backing[i].Key != "" || backing[i].Value != nil {
			t.Errorf("slot %d still holds %+v after Reset, want the zero Param", i, backing[i])
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
	if s := p.slots[:2][1]; s.Key != "" || s.Value != nil {
		t.Errorf("slot 1 still holds %+v after truncate, want the zero Param", s)
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

// TestGetAllocatesNothing backs the claim Get's doc comment makes. A scan over a
// fixed array returning a slice header should not allocate, but this project does
// not take that on faith: an unbacked allocation claim is exactly the defect M2's
// final review caught twice.
func TestGetAllocatesNothing(t *testing.T) {
	var p Params
	p.add("id", []byte("42"))
	p.add("slug", []byte("hello"))

	if got := p.Get("slug"); !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("Get(\"slug\") = %q, want %q; the measurement below would be meaningless", got, "hello")
	}

	// A miss scans every occupied slot, so it is the more expensive direction.
	if got := testing.AllocsPerRun(1000, func() {
		_ = p.Get("absent")
	}); got != 0 {
		t.Errorf("Get on a miss allocated %.1f objects per call, want 0", got)
	}

	if got := testing.AllocsPerRun(1000, func() {
		_ = p.Get("slug")
	}); got != 0 {
		t.Errorf("Get on a hit allocated %.1f objects per call, want 0", got)
	}
}
