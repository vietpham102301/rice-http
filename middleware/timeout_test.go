package middleware_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// waitDone blocks until the request's context is done and returns its error.
// It gives up after two seconds so that a deadline which never fires fails the
// test instead of hanging it.
func waitDone(c *rice.Ctx) error {
	select {
	case <-c.Context().Done():
		return c.Context().Err()
	case <-time.After(2 * time.Second):
		return errors.New("test bug: the deadline never fired")
	}
}

// timedApp returns an App with Timeout(d) installed and h on GET /t.
func timedApp(d time.Duration, h rice.Handler) *rice.App {
	app := rice.New()
	app.Use(middleware.Timeout(d))
	app.GET("/t", h)
	return app
}

// send dispatches GET /t and returns the status and body the client received.
func send(app *rice.App) (int, string) {
	fctx := newRequest("GET", "/t", nil)
	app.FasthttpHandler()(fctx)
	return fctx.Response.StatusCode(), string(fctx.Response.Body())
}

func TestTimeoutAnswers503WhenTheHandlerHonoursTheDeadline(t *testing.T) {
	app := timedApp(10*time.Millisecond, func(c *rice.Ctx) error { return waitDone(c) })

	status, body := send(app)

	if status != 503 {
		t.Errorf("status = %d, want 503", status)
	}
	if body != "Service Unavailable" {
		t.Errorf("body = %q, want the status text alone", body)
	}
}

// driverError stands in for a database driver that reports a timeout in its
// own error type and does not unwrap to context.DeadlineExceeded.
type driverError struct{}

func (driverError) Error() string { return "driver: query interrupted" }

// TestTimeoutAsksItsContextNotTheError is why Timeout checks its own
// context's Err instead of calling errors.Is on the returned error: many
// drivers wrap a timeout without unwrapping to context.DeadlineExceeded, and
// errors.Is would miss exactly the case a timeout exists for.
func TestTimeoutAsksItsContextNotTheError(t *testing.T) {
	app := timedApp(10*time.Millisecond, func(c *rice.Ctx) error {
		_ = waitDone(c)
		return driverError{}
	})

	status, body := send(app)

	if status != 503 {
		t.Errorf("status = %d, want 503 for a driver error returned after the deadline", status)
	}
	if strings.Contains(body, "driver") {
		t.Errorf("body = %q, want the driver's error kept out of the response", body)
	}
}

// TestTimeoutKeepsAnHTTPErrorTheHandlerChose follows the rule binding set for
// Validate: an author who chose a status keeps it.
func TestTimeoutKeepsAnHTTPErrorTheHandlerChose(t *testing.T) {
	app := timedApp(10*time.Millisecond, func(c *rice.Ctx) error {
		_ = waitDone(c)
		return rice.NewHTTPError(400, "bad input")
	})

	if status, _ := send(app); status != 400 {
		t.Errorf("status = %d, want the handler's own 400", status)
	}
}

// TestTimeoutDoesNotStopAHandlerThatIgnoresTheContext pins two statements at
// once. D3: when the deadline passes but the handler still succeeds, its
// answer is returned. D6, the documented limitation: Timeout is a deadline the
// work is asked to honour, not a switch that cuts it off, so a handler that
// ignores its context runs to completion. If this ever starts failing because
// Timeout begins to cut handlers off, the documentation must change with it.
func TestTimeoutDoesNotStopAHandlerThatIgnoresTheContext(t *testing.T) {
	app := timedApp(10*time.Millisecond, func(c *rice.Ctx) error {
		time.Sleep(60 * time.Millisecond) // ignores the deadline entirely
		return c.String(200, "late but done")
	})

	status, body := send(app)

	if status != 200 || body != "late but done" {
		t.Errorf("got %d %q, want the handler's own late 200", status, body)
	}
}

// TestTimeoutLeavesACancellationAlone covers a context cancelled rather than
// timed out — by ADR-0010's force-close, or by a middleware outside this one.
// That is not this middleware's timeout, so the error is not rewritten.
func TestTimeoutLeavesACancellationAlone(t *testing.T) {
	app := rice.New()
	app.Use(
		func(next rice.Handler) rice.Handler {
			return func(c *rice.Ctx) error {
				ctx, cancel := context.WithCancel(c.Context())
				cancel() // cancelled before Timeout ever runs
				c.SetContext(ctx)
				return next(c)
			}
		},
		middleware.Timeout(time.Second),
	)
	app.GET("/t", func(c *rice.Ctx) error { return waitDone(c) })

	if status, _ := send(app); status == 503 {
		t.Error("status = 503 for a cancelled context, want the cancellation left alone")
	}
}

// TestTimeoutRestoresThePreviousContext keeps an outer middleware — Logger,
// which hands c.Context() to slog — from receiving a context that has already
// expired.
func TestTimeoutRestoresThePreviousContext(t *testing.T) {
	var before, after context.Context
	app := rice.New()
	app.Use(
		func(next rice.Handler) rice.Handler {
			return func(c *rice.Ctx) error {
				before = c.Context()
				err := next(c)
				after = c.Context()
				return err
			}
		},
		middleware.Timeout(10*time.Millisecond),
	)
	app.GET("/t", func(c *rice.Ctx) error { return waitDone(c) })

	send(app)

	if after != before {
		t.Error("the context outside Timeout changed; want the previous one restored")
	}
}

// TestTimeoutReleasesItsTimer pins that cancel runs when the chain returns,
// not when the deadline eventually passes: a context the handler captured is
// already done the moment the request finishes, an hour before its deadline.
func TestTimeoutReleasesItsTimer(t *testing.T) {
	var captured context.Context
	app := timedApp(time.Hour, func(c *rice.Ctx) error {
		captured = c.Context()
		return c.String(200, "quick")
	})

	send(app)

	if captured.Err() == nil {
		t.Error("the handler's context is still live after the request; cancel did not run")
	}
}

// TestANestedTimeoutOnlyShortens pins that a route's Timeout inside an
// application-wide one cannot extend it: a context's deadline is never later
// than its parent's.
func TestANestedTimeoutOnlyShortens(t *testing.T) {
	app := rice.New()
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.GET("/t", func(c *rice.Ctx) error { return waitDone(c) }, middleware.Timeout(time.Hour))

	start := time.Now()
	status, _ := send(app)

	if status != 503 {
		t.Errorf("status = %d, want 503", status)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("the request took %v, want the application-wide 20ms deadline to win", elapsed)
	}
}

func TestLoggerOutsideTimeoutRecords503(t *testing.T) {
	var buf bytes.Buffer
	app := rice.New()
	app.Use(
		middleware.Logger(slog.New(slog.NewJSONHandler(&buf, nil))),
		middleware.Timeout(10*time.Millisecond),
	)
	app.GET("/t", func(c *rice.Ctx) error { return waitDone(c) })

	line := one(t, logRequest(t, &buf, app, "GET", "/t", "", ""))

	if line["status"] != float64(503) {
		t.Errorf("logged status = %v, want 503", line["status"])
	}
}

func TestTimeoutPanicsOnANonPositiveDuration(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		func() {
			defer func() {
				r := recover()
				s, ok := r.(string)
				if !ok || !strings.HasPrefix(s, "rice: ") {
					t.Errorf("Timeout(%v) panicked with %v, want a string starting %q", d, r, "rice: ")
				}
			}()
			middleware.Timeout(d)
		}()
	}
}
