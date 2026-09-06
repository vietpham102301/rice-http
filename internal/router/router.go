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
// That is the exact form the Go compiler documents this optimisation for: an
// inline string(path) used directly as a map key does not copy the bytes.
// Measured on go1.25.6, hoisting the conversion into a variable —
// key := string(path) — does not currently cost an allocation either; the
// optimisation survives a single-use local. The one-line form is still the
// right one to write, because it is the form the guarantee is documented
// against and it cannot quietly become an escaping conversion later, the way
// a local can if the code around it grows. alloc_test.go's lookup budget
// tests keep this path pinned at zero allocations, which still catches a real
// regression — it just would not have caught this particular rewrite.
func (t *Tree[H]) Lookup(path []byte) (H, bool) {
	h, ok := t.routes[string(path)]
	return h, ok
}

// Len returns the number of registered routes.
func (t *Tree[H]) Len() int { return len(t.routes) }
