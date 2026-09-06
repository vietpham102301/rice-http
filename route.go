package rice

import (
	"fmt"

	"github.com/vietpham102301/rice-http/internal/router"
)

// Handle registers h for the given method and path.
//
// It panics on a programmer error: an empty path, a path without a leading
// slash, a nil handler, or a route already registered for the same method and
// path. These are mistakes discovered at startup rather than runtime
// conditions, and a duplicate in particular means one of the two handlers can
// never run — silent, and expensive to debug. Panicking at the call site puts
// the mistake in the stack trace.
//
// M4 appends a variadic mw ...Middleware parameter. Doing so does not break
// existing calls.
func (a *App) Handle(method, path string, h Handler) {
	if path == "" {
		panic("rice: route path is empty for method " + method)
	}
	if path[0] != '/' {
		panic("rice: route path " + path + " does not begin with /")
	}
	if h == nil {
		panic("rice: nil handler for " + method + " " + path)
	}

	if err := a.treeFor(method).Insert(path, h); err != nil {
		panic(fmt.Sprintf("rice: %v: %s %s", err, method, path))
	}
}

// GET registers h for GET requests to path.
func (a *App) GET(path string, h Handler) { a.Handle("GET", path, h) }

// POST registers h for POST requests to path.
func (a *App) POST(path string, h Handler) { a.Handle("POST", path, h) }

// PUT registers h for PUT requests to path.
func (a *App) PUT(path string, h Handler) { a.Handle("PUT", path, h) }

// PATCH registers h for PATCH requests to path.
func (a *App) PATCH(path string, h Handler) { a.Handle("PATCH", path, h) }

// DELETE registers h for DELETE requests to path.
func (a *App) DELETE(path string, h Handler) { a.Handle("DELETE", path, h) }

// HEAD registers h for HEAD requests to path.
func (a *App) HEAD(path string, h Handler) { a.Handle("HEAD", path, h) }

// OPTIONS registers h for OPTIONS requests to path.
func (a *App) OPTIONS(path string, h Handler) { a.Handle("OPTIONS", path, h) }

// treeFor returns the tree for method, creating one for an uncommon verb.
//
// Registration time, not request time: the []byte conversion below allocates,
// and that is fine here. The request path through lookup does not convert.
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

// lookup finds the handler for a request.
//
// This is the hot path. For a common verb it costs one array index and one map
// probe, and it allocates nothing: both method and path stay as borrowed byte
// slices throughout.
func (a *App) lookup(method, path []byte) (Handler, bool) {
	if i, ok := methodIndex(method); ok {
		return a.trees[i].Lookup(path)
	}

	if a.rare == nil {
		return nil, false
	}
	t, ok := a.rare[string(method)]
	if !ok {
		return nil, false
	}
	return t.Lookup(path)
}
