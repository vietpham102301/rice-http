package rice

import (
	"errors"
	"fmt"

	"github.com/vietpham102301/rice-http/internal/router"
)

// Handle registers h for the given method and path.
//
// All routes must be registered before serving begins. The route trees are
// mutated here without synchronisation and read on every request, so registering
// a route after Run or Serve is a data race, not merely a late change.
//
// path may contain ":name" segments, each capturing one path segment under
// that name, and a trailing "*name" wildcard capturing everything after it. A
// wildcard requires at least one byte to match, so "/files/*path" matches
// neither "/files" nor "/files/" — the commonest use of a wildcard is a
// single-page-app catch-all such as "/*all", and it will not match the bare
// "/"; register that path separately if it needs its own handler.
//
// It panics on a programmer error: an empty or lowercase method, a nil handler,
// a pattern that is malformed or could never match a request, or a route already
// registered for the same method and path. These are mistakes discovered at
// startup rather than runtime conditions, and a duplicate in particular means one
// of the two handlers can never run — silent, and expensive to debug. Panicking
// at the call site puts the mistake in the stack trace.
//
// M4 appends a variadic mw ...Middleware parameter. Doing so does not break
// existing calls.
func (a *App) Handle(method, path string, h Handler) {
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

	if err := a.treeFor(method).Insert(path, h); err != nil {
		if errors.Is(err, router.ErrDuplicate) {
			// ErrDuplicate carries no path or method of its own (see its doc
			// comment), so the message is built here, in the same "route path
			// ..." shape parsePattern's own errors use below — one shape for
			// every startup panic Handle can raise, none of them naming the
			// internal router package.
			panic(fmt.Sprintf("rice: route path %s is already registered for %s", path, method))
		}
		// parsePattern's and the tree's errors already name the offending
		// pattern and say what to write instead, so they are surfaced verbatim.
		panic("rice: " + err.Error())
	}
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
