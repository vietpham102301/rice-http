package rice

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
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
