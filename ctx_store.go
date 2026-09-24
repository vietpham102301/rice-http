package rice

// storeCapacity is how many keys a pooled Ctx holds before its store grows.
//
// Middleware typically stores one or two values. Past four, the slice grows —
// once per pooled Ctx, since the grown slice is kept. That is the documented
// cliff in docs/03-core-concepts.md.
const storeCapacity = 4

// keyID is a key's identity. Only its address matters; the name is for people.
type keyID struct{ name string }

// entry is one key/value pair in the per-request store.
type entry struct {
	key *keyID
	val any
}

// Key identifies one value in the per-request store and fixes its type.
//
// Create keys once, at package level, with NewKey:
//
//	var userKey = rice.NewKey[*User]("user")
//
//	userKey.Set(c, u)
//	u, ok := userKey.Get(c) // u is a *User
//
// A key's identity is made by NewKey, not by its name: two NewKey calls give
// two keys that never share a value, even with the same name and type. A copy
// of a Key is the same key. The zero Key is not usable; Set and Get on it
// panic.
//
// Borrowed: a value lives as long as the Ctx it was stored on, and cannot be
// read back through that Ctx once the handler has returned.
type Key[T any] struct{ id *keyID }

// NewKey returns a new key for values of type T. name appears in String and
// in nothing else; it must not be empty.
//
// It allocates once, for the key's identity. Creating a key per request pays
// that each time and gives a key the next request cannot read back: declare
// keys as package-level variables.
func NewKey[T any](name string) Key[T] {
	if name == "" {
		panic("rice: NewKey: name must not be empty")
	}
	return Key[T]{id: &keyID{name: name}}
}

// Set stores v for the rest of the request, replacing any earlier value for k.
//
// It allocates nothing itself. A non-pointer T can allocate at the call site,
// where Go boxes v into the store's any; a pointer T does not.
func (k Key[T]) Set(c *Ctx, v T) {
	c.poison.check()
	k.mustBeMade()
	c.set(k.id, v)
}

// Get returns the value stored for k, and whether there was one. With nothing
// stored it returns the zero T and false.
func (k Key[T]) Get(c *Ctx) (T, bool) {
	c.poison.check()
	k.mustBeMade()
	v, ok := c.get(k.id)
	if !ok || v == nil {
		// A nil v is a stored nil of an interface type T: asserting a nil any
		// to an interface type would panic, and the zero T is that nil.
		var zero T
		return zero, ok
	}
	return v.(T), true
}

// String returns the name the key was made with.
func (k Key[T]) String() string {
	if k.id == nil {
		return ""
	}
	return k.id.name
}

// mustBeMade panics on a zero Key. Every zero Key has a nil id, so letting one
// through would give all of them a single shared slot.
func (k Key[T]) mustBeMade() {
	if k.id == nil {
		panic("rice: Key used without NewKey; create keys with rice.NewKey")
	}
}

// set stores v under id, replacing an earlier value. A linear scan beats a map
// at the handful of keys a request carries, and allocates nothing.
func (c *Ctx) set(id *keyID, v any) {
	for i := range c.store {
		if c.store[i].key == id {
			c.store[i].val = v
			return
		}
	}
	c.store = append(c.store, entry{key: id, val: v})
}

// get returns the value stored under id, and whether there was one.
func (c *Ctx) get(id *keyID) (any, bool) {
	for i := range c.store {
		if c.store[i].key == id {
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
