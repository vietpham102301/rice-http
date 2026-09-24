# Static File Serving Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `App.Static(prefix, fsys, mw...)` and `Group.Static(...)` serve the files of an `fs.FS` for GET and HEAD, with every failure answered by rice's error funnel.

**Architecture:** Each `Static` call records a `staticEntry` and registers GET and HEAD for `prefix`, `prefix+"/"` and `prefix+"/*filepath"`. `Build` gives each entry one long-lived `fasthttp.FS` handler whose `PathRewrite` strips the prefix from the normalised path. The route handler calls that handler, then reads the status it wrote and turns a failure into an error for the funnel, and fasthttp's directory redirect into a 301 rice builds itself. `Shutdown` closes each entry's `CleanStop` after the drain.

**Tech Stack:** Go 1.25, `io/fs`, `testing/fstest`, fasthttp v1.73.0 (`fasthttp.FS`). No new dependency.

**Spec:** [docs/superpowers/specs/2026-09-25-static-files-design.md](../specs/2026-09-25-static-files-design.md)

## Global Constraints

- **Branch:** `static-files`, already created, already holding the spec and plan commits. Do not commit to `main`.
- **Exported surface added:** exactly `App.Static` and `Group.Static`. Nothing else exported changes.
- **Panics carry the prefix `rice: `.** A bad prefix panics through `checkGroupPrefix`; a nil `fsys` panics with `rice: nil fs.FS for static prefix "<prefix>"`.
- **fasthttp.FS fixed configuration (spec D3):** `Root: ""`, `AllowEmptyRoot: true`, `IndexNames: []string{"index.html"}`, `GenerateIndexPages: false`, `AcceptByteRange: true`, `Compress: false`, `CacheDuration` left zero (fasthttp's 10 s), `CleanStop` owned by the entry, `PathRewrite` returning `fctx.Path()[len(prefix):]`.
- **The fasthttp handler is built in `Build`, never in `Static`.** An App that is never built starts no goroutine.
- **Status mapping (spec D5):** 2xx and 304 → `nil`; 302 → rice's 301 (Task 3); 404 and 403 → `ErrNotFound`; any other status ≥ 400 → `NewHTTPError(status, "")`.
- **Directory redirect (spec D4):** status 301, `Location` relative, built from the request's normalised, percent-encoded path plus `/`, query string kept.
- **Budgets:** declared `const want float64 = …`; no figure is written into a document until measured with and without `-race`.
- **Run `make test`, `make test-debug` and `make lint` before each commit.** Commit messages: subject, blank line, then the session's `Co-Authored-By` trailer on its own line.

## Review Focus

Inputs the spec implies but does not enumerate. Each has a test in the task that owns the code.

1. **A directory whose name needs escaping** — `my docs/` requested as `/assets/my%20docs`. `Location` must be `/assets/my%20docs/`, not a raw space. (Task 3)
2. **A redirect that could leave the site** — with an empty prefix, `//evil.example/` (a directory named `evil.example`). `Location` must begin with a single `/`, never `//`. (Task 3)
3. **HEAD for a missing file** — 404 through the funnel with no body, like GET's status. (Task 2)
4. **`index.html` requested by name** — `/assets/index.html` serves the file with 200; it does not redirect. (Task 1)
5. **An empty file** — 200 with `Content-Length: 0`, not a 404 or a hang. (Task 1)

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `static.go` (create) | `App.Static`, `Group.Static`, `staticEntry`, its `build` and `serve`, the status mapping, the directory redirect, `stopStatic` | 1–4 |
| `app.go` (modify) | the `statics []*staticEntry` field on `App` | 1 |
| `build.go` (modify) | build every entry at the end of `build` | 1 |
| `lifecycle.go` (modify) | `runShutdown` calls `stopStatic` first | 4 |
| `static_test.go` (create) | behaviour tests and the request helper | 1–4 |
| `alloc_test.go` (modify) | two budgets | 5 |
| `bench/rice_bench_test.go` (modify) | `BenchmarkStaticSmallFile` | 5 |
| `docs/adr/0017-static-files-wrap-fasthttp-fs.md` (create), `docs/adr/README.md`, `docs/03-core-concepts.md`, `docs/05-performance-model.md`, `docs/04-roadmap.md`, `docs/progress.md`, `README.md` | documentation | 6 |

---

### Task 1: Register Static routes and serve files

**Files:**
- Create: `static.go`, `static_test.go`
- Modify: `app.go` (App struct, after the `routes` field), `build.go` (end of `build`, before `a.built = true`)

**Interfaces:**
- Consumes: `checkGroupPrefix(prefix string)` (group.go), `(*App).register(method, path string, h Handler, g *Group, mw []Middleware)` (route.go), `(*App).handle(*fasthttp.RequestCtx)` (app.go).
- Produces: `func (a *App) Static(prefix string, fsys fs.FS, mw ...Middleware)`, `func (g *Group) Static(prefix string, fsys fs.FS, mw ...Middleware)`, `type staticEntry struct{ prefix string; fsys fs.FS; stop chan struct{}; h fasthttp.RequestHandler }`, `func (e *staticEntry) build()`, `func (e *staticEntry) serve(c *Ctx) error`, `var errStaticNotBuilt error`, App field `statics []*staticEntry`. Test helpers `staticFS()`, `doStatic(app *App, method, uri string, headers ...string) *fasthttp.RequestCtx`, `staticModTime`.

- [ ] **Step 1: Confirm the branch**

Run: `git branch --show-current`
Expected: `static-files`

- [ ] **Step 2: Write the failing tests**

Create `static_test.go`:

```go
package rice

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/valyala/fasthttp"
)

// staticModTime is every test file's modification time, fixed so that a
// conditional request can name it exactly.
var staticModTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// staticFS is the tree most tests serve:
//
//	index.html          "root"
//	a.txt               "hi\n"
//	style.css           "b{}"
//	empty.txt           ""
//	docs/index.html     "docs"
//	docs/deep/x.txt     "deep"
//	nodir/f.txt         "f"      (a directory with no index)
//	my docs/index.html  "spaced"
//	health              "file"   (shadowed by a route in one test)
func staticFS() fstest.MapFS {
	f := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s), ModTime: staticModTime} }
	return fstest.MapFS{
		"index.html":         f("root"),
		"a.txt":              f("hi\n"),
		"style.css":          f("b{}"),
		"empty.txt":          f(""),
		"docs/index.html":    f("docs"),
		"docs/deep/x.txt":    f("deep"),
		"nodir/f.txt":        f("f"),
		"my docs/index.html": f("spaced"),
		"health":             f("file"),
	}
}

// discardLogger silences fasthttp.FS, which logs every miss through the
// request's logger (spec, finding 5).
type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// doStatic dispatches one request through app.handle and returns the context
// holding the response. headers are name, value pairs.
//
// The context is initialised with Init because fasthttp.FS calls ctx.Logger(),
// which dereferences a nil server on a bare RequestCtx.
func doStatic(app *App, method, uri string, headers ...string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	fctx := &fasthttp.RequestCtx{}
	fctx.Init(&req, nil, discardLogger{})
	app.handle(fctx)
	return fctx
}

func TestStaticServesFiles(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()

	cases := []struct {
		uri, body, contentType string
	}{
		{"/assets/a.txt", "hi\n", "text/plain"},
		{"/assets/style.css", "b{}", "text/css"},
		{"/assets/docs/deep/x.txt", "deep", "text/plain"},
		{"/assets/", "root", "text/html"},
		{"/assets/docs/", "docs", "text/html"},
		{"/assets/index.html", "root", "text/html"},
	}
	for _, tc := range cases {
		fctx := doStatic(app, "GET", tc.uri)
		if got := fctx.Response.StatusCode(); got != 200 {
			t.Errorf("GET %s: status %d, want 200", tc.uri, got)
			continue
		}
		if got := string(fctx.Response.Body()); got != tc.body {
			t.Errorf("GET %s: body %q, want %q", tc.uri, got, tc.body)
		}
		if got := string(fctx.Response.Header.ContentType()); !strings.HasPrefix(got, tc.contentType) {
			t.Errorf("GET %s: Content-Type %q, want prefix %q", tc.uri, got, tc.contentType)
		}
	}
}

func TestStaticServesAnEmptyFile(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()

	fctx := doStatic(app, "GET", "/assets/empty.txt")
	if got := fctx.Response.StatusCode(); got != 200 {
		t.Fatalf("status %d, want 200", got)
	}
	if got := fctx.Response.Header.ContentLength(); got != 0 {
		t.Errorf("Content-Length %d, want 0", got)
	}
}

func TestStaticHeadSendsHeadersWithoutBody(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()

	fctx := doStatic(app, "HEAD", "/assets/a.txt")
	if got := fctx.Response.StatusCode(); got != 200 {
		t.Fatalf("status %d, want 200", got)
	}
	if got := fctx.Response.Header.ContentLength(); got != 3 {
		t.Errorf("Content-Length %d, want 3", got)
	}
	if got := len(fctx.Response.Body()); got != 0 {
		t.Errorf("HEAD body has %d bytes, want 0", got)
	}
}

func TestStaticAnswersARangeWithPartialContent(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()

	fctx := doStatic(app, "GET", "/assets/a.txt", "Range", "bytes=0-0")
	if got := fctx.Response.StatusCode(); got != 206 {
		t.Fatalf("status %d, want 206", got)
	}
	if got := string(fctx.Response.Body()); got != "h" {
		t.Errorf("body %q, want %q", got, "h")
	}
}

func TestStaticAnswersNotModified(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()

	since := string(fasthttp.AppendHTTPDate(nil, staticModTime))
	fctx := doStatic(app, "GET", "/assets/a.txt", "If-Modified-Since", since)
	if got := fctx.Response.StatusCode(); got != 304 {
		t.Errorf("status %d, want 304", got)
	}
}

func TestStaticWithAnEmptyPrefixServesFromTheRoot(t *testing.T) {
	app := New()
	app.Static("", staticFS())
	app.Build()

	for uri, body := range map[string]string{"/": "root", "/a.txt": "hi\n", "/docs/": "docs"} {
		fctx := doStatic(app, "GET", uri)
		if got := string(fctx.Response.Body()); fctx.Response.StatusCode() != 200 || got != body {
			t.Errorf("GET %s: %d %q, want 200 %q", uri, fctx.Response.StatusCode(), got, body)
		}
	}
}

func TestGroupStaticJoinsThePrefixAndRunsTheGroupMiddleware(t *testing.T) {
	app := New()
	mark := func(next Handler) Handler {
		return func(c *Ctx) error {
			c.SetHeader("X-Group", "yes")
			return next(c)
		}
	}
	app.Group("/v1", mark).Static("/assets", staticFS())
	app.Build()

	fctx := doStatic(app, "GET", "/v1/assets/a.txt")
	if got := string(fctx.Response.Body()); got != "hi\n" {
		t.Errorf("body %q, want %q", got, "hi\n")
	}
	if got := string(fctx.Response.Header.Peek("X-Group")); got != "yes" {
		t.Errorf("X-Group %q, want %q: group middleware did not run", got, "yes")
	}
}

func TestStaticRouteMiddlewareRuns(t *testing.T) {
	app := New()
	cache := func(next Handler) Handler {
		return func(c *Ctx) error {
			c.SetHeader("Cache-Control", "max-age=60")
			return next(c)
		}
	}
	app.Static("/assets", staticFS(), cache)
	app.Build()

	fctx := doStatic(app, "GET", "/assets/a.txt")
	if got := string(fctx.Response.Header.Peek("Cache-Control")); got != "max-age=60" {
		t.Errorf("Cache-Control %q, want %q", got, "max-age=60")
	}
}

func TestARegisteredRouteOutranksAStaticFile(t *testing.T) {
	app := New()
	app.GET("/assets/health", func(c *Ctx) error { return c.String(200, "route") })
	app.Static("/assets", staticFS())
	app.Build()

	fctx := doStatic(app, "GET", "/assets/health")
	if got := string(fctx.Response.Body()); got != "route" {
		t.Errorf("body %q, want %q: the file shadowed the route", got, "route")
	}
}

func TestStaticPanicsOnBadConfiguration(t *testing.T) {
	cases := []struct {
		name, want string
		call       func()
	}{
		{"prefix without slash", "must begin with /", func() { New().Static("assets", staticFS()) }},
		{"prefix with trailing slash", "must not end with /", func() { New().Static("/assets/", staticFS()) }},
		{"nil fs", `rice: nil fs.FS for static prefix "/assets"`, func() { New().Static("/assets", nil) }},
		{"group prefix without slash", "must begin with /", func() { New().Group("/v1").Static("assets", staticFS()) }},
		{"collision", "already registered", func() {
			a := New()
			a.GET("/assets/", func(c *Ctx) error { return nil })
			a.Static("/assets", staticFS())
		}},
		{"after Build", "after Build", func() {
			a := New()
			a.Build()
			a.Static("/assets", staticFS())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("did not panic")
				}
				if msg, _ := r.(string); !strings.Contains(msg, tc.want) {
					t.Errorf("panic %q, want it to contain %q", r, tc.want)
				}
			}()
			tc.call()
		})
	}
}

func TestStaticDispatchedBeforeBuildIsAnError(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())

	fctx := doStatic(app, "GET", "/assets/a.txt")
	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status %d, want 500", got)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test -run 'Static|ARegisteredRouteOutranks' -count=1 .`
Expected: FAIL to compile, `app.Static undefined (type *App has no field or method Static)`.

- [ ] **Step 4: Write the implementation**

In `app.go`, add after the `routes` field of `App`:

```go
	// statics records every Static call. Build gives each its fasthttp file
	// handler and Shutdown stops each one's cache goroutine. Written during
	// registration and read by Build and Shutdown, on the same argument as
	// routes: registration ends before serving begins.
	statics []*staticEntry
```

In `build.go`, at the end of `build`, immediately before `a.built = true`:

```go
	// Each Static call gets its file handler here, not at the call, because
	// creating one starts a goroutine; an App that is never built starts none.
	for _, e := range a.statics {
		e.build()
	}
```

Create `static.go`:

```go
package rice

import (
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

// static registers GET and HEAD for prefix+"/" and prefix+"/*filepath". A
// wildcard captures at least one byte, so the fs root needs its own pattern.
func (a *App) static(prefix string, fsys fs.FS, g *Group, mw []Middleware) {
	if fsys == nil {
		panic("rice: nil fs.FS for static prefix " + strconv.Quote(prefix))
	}
	e := &staticEntry{prefix: prefix, fsys: fsys}
	for _, method := range [...]string{"GET", "HEAD"} {
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
func (e *staticEntry) serve(c *Ctx) error {
	if e.h == nil {
		return errStaticNotBuilt
	}
	e.h(c.fctx)
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -run 'Static|ARegisteredRouteOutranks' -count=1 .`
Expected: PASS.

- [ ] **Step 6: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS. `TestEveryCtxMethodPanicsAfterRelease` is unaffected: no `Ctx` method was added.

- [ ] **Step 7: Commit**

```bash
git add static.go static_test.go app.go build.go
git commit -m "rice: Static serves an fs.FS under a prefix through fasthttp.FS

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Every failure goes through the funnel

**Files:**
- Modify: `static.go` (`serve`, new `staticResult`, new `hasDotDotSegment`)
- Test: `static_test.go`

**Interfaces:**
- Consumes: `staticEntry.serve`, `doStatic`, `staticFS` (Task 1); `ErrNotFound`, `NewHTTPError`, `WithErrorHandler`, `(*Ctx).JSON` (existing).
- Produces: `func staticResult(fctx *fasthttp.RequestCtx) error` (Task 3 adds a case to it), `func hasDotDotSegment(p []byte) bool`.

- [ ] **Step 1: Write the failing tests**

Append to `static_test.go` (add `"encoding/json"`, `"errors"`, `"os"` and `"path/filepath"` to its imports):

```go
// jsonErrors answers every error as {"status": code}, so a test can tell a
// response the funnel wrote from one fasthttp wrote itself.
func jsonErrors(c *Ctx, err error) {
	code := 500
	var he *HTTPError
	if errors.As(err, &he) {
		code = he.Code
	}
	_ = c.JSON(code, map[string]int{"status": code})
}

func TestStaticFailuresReachTheErrorHandler(t *testing.T) {
	app := New(WithErrorHandler(jsonErrors))
	app.Static("/assets", staticFS())
	app.Build()

	cases := []struct {
		name    string
		uri     string
		headers []string
		code    int
	}{
		{"missing file", "/assets/missing", nil, 404},
		{"directory without index", "/assets/nodir/", nil, 404},
		{"unsatisfiable range", "/assets/a.txt", []string{"Range", "bytes=99-"}, 416},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fctx := doStatic(app, "GET", tc.uri, tc.headers...)
			if got := fctx.Response.StatusCode(); got != tc.code {
				t.Errorf("status %d, want %d", got, tc.code)
			}
			var body struct{ Status int }
			if err := json.Unmarshal(fctx.Response.Body(), &body); err != nil || body.Status != tc.code {
				t.Errorf("body %q is not the ErrorHandler's JSON for %d", fctx.Response.Body(), tc.code)
			}
		})
	}
}

