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

// Get returns the value captured for name, or nil if there is none.
//
// A linear scan over at most MaxParams entries beats a map decisively at this
// size and allocates nothing. See TestGetAllocatesNothing.
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
