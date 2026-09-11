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
//
// Its own message carries no pattern or method, because Insert has no method
// to report and route.go is the caller that has both; route.go builds the
// user-facing message from the path and method it already holds rather than
// from this error's text.
var ErrDuplicate = errors.New("duplicate route")

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