func TestStaticDefaultErrorBodyIsNotFasthttps(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()

	fctx := doStatic(app, "GET", "/assets/nodir/")
	if got := string(fctx.Response.Body()); got != "Not Found" {
		t.Errorf("body %q, want %q", got, "Not Found")
	}
}

func TestStaticHeadForAMissingFileIsNotFound(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()

	fctx := doStatic(app, "HEAD", "/assets/missing")
	if got := fctx.Response.StatusCode(); got != 404 {
		t.Errorf("status %d, want 404", got)
	}
}

func TestStaticNeverServesOutsideTheFS(t *testing.T) {
	root := t.TempDir()
	pub := filepath.Join(root, "pub")
	if err := os.Mkdir(pub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pub, "a.txt"), []byte("public"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.Static("/assets", os.DirFS(pub))
	app.Build()

	if got := string(doStatic(app, "GET", "/assets/a.txt").Response.Body()); got != "public" {
		t.Fatalf("control request: body %q, want %q", got, "public")
	}
	for _, uri := range []string{
		"/assets/../secret.txt",
		"/assets/%2e%2e/secret.txt",
		"/assets/..%2fsecret.txt",
		"/assets/a.txt%00",
	} {
		fctx := doStatic(app, "GET", uri)
		if strings.Contains(string(fctx.Response.Body()), "top secret") {
			t.Errorf("GET %s served a file outside the fs", uri)
		}
		if got := fctx.Response.StatusCode(); got != 404 {
			t.Errorf("GET %s: status %d, want 404", uri, got)
		}
	}
}

func TestHasDotDotSegment(t *testing.T) {
	cases := map[string]bool{
		"":        false,
		"/":       false,
		"/a/b":    false,
		"/..a":    false,
		"/a..":    false,
		"/...":    false,
		"/..":     true,
		"/a/..":   true,
		"/a/../b": true,
		"..":      true,
	}
	for p, want := range cases {
		if got := hasDotDotSegment([]byte(p)); got != want {
			t.Errorf("hasDotDotSegment(%q) = %v, want %v", p, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -run 'StaticFailures|StaticDefaultErrorBody|StaticHeadForAMissing|StaticNeverServes|HasDotDot' -count=1 .`
Expected: FAIL to compile, `undefined: hasDotDotSegment`. (With that stubbed, the funnel tests fail with fasthttp's bodies, e.g. `"Directory index is forbidden"` and status 403.)

- [ ] **Step 3: Write the implementation**

In `static.go`, add `"bytes"` to the imports and replace `serve` with:

```go
// serve is the handler every Static pattern runs.
//
// A NUL byte or a ".." segment is refused before fasthttp sees the path.
// fctx.Path() is already normalised, so this is a second line rather than the
// only one, and it makes rice's answer — a 404 — independent of fasthttp's, which
// is 400 and 500 for these.
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
	case code == fasthttp.StatusNotFound, code == fasthttp.StatusForbidden:
		return ErrNotFound
	case code >= 400:
		return NewHTTPError(code, "")
	}
	return nil
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -run 'Static|HasDotDot|ARegisteredRouteOutranks' -count=1 .`
Expected: PASS. If `TestStaticNeverServesOutsideTheFS` fails on one URI with a status other than 404 (for example a route miss the normaliser produced), print the status and path: a normalised `/secret.txt` matches no route and is already a 404; any 200 is a real escape and must be fixed, not the test.

- [ ] **Step 5: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add static.go static_test.go
git commit -m "rice: a Static failure reaches the ErrorHandler, and .. or NUL is a 404

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: rice owns the directory redirect

**Files:**
- Modify: `static.go` (`static` registers the bare prefix; `staticResult` gains the 302 case; new `redirectToDir`, `writeDirRedirect`)
- Test: `static_test.go`

**Interfaces:**
- Consumes: `staticResult`, `(*App).static` (Tasks 1–2).
- Produces: `func redirectToDir(c *Ctx) error`, `func writeDirRedirect(fctx *fasthttp.RequestCtx)`.

- [ ] **Step 1: Write the failing tests**

Append to `static_test.go`:

```go
func TestStaticRedirectsADirectoryWithoutItsSlash(t *testing.T) {
	cases := []struct {
		prefix, uri, location string
	}{
		{"/assets", "/assets/docs", "/assets/docs/"},
		{"/assets", "/assets/docs?x=1&y=2", "/assets/docs/?x=1&y=2"},
		{"/assets", "/assets/my%20docs", "/assets/my%20docs/"},
		{"/assets", "/assets", "/assets/"},
		{"/assets", "/assets?x=1", "/assets/?x=1"},
		{"", "/docs", "/docs/"},
	}
	for _, tc := range cases {
		app := New()
		app.Static(tc.prefix, staticFS())
		app.Build()

		for _, method := range []string{"GET", "HEAD"} {
			fctx := doStatic(app, method, tc.uri)
			if got := fctx.Response.StatusCode(); got != 301 {
				t.Errorf("%s %s: status %d, want 301", method, tc.uri, got)
			}
			if got := string(fctx.Response.Header.Peek("Location")); got != tc.location {
				t.Errorf("%s %s: Location %q, want %q", method, tc.uri, got, tc.location)
			}
		}
	}
}

func TestStaticRedirectNeverLeavesTheSite(t *testing.T) {
	m := staticFS()
	m["evil.example/index.html"] = &fstest.MapFile{Data: []byte("x"), ModTime: staticModTime}
	app := New()
	app.Static("", m)
	app.Build()

	fctx := doStatic(app, "GET", "//evil.example")
	loc := string(fctx.Response.Header.Peek("Location"))
	if strings.HasPrefix(loc, "//") {
		t.Errorf("Location %q is protocol-relative: it leaves the site", loc)
	}
}

func TestStaticRedirectFollowedServesTheIndex(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()

	loc := string(doStatic(app, "GET", "/assets/docs").Response.Header.Peek("Location"))
	fctx := doStatic(app, "GET", loc)
	if got := string(fctx.Response.Body()); got != "docs" {
		t.Errorf("following %q: body %q, want %q", loc, got, "docs")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -run 'StaticRedirect' -count=1 .`
Expected: FAIL. `/assets/docs` answers 302 with `Location: http:///docs/` (fasthttp's, prefix lost); `/assets` answers 404 (no pattern).

- [ ] **Step 3: Write the implementation**

In `static.go`, in `(*App).static`, register the bare prefix inside the method loop, before the other two:

```go
	for _, method := range [...]string{"GET", "HEAD"} {
		if prefix != "" {
			// The bare prefix is a directory without its slash.
			a.register(method, prefix, redirectToDir, g, mw)
		}
		a.register(method, prefix+"/", e.serve, g, mw)
		a.register(method, prefix+"/*filepath", e.serve, g, mw)
	}
```

and update its doc comment to: `// static registers GET and HEAD for prefix, prefix+"/" and prefix+"/*filepath". A wildcard captures at least one byte, so the fs root needs its own pattern, and the bare prefix redirects to it.`

In `staticResult`, add the first case of the switch:

```go
	case code == fasthttp.StatusFound:
		// fasthttp's only 302 is a directory without its slash, and its
		// Location is built from the rewritten path, so it has lost the prefix.
		writeDirRedirect(fctx)
		return nil
```

Add below `staticResult`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -run 'Static|HasDotDot|ARegisteredRouteOutranks' -count=1 .`
Expected: PASS. If `TestStaticRedirectNeverLeavesTheSite` fails, print `fctx.URI().Path()`: the test depends on fasthttp's normalisation collapsing `//`, and a failure means `writeDirRedirect` must collapse a leading `//` itself — fix the code, not the test.

- [ ] **Step 5: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add static.go static_test.go
git commit -m "rice: a directory without its slash redirects with its prefix kept

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Shutdown stops the cache goroutines

**Files:**
- Modify: `static.go` (new `stopStatic`), `lifecycle.go` (`runShutdown`)
- Test: `static_test.go`

**Interfaces:**
- Consumes: `staticEntry.stop` (Task 1), `(*App).runShutdown` (lifecycle.go), `(*App).Shutdown`.
- Produces: `func (a *App) stopStatic()`.

- [ ] **Step 1: Write the failing tests**

Append to `static_test.go` (add `"context"` and `"runtime"` to its imports):

```go
// cacheGoroutines counts fasthttp.FS cache goroutines by their frame, so the
// count is exact whatever else the process runs. Tests before these build Apps
// they never shut down, whose goroutines never exit, so every check is a delta
// from a count taken at the start.
func cacheGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "(*inMemoryCacheManager).handleCleanCache(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// cacheGoroutinesSettleAt polls, because a goroutine told to stop is still
// listed until it has been scheduled to return.
func cacheGoroutinesSettleAt(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for cacheGoroutines() != want {
		if time.Now().After(deadline) {
			t.Fatalf("fasthttp.FS cache goroutines: %d, want %d", cacheGoroutines(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStaticStartsNoGoroutineUntilBuild(t *testing.T) {
	base := cacheGoroutines()
	app := New()
	app.Static("/a", staticFS())
	app.Static("/b", staticFS())
	cacheGoroutinesSettleAt(t, base)

	app.Build()
	cacheGoroutinesSettleAt(t, base+2)

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	cacheGoroutinesSettleAt(t, base)
}

func TestStaticShutdownTwiceDoesNotPanic(t *testing.T) {
	app := New()
	app.Static("/a", staticFS())
	app.Build()
	for i := 0; i < 2; i++ {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStaticShutdownOfAnUnbuiltAppDoesNotPanic(t *testing.T) {
	app := New()
	app.Static("/a", staticFS())
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -run 'StaticStartsNoGoroutine|StaticShutdown' -count=1 .`
Expected: `TestStaticStartsNoGoroutineUntilBuild` FAILS at the last check with `fasthttp.FS cache goroutines: <base+2>, want <base>`. The other two pass already; they guard the implementation against a double close and a nil channel.

- [ ] **Step 3: Write the implementation**

Append to `static.go`:

```go
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
```

In `lifecycle.go`, in `runShutdown`, make `a.stopStatic()` the first statement inside `a.shutdownOnce.Do(func() { ... })`, before `defer close(a.shutdownDone)`:

```go
	a.shutdownOnce.Do(func() {
		a.stopStatic()
		defer close(a.shutdownDone)
		close(a.hooksStarted)
```

and add one sentence to `runShutdown`'s doc comment: `It first stops the Static routes' cache goroutines.`

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -run 'StaticStartsNoGoroutine|StaticShutdown' -count=5 .`
Expected: PASS on every run.

- [ ] **Step 5: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add static.go static_test.go lifecycle.go
git commit -m "rice: Shutdown stops each Static route's cache goroutine after the drain

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Budgets and a benchmark

**Files:**
- Modify: `alloc_test.go`, `bench/rice_bench_test.go`
- Create: `bench/results/post-M8-static-files.txt` (by the record step)

**Interfaces:**
- Consumes: `App.Static`, `staticFS`, `discardLogger` (Tasks 1–2), `budget` (budget_test.go).
- Produces: `TestAllocBudgetStaticFile`, `TestAllocBudgetStatic404`, `BenchmarkStaticSmallFile`. Two measured figures that Task 6 writes into the docs.

- [ ] **Step 1: Measure**

Create a temporary file `static_measure_test.go`:

```go
//go:build !ricedebug

package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

func TestMeasureStatic(t *testing.T) {
	for _, uri := range []string{"/assets/a.txt", "/assets/missing"} {
		app := New()
		app.Static("/assets", staticFS())
		app.Build()
		var req fasthttp.Request
		req.Header.SetMethod("GET")
		req.SetRequestURI(uri)
		fctx := &fasthttp.RequestCtx{}
		fctx.Init(&req, nil, discardLogger{})
		fn := func() {
			fctx.Response.Reset()
			app.handle(fctx)
		}
		fn()
		t.Logf("%s: %.2f allocs/op", uri, testing.AllocsPerRun(1000, fn))
	}
}
```

Run both:

```bash
go test -run TestMeasureStatic -count=1 -v .
go test -run TestMeasureStatic -count=1 -v -race .
```

Record the four figures. For each URI, the budget is the larger of its two figures, rounded up to an integer. Then delete the file: `rm static_measure_test.go`. No `want` below is ever edited to match a later run; a budget that fails later is a regression to explain.

- [ ] **Step 2: Write the budgets**

Append to `alloc_test.go`, writing the measured integers where the comments say:

```go
// staticFctx returns a request context for a static budget. Init gives it a
// logger, which fasthttp.FS needs on its miss path.
func staticFctx(uri string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod("GET")
	req.SetRequestURI(uri)
	fctx := &fasthttp.RequestCtx{}
	fctx.Init(&req, nil, discardLogger{})
	return fctx
}

// TestAllocBudgetStaticFile pins a GET for a small file already in fasthttp's
// handle cache. Static files are outside the zero-allocation claim; this is a
// regression guard, not a target. Measured on darwin arm64, with and without
// -race. The response is reset between calls, as fasthttp's server does.
func TestAllocBudgetStaticFile(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()
	fctx := staticFctx("/assets/a.txt")

	const want float64 = /* measured figure for /assets/a.txt */
	budget(t, "static file from the handle cache", want, func() {
		fctx.Response.Reset()
		app.handle(fctx)
	})
}

// TestAllocBudgetStatic404 pins a missing file through a Static route, the path
// a scanner exercises: fasthttp's failed open and its log line, then the funnel.
func TestAllocBudgetStatic404(t *testing.T) {
	app := New()
	app.Static("/assets", staticFS())
	app.Build()
	fctx := staticFctx("/assets/missing")

	const want float64 = /* measured figure for /assets/missing */
	budget(t, "static 404", want, func() {
		fctx.Response.Reset()
		app.handle(fctx)
	})
}
```

Replace each `/* measured figure … */` with the integer from Step 1 before running anything; the file does not compile until you do.

- [ ] **Step 3: Run the budgets**

Run: `go test -run 'AllocBudgetStatic' -count=3 . && go test -run 'AllocBudgetStatic' -count=3 -race .`
Expected: PASS on every run.

- [ ] **Step 4: Write the benchmark**

Append to `bench/rice_bench_test.go` (add `"testing/fstest"` to its imports):

```go
// BenchmarkStaticSmallFile measures a GET for a 3-byte file through Static, with
// the file already in fasthttp.FS's handle cache. Static files are outside the
// zero-allocation claim; this records what they cost. The response is reset
// each iteration, as fasthttp's server does between requests.
func BenchmarkStaticSmallFile(b *testing.B) {
	app := rice.New()
	app.Static("/assets", fstest.MapFS{"a.txt": {Data: []byte("hi\n")}})

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/assets/a.txt")
	h(fctx) // warm: opens the file and fills the cache
	if fctx.Response.StatusCode() != 200 {
		b.Fatalf("warm-up status %d, want 200", fctx.Response.StatusCode())
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fctx.Response.Reset()
		h(fctx)
	}
}
```

- [ ] **Step 5: Run the benchmark once**

Run: `go test ./bench -run '^$' -bench BenchmarkStaticSmallFile -benchmem -count=1`
Expected: one result line; its allocs/op agrees with `TestAllocBudgetStaticFile`'s figure.

- [ ] **Step 6: Record the suite**

Run: `make bench-record LABEL=post-M8-static-files`
Expected: `wrote bench/results/post-M8-static-files.txt`. Note the median ns/op, B/op and allocs/op of `BenchmarkStaticSmallFile` over the ten runs for Task 6.

- [ ] **Step 7: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add alloc_test.go bench/rice_bench_test.go bench/results/post-M8-static-files.txt
git commit -m "rice: pin the Static budgets and record a small-file benchmark

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Documentation

**Files:**
- Create: `docs/adr/0017-static-files-wrap-fasthttp-fs.md`
- Modify: `docs/adr/README.md`, `docs/03-core-concepts.md` (end of section 4, before `## 5. Group`; and the Group signature block), `docs/05-performance-model.md` (*What "zero allocations" excludes* and *Request path — dispatch*), `docs/04-roadmap.md` (*Explicitly deferred* and *Done after M8*), `docs/progress.md` (new entry at the top), `README.md`

**Interfaces:**
- Consumes: the figures from Task 5; every behaviour from Tasks 1–4.
- Produces: nothing code depends on.

- [ ] **Step 1: Write ADR-0017**

Create `docs/adr/0017-static-files-wrap-fasthttp-fs.md`, in the shape of the existing ADRs:

```markdown
# ADR-0017 — Static files wrap fasthttp.FS behind the error funnel

Status: Accepted
Date: 2026-09-25

## Context

Static file serving was on the roadmap's *Explicitly deferred* list. The owner wanted one API for
files on disk and files embedded in the binary, a directory answered by its `index.html`, and no
SPA fallback. fasthttp v1.73 ships a file server, `fasthttp.FS`, that accepts an `fs.FS` and
already answers byte ranges, `If-Modified-Since` and HEAD, with a cache of open file handles.

Probing it behind a prefix found six things (the design's *What probing found*): it writes its
own error responses with `ctx.Error`, so a JSON ErrorHandler never sees them; its directory
redirect is built from the rewritten path and loses the prefix; its root must be `""`, not `"."`;
its handle cache runs a goroutine until `CleanStop` is closed; it logs every miss through the
server's logger; and with `Compress` it writes compressed copies of files to disk.

## Decision

`App.Static(prefix, fsys, mw...)` and `Group.Static` wrap one `fasthttp.FS` per call, created in
`Build`, and rice owns every edge fasthttp gets wrong for it:

- **Errors.** After fasthttp's handler returns, rice reads the status it wrote. 404, and the 403
  for a directory without an index, become `ErrNotFound`; any other status ≥ 400 becomes an
  `HTTPError` with that code. The funnel's `respond` overwrites fasthttp's body.
- **Traversal.** A NUL byte or a `..` segment is a 404 before fasthttp sees the path.
- **Directory redirect.** fasthttp's 302 is replaced by a 301 whose relative `Location` is the
  request's own percent-encoded path plus `/`, query kept.
- **Lifecycle.** `Shutdown` closes every `CleanStop` after the drain and before the `OnShutdown`
  hooks.
- **Configuration is fixed:** `index.html` only, no listings, ranges on, compression off.

## Alternatives

**Implement file serving on `fs.FS` directly.** Full control, no goroutine, errors native to the
funnel. Rejected because byte ranges and conditional requests are exactly the code where a new
implementation grows bugs, and fasthttp's is exercised by its users already. Nothing rice needs
to control lies inside that code; everything it needs to control lies at its edges.

**Stat each path first, then hand only files to fasthttp.** Makes a 404 and a directory rice's
own decision before fasthttp runs, and silences fasthttp's miss log. Rejected because it costs a
stat per request and bypasses the handle cache that is the reason for reusing fasthttp.

**Let fasthttp's responses stand.** Rejected: an App with a JSON ErrorHandler would answer every
static failure in plain text, and the directory redirect would send clients out of the prefix.

## Consequences

**The redirect does not contradict ADR-0007.** ADR-0007 refuses to treat `/users` and `/users/`
as one route. A directory without its slash is one resource whose relative links resolve wrongly;
redirecting it is the resource's semantics, as `net/http.FileServer` also does.

**Mapping by status depends on fasthttp's behaviour.** Each row of the mapping has a test, so an
upgrade that changes a status fails a test rather than a client.

**fasthttp's miss log is accepted.** Each missing file logs one line through the server's logger.
Silencing it needs a stat per request or replacing fasthttp's logger for everything; an
application that cares sets `fasthttp.Server.Logger` when mounting, or puts a proxy in front.

**A mounted App keeps its goroutines.** An App used through `FasthttpHandler` and never shut down
keeps one goroutine per `Static` call until the process exits.

**Static files are outside the zero-allocation claim.** Their cost is pinned in
[05-performance-model.md](../05-performance-model.md).
```

- [ ] **Step 2: List it**

In `docs/adr/README.md`, add a row for ADR-0017 after ADR-0016, in the existing row format.

- [ ] **Step 3: Core concepts**

In `docs/03-core-concepts.md`, add at the end of section 4 (App), before `## 5. Group`:

````markdown
### Serving static files

```go
func (a *App) Static(prefix string, fsys fs.FS, mw ...Middleware)
func (g *Group) Static(prefix string, fsys fs.FS, mw ...Middleware)
```

`Static("/assets", fsys)` registers GET and HEAD for `/assets`, `/assets/` and
`/assets/*filepath`. `fsys` is `os.DirFS` for a directory on disk or an `embed.FS` (through
`fs.Sub`) for files compiled in. A directory is answered by its `index.html`; one without an index
is a 404 and nothing is ever listed. A directory requested without its slash answers 301 to the
same path with the slash. Byte ranges and `If-Modified-Since` are answered; nothing is compressed.
Every failure reaches the ErrorHandler. Routes registered separately under the prefix outrank a
file of the same name. Headers such as `Cache-Control` come from middleware passed in `mw`. See
[ADR-0017](adr/0017-static-files-wrap-fasthttp-fs.md).
````

and add `func (g *Group) Static(prefix string, fsys fs.FS, mw ...Middleware)` to the Group signature block in section 5.

- [ ] **Step 4: Performance model**

In `docs/05-performance-model.md`, add to the *What "zero allocations" excludes* list:

```markdown
- Static files served by `Static`. They are fasthttp's file server behind rice's routing; their
  figures are pinned under [Request path — dispatch](#request-path--dispatch).
```

and add two rows to the table under *Request path — dispatch*, in its existing column format, with the figures from Task 5: `Static`, a small file from the handle cache → `TestAllocBudgetStaticFile`; `Static`, a missing file → `TestAllocBudgetStatic404`. Name `BenchmarkStaticSmallFile` and `bench/results/post-M8-static-files.txt` beside them.

- [ ] **Step 5: Roadmap**

In `docs/04-roadmap.md`, remove `- Static file serving` from *Explicitly deferred*, and append a bullet to *Done after M8*:

```markdown
- `App.Static` and `Group.Static`, serving an `fs.FS` — a directory through `os.DirFS` or files
  compiled in through `embed.FS` — for GET and HEAD. Probing found that fasthttp's file server
  answers its own errors, loses a prefix in its directory redirect and runs a cache goroutine
  until told to stop, so rice wraps it and owns each edge: every failure reaches the ErrorHandler,
  the redirect is rice's 301, and Shutdown stops the goroutine
  ([ADR-0017](adr/0017-static-files-wrap-fasthttp-fs.md)).
```

- [ ] **Step 6: Progress entry**

Add at the top of `docs/progress.md`, below the `---` that follows the entry template, an entry `## 2026-09-25 — post-M8 — Static files wrap fasthttp.FS` with the four headings:

- **Did:** the API; the three patterns for GET and HEAD; one `fasthttp.FS` per call built in `Build`; the status mapping; the NUL and `..` check; the 301; `Shutdown` stopping the goroutine; the test names by group (serving, funnel, traversal, redirect, lifecycle); the two budgets; ADR-0017 and the documents changed.
- **Learned:** three numbered items, each a finding that changed the design: fasthttp's redirect is built from the rewritten path; `Root: "."` breaks the root index; `Compress` writes to disk. Add any surprise met while executing this plan.
- **Measured:** the two budgets' figures with and without `-race`, `BenchmarkStaticSmallFile`'s median ns/op, B/op and allocs/op, the machine and Go version from the results file header, and the root package's coverage from `make cover`.
- **Next:** content negotiation, the next deferred item, which needs its own brainstorm.

- [ ] **Step 7: README**

In `README.md`, beside the existing routing example, add:

````markdown
```go
//go:embed web
var web embed.FS

sub, _ := fs.Sub(web, "web")
app.Static("/assets", sub)                // embedded files
app.Static("/uploads", os.DirFS("data"))  // a directory on disk
```
````

- [ ] **Step 8: Verify and commit**

Run: `make test && make test-debug && make lint && make cover`
Expected: PASS; coverage of the root package not below 99.4%.

```bash
git add docs README.md
git commit -m "docs: ADR-0017 and the docs for static file serving

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```
