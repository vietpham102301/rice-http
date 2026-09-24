package rice

import (
	"bytes"
	"errors"
	"io/fs"
	"strconv"

	"github.com/valyala/fasthttp"
)

// errStaticNotBuilt answers a Static route dispatched on an App that was never
// built. Run, Serve and FasthttpHandler all build, so only a test that calls
// the dispatch path directly can reach it; it becomes a 500 rather than a nil
// function call.
var errStaticNotBuilt = errors.New("rice: a Static route was dispatched before Build")

// staticIndexNames is the file a directory is answered with. It is the only one:
// the spec's D3 fixes the configuration, and more names are an option nobody
// has asked for.
var staticIndexNames = []string{"index.html"}

// Static serves the files in fsys under prefix, for GET and HEAD, wrapped in mw.
//
// fsys is any fs.FS: os.DirFS for a directory on disk, an embed.FS for files
// compiled into the binary (use fs.Sub to serve one of its directories).
// prefix follows the Group prefix rule: empty, or beginning with "/" and not
// ending with "/". An empty prefix serves from the root of the URL space.
//
// A directory is answered by its index.html; one without an index is a 404,
// and no directory is ever listed. Every failure reaches the ErrorHandler like
// any other route's. Byte ranges and If-Modified-Since are answered; responses
// are never compressed.
//
// Files are served by one fasthttp.FS per call, created in Build. It caches open
// file handles for ten seconds and runs one goroutine to expire them, which
// Shutdown stops. An App mounted with FasthttpHandler and never shut down keeps
// that goroutine until the process exits. fasthttp logs every missing file
// through the server's logger. See ADR-0017.
func (a *App) Static(prefix string, fsys fs.FS, mw ...Middleware) {
	checkGroupPrefix(prefix)
	a.static(prefix, fsys, nil, mw)
}

// Static serves the files in fsys under the group's prefix joined with prefix,
// wrapped in the group's middleware and then mw. See App.Static.
func (g *Group) Static(prefix string, fsys fs.FS, mw ...Middleware) {
	checkGroupPrefix(prefix)
	g.app.static(g.prefix+prefix, fsys, g, mw)
}

// staticEntry is one Static call.
type staticEntry struct {
	prefix string // joined with any group prefix; "" for the root
	fsys   fs.FS

	// stop is fasthttp.FS's CleanStop. Closing it ends the cache goroutine.
	// Nil until build.
	stop chan struct{}

	// h is fasthttp's file handler. Nil until build.
	h fasthttp.RequestHandler
}

// static registers GET and HEAD for prefix, prefix+"/" and prefix+"/*filepath". A
// wildcard captures at least one byte, so the fs root needs its own pattern, and the
// bare prefix redirects to it.
func (a *App) static(prefix string, fsys fs.FS, g *Group, mw []Middleware) {
	if fsys == nil {
		panic("rice: nil fs.FS for static prefix " + strconv.Quote(prefix))
	}
	e := &staticEntry{prefix: prefix, fsys: fsys}
	for _, method := range [...]string{"GET", "HEAD"} {
		if prefix != "" {
			// The bare prefix is a directory without its slash.
			a.register(method, prefix, redirectToDir, g, mw)
		}
		a.register(method, prefix+"/", e.serve, g, mw)
		a.register(method, prefix+"/*filepath", e.serve, g, mw)
	}
	a.statics = append(a.statics, e)
}

// build creates the entry's fasthttp file handler. Build calls it once.
//
// Root must be empty, not ".": with "." fasthttp looks up the root's index as
// "./index.html", which is not a valid fs.FS path, and answers 403.
func (e *staticEntry) build() {
	n := len(e.prefix)
	e.stop = make(chan struct{})
	f := &fasthttp.FS{
		FS:              e.fsys,
		Root:            "",
		AllowEmptyRoot:  true,
		IndexNames:      staticIndexNames,
		AcceptByteRange: true,
		CleanStop:       e.stop,
		// The route matched, so the normalised path begins with the prefix.
		// Slicing it off costs nothing and needs no user value.
		PathRewrite: func(fctx *fasthttp.RequestCtx) []byte {
			return fctx.Path()[n:]
		},
	}
	e.h = f.NewRequestHandler()
}

