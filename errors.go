package rice

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/valyala/fasthttp"
)

// ErrNotFound is passed into the error funnel when no route matches the
// request. The error handler turns it into a 404.
//
// It is a package-level value created once at init, so returning it costs no
// allocation — which matters, because it is returned on the path that mistaken
// and hostile traffic hits hardest.
//
// M5 generalises the funnel to an HTTPError type. errors.Is keeps working
// against this sentinel, so checks written against it today keep working then.
var ErrNotFound = errors.New("rice: not found")

// HTTPError is an error with an HTTP status attached. It is the one error type
// the funnel understands: DefaultErrorHandler finds it with errors.As and
// answers with its code, and everything else becomes a 500.
//
// The fields are exported, so wrapping a cause is a composite literal:
//
//	&rice.HTTPError{Code: 400, Message: "bad id", Err: err}
//
// which is why there is one constructor rather than two.
type HTTPError struct {
	// Code is the HTTP status code sent to the client.
	Code int

	// Message is the body sent to the client. Empty means the status text for
	// Code. It is written to the response, so it must not carry internals.
	Message string

	// Err is the real cause. It is logged and it is reachable with errors.Is
	// and errors.As. It is never written to the response.
	Err error
}

// NewHTTPError returns an HTTPError with no wrapped cause.
func NewHTTPError(code int, msg string) *HTTPError {
	return &HTTPError{Code: code, Message: msg}
}

// Error renders the code, the message, and the cause when there is one. This
// string is what gets logged. It is never what gets sent.
func (e *HTTPError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = fasthttp.StatusMessage(e.Code)
	}
	s := strconv.Itoa(e.Code) + ": " + msg
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the wrapped cause, so errors.Is and errors.As reach through.
func (e *HTTPError) Unwrap() error { return e.Err }

// PanicError is a recovered panic, presented to the error funnel as an error.
//
// It exists so that a panic reaches the same ErrorHandler as everything else,
// which is what "one funnel" means. DefaultErrorHandler always answers 500 for
// it, whatever it wraps — see the ordering note there.
type PanicError struct {
	// Value is what was passed to panic().
	Value any

	// Stack is the stack trace captured at recovery, while the panicking frames
	// were still live.
	Stack []byte
}

// Error renders the panicked value.
func (e *PanicError) Error() string { return "panic: " + fmt.Sprint(e.Value) }

// Unwrap returns Value when a panic carried an error, so errors.As can find it.
// It returns nil otherwise, which is what makes panic("boom") not an error
// chain of its own.
func (e *PanicError) Unwrap() error {
	if err, ok := e.Value.(error); ok {
		return err
	}
	return nil
}
