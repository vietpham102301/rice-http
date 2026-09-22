package rice

import (
	"errors"
	"fmt"

	"github.com/vietpham102301/rice-http/internal/router"
)

// Handle registers h for the given method and path, wrapped in mw.
//
// All routes must be registered before serving begins. The route trees and
// middleware lists are mutated here and read on every request without
// synchronisation, so registering after Build is a data race rather than merely a
// late change — and it panics rather than racing.
//
// It panics on a programmer error: an empty or lowercase method, a nil handler, a
// pattern that is malformed or could never match a request, or a route already
// registered for the same method and path.
//
// Pattern syntax: a segment beginning with ':' captures one path segment by name,
// and a final segment beginning with '*' captures the remainder. A wildcard must
// capture at least one byte, so /files/*path matches /files/a but not /files/ or
// /files.
func (a *App) Handle(method, path string, h Handler, mw ...Middleware) {
	a.register(method, path, h, nil, mw)
}

// GET registers h for GET requests to path, wrapped in mw.
func (a *App) GET(path string, h Handler, mw ...Middleware) {
	a.register("GET", path, h, nil, mw)
}

// POST registers h for POST requests to path, wrapped in mw.
func (a *App) POST(path string, h Handler, mw ...Middleware) {
	a.register("POST", path, h, nil, mw)
}

// PUT registers h for PUT requests to path, wrapped in mw.
func (a *App) PUT(path string, h Handler, mw ...Middleware) {
	a.register("PUT", path, h, nil, mw)
}

// PATCH registers h for PATCH requests to path, wrapped in mw.
func (a *App) PATCH(path string, h Handler, mw ...Middleware) {
	a.register("PATCH", path, h, nil, mw)
}

// DELETE registers h for DELETE requests to path, wrapped in mw.
func (a *App) DELETE(path string, h Handler, mw ...Middleware) {
	a.register("DELETE", path, h, nil, mw)
}

// HEAD registers h for HEAD requests to path, wrapped in mw.
func (a *App) HEAD(path string, h Handler, mw ...Middleware) {
	a.register("HEAD", path, h, nil, mw)
}

// OPTIONS registers h for OPTIONS requests to path, wrapped in mw.
func (a *App) OPTIONS(path string, h Handler, mw ...Middleware) {
	a.register("OPTIONS", path, h, nil, mw)
}

// Use adds application-level middleware, which wraps every route.
//
// Application middleware registered with Use also runs on a request that
// matches no route, where group and route middleware do not; see ADR-0012.
//
// Order of calls does not matter relative to route registration: chains are
// compiled in Build, so Use written after a route still applies to it. That is
// ADR-0003's central reason for having a build phase at all — the alternative
// silently drops middleware added late, and middleware added late is usually
// authentication.
func (a *App) Use(mw ...Middleware) {
	checkMiddleware(mw)
	if a.built {
		panic("rice: cannot call Use after Build; all middleware must be registered before serving begins")
	}
	// append into the App's own slice rather than keeping mw, so the caller's
	// slice is never aliased and cannot be changed underneath us.
	a.mws = append(a.mws, mw...)
}

// register is the single path every registration takes, from the App and from any
// Group. g is nil for a route registered directly on the App.
func (a *App) register(method, path string, h Handler, g *Group, mw []Middleware) {
	checkMiddleware(mw)
	if a.built {
		panic("rice: cannot register " + method + " " + path +
			" after Build; all routes must be registered before serving begins")
	}
	if method == "" {
		panic("rice: route method is empty for path " + path)
	}
	if hasLowercaseByte(method) {
		panic("rice: route method " + method + " for path " + path +
			" is not uppercase; HTTP methods are matched case-sensitively")
	}
	if h == nil {
		panic("rice: nil handler for " + method + " " + path)
	}

	// Insert the raw handler now, so a malformed pattern or a conflicting route
	// panics from the call that wrote it. Build discards this tree and rebuilds
	// with the compiled chain.
	t := a.treeFor(method)
	if err := t.Insert(path, h); err != nil {
		if errors.Is(err, router.ErrDuplicate) {
			// ErrDuplicate carries no path or method of its own (see its doc
			// comment), so the message is built here, in the same "route path
			// ..." shape parsePattern's own errors use below — one shape for
			// every startup panic registration can raise, none of them naming
			// the internal router package.
			panic(fmt.Sprintf("rice: route path %s is already registered for %s", path, method))
		}
		// parsePattern's and the tree's errors already name the offending
		// pattern and say what to write instead, so they are surfaced verbatim.
		panic("rice: " + err.Error())
	}
	a.maxParams = max(a.maxParams, t.MaxParams())

	a.routes = append(a.routes, route{
		method: method,
		path:   path,
		h:      h,
		group:  g,
		// Copy rather than keep mw: it may be a caller's slice with spare
		// capacity, and an append on their side must not reach into ours.
		mws: append([]Middleware(nil), mw...),
	})
}

// hasLowercaseByte reports whether s contains an ASCII lowercase letter.
//
// fasthttp uppercases the request method before Handle's registrations are
// ever consulted, and method matching is case-sensitive (see App.lookup), so
// a lowercase or mixed-case method registered here can never match a real
// request.
func hasLowercaseByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'a' && s[i] <= 'z' {
			return true
		}
	}
	return false
}

// treeFor returns the tree for method, creating one for an uncommon verb.
//
// Registration time, not request time, so an allocation here would be an
// acceptable price. There isn't one: go build -gcflags=-m reports
// "([]byte)(method) does not escape" and "zero-copy string->[]byte
// conversion" for the conversion below. The request path through lookup
// never even performs the conversion — method arrives there already as a
// []byte, straight from fctx.Method().
func (a *App) treeFor(method string) *router.Tree[Handler] {
	if i, ok := methodIndex([]byte(method)); ok {
		return &a.trees[i]
	}

	if a.rare == nil {
		a.rare = make(map[string]*router.Tree[Handler])
	}
	t, ok := a.rare[method]
	if !ok {
		t = &router.Tree[Handler]{}
		a.rare[method] = t
	}
	return t
}

// lookup finds the handler for a request, filling params with whatever the
// matched route captured.
//
// This is the hot path. On the common-verb branch, method and path stay
// borrowed byte slices and are never converted to strings; params is storage
// the caller already owns rather than something lookup allocates. That is what
// makes zero-allocation parameter capture possible, and alloc_test.go's
// parameter-capture budgets (TestAllocBudgetLookupOneParameter and its
// siblings) verify it.
func (a *App) lookup(method, path []byte, params *router.Params) (Handler, bool) {
	if i, ok := methodIndex(method); ok {
		return a.trees[i].Lookup(path, params)
	}

	if a.rare == nil {
		return nil, false
	}
	t, ok := a.rare[string(method)]
	if !ok {
		return nil, false
	}
	return t.Lookup(path, params)
}
