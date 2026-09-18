package router

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
// It lives inside a pooled Ctx by value, so that capture costs no allocation of
// its own. That is the whole reason Lookup takes a *Params instead of returning a
// slice — see ADR-0005.
//
// Storage is a slice rather than a fixed array. Package rice pre-sizes it with
// MakeParams from the largest route registered, so on a served App capture never
// grows it. If a Params is too small — a Ctx built before a larger route was
// registered — add grows it once and the pooled Ctx keeps the larger slice. A
// wrong size therefore costs one allocation, never a missed match. See the M6
// design doc, D3.
//
// The zero value is ready to use.
type Params struct {
	slots []Param
}

// MakeParams returns a Params with room for capacity captures before it grows.
func MakeParams(capacity int) Params {
	return Params{slots: make([]Param, 0, capacity)}
}

// Cap returns how many parameters this Params can hold before it grows.
//
// It exists so package rice can verify that pooled contexts are pre-sized; see
// the M6 design doc, D3.
func (p *Params) Cap() int { return cap(p.slots) }

// Reset discards every captured parameter.
//
// It is exported because package rice calls it from Ctx.reset, across the
// internal/ package boundary where an unexported method would be unreachable.
func (p *Params) Reset() { p.truncate(0) }

// truncate rolls capture back to n entries.
//
// The discarded slots are zeroed rather than merely sliced away: a stale Value
// in the backing array keeps fasthttp's request buffer reachable, and a pooled
// Params outlives the request that filled it.
func (p *Params) truncate(n int) {
	clear(p.slots[n:])
	p.slots = p.slots[:n]
}

// add appends a captured parameter.
//
// The slice is stored, not copied: Value is a view into the request path.
func (p *Params) add(key string, value []byte) {
	p.slots = append(p.slots, Param{Key: key, Value: value})
}

// Set records a captured parameter, replacing any earlier capture of the same
// name.
//
// The tree fills Params through the unexported add, which is append-only because
// a lookup never revisits a name. Set exists for package rice, which cannot reach
// add across the internal/ boundary, and for tests that need to stage a Ctx
// without running a lookup.
func (p *Params) Set(name string, value []byte) {
	for i := range p.slots {
		if p.slots[i].Key == name {
			p.slots[i].Value = value
			return
		}
	}
	p.add(name, value)
}

// Get returns the value captured for name, or nil if there is none.
//
// A linear scan over a route's few parameters beats a map decisively and
// allocates nothing. See TestGetAllocatesNothing.
func (p *Params) Get(name string) []byte {
	for i := range p.slots {
		if p.slots[i].Key == name {
			return p.slots[i].Value
		}
	}
	return nil
}

// Len returns the number of captured parameters.
func (p *Params) Len() int { return len(p.slots) }

// At returns the i'th captured parameter in the order the lookup captured them.
// It panics if i is out of range.
func (p *Params) At(i int) Param {
	if i < 0 || i >= len(p.slots) {
		panic("router: Params.At index out of range")
	}
	return p.slots[i]
}
