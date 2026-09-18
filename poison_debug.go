//go:build ricedebug

package rice

// poolReuse is false in the debug build. A poisoned Ctx that went back into the
// pool would be un-poisoned by the next acquire, and a stale reference to it
// would silently read another request's data instead of panicking — the exact
// bug this build exists to catch. See the M6 design doc, D5, and ADR-0005.
const poolReuse = false

// errUseAfterRelease is the panic value for any use of a released Ctx.
const errUseAfterRelease = "rice: *Ctx used after its handler returned; see the borrow contract in the package documentation"

// poison marks a Ctx as released.
type poison struct{ released bool }

func (p *poison) mark() { p.released = true }

func (p *poison) check() {
	if p.released {
		panic(errUseAfterRelease)
	}
}
