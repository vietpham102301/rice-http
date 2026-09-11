package bench

// M2's exact-match map router, frozen for comparison.
//
// This is a deliberate duplicate of code that no longer exists in rice. Its
// entire value is that it does not change: every recording from M3 onward can
// measure the tree against the map in the same binary, on the same machine, in
// the same session, which is what bench/results/README.md requires and what
// deleting the map would have made impossible.
//
// Do not "fix" it, extend it, or make it support parameters. It is a
// measurement instrument, not a component.

type mapTree struct {
	routes map[string]int
}

func (t *mapTree) insert(path string, h int) {
	if t.routes == nil {
		t.routes = make(map[string]int)
	}
	t.routes[path] = h
}

// lookup keeps M2's one-line map probe verbatim. The single-expression form is
// the one the compiler documents its map-index conversion optimisation for.
func (t *mapTree) lookup(path []byte) (int, bool) {
	h, ok := t.routes[string(path)]
	return h, ok
}
