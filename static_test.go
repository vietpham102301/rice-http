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
