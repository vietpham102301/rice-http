package rice

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestHTTPErrorRendersCodeAndMessage(t *testing.T) {
	e := NewHTTPError(418, "teapot")
	if got, want := e.Error(), "418: teapot"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestHTTPErrorRendersTheStatusTextWhenMessageIsEmpty(t *testing.T) {
	e := NewHTTPError(404, "")
	if got, want := e.Error(), "404: Not Found"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestHTTPErrorAppendsTheCause(t *testing.T) {
	e := &HTTPError{Code: 500, Message: "broken", Err: errors.New("disk on fire")}
	if got, want := e.Error(), "500: broken: disk on fire"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestHTTPErrorUnwrapReachesTheCause pins the property the funnel depends on:
// errors.Is and errors.As must see through an HTTPError to what it wraps.
func TestHTTPErrorUnwrapReachesTheCause(t *testing.T) {
	sentinel := errors.New("underlying")
	e := &HTTPError{Code: 500, Message: "wrapped", Err: sentinel}

	if !errors.Is(e, sentinel) {
		t.Error("errors.Is did not reach the cause through HTTPError")
	}
}

// TestHTTPErrorIsFoundThroughAWrappingError is the other direction: an
// HTTPError buried under fmt.Errorf must still be findable, because that is
// how handlers add context to a failure.
func TestHTTPErrorIsFoundThroughAWrappingError(t *testing.T) {
	wrapped := fmt.Errorf("loading user: %w", NewHTTPError(404, "gone"))

	var he *HTTPError
	if !errors.As(wrapped, &he) {
		t.Fatal("errors.As did not find the HTTPError under fmt.Errorf")
	}
	if he.Code != 404 {
		t.Errorf("Code = %d, want 404", he.Code)
	}
}

func TestPanicErrorNamesTheValue(t *testing.T) {
	e := &PanicError{Value: "boom"}
	if got, want := e.Error(), "panic: boom"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestPanicErrorNamesANonStringValue(t *testing.T) {
	e := &PanicError{Value: 42}
	if got, want := e.Error(), "panic: 42"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestPanicErrorUnwrapReturnsAPanickedError(t *testing.T) {
	sentinel := errors.New("panicked with an error")
	e := &PanicError{Value: sentinel}

	if !errors.Is(e, sentinel) {
		t.Error("errors.Is did not reach a panicked error value")
	}
}

func TestPanicErrorUnwrapReturnsNilForANonError(t *testing.T) {
	e := &PanicError{Value: "just a string"}
	if e.Unwrap() != nil {
		t.Errorf("Unwrap() = %v, want nil", e.Unwrap())
	}
}

// TestCaptureLogSeesWhatWasLogged proves the harness itself works. A test
// helper that silently captures nothing would make every later logging
// assertion pass for the wrong reason.
func TestCaptureLogSeesWhatWasLogged(t *testing.T) {
	logged := captureLog(t)
	log.Printf("rice: a distinctive marker")

	if !strings.Contains(logged(), "a distinctive marker") {
		t.Errorf("captureLog did not capture the log line, got %q", logged())
	}
}

func TestDefaultErrorHandlerAnswersAnHTTPErrorWithItsCode(t *testing.T) {
	c, fctx := newTestCtx("GET", "/")
	DefaultErrorHandler(c, NewHTTPError(418, "teapot"))

	if got := fctx.Response.StatusCode(); got != 418 {
		t.Errorf("status = %d, want 418", got)
	}
	if got := string(fctx.Response.Body()); got != "teapot" {
		t.Errorf("body = %q, want %q", got, "teapot")
	}
}

func TestDefaultErrorHandlerUsesTheStatusTextForAnEmptyMessage(t *testing.T) {
	c, fctx := newTestCtx("GET", "/")
	DefaultErrorHandler(c, NewHTTPError(403, ""))

	if got := string(fctx.Response.Body()); got != "Forbidden" {
		t.Errorf("body = %q, want %q", got, "Forbidden")
	}
}

func TestDefaultErrorHandlerAnswers500ForAnUnrecognisedError(t *testing.T) {
	c, fctx := newTestCtx("GET", "/")
	DefaultErrorHandler(c, errors.New("something went wrong"))

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500", got)
	}
	if got := string(fctx.Response.Body()); got != "Internal Server Error" {
		t.Errorf("body = %q, want %q", got, "Internal Server Error")
	}
}

func TestDefaultErrorHandlerLogsAnUnrecognisedError(t *testing.T) {
	logged := captureLog(t)
	c, _ := newTestCtx("GET", "/")
	DefaultErrorHandler(c, errors.New("the database is on fire"))

	if !strings.Contains(logged(), "the database is on fire") {
		t.Errorf("the cause was not logged, got %q", logged())
	}
}

// TestDefaultErrorHandlerFindsAnHTTPErrorThroughAWrapper is the errors.As
// requirement from the roadmap's exit criteria.
func TestDefaultErrorHandlerFindsAnHTTPErrorThroughAWrapper(t *testing.T) {
	c, fctx := newTestCtx("GET", "/")
	DefaultErrorHandler(c, fmt.Errorf("loading user: %w", NewHTTPError(404, "gone")))

	if got := fctx.Response.StatusCode(); got != 404 {
		t.Errorf("status = %d, want 404", got)
	}
	if got := string(fctx.Response.Body()); got != "gone" {
		t.Errorf("body = %q, want %q", got, "gone")
	}
}

// TestDefaultErrorHandlerFindsAWrappedPanicError is TestHTTPErrorIsFoundThroughAWrapper's
// counterpart for *PanicError. Nothing in rice ever wraps one — handle passes
// a bare &PanicError{} straight to callErrorHandler — but DefaultErrorHandler
// still finds one through an Unwrap chain via errors.As, the slow path behind
// the direct type assertion that handles the unwrapped case for free.
func TestDefaultErrorHandlerFindsAWrappedPanicError(t *testing.T) {
	logged := captureLog(t)
	c, fctx := newTestCtx("GET", "/")

	DefaultErrorHandler(c, fmt.Errorf("ctx: %w", &PanicError{Value: "boom"}))

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500", got)
	}
	if got := string(fctx.Response.Body()); got != "Internal Server Error" {
		t.Errorf("body = %q, want the generic body", got)
	}
	if !strings.Contains(logged(), "boom") {
		t.Errorf("the wrapped panic value was not logged, got %q", logged())
	}
}

// TestAWrappedPanickedHTTPErrorIsStill500 is D5's ordering invariant, exercised
// on the errors.As fallback rather than the type-switch fast path.
//
// TestAPanickedHTTPErrorIsStill500 (panic_test.go) drives the same rule with an
// unwrapped *PanicError, which the type switch above intercepts by exact type —
// that test would pass no matter which order the switch's two cases are
// written in, because Go dispatches a type switch on the dynamic type alone.
// Only a *PanicError arriving wrapped (fmt.Errorf("...: %w", ...)) reaches the
// errors.As pair below the switch, and only there does swapping the *PanicError
// and *HTTPError checks change the answer: this test fails with status 400,
// not 500, if that pair is reordered. Confirmed by hand while writing it.
func TestAWrappedPanickedHTTPErrorIsStill500(t *testing.T) {
	c, fctx := newTestCtx("GET", "/")
	wrapped := fmt.Errorf("ctx: %w", &PanicError{Value: NewHTTPError(400, "not your fault")})

	DefaultErrorHandler(c, wrapped)

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500 — a panic is always a bug, never a client error, even wrapped", got)
	}
}

// TestTheCauseNeverReachesTheBody is the roadmap's own exit criterion, and it
// gets its own test rather than a clause bolted onto another. Every funnel
// input that carries a cause is driven through, and one distinctive token is
// searched for in every response.
func TestTheCauseNeverReachesTheBody(t *testing.T) {
	const token = "PGPASSWORD=hunter2"
	cause := errors.New(token)

	cases := []struct {
		name string
		err  error
	}{
		{"bare error", cause},
		{"HTTPError wrapping it", &HTTPError{Code: 400, Message: "bad request", Err: cause}},
		{"HTTPError with an empty message", &HTTPError{Code: 400, Err: cause}},
		{"error wrapping an HTTPError", fmt.Errorf("ctx: %w", &HTTPError{Code: 502, Message: "upstream", Err: cause})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fctx := newTestCtx("GET", "/")
			DefaultErrorHandler(c, tc.err)

			if body := string(fctx.Response.Body()); strings.Contains(body, token) {
				t.Errorf("the cause leaked into the response body: %q", body)
			}
		})
	}
}

func TestRespondDiscardsAPreviouslyWrittenBody(t *testing.T) {
	c, fctx := newTestCtx("GET", "/")
	_ = c.String(200, "partial output")

	respond(c, 500, "Internal Server Error")

	if got := string(fctx.Response.Body()); got != "Internal Server Error" {
		t.Errorf("body = %q, want the error body only", got)
	}
}

func TestErrNotFoundCarriesItsOwnStatus(t *testing.T) {
	if ErrNotFound.Code != fasthttp.StatusNotFound {
		t.Errorf("ErrNotFound.Code = %d, want 404", ErrNotFound.Code)
	}
	c, fctx := newTestCtx("GET", "/")
	DefaultErrorHandler(c, ErrNotFound)
	if got := fctx.Response.StatusCode(); got != 404 {
		t.Errorf("status = %d, want 404", got)
	}
}

// TestErrNotFoundStillWorksWithErrorsIs pins the compatibility claim in D2:
// the sentinel changed type, and code written against it must keep working.
func TestErrNotFoundStillWorksWithErrorsIs(t *testing.T) {
	var err error = ErrNotFound
	if !errors.Is(err, ErrNotFound) {
		t.Error("errors.Is no longer matches the sentinel against itself")
	}
	if !errors.Is(fmt.Errorf("wrapped: %w", ErrNotFound), ErrNotFound) {
		t.Error("errors.Is no longer matches a wrapped sentinel")
	}
}

func TestWithErrorHandlerReplacesTheDefault(t *testing.T) {
	app := New(WithErrorHandler(func(c *Ctx, err error) {
		respond(c, 599, "custom handler ran")
	}))
	app.GET("/boom", func(c *Ctx) error { return errors.New("anything") })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/boom")

	app.Build()
	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != 599 {
		t.Errorf("status = %d, want 599 from the custom handler", got)
	}
	if got := string(fctx.Response.Body()); got != "custom handler ran" {
		t.Errorf("body = %q, want the custom handler's", got)
	}
}

func TestWithErrorHandlerAlsoHandlesA404(t *testing.T) {
	var saw error
	app := New(WithErrorHandler(func(c *Ctx, err error) { saw = err }))

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/missing")

	app.Build()
	app.handle(fctx)

	if !errors.Is(saw, ErrNotFound) {
		t.Errorf("the custom handler received %v, want ErrNotFound", saw)
	}
}

func TestWithErrorHandlerRejectsNil(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("WithErrorHandler(nil) did not panic")
		}
		msg, ok := r.(string)
		if !ok || !strings.HasPrefix(msg, "rice: ") {
			t.Errorf("panic = %v, want a rice-prefixed string", r)
		}
	}()

	New(WithErrorHandler(nil))
}

