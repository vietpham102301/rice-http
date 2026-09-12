// Package chain compiles a middleware slice into a single handler.
//
// It is generic because docs/02-architecture.md forbids anything under internal/
// from importing package rice, so it cannot name rice.Handler or rice.Middleware.
// The ~func(H) H constraint is what lets a []rice.Middleware be passed without
// converting the slice, which would copy it.
package chain

// Compile folds mws around h so that mws[0] ends up outermost: it runs first on
// the way in and last on the way out.
//
// The fold runs backwards for that reason. Folding forwards produces a chain that
// still works and still runs every middleware, in exactly the wrong order — which
// is why this package exists separately and why its tests assert the order rather
// than only the effect.
//
// An empty slice returns h unchanged rather than wrapping it, so a route with no
// middleware costs no extra closure call.
func Compile[H any, M ~func(H) H](h H, mws []M) H {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
