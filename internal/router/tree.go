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
					"route path %s declares parameter :%s where :%s is already registered at the same position",
					pattern, seg.text, cur.param.name)
			}
			cur = cur.param

		case segWildcard:
			if cur.wildcard == nil {
				cur.wildcard = &node[H]{name: seg.text}
			} else if cur.wildcard.name != seg.text {
				return fmt.Errorf(
					"route path %s declares wildcard *%s where *%s is already registered at the same position",
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
	}

	// Wildcard: consume the remainder, which must be non-empty.
	if n.wildcard != nil {
		saved := params.Len()
		params.add(n.wildcard.name, path)
		if n.wildcard.hasHandler {
			return n.wildcard.handler, true
		}
		params.truncate(saved)
	}

	return zero, false
}
