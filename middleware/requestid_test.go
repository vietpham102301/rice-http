package middleware_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

var generatedID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// idApp answers GET /id with the id RequestIDFrom returns.
func idApp() *rice.App {
	app := rice.New()
	app.Use(middleware.RequestID())
	app.GET("/id", func(c *rice.Ctx) error { return c.String(200, middleware.RequestIDFrom(c)) })
	return app
}

// dispatchWithID sends GET /id, with an X-Request-Id header when incoming is
// not empty, and returns the id the handler saw and the response header.
func dispatchWithID(t *testing.T, app *rice.App, incoming string) (seen, header string) {
	t.Helper()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/id")
	if incoming != "" {
		fctx.Request.Header.Set("X-Request-Id", incoming)
	}
	app.FasthttpHandler()(fctx)
	return string(fctx.Response.Body()), string(fctx.Response.Header.Peek("X-Request-Id"))
}

func TestRequestIDGeneratesAnIDWhenNoneArrives(t *testing.T) {
	seen, header := dispatchWithID(t, idApp(), "")

	if !generatedID.MatchString(seen) {
		t.Errorf("generated id = %q, want 32 lowercase hex characters", seen)
	}
	if header != seen {
		t.Errorf("response header = %q, want the same id the handler saw, %q", header, seen)
	}
}

func TestRequestIDKeepsAValidIncomingID(t *testing.T) {
	seen, header := dispatchWithID(t, idApp(), "gw-7f3a_b2")

	if seen != "gw-7f3a_b2" || header != "gw-7f3a_b2" {
		t.Errorf("handler saw %q and header is %q, want the incoming id kept", seen, header)
	}
}

// TestRequestIDReplacesAnUnsafeIncomingID covers the security control. A
// client-supplied id goes into every log line for its request, so one carrying
// a newline would let a client forge log entries.
func TestRequestIDReplacesAnUnsafeIncomingID(t *testing.T) {
	for _, tc := range []struct {
		name, incoming string
	}{
		{"newline", "abc\nFORGED log line"},
		{"too long", strings.Repeat("a", 65)},
		{"space", "has space"},
		{"punctuation", "id;drop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen, header := dispatchWithID(t, idApp(), tc.incoming)

			if !generatedID.MatchString(seen) {
				t.Errorf("id = %q, want a freshly generated one", seen)
			}
			if strings.Contains(header, "FORGED") || strings.Contains(seen, "FORGED") {
				t.Errorf("the unsafe incoming id reached the response: header %q, handler saw %q", header, seen)
			}
		})
	}
}

func TestRequestIDAcceptsExactlySixtyFourCharacters(t *testing.T) {
	id := strings.Repeat("a", 64)
	if seen, _ := dispatchWithID(t, idApp(), id); seen != id {
		t.Errorf("a 64-character id was replaced; want it kept")
	}
}

func TestRequestIDGeneratesADifferentIDEachTime(t *testing.T) {
	app := idApp()
	a, _ := dispatchWithID(t, app, "")
	b, _ := dispatchWithID(t, app, "")
	if a == b {
		t.Errorf("two requests got the same generated id %q", a)
	}
}

func TestRequestIDFromIsEmptyWithoutTheMiddleware(t *testing.T) {
	app := rice.New()
	app.GET("/id", func(c *rice.Ctx) error { return c.String(200, middleware.RequestIDFrom(c)) })

	if seen, _ := dispatchWithID(t, app, ""); seen != "" {
		t.Errorf("RequestIDFrom = %q with no RequestID installed, want empty", seen)
	}
}