// serve is the handler every Static pattern runs.
//
// A NUL byte or a ".." segment is refused before fasthttp sees the path.
// fasthttp normalises fctx.Path() unconditionally; a ".." cannot reach here
// today. A request containing one is a route miss that returns 404 without this
// check. This is a defensive second line that keeps rice's answer a 404 if that
// ever changes, rather than fasthttp.FS's 500. A NUL byte does reach here (it
// survives normalisation, e.g. %00) and fasthttp.FS would answer it with 400.
func (e *staticEntry) serve(c *Ctx) error {
	if e.h == nil {
		return errStaticNotBuilt
	}
	p := c.fctx.Path()
	if bytes.IndexByte(p, 0) >= 0 || hasDotDotSegment(p) {
		return ErrNotFound
	}
	e.h(c.fctx)
	return staticResult(c.fctx)
}

// staticResult reads the status fasthttp's file handler wrote and returns the
// error the funnel should answer instead, or nil when the response stands.
//
// fasthttp writes its own error responses with ctx.Error. respond overwrites the
// body, so none of fasthttp's texts reaches a client. A directory without an
// index is fasthttp's 403; it becomes a 404 so a response never confirms that a
// directory exists. Tests pin every row, so a fasthttp upgrade that changes a
// status fails a test rather than a client.
func staticResult(fctx *fasthttp.RequestCtx) error {
	switch code := fctx.Response.StatusCode(); {
	case code == fasthttp.StatusFound:
		// fasthttp's only 302 is a directory without its slash, and its
		// Location is built from the rewritten path, so it has lost the prefix.
		writeDirRedirect(fctx)
		return nil
	case code == fasthttp.StatusNotFound, code == fasthttp.StatusForbidden:
		return ErrNotFound
	case code >= 400:
		return NewHTTPError(code, "")
	}
	return nil
}

// redirectToDir answers the bare prefix, which is the fs root without its slash.
func redirectToDir(c *Ctx) error {
	writeDirRedirect(c.fctx)
	return nil
}

// writeDirRedirect answers 301 to the request's own path with a slash appended
// and the query string kept.
//
// Without the slash, every relative link in the directory's index.html resolves
// against its parent. That is the resource's semantics, not the router's, so it
// does not contradict ADR-0007; see ADR-0017.
//
// The path comes from URI.RequestURI, which percent-encodes the normalised path.
// Normalisation has already collapsed "//", so the Location is never
// protocol-relative. It is relative, so no Host header is trusted. 301 is safe
// because Static registers only GET and HEAD.
func writeDirRedirect(fctx *fasthttp.RequestCtx) {
	uri := fctx.URI().RequestURI()
	i := bytes.IndexByte(uri, '?')
	if i < 0 {
		i = len(uri)
	}
	loc := make([]byte, 0, len(uri)+1)
	loc = append(loc, uri[:i]...)
	loc = append(loc, '/')
	loc = append(loc, uri[i:]...)

	fctx.Response.ResetBody()
	fctx.Response.Header.SetBytesV(fasthttp.HeaderLocation, loc)
	fctx.SetStatusCode(fasthttp.StatusMovedPermanently)
}

// hasDotDotSegment reports whether p has a path segment that is exactly "..".
func hasDotDotSegment(p []byte) bool {
	for len(p) > 0 {
		seg := p
		if i := bytes.IndexByte(p, '/'); i >= 0 {
			seg, p = p[:i], p[i+1:]
		} else {
			p = nil
		}
		if len(seg) == 2 && seg[0] == '.' && seg[1] == '.' {
			return true
		}
	}
	return false
}

// stopStatic ends every Static entry's cache goroutine. runShutdown calls it
// once, after the drain — a body stream may read a cached file until then —
// and before the OnShutdown hooks, which cannot reorder it. An entry that was
// never built has no channel.
func (a *App) stopStatic() {
	for _, e := range a.statics {
		if e.stop != nil {
			close(e.stop)
		}
	}
}
