//go:build !ricedebug

package rice

// poolReuse is true in release builds: a released Ctx goes back to the pool.
const poolReuse = true

// poison is zero-sized in release builds and its methods are empty, so every
// check() call on the hot path compiles to nothing. Build with -tags ricedebug to
// make use-after-release panic. See poison_debug.go.
type poison struct{}

func (*poison) mark()  {}
func (*poison) check() {}
