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
