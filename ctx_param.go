package rice

// Param returns the value captured for a named route parameter, or an empty
// slice if the route captured no such name.
//
// Borrowed: the returned slice points into fasthttp's request buffer and is
// valid only until the handler returns. Use ParamString to keep it.
func (c *Ctx) Param(name string) []byte {
	c.poison.check()
	return c.params.Get(name)
}

// ParamString returns the value captured for a named route parameter as a
// string, or the empty string if the route captured no such name.
//
// Owned: it copies, costs one allocation, and is safe to keep past the handler.
// The naming rule holds across the whole API — the byte-returning accessor is
// free and borrowed, the string-returning one is the longer name to type because
// it is the expensive choice.
func (c *Ctx) ParamString(name string) string {
	c.poison.check()
	return string(c.params.Get(name))
}
