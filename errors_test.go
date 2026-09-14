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
