package rice

// storeCapacity is how many Set keys a pooled Ctx holds before its store grows.
//
// Middleware typically stores one or two values. Past four, the slice grows —
// once per pooled Ctx, since the grown slice is kept. That is the documented
// cliff in docs/03-core-concepts.md.
const storeCapacity = 4

// entry is one key/value pair in the per-request store.
type entry struct {
	key string
	val any
}

// Set stores a value for the rest of the request, replacing any earlier value for
// key.
//
// It allocates nothing itself. Passing a non-pointer value can allocate at the
// call site, where Go boxes it into an any; pass a pointer to avoid that.
//
// Borrowed: the store dies with the Ctx when the handler returns. The value is
// yours, but it cannot be read back through this Ctx afterwards.
func (c *Ctx) Set(key string, v any) {
	c.poison.check()
	for i := range c.store {
		if c.store[i].key == key {
			c.store[i].val = v
			return
		}
	}
	c.store = append(c.store, entry{key: key, val: v})
}

// Get returns the value stored for key, and whether there was one.
//
// A linear scan beats a map at the handful of keys a request carries, and
// allocates nothing.
func (c *Ctx) Get(key string) (any, bool) {
	c.poison.check()
	for i := range c.store {
		if c.store[i].key == key {
			return c.store[i].val, true
		}
	}
	return nil, false
}

// resetStore empties the store and zeroes the entries it held.
//
// Zeroing is not optional. A truncated slice keeps its old values in the backing
// array, and a pooled Ctx would keep every value a previous request stored alive
// for as long as it sat in the pool.
func (c *Ctx) resetStore() {
	clear(c.store)
	c.store = c.store[:0]
}
