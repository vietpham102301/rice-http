package rice

import (
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/valyala/fasthttp"
)

// ErrNotFound is passed into the error funnel when no route matches the
// request, and a handler may return it to produce a 404 of its own.
//
// It is an *HTTPError built once at package initialisation, so a 404 costs no
// allocation for the error itself — which matters, because it is the path
// mistaken and hostile traffic hits hardest. Carrying its own status is also
// what lets the funnel drop its last special case: DefaultErrorHandler finds
// this with the same errors.As it uses for everything else.
//
// It changed type in M5, from errors.New to *HTTPError. errors.Is against it
// keeps working, because it is still the same pointer.
var ErrNotFound = &HTTPError{Code: fasthttp.StatusNotFound, Message: "Not Found"}

// ErrorHandler turns an error into a response. It returns nothing: it is the
// end of the line, and there is nowhere left to report a failure to.
//
// There is exactly one per App, set with WithErrorHandler. Replacing it is the
// supported way to change how a service reports failure — to emit RFC 7807
// problem documents, for example.
type ErrorHandler func(c *Ctx, err error)

// DefaultErrorHandler is the ErrorHandler an App uses unless WithErrorHandler
// replaces it. It is exported so a custom handler can delegate the cases it
// does not care about.
//
// An *HTTPError anywhere in the chain answers with its code. Anything else is a
// 500 with a generic body, and the real error is logged rather than sent:
// leaking internal error strings to clients is how databases end up described
// in HTTP responses.
func DefaultErrorHandler(c *Ctx, err error) {
	var he *HTTPError
	if errors.As(err, &he) {
		respond(c, he.Code, he.Message)
		return
	}

	log.Printf("rice: unhandled error: %v", err)
	respond(c, fasthttp.StatusInternalServerError, "Internal Server Error")
}

// respond writes a status and a plain-text body, discarding whatever the
// handler had written first.
//
// SetBodyString below discards any prior body — stream, raw, or buffered —
// before writing, which is ADR-0002's rule, settled in M1: a handler that
// writes a response and then returns an error has its body discarded and
// receives the error handler's response instead. There is deliberately no
// explicit ResetBody call: it would be a no-op here, and a no-op whose comment
// claims to enforce a rule is worse than its absence.
func respond(c *Ctx, code int, msg string) {
	if msg == "" {
		msg = fasthttp.StatusMessage(code)
	}
	c.fctx.SetStatusCode(code)
	c.fctx.SetContentType(MIMETextPlainUTF8)
	c.fctx.SetBodyString(msg)
}

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
