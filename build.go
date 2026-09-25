package rice

import (
	"github.com/vietpham102301/rice-http/internal/chain"
	"github.com/vietpham102301/rice-http/internal/router"
)

// Build compiles every registered route's middleware chain and installs the
// result. It runs once; later calls do nothing.
//
// Run, Serve and FasthttpHandler all call it, so most programs never call it
// directly. Doing so is useful to reject a bad configuration before a socket is
// ever opened.
//
// It returns nothing. Every error a configuration can contain — a malformed
// pattern, a duplicate route, a conflicting parameter name — is rejected by the
// registration call that introduced it, so by the time Build runs there is
// nothing left to report. Configuration mistakes panic here as they do there.
func (a *App) Build() { a.buildOnce.Do(a.build) }

// build is the body of Build, run exactly once.
func (a *App) build() {
	// The registration-time trees have done their job: they rejected bad
	// configuration where the mistake was written. Discard them and build the
	// trees that will actually serve, holding compiled chains.
	a.trees = [methodCount]router.Tree[Handler]{}
	a.rare = nil

	for i := range a.routes {
		r := &a.routes[i]
		compiled := chain.Compile(r.h, a.middlewareFor(r))

		if err := a.treeFor(r.method).Insert(r.path, compiled); err != nil {
			// Unreachable: this exact insertion succeeded during registration,
			// against an identical route set. Panicking rather than ignoring it
			// means a wrong assumption here surfaces instead of silently
			// dropping a route.
			panic("rice: build: " + err.Error() + ": " + r.method + " " + r.path)
		}
	}

	// The miss chain gets the application's middleware and nothing else. With
	// none, chain.Compile returns notFound itself and a miss behaves exactly as
	// it did before this chain existed.
	a.miss = chain.Compile(Handler(notFound), a.mws)

	// Each Static call gets its file handler here, not at the call, because
	// creating one starts a goroutine; an App that is never built starts none.
	// Nor does one already shut down: its routes answer errStaticNotBuilt.
	if !a.staticStopped {
		for _, e := range a.statics {
			e.build()
		}
	}

	a.built = true
}

// middlewareFor assembles the full chain for one route, outermost first:
// application, then each group from outermost to innermost, then the route's own.
//
// The result is a fresh slice sized exactly, so it aliases nothing a caller or a
// group can later append to.
func (a *App) middlewareFor(r *route) []Middleware {
	groups := groupChain(r.group)

	n := len(a.mws) + len(r.mws)
	for _, g := range groups {
		n += len(g.mws)
	}
	if n == 0 {
		return nil
	}

	mws := make([]Middleware, 0, n)
	mws = append(mws, a.mws...)
	for _, g := range groups {
		mws = append(mws, g.mws...)
	}
	return append(mws, r.mws...)
}

// groupChain returns g and its ancestors ordered outermost first.
//
// A Group points at its parent rather than holding a copy of the parent's
// middleware, so that a parent given middleware after the child was created still
// reaches the child's routes. That means the chain is only knowable here, at
// build time, by walking up and reversing. See the M4 design doc, D3.
func groupChain(g *Group) []*Group {
	if g == nil {
		return nil
	}

	var out []*Group
	for ; g != nil; g = g.parent {
		out = append(out, g)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
