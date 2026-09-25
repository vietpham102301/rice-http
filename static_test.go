package rice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
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

// cacheGoroutines counts fasthttp.FS cache goroutines by their frame, so the
// count is exact whatever else the process runs. The count is a snapshot at the
// moment it is called; it may change afterwards, for example when an unreachable
// App is garbage collected. fasthttp registers a runtime cleanup that stops the
// cache goroutine at that time.
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

// stableCacheGoroutines returns the cache goroutine count once it stabilizes
// after garbage collection. fasthttp stops an unreachable FS's cache goroutine
// through a runtime cleanup that runs asynchronously after a GC, and earlier
// tests in this package drop built Apps. This helper turns automatic GC off for
// the test and waits until explicit GCs stop changing the count, allowing all
// queued cleanups to run before returning the baseline.
func stableCacheGoroutines(t *testing.T) int {
	t.Helper()
	old := debug.SetGCPercent(-1)
	t.Cleanup(func() { debug.SetGCPercent(old) })

	deadline := time.Now().Add(3 * time.Second)
	prev := -1
	for {
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
		curr := cacheGoroutines()
		if curr == prev {
			return curr
		}
		prev = curr
		if time.Now().After(deadline) {
			t.Fatalf("cache goroutine count did not stabilize after 3s: last read %d", prev)
		}
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
	base := stableCacheGoroutines(t)
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
	runtime.KeepAlive(app)
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

// setHeader is middleware that sets one response header before calling next.
func setHeader(name, value string) Middleware {
	return func(next Handler) Handler {
		return func(c *Ctx) error {
			c.SetHeader(name, value)
			return next(c)
		}
	}
}

func TestStaticKeepsMiddlewareHeadersOnEveryResponse(t *testing.T) {
	app := New()
	app.Use(setHeader("Access-Control-Allow-Origin", "*"), setHeader("X-Request-Id", "r1"))
	app.Static("/assets", staticFS(), setHeader("Cache-Control", "max-age=60"))
	app.Build()

	since := string(fasthttp.AppendHTTPDate(nil, staticModTime))
	cases := []struct {
		name    string
		uri     string
		headers []string
		code    int
	}{
		{"file", "/assets/a.txt", nil, 200},
		{"not modified", "/assets/a.txt", []string{"If-Modified-Since", since}, 304},
		{"missing file", "/assets/missing", nil, 404},
		{"directory without index", "/assets/nodir/", nil, 404},
		{"unsatisfiable range", "/assets/a.txt", []string{"Range", "bytes=99-"}, 416},
		{"directory without its slash", "/assets/docs", nil, 301},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fctx := doStatic(app, "GET", tc.uri, tc.headers...)
			if got := fctx.Response.StatusCode(); got != tc.code {
				t.Errorf("status %d, want %d", got, tc.code)
			}
			for name, want := range map[string]string{
				"Access-Control-Allow-Origin": "*",
				"X-Request-Id":                "r1",
				"Cache-Control":               "max-age=60",
			} {
				if got := string(fctx.Response.Header.Peek(name)); got != want {
					t.Errorf("%s %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestStaticRedirectIgnoresTheRawPath(t *testing.T) {
	cases := []struct{ prefix, uri, location string }{
		{"/assets", "//evil.example/../assets/docs", "/assets/docs/"},
		{"", "//evil.example/../docs", "/docs/"},
	}
	for _, tc := range cases {
		app := New()
		app.Static(tc.prefix, staticFS())
		app.Build()

		// An outer handler may turn normalising off; routing still uses the
		// normalised path, but the raw one is what RequestURI returns. The Host
		// header keeps fasthttp from reading "//evil.example" as an authority,
		// as a server's request would.
		var req fasthttp.Request
		req.Header.SetMethod("GET")
		req.Header.SetHost("site.example")
		req.SetRequestURI(tc.uri)
		req.URI().DisablePathNormalizing = true
		if !strings.Contains(string(req.URI().RequestURI()), "evil.example") {
			t.Fatalf("%s: precondition: RequestURI %q is normalised", tc.uri, req.URI().RequestURI())
		}
		fctx := &fasthttp.RequestCtx{}
		fctx.Init(&req, nil, discardLogger{})
		app.handle(fctx)

		if got := fctx.Response.StatusCode(); got != 301 {
			t.Errorf("GET %s: status %d, want 301", tc.uri, got)
		}
		loc := string(fctx.Response.Header.Peek("Location"))
		if strings.HasPrefix(loc, "//") || strings.Contains(loc, "evil.example") {
			t.Errorf("GET %s: Location %q leaves the site", tc.uri, loc)
		}
		if loc != tc.location {
			t.Errorf("GET %s: Location %q, want %q", tc.uri, loc, tc.location)
		}
	}
}

func TestStaticBuildAfterShutdownStartsNoGoroutine(t *testing.T) {
	base := stableCacheGoroutines(t)
	app := New()
	app.Static("/a", staticFS())
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.Build()
	cacheGoroutinesSettleAt(t, base)

	// Nothing would ever stop a goroutine started now, so the route answers
	// as an unbuilt one does.
	if got := doStatic(app, "GET", "/a/a.txt").Response.StatusCode(); got != 500 {
		t.Errorf("status %d, want 500", got)
	}
	runtime.KeepAlive(app)
}
