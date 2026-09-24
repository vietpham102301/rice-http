package rice

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