func TestAnAppWithNoOptionUsesTheDefaultHandler(t *testing.T) {
	app := New()
	if app.errorHandler == nil {
		t.Fatal("errorHandler is nil on a plain New()")
	}

	c, fctx := newTestCtx("GET", "/")
	app.errorHandler(c, NewHTTPError(418, "teapot"))
	if got := fctx.Response.StatusCode(); got != 418 {
		t.Errorf("status = %d, want the default handler's behaviour", got)
	}
}

// TestAPanickingErrorHandlerStillProducesAResponse covers the failure mode this
// milestone exists to remove, in its nastiest form: it only fires once
// something has already gone wrong, so it is intermittent in production.
func TestAPanickingErrorHandlerStillProducesAResponse(t *testing.T) {
	app := New(WithErrorHandler(func(c *Ctx, err error) {
		panic("the error handler is broken too")
	}))
	app.GET("/boom", func(c *Ctx) error { return errors.New("the first failure") })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/boom")

	app.Build()
	app.handle(fctx) // must not panic out of here

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500 from the last-resort net", got)
	}
	if got := string(fctx.Response.Body()); got != "Internal Server Error" {
		t.Errorf("body = %q, want the last-resort body", got)
	}
}

func TestAPanickingErrorHandlerIsLogged(t *testing.T) {
	logged := captureLog(t)
	app := New(WithErrorHandler(func(c *Ctx, err error) {
		panic("handler exploded")
	}))
	app.GET("/boom", func(c *Ctx) error { return errors.New("first") })

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/boom")

	app.Build()
	app.handle(fctx)

	if !strings.Contains(logged(), "handler exploded") {
		t.Errorf("the ErrorHandler's panic was not logged, got %q", logged())
	}
}

// TestAPanickingErrorHandlerOnTheMissPathIsAlsoCaught pins that the net covers
// every funnel entry point, not just the one the first test happens to use.
func TestAPanickingErrorHandlerOnTheMissPathIsAlsoCaught(t *testing.T) {
	app := New(WithErrorHandler(func(c *Ctx, err error) { panic("boom") }))

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/missing")

	app.Build()
	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500", got)
	}
}
