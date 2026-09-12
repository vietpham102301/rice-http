package rice

import "strings"

// Group is a route prefix plus middleware, and a convenience that exists only at
// registration time. By the time a request arrives, every route holds one flat
// compiled chain and no group is involved.
//
// A Group holds a pointer to its parent rather than a copy of the parent's
// middleware, so that middleware added to a parent after the child was created
// still wraps the child's routes. Snapshotting instead would reintroduce, one
// level down, exactly the bug the build phase exists to prevent. See the M4
// design doc, D3.
type Group struct {
	app    *App
	parent *Group
	prefix string
	mws    []Middleware
}

// Group creates a route group under prefix, wrapped in mw.
//
// The prefix must be empty, or begin with "/" and not end with "/". An empty
// prefix is a group that shares middleware without sharing a path. The rule makes
// joining total: a valid prefix joined to a valid route path is always a valid
// pattern, so a bad prefix panics here rather than producing a malformed pattern
// that fails later naming a string nobody wrote.
func (a *App) Group(prefix string, mw ...Middleware) *Group {
	checkGroupPrefix(prefix)
	if a.built {
		panic("rice: cannot create a group after Build; all routes must be registered before serving begins")
	}

	return &Group{
		app:    a,
		prefix: prefix,
		// Copy rather than keep mw: it may be a caller's slice with spare
		// capacity, and an append on their side must not reach into ours.
		mws: append([]Middleware(nil), mw...),
	}
}

// Group creates a nested group, concatenating prefixes and inheriting middleware.
func (g *Group) Group(prefix string, mw ...Middleware) *Group {
	checkGroupPrefix(prefix)
	if g.app.built {
		panic("rice: cannot create a group after Build; all routes must be registered before serving begins")
	}

	return &Group{
		app:    g.app,
		parent: g,
		prefix: g.prefix + prefix,
		mws:    append([]Middleware(nil), mw...),
	}
}

// Use adds middleware to this group, wrapping every route registered on it or on
// any group nested inside it.
//
// Order does not matter: chains are compiled in Build, so Use written after a
// route — or after a nested group was created — still applies to it.
func (g *Group) Use(mw ...Middleware) {
	if g.app.built {
		panic("rice: cannot call Use after Build; all middleware must be registered before serving begins")
	}
	g.mws = append(g.mws, mw...)
}

// Handle registers h for method at the group's prefix joined with path.
func (g *Group) Handle(method, path string, h Handler, mw ...Middleware) {
	g.app.register(method, g.prefix+path, h, g, mw)
}

// GET registers h for GET requests to the group's prefix joined with path.
func (g *Group) GET(path string, h Handler, mw ...Middleware) {
	g.app.register("GET", g.prefix+path, h, g, mw)
}

// POST registers h for POST requests to the group's prefix joined with path.
func (g *Group) POST(path string, h Handler, mw ...Middleware) {
	g.app.register("POST", g.prefix+path, h, g, mw)
}

// PUT registers h for PUT requests to the group's prefix joined with path.
func (g *Group) PUT(path string, h Handler, mw ...Middleware) {
	g.app.register("PUT", g.prefix+path, h, g, mw)
}

// PATCH registers h for PATCH requests to the group's prefix joined with path.
func (g *Group) PATCH(path string, h Handler, mw ...Middleware) {
	g.app.register("PATCH", g.prefix+path, h, g, mw)
}

// DELETE registers h for DELETE requests to the group's prefix joined with path.
func (g *Group) DELETE(path string, h Handler, mw ...Middleware) {
	g.app.register("DELETE", g.prefix+path, h, g, mw)
}

// HEAD registers h for HEAD requests to the group's prefix joined with path.
func (g *Group) HEAD(path string, h Handler, mw ...Middleware) {
	g.app.register("HEAD", g.prefix+path, h, g, mw)
}

// OPTIONS registers h for OPTIONS requests to the group's prefix joined with path.
func (g *Group) OPTIONS(path string, h Handler, mw ...Middleware) {
	g.app.register("OPTIONS", g.prefix+path, h, g, mw)
}

// checkGroupPrefix rejects a prefix that would not join cleanly onto a route path.
func checkGroupPrefix(prefix string) {
	if prefix == "" {
		return
	}
	if prefix[0] != '/' {
		panic("rice: group prefix " + prefix + " must begin with /")
	}
	if prefix[len(prefix)-1] == '/' {
		panic("rice: group prefix " + prefix + " must not end with /; write " +
			strings.TrimRight(prefix, "/"))
	}
}
