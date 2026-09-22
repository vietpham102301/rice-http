package middleware_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// logRequest dispatches one request through app and returns every log line
// the Logger wrote, each decoded from JSON. peer, when not empty, is the
// connection's address; xff, when not empty, is an X-Forwarded-For value.
func logRequest(t *testing.T, buf *bytes.Buffer, app *rice.App, method, uri, peer, xff string) []map[string]any {
	t.Helper()
	buf.Reset()
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	fctx := &fasthttp.RequestCtx{}
	if peer != "" {
		fctx.Init(&req, &net.TCPAddr{IP: net.ParseIP(peer), Port: 4000}, nil)
	} else {
		req.CopyTo(&fctx.Request)
	}
	app.FasthttpHandler()(fctx)

	var lines []map[string]any
	for _, l := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(l) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatalf("log line is not JSON: %v: %s", err, l)
		}
		lines = append(lines, m)
	}
	return lines
}

// loggedApp returns an App with Logger outermost and Recover inside it — the
// recommended order — and routes that end in each kind of outcome.
func loggedApp(buf *bytes.Buffer, opts ...rice.Option) *rice.App {
	app := rice.New(opts...)
	app.Use(
		middleware.Logger(slog.New(slog.NewJSONHandler(buf, nil))),
		middleware.Recover(),
		middleware.RealIP(1),
		middleware.RequestID(),
	)
	app.GET("/ok", func(c *rice.Ctx) error { return c.String(200, "ok") })
	app.GET("/teapot", func(c *rice.Ctx) error { return rice.NewHTTPError(418, "teapot") })
	app.GET("/boom", func(c *rice.Ctx) error { return errors.New("db down") })
	app.GET("/panic", func(c *rice.Ctx) error { panic("handler exploded") })
	return app
}

// one returns the single line a request produced, failing if there is not
// exactly one.
func one(t *testing.T, lines []map[string]any) map[string]any {
	t.Helper()
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want exactly 1: %v", len(lines), lines)
	}
	return lines[0]
}

func TestLoggerRecordsTheRequest(t *testing.T) {
	var buf bytes.Buffer
	line := one(t, logRequest(t, &buf, loggedApp(&buf), "GET", "/ok", "10.0.0.2", "203.0.113.9"))

	for key, want := range map[string]any{
		"method": "GET",
		"path":   "/ok",
		"status": float64(200),
		"ip":     "203.0.113.9",
		"level":  "INFO",
	} {
		if line[key] != want {
			t.Errorf("%s = %v, want %v", key, line[key], want)
		}
	}
	if _, ok := line["latency"]; !ok {
		t.Error("latency is missing")
	}
	if id, _ := line["request_id"].(string); !generatedID.MatchString(id) {
		t.Errorf("request_id = %v, want a generated id", line["request_id"])
	}
}

// TestLoggerRecordsAMissAs404 is the end-to-end test of both core changes. Before
// them a Logger did not run on a miss at all, and would have recorded 200 if it
// had.
func TestLoggerRecordsAMissAs404(t *testing.T) {
	var buf bytes.Buffer
	line := one(t, logRequest(t, &buf, loggedApp(&buf), "GET", "/nope", "", ""))

	if line["status"] != float64(404) {
		t.Errorf("status = %v, want 404", line["status"])
	}
}

func TestLoggerRecordsTheStatusTheFunnelWrote(t *testing.T) {
	var buf bytes.Buffer
	app := loggedApp(&buf)
	for path, want := range map[string]float64{"/teapot": 418, "/boom": 500} {
		if got := one(t, logRequest(t, &buf, app, "GET", path, "", ""))["status"]; got != want {
			t.Errorf("%s: status = %v, want %v", path, got, want)
		}
	}
}

// TestLoggerRecordsACustomErrorHandlersStatus is what distinguishes
// HandleError from a function that maps errors to statuses the way the default
// handler would: the log records what the App's real ErrorHandler answered.
func TestLoggerRecordsACustomErrorHandlersStatus(t *testing.T) {
	var buf bytes.Buffer
	app := loggedApp(&buf, rice.WithErrorHandler(func(c *rice.Ctx, err error) {
		_ = c.String(503, "custom")
	}))

	if got := one(t, logRequest(t, &buf, app, "GET", "/teapot", "", ""))["status"]; got != float64(503) {
		t.Errorf("status = %v, want the custom handler's 503", got)
	}
}

func TestLoggerDoesNotRecordTheQueryString(t *testing.T) {
	var buf bytes.Buffer
	line := one(t, logRequest(t, &buf, loggedApp(&buf), "GET", "/ok?token=s3cret", "", ""))

	if line["path"] != "/ok" {
		t.Errorf("path = %v, want /ok", line["path"])
	}
	if strings.Contains(buf.String(), "s3cret") {
		t.Errorf("the query string reached the log: %s", buf.String())
	}
}

func TestLoggerLevelsByStatus(t *testing.T) {
	var buf bytes.Buffer
	app := loggedApp(&buf)
	for path, want := range map[string]string{"/ok": "INFO", "/teapot": "INFO", "/boom": "ERROR"} {
		if got := one(t, logRequest(t, &buf, app, "GET", path, "", ""))["level"]; got != want {
			t.Errorf("%s: level = %v, want %v", path, got, want)
		}
	}
}

func TestLoggerRecordsAPanicAs500WithRecoverInside(t *testing.T) {
	var buf bytes.Buffer
	if got := one(t, logRequest(t, &buf, loggedApp(&buf), "GET", "/panic", "", ""))["status"]; got != float64(500) {
		t.Errorf("status = %v, want 500", got)
	}
}

// TestLoggerDoesNotLogAPanicWithoutRecoverInside pins a documented limitation
// rather than a bug. Without Recover beneath it, a panic unwinds through
// Logger's frame before it records anything. If this ever starts passing for a
// different reason, the documentation in middleware/doc.go must change with it.
func TestLoggerDoesNotLogAPanicWithoutRecoverInside(t *testing.T) {
	var buf bytes.Buffer
	app := rice.New()
	app.Use(middleware.Logger(slog.New(slog.NewJSONHandler(&buf, nil))))
	app.GET("/panic", func(c *rice.Ctx) error { panic("handler exploded") })

	lines := logRequest(t, &buf, app, "GET", "/panic", "", "")

	if len(lines) != 0 {
		t.Errorf("got %d log lines, want 0 — the documented limitation no longer holds: %v", len(lines), lines)
	}
}

func TestLoggerPanicsOnANilLogger(t *testing.T) {
	defer func() {
		r := recover()
		s, ok := r.(string)
		if !ok || !strings.HasPrefix(s, "rice: ") {
			t.Errorf("Logger(nil) panicked with %v, want a string starting %q", r, "rice: ")
		}
	}()
	middleware.Logger(nil)
}
