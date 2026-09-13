# M5 Error Handling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give rice one place where an `error` becomes a response, and make a panicking handler survivable instead of fatal to the process.

**Architecture:** `HTTPError` carries a status code, so `errors.As` alone drives the funnel and no error value is special to the framework. A single `ErrorHandler`, set with an `Option` at construction, converts every failure — returned errors, route misses and recovered panics — into a response. `handle` installs a deferred `recover()` around the whole dispatch, because fasthttp has no panic hook and an unrecovered panic currently kills the process.

**Tech Stack:** Go 1.25, fasthttp v1.73.0, standard library `log` and `runtime/debug`.

**Spec:** [docs/superpowers/specs/2026-09-13-m5-error-handling-design.md](../specs/2026-09-13-m5-error-handling-design.md)

## Global Constraints

- Nothing under `internal/` may import package `rice`. `middleware/` is not under `internal/` and may import it.
- `github.com/valyala/fasthttp` is the only runtime dependency. Package `rice` must not import `reflect` or `encoding/json` (ADR-0006). `log`, `fmt`, `strconv` and `runtime/debug` are standard library and are allowed.
- Package `rice` must not import `net/http`. Use `fasthttp.StatusMessage(code)` and the `fasthttp.Status*` constants.
- Every user-facing panic is prefixed `rice: ` and names the offending value.
- The cause of an error is never written to the response body. It is logged.
- A response written by a handler that then returns an error is discarded (ADR-0002, settled in M1).
- End-to-end per-request allocation stays at 1, the `Ctx`. M6 removes it. An installed recovery that never fires must add 0.
- Code belonging to a later milestone carries a comment naming that milestone.
- `make lint`, `make test`, `make cover` must exit 0 at the end of every task. Coverage must not drop below 98.4%.
- Every new guard is broken on purpose and watched to fail, with the failure text recorded in the task report. A guard nobody watched go red is not a guard.

---

### Task 1: `HTTPError`, `PanicError`, and the log-capture harness

**Files:**
- Modify: `errors.go` (currently 14 lines, holds only `ErrNotFound`)
- Create: `errors_test.go`
- Create: `logcapture_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `type HTTPError struct { Code int; Message string; Err error }`, `func NewHTTPError(code int, msg string) *HTTPError`, `func (e *HTTPError) Error() string`, `func (e *HTTPError) Unwrap() error`, `type PanicError struct { Value any; Stack []byte }`, `func (e *PanicError) Error() string`, `func (e *PanicError) Unwrap() error`, and the test-only helper `captureLog(t *testing.T) func() string`.

- [ ] **Step 1: Write the log-capture harness**

Package `rice` has no `TestMain` today. Create `logcapture_test.go`:

```go
package rice

import (
	"bytes"
	"io"
	"log"
	"os"
	"testing"
)

// TestMain silences the standard logger for the whole package.
//
// From M5 the default error handler logs every unhandled error and every
// recovered panic, and this package's tests produce a great many of both on
// purpose. Without this, a real failure is buried in expected noise.
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// captureLog redirects the standard logger into a buffer for the duration of
// one test and returns a function yielding what was written. The output is
// restored to io.Discard on cleanup, not to os.Stderr, so a test that forgets
// to assert does not start leaking into the next one.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	return buf.String
}
```

- [ ] **Step 2: Write the failing tests for both error types**

Create `errors_test.go`:

```go
package rice

import (
	"errors"
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
```

Add `"fmt"` and `"log"` to that file's imports.

- [ ] **Step 3: Run the tests and verify they fail**

Run: `go test . -run 'HTTPError|PanicError|CaptureLog' -v`
Expected: compile failure — `undefined: NewHTTPError`, `undefined: HTTPError`, `undefined: PanicError`.

- [ ] **Step 4: Implement both types**

Append to `errors.go` (keep the existing `ErrNotFound` exactly as it is — Task 2 changes it):

```go
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
```

Set the import block at the top of `errors.go` to:

```go
import (
	"errors"
	"fmt"
	"strconv"

	"github.com/valyala/fasthttp"
)
```

`errors` is still needed by the existing `ErrNotFound`.

- [ ] **Step 5: Run the tests and verify they pass**

Run: `go test . -run 'HTTPError|PanicError|CaptureLog' -v`
Expected: PASS, all ten.

- [ ] **Step 6: Break each guard on purpose and watch it fail**

Perform each of these, record the exact failure line, then revert:

1. Make `Error()` skip the status-text fallback (`msg := e.Message` with no `if`). Expect `TestHTTPErrorRendersTheStatusTextWhenMessageIsEmpty` to fail with `Error() = "404: ", want "404: Not Found"`.
2. Make `HTTPError.Unwrap` return `nil`. Expect `TestHTTPErrorUnwrapReachesTheCause` to fail.
3. Make `PanicError.Unwrap` always return `nil`. Expect `TestPanicErrorUnwrapReturnsAPanickedError` to fail. Do not try the `e.Value.(error)` variant without the comma-ok: it panics on the string case, which is a different failure from the one the test names, and proves nothing about the guard.
4. Make `captureLog` return `func() string { return "" }`. Expect `TestCaptureLogSeesWhatWasLogged` to fail. This one matters most: it is the guard on the guard.

- [ ] **Step 7: Run the full suite and commit**

Run: `make lint && make test && make cover`

```bash
git add errors.go errors_test.go logcapture_test.go
git commit -m "feat: add HTTPError and PanicError"
```

---

### Task 2: The funnel — `ErrorHandler`, `DefaultErrorHandler`, and the deleted special case

**Files:**
- Modify: `errors.go` (retype `ErrNotFound`, add `ErrorHandler`, `DefaultErrorHandler`, `respond`)
- Modify: `app.go` — add the `errorHandler` field to `App`, default it in `New` (line 67), replace `handleError` (lines ~120-140) and its two call sites in `handle`
- Modify: `app_test.go` — the existing funnel tests
- Create: additions to `errors_test.go`

**Interfaces:**
- Consumes: `HTTPError`, `NewHTTPError` from Task 1.
- Produces: `type ErrorHandler func(c *Ctx, err error)`, `func DefaultErrorHandler(c *Ctx, err error)`, unexported `func respond(c *Ctx, code int, msg string)`, the `App.errorHandler ErrorHandler` field, and `var ErrNotFound = &HTTPError{...}`.

- [ ] **Step 1: Write the failing funnel tests**

**Do not define a `newTestCtx` helper.** One already exists in `ctx_response_test.go` with the signature `newTestCtx(method, uri string) (*Ctx, *fasthttp.RequestCtx)`, in this same package. Redefining it is a compile error. Call the existing one.

Append to `errors_test.go`:

```go
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
```

Add `"fmt"` and `"github.com/valyala/fasthttp"` to `errors_test.go`'s imports.

- [ ] **Step 2: Run and verify they fail**

Run: `go test . -run 'DefaultErrorHandler|TheCauseNever|Respond|ErrNotFound' -v`
Expected: compile failure — `undefined: DefaultErrorHandler`, `undefined: respond`, and `ErrNotFound.Code undefined (type error has no field or method Code)`.

- [ ] **Step 3: Retype `ErrNotFound` and add the funnel to `errors.go`**

Replace the existing `ErrNotFound` declaration and its comment with:

```go
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
// The reset is ADR-0002's rule, settled in M1: a handler that writes a response
// and then returns an error has its body discarded and receives the error
// handler's response instead.
func respond(c *Ctx, code int, msg string) {
	if msg == "" {
		msg = fasthttp.StatusMessage(code)
	}
	c.fctx.ResetBody()
	c.fctx.SetStatusCode(code)
	c.fctx.SetContentType(MIMETextPlainUTF8)
	c.fctx.SetBodyString(msg)
}
```

Add `"log"` to `errors.go`'s imports.

- [ ] **Step 4: Wire the funnel into `App`**

In `app.go`, add to the `App` struct, after the `built` field:

```go
	// errorHandler converts every failure into a response. New sets it to
	// DefaultErrorHandler before applying options, so it is never nil and the
	// dispatch path never checks.
	//
	// It is written once at construction and read on every failing request, with
	// no lock. That is safe because it is set before the App can serve: unlike
	// routes and middleware, there is no setter, so there is no window in which
	// a serving App can be reconfigured.
	errorHandler ErrorHandler
```

In `New`, set the default before the option loop:

```go
func New(opts ...Option) *App {
	a := &App{errorHandler: DefaultErrorHandler}
	a.srv = &fasthttp.Server{
		Handler: a.handle,
		Name:    "rice",
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}
```

Delete the whole `handleError` method, including its doc comment. Replace both call sites in `handle`:

```go
	h, ok := a.lookup(fctx.Method(), fctx.Path(), &c.params)
	if !ok {
		a.errorHandler(c, ErrNotFound)
		return
	}

	if err := h(c); err != nil {
		a.errorHandler(c, err)
	}
```

Remove `"errors"` from `app.go`'s import block. It was used only by `handleError`; `go vet` will fail if it is left.

- [ ] **Step 5: Update the existing funnel tests in `app_test.go`**

`TestThe404BodyDoesNotLeakTheSentinelMessage` asserts the body does not contain `"rice:"`. `ErrNotFound.Message` is now `"Not Found"` and `Error()` renders `"404: Not Found"` with no `rice:` prefix, so the assertion no longer tests anything. Replace its second check with one that still bites:

```go
	if strings.Contains(body, "404:") {
		t.Errorf("the sentinel's Error() rendering leaked into the response: %q", body)
	}
```

Leave every other test in `app_test.go` unchanged. `TestHandleConvertsAReturnedErrorInto500`, `TestHandleDiscardsAPartialBodyWhenTheHandlerErrors`, `TestAHandlerErrorStillProduces500AfterTheFunnelLearnedAbout404` and `TestAHandlerMayReturnErrNotFoundToGetA404` must all pass against the new funnel without modification. If any of them needs changing, stop and report it — that is a behaviour change the spec did not ask for.

- [ ] **Step 6: Run the tests and verify they pass**

Run: `make test`
Expected: all packages pass.

- [ ] **Step 7: Verify the special case is actually gone**

Run: `grep -n 'ErrNotFound' app.go`
Expected: exactly one line, the `a.errorHandler(c, ErrNotFound)` call. No `errors.Is`, no branch. Exit criterion 5 is checked by this grep, and the task report must quote its output.

- [ ] **Step 8: Break each guard on purpose and watch it fail**

1. Make `respond` skip `ResetBody()`. Expect `TestRespondDiscardsAPreviouslyWrittenBody` and `TestHandleDiscardsAPartialBodyWhenTheHandlerErrors` to fail.
2. Make `DefaultErrorHandler` write `err.Error()` as the body in the 500 branch. Expect `TestTheCauseNeverReachesTheBody/bare_error` to fail naming the token. **This is the roadmap's exit criterion; record the exact failure text.**
3. Change `errors.As` to a type assertion `he, ok := err.(*HTTPError)`. Expect `TestDefaultErrorHandlerFindsAnHTTPErrorThroughAWrapper` to fail with status 500.
4. Revert `ErrNotFound` to `errors.New("rice: not found")`. Expect a compile failure plus `TestErrNotFoundCarriesItsOwnStatus` — confirm the retype is load-bearing.

- [ ] **Step 9: Commit**

```bash
git add errors.go errors_test.go app.go app_test.go
git commit -m "feat: replace the two-branch funnel with HTTPError and ErrorHandler"
```

---

### Task 3: `WithErrorHandler`

**Files:**
- Modify: `app.go` — add the option beside `type Option`
- Create: additions to `errors_test.go`

**Interfaces:**
- Consumes: `ErrorHandler`, `DefaultErrorHandler`, `App.errorHandler` from Task 2.
- Produces: `func WithErrorHandler(h ErrorHandler) Option`.

- [ ] **Step 1: Write the failing tests**

Append to `errors_test.go`:

```go
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
```

- [ ] **Step 2: Run and verify they fail**

Run: `go test . -run 'WithErrorHandler|AnAppWithNoOption' -v`
Expected: compile failure — `undefined: WithErrorHandler`.

- [ ] **Step 3: Implement the option**

In `app.go`, immediately after the `Option` type declaration, replacing its now-stale doc comment:

```go
// Option configures an App at construction time.
//
// Configuration happens here rather than through setters so that a serving App
// cannot be reconfigured underneath a request. M7 adds server timeouts.
type Option func(*App)

// WithErrorHandler replaces the ErrorHandler an App uses for every failure:
// returned errors, route misses, and recovered panics alike.
//
// A nil handler panics here rather than falling back to the default silently,
// in the style of every other configuration mistake in rice.
func WithErrorHandler(h ErrorHandler) Option {
	if h == nil {
		panic("rice: WithErrorHandler: handler is nil")
	}
	return func(a *App) { a.errorHandler = h }
}
```

Note the nil check is outside the returned closure, so it fires at the `WithErrorHandler(nil)` call rather than inside `New`. That is what `TestWithErrorHandlerRejectsNil` asserts by calling both in one expression; keeping the check outside means it would also fire for an option constructed and never used.

- [ ] **Step 4: Run and verify they pass**

Run: `make test`

- [ ] **Step 5: Break the guards**

1. Delete the nil check. Expect `TestWithErrorHandlerRejectsNil` to fail with "did not panic".
2. In `New`, move `errorHandler: DefaultErrorHandler` to *after* the option loop. Expect `TestWithErrorHandlerReplacesTheDefault` to fail with status 500 — the option is overwritten.

The second injection is the one worth doing carefully: it is a real ordering bug that a reviewer would not catch by reading.

- [ ] **Step 6: Commit**

```bash
git add app.go errors_test.go
git commit -m "feat: add WithErrorHandler"
```

---

### Task 4: `callErrorHandler`, the last-resort net

**Files:**
- Modify: `app.go` — add the method, route both funnel call sites through it
- Create: additions to `errors_test.go`

**Interfaces:**
- Consumes: `App.errorHandler`, `WithErrorHandler` from Tasks 2 and 3.
- Produces: unexported `func (a *App) callErrorHandler(c *Ctx, err error)`. Every funnel entry point calls this, never `a.errorHandler` directly.

- [ ] **Step 1: Write the failing test**

Append to `errors_test.go`:

```go
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
```

- [ ] **Step 2: Run and verify they fail**

Run: `go test . -run 'PanickingErrorHandler' -v`
Expected: the test binary panics — `panic: the error handler is broken too`, and the package fails. Record this: it is the disease.

- [ ] **Step 3: Implement the net**

In `app.go`, after `handle`:

```go
// callErrorHandler runs the App's ErrorHandler with a last-resort net beneath it.
//
// Without this, a panic inside a user's ErrorHandler reintroduces exactly the
// failure the recovery in handle removes — and reintroduces it in its worst
// form, because it fires only when something has already gone wrong, which
// makes it intermittent and hard to reproduce.
//
// It costs nothing on the hot path: a request that succeeds never gets here.
// Every funnel entry point calls this rather than a.errorHandler directly.
//
// The last-resort response is written with raw fasthttp calls rather than
// through respond, so that a bug in rice's own response path cannot recurse.
func (a *App) callErrorHandler(c *Ctx, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("rice: ErrorHandler panicked: %v", r)
			c.fctx.ResetBody()
			c.fctx.SetStatusCode(fasthttp.StatusInternalServerError)
			c.fctx.SetContentType(MIMETextPlainUTF8)
			c.fctx.SetBodyString("Internal Server Error")
		}
	}()

	a.errorHandler(c, err)
}
```

Add `"log"` to `app.go`'s imports.

Change both call sites in `handle` from `a.errorHandler(c, ...)` to `a.callErrorHandler(c, ...)`.

- [ ] **Step 4: Run and verify they pass**

Run: `make test`

- [ ] **Step 5: Verify no call site was missed**

Run: `grep -n 'a\.errorHandler(' app.go`
Expected: exactly one line — the call inside `callErrorHandler`. Quote the output in the task report.

- [ ] **Step 6: Break the guards**

1. Delete the `defer` in `callErrorHandler`. Expect all three tests to fail by panicking the test binary.
2. Change one `handle` call site back to `a.errorHandler(` and expect exactly one of the three tests to fail — this proves the tests cover both entry points separately rather than one covering for the other.

- [ ] **Step 7: Commit**

```bash
git add app.go errors_test.go
git commit -m "feat: guard the ErrorHandler with a last-resort net"
```

---

### Task 5: Core panic recovery

**Files:**
- Modify: `app.go` — the deferred recovery in `handle`
- Modify: `errors.go` — the `*PanicError` branch in `DefaultErrorHandler`
- Create: `panic_test.go`
- Modify: `server_test.go` — the end-to-end survival test

**Interfaces:**
- Consumes: `PanicError` from Task 1, `callErrorHandler` from Task 4.
- Produces: no new exported symbols. `handle` recovers; `DefaultErrorHandler` answers 500 for a `*PanicError` and logs its stack.

- [ ] **Step 1: Write the failing tests**

Create `panic_test.go`:

```go
package rice

import (
	"errors"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// dispatchCtx drives one request and hands back the whole RequestCtx.
//
// Package rice already has a dispatch helper, in build_test.go, but it returns
// only the status code and these tests assert on bodies too. The name is
// different because two helpers with one name in one package is a compile
// error, not a style question.
func dispatchCtx(app *App, method, path string) *fasthttp.RequestCtx {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(method)
	fctx.Request.SetRequestURI(path)
	app.Build()
	app.handle(fctx)
	return fctx
}

func TestAPanickingHandlerProduces500(t *testing.T) {
	app := New()
	app.GET("/boom", func(c *Ctx) error { panic("handler exploded") })

	fctx := dispatchCtx(app, "GET", "/boom")

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500", got)
	}
	if got := string(fctx.Response.Body()); got != "Internal Server Error" {
		t.Errorf("body = %q, want the generic body", got)
	}
}

func TestAPanickingHandlerDoesNotLeakThePanicValueIntoTheBody(t *testing.T) {
	const token = "SECRET-PANIC-VALUE"
	app := New()
	app.GET("/boom", func(c *Ctx) error { panic(token) })

	fctx := dispatchCtx(app, "GET", "/boom")

	if body := string(fctx.Response.Body()); strings.Contains(body, token) {
		t.Errorf("the panic value leaked into the response: %q", body)
	}
}

func TestAPanickingHandlerIsLoggedWithItsStack(t *testing.T) {
	logged := captureLog(t)
	app := New()
	app.GET("/boom", func(c *Ctx) error { panic("handler exploded") })

	dispatchCtx(app, "GET", "/boom")

	out := logged()
	if !strings.Contains(out, "handler exploded") {
		t.Errorf("the panic value was not logged, got %q", out)
	}
	if !strings.Contains(out, "panic_test.go") {
		t.Errorf("the log carries no stack naming this test file, got %q", out)
	}
}

func TestAPanicWithANonStringValueIsHandled(t *testing.T) {
	app := New()
	app.GET("/boom", func(c *Ctx) error { panic(42) })

	fctx := dispatchCtx(app, "GET", "/boom")

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500", got)
	}
}

func TestAPanicInsideMiddlewareIsRecovered(t *testing.T) {
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error { panic("middleware exploded") }
	})
	app.GET("/x", func(c *Ctx) error { return c.String(200, "never reached") })

	fctx := dispatchCtx(app, "GET", "/x")

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500", got)
	}
}

// TestAPanickedHTTPErrorIsStill500 pins D5's ordering. PanicError.Unwrap
// returns the panicked value, so without checking *PanicError first this would
// answer 400 — a panic quietly becoming a client error.
func TestAPanickedHTTPErrorIsStill500(t *testing.T) {
	app := New()
	app.GET("/boom", func(c *Ctx) error { panic(NewHTTPError(400, "not your fault")) })

	fctx := dispatchCtx(app, "GET", "/boom")

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500 — a panic is always a bug, never a client error", got)
	}
}

// TestACustomErrorHandlerReceivesAPanicError proves the panic reaches the same
// funnel as everything else, which is what "one funnel" means.
func TestACustomErrorHandlerReceivesAPanicError(t *testing.T) {
	var got *PanicError
	app := New(WithErrorHandler(func(c *Ctx, err error) {
		_ = errors.As(err, &got)
		respond(c, 500, "custom")
	}))
	app.GET("/boom", func(c *Ctx) error { panic("inspect me") })

	dispatchCtx(app, "GET", "/boom")

	if got == nil {
		t.Fatal("the custom handler did not receive a *PanicError")
	}
	if got.Value != "inspect me" {
		t.Errorf("Value = %v, want %q", got.Value, "inspect me")
	}
	if len(got.Stack) == 0 {
		t.Error("Stack is empty")
	}
}

func TestASuccessfulRequestIsUnaffectedByTheRecovery(t *testing.T) {
	app := New()
	app.GET("/ok", func(c *Ctx) error { return c.String(200, "fine") })

	fctx := dispatchCtx(app, "GET", "/ok")

	if got := fctx.Response.StatusCode(); got != 200 {
		t.Errorf("status = %d, want 200", got)
	}
	if got := string(fctx.Response.Body()); got != "fine" {
		t.Errorf("body = %q, want %q", got, "fine")
	}
}
```

Append to `server_test.go` — the test that matters most, because the unit tests above all run inside a `testing` goroutine that would report a panic as a test failure rather than as process death:

```go
// TestServeSurvivesAPanickingHandler is the exit criterion. Before M5 a
// panicking handler killed the process: fasthttp has no PanicHandler, calls the
// handler bare from a worker-pool goroutine, and an unrecovered panic there
// takes the program down with exit status 2 — every other connection with it.
//
// The roadmap asked for "without dropping the connection". That understated the
// problem, so this asserts the stronger thing: the server keeps serving.
func TestServeSurvivesAPanickingHandler(t *testing.T) {
	app := rice.New()
	app.GET("/boom", func(c *rice.Ctx) error { panic("handler exploded") })
	app.GET("/ok", func(c *rice.Ctx) error { return c.String(200, "still alive") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	addr := waitForAddr(t, app)

	if code, _ := get(t, addr, "/ok"); code != 200 {
		t.Fatalf("before the panic: status = %d, want 200", code)
	}

	code, body := get(t, addr, "/boom")
	if code != 500 {
		t.Errorf("panicking route: status = %d, want 500", code)
	}
	if strings.Contains(body, "exploded") {
		t.Errorf("the panic value reached the client: %q", body)
	}

	if code, body := get(t, addr, "/ok"); code != 200 || body != "still alive" {
		t.Errorf("after the panic: status = %d body = %q, want 200 and %q", code, body, "still alive")
	}
}
```

Check `server_test.go`'s existing imports and add only what is missing (`context`, `net`, `strings` are likely already there — read the file rather than assuming).

Add one more to `server_test.go`, using a raw connection, because the spec asks specifically that the *connection* survives and `http.Get` gives no control over reuse:

```go
// TestAPanicDoesNotKillTheKeepAliveConnection writes two requests down one TCP
// connection by hand. The first panics. The second must still be answered on
// that same connection — "does not take the connection down" is the roadmap's
// original wording, and http.Get cannot prove it because the client is free to
// open a fresh connection without telling anyone.
func TestAPanicDoesNotKillTheKeepAliveConnection(t *testing.T) {
	app := rice.New()
	app.GET("/boom", func(c *rice.Ctx) error { panic("handler exploded") })
	app.GET("/ok", func(c *rice.Ctx) error { return c.String(200, "still alive") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	addr := waitForAddr(t, app)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	br := bufio.NewReader(conn)

	write := func(path string) {
		t.Helper()
		req := "GET " + path + " HTTP/1.1\r\nHost: x\r\n\r\n"
		if _, err := conn.Write([]byte(req)); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	read := func(path string) *http.Response {
		t.Helper()
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("read response for %s: %v", path, err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp
	}

	write("/boom")
	if got := read("/boom").StatusCode; got != 500 {
		t.Errorf("panicking route: status = %d, want 500", got)
	}

	write("/ok")
	if got := read("/ok").StatusCode; got != 200 {
		t.Errorf("after the panic, on the same connection: status = %d, want 200", got)
	}
}
```

This needs `bufio`, `io`, `net/http` and `time` in `server_test.go`. `net/http` is already imported there for the `get` helper; read the file and add only what is missing.

- [ ] **Step 2: Run and verify they fail**

Run: `go test . -run 'Panick|Panic' -v`
Expected: the test binary dies. Record the exact output — this is the same failure the design's probe recorded, reproduced inside the suite.

- [ ] **Step 3: Add the `*PanicError` branch to `DefaultErrorHandler`**

In `errors.go`, insert **before** the existing `*HTTPError` branch:

```go
	// Checked before *HTTPError, and the order is load-bearing.
	// PanicError.Unwrap returns the panicked value, so panic(NewHTTPError(400,
	// ...)) would otherwise answer 400 — a panic quietly becoming a client
	// error. A panic is a bug, never a way to signal failure, and it is always
	// a 500.
	var pe *PanicError
	if errors.As(err, &pe) {
		log.Printf("rice: panic recovered: %v\n%s", pe.Value, pe.Stack)
		respond(c, fasthttp.StatusInternalServerError, "Internal Server Error")
		return
	}
```

- [ ] **Step 4: Install the recovery in `handle`**

In `app.go`, immediately after `c.reset(a, fctx)` and before the lookup:

```go
	// fasthttp has no panic hook. Its only recover() guards body-stream writes;
	// server.go calls the handler bare from a worker-pool goroutine, so an
	// unrecovered panic here takes the whole process down, not just this
	// connection. Recovering is therefore core behaviour rather than opt-in
	// middleware, which is a deliberate exception to design principle 7 —
	// see ADR-0008.
	//
	// The closure is written out rather than expressed as defer a.recover(c)
	// because this is the shape that was measured: 1 alloc/op, unchanged.
	defer func() {
		if r := recover(); r != nil {
			a.callErrorHandler(c, &PanicError{Value: r, Stack: debug.Stack()})
		}
	}()
```

Add `"runtime/debug"` to `app.go`'s imports.

- [ ] **Step 5: Run and verify they pass**

Run: `make test`
Expected: all packages pass, including `TestServeSurvivesAPanickingHandler`.

- [ ] **Step 6: Break the guards**

1. Delete the `defer` block in `handle`. Expect `TestServeSurvivesAPanickingHandler` to fail — record how it fails, because a process death inside `go test` presents differently from an assertion failure.
2. Move the `*PanicError` branch *after* the `*HTTPError` branch. Expect `TestAPanickedHTTPErrorIsStill500` to fail with status 400. This is D5's whole justification; if this injection does not go red, the branch order is not doing what the comment claims.
3. Set `Stack: nil` instead of `debug.Stack()`. Expect `TestACustomErrorHandlerReceivesAPanicError` and `TestAPanickingHandlerIsLoggedWithItsStack` to fail.

- [ ] **Step 7: Commit**

```bash
git add app.go errors.go panic_test.go server_test.go
git commit -m "feat: recover panics in the core so one bad handler cannot kill the process"
```

---

### Task 6: `middleware.Recover`

**Files:**
- Create: `middleware/doc.go`
- Create: `middleware/recover.go`
- Create: `middleware/recover_test.go`

**Interfaces:**
- Consumes: `rice.Middleware`, `rice.Handler`, `rice.Ctx`, `rice.PanicError` — all exported from package `rice`.
- Produces: `func Recover() rice.Middleware` in package `middleware`, import path `github.com/vietpham102301/rice-http/middleware`.

- [ ] **Step 1: Write the failing tests**

Create `middleware/recover_test.go`:

```go
package middleware_test

import (
	"errors"
	"testing"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

func dispatch(t *testing.T, app *rice.App, path string) *fasthttp.RequestCtx {
	t.Helper()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI(path)
	app.FasthttpHandler()(fctx) // FasthttpHandler calls Build
	return fctx
}

func TestRecoverTurnsAPanicInto500(t *testing.T) {
	app := rice.New()
	app.Use(middleware.Recover())
	app.GET("/boom", func(c *rice.Ctx) error { panic("exploded") })

	fctx := dispatch(t, app, "/boom")

	if got := fctx.Response.StatusCode(); got != 500 {
		t.Errorf("status = %d, want 500", got)
	}
}

// TestRecoverLetsOuterMiddlewareSeeTheError is the only reason this package
// exists. Core recovery sits outside the whole chain, so by the time it runs
// every middleware frame has unwound and a middleware written as
// "err := next(c); record(err); return err" never reaches its record call.
// With Recover installed beneath it, the panic arrives as an ordinary return.
func TestRecoverLetsOuterMiddlewareSeeTheError(t *testing.T) {
	var observed error
	recorded := false

	app := rice.New()
	app.Use(func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			err := next(c)
			recorded = true
			observed = err
			return err
		}
	})
	app.Use(middleware.Recover())
	app.GET("/boom", func(c *rice.Ctx) error { panic("exploded") })

	dispatch(t, app, "/boom")

	if !recorded {
		t.Fatal("the outer middleware never resumed; the panic unwound past it")
	}
	var pe *rice.PanicError
	if !errors.As(observed, &pe) {
		t.Fatalf("the outer middleware observed %v, want a *rice.PanicError", observed)
	}
	if pe.Value != "exploded" {
		t.Errorf("Value = %v, want %q", pe.Value, "exploded")
	}
}

// TestWithoutRecoverTheOuterMiddlewareIsSkipped is the control. It is what
// makes the test above evidence rather than decoration: the same chain without
// Recover must fail to record, or the property is not Recover's doing.
func TestWithoutRecoverTheOuterMiddlewareIsSkipped(t *testing.T) {
	recorded := false

	app := rice.New()
	app.Use(func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			err := next(c)
			recorded = true
			return err
		}
	})
	app.GET("/boom", func(c *rice.Ctx) error { panic("exploded") })

	dispatch(t, app, "/boom")

	if recorded {
		t.Error("the outer middleware resumed without Recover installed; core recovery is not supposed to make that possible")
	}
}

func TestRecoverPassesASuccessfulRequestThrough(t *testing.T) {
	app := rice.New()
	app.Use(middleware.Recover())
	app.GET("/ok", func(c *rice.Ctx) error { return c.String(200, "fine") })

	fctx := dispatch(t, app, "/ok")

	if got := string(fctx.Response.Body()); got != "fine" {
		t.Errorf("body = %q, want %q", got, "fine")
	}
}

func TestRecoverPassesAReturnedErrorThroughUnchanged(t *testing.T) {
	sentinel := errors.New("ordinary failure")
	var observed error

	app := rice.New(rice.WithErrorHandler(func(c *rice.Ctx, err error) { observed = err }))
	app.Use(middleware.Recover())
	app.GET("/err", func(c *rice.Ctx) error { return sentinel })

	dispatch(t, app, "/err")

	if !errors.Is(observed, sentinel) {
		t.Errorf("the funnel received %v, want the handler's own error", observed)
	}
}

func TestRecoverCapturesAStack(t *testing.T) {
	var pe *rice.PanicError
	app := rice.New(rice.WithErrorHandler(func(c *rice.Ctx, err error) {
		_ = errors.As(err, &pe)
	}))
	app.Use(middleware.Recover())
	app.GET("/boom", func(c *rice.Ctx) error { panic("exploded") })

	dispatch(t, app, "/boom")

	if pe == nil {
		t.Fatal("the funnel did not receive a *rice.PanicError")
	}
	if len(pe.Stack) == 0 {
		t.Error("Stack is empty")
	}
}
```

The test package is `middleware_test`, not `middleware`. That is deliberate: it forces the tests to use only the exported surface, which is the same surface a user has, and it proves the import direction works from outside.

- [ ] **Step 2: Run and verify they fail**

Run: `go test ./middleware/ -v`
Expected: `no Go files in .../middleware` or `undefined: middleware.Recover`.

- [ ] **Step 3: Write the package doc**

Create `middleware/doc.go`:

```go
// Package middleware holds rice's optional, opt-in middleware.
//
// Nothing here is imported by package rice. Importing rice must not drag in
// anything a user did not ask for, and the import graph is the honest signal of
// what costs what — which is why this is a separate package rather than a
// subdirectory of helpers or a set of methods on App.
//
// The dependency runs one way: middleware imports rice. Nothing under
// internal/ may import rice; middleware/ is not under internal/, so it may.
package middleware
```

- [ ] **Step 4: Implement `Recover`**

Create `middleware/recover.go`:

```go
package middleware

import (
	"runtime/debug"

	rice "github.com/vietpham102301/rice-http"
)

// Recover converts a panic below it into a *rice.PanicError returned normally.
//
// rice already recovers panics in its core, so this is not what keeps a
// panicking handler from killing the process — that happens with or without it.
// What this adds is where the conversion happens. Core recovery sits outside
// the entire chain, so by the time it runs every middleware frame has unwound.
// A middleware written as
//
//	err := next(c)
//	record(err)
//	return err
//
// never reaches its record call when the handler panics. With Recover installed
// beneath it, the panic arrives as an ordinary return value and the rest of the
// chain behaves normally.
//
// Install it as the outermost middleware you want to protect, and put anything
// that must observe failures outside it.
func Recover() rice.Middleware {
	return func(next rice.Handler) rice.Handler {
		// The named return is the whole mechanism: the deferred function
		// overwrites it, which is how a panic becomes a returned error.
		return func(c *rice.Ctx) (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = &rice.PanicError{Value: r, Stack: debug.Stack()}
				}
			}()

			return next(c)
		}
	}
}
```

- [ ] **Step 5: Run and verify they pass**

Run: `go test ./middleware/ -v && make test`

- [ ] **Step 6: Verify the import direction**

Run: `go list -deps github.com/vietpham102301/rice-http | grep -c 'rice-http/middleware'`
Expected: `0` — package `rice` must not depend on `middleware`.

Run: `go list -deps github.com/vietpham102301/rice-http/middleware | grep -c 'rice-http$'`
Expected: `1` — `middleware` does depend on `rice`.

Quote both in the task report.

- [ ] **Step 7: Break the guards**

1. Remove the named return, using `func(c *rice.Ctx) error` and a plain `return next(c)`. The deferred assignment no longer changes anything. Expect `TestRecoverLetsOuterMiddlewareSeeTheError` to fail — the observed error will be nil. This is the injection that matters: the named return is easy to lose in a refactor and nothing else catches it.
2. Delete the whole `defer` block. Expect `TestRecoverLetsOuterMiddlewareSeeTheError` to fail on `recorded`.
3. Confirm `TestWithoutRecoverTheOuterMiddlewareIsSkipped` **passes both before and after** the fix — it is a control, not a guard, and if it ever goes red the claim about core recovery is wrong.

- [ ] **Step 8: Commit**

```bash
git add middleware/
git commit -m "feat: add middleware.Recover, the first opt-in package"
```

---

### Task 7: Allocation budgets and benchmarks

**Files:**
- Modify: `alloc_test.go`
- Create: `bench/error_bench_test.go`
- Create: `bench/results/M5-error-handling.txt`

**Interfaces:**
- Consumes: everything from Tasks 1-6.
- Produces: `TestAllocBudgetDispatchWithRecover`, `TestAllocBudget404`, `TestAllocBudgetHTTPErrorReturn` in package `rice`; five benchmarks in package `bench`.

- [ ] **Step 1: Write the failing allocation budgets**

Append to `alloc_test.go`:

```go
// TestAllocBudgetDispatchWithRecover is the load-bearing row of M5's budget
// table: an installed recovery that never fires must cost nothing. The whole
// justification for making recovery core behaviour rather than opt-in rests on
// this being 0.
func TestAllocBudgetDispatchWithRecover(t *testing.T) {
	app := New()
	app.GET("/ok", func(c *Ctx) error { return c.String(200, "ok") })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/ok")

	budget(t, "dispatch with recovery installed", 1, func() {
		app.handle(fctx)
	})
}

// TestAllocBudget404 pins D2's claim that a prebuilt ErrNotFound makes a miss
// cost exactly what a hit costs: one Ctx, nothing for the error.
func TestAllocBudget404(t *testing.T) {
	app := New()
	app.GET("/ok", func(c *Ctx) error { return nil })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/missing")

	budget(t, "404 through the funnel", 1, func() {
		app.handle(fctx)
	})
}

// TestAllocBudgetHTTPErrorReturn records the cost of a handler constructing an
// error: the Ctx plus the HTTPError. It is 2 and it is meant to be 2 — a
// budget that documents a cost rather than forbidding one.
func TestAllocBudgetHTTPErrorReturn(t *testing.T) {
	app := New()
	app.GET("/bad", func(c *Ctx) error { return NewHTTPError(400, "bad request") })
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/bad")

	budget(t, "handler returning a fresh HTTPError", 2, func() {
		app.handle(fctx)
	})
}
```

- [ ] **Step 2: Run them**

Run: `go test . -run 'AllocBudgetRecovery|AllocBudget404|AllocBudgetHTTPError' -v`
Expected: PASS. If `TestAllocBudgetDispatchWithRecover` reports more than 1, stop and report — the design's central claim is wrong and the spec needs revisiting, not the test's budget.

- [ ] **Step 3: Verify the budgets are not vacuous**

Each `budget` call must be shown to fail when the budget is tightened by one. Run each of the three with `want` reduced by 1 and record the reported figure. A budget test that passes at any value is measuring an elided expression, which happened twice in M3.

- [ ] **Step 4: Write the benchmarks**

Create `bench/error_bench_test.go`:

```go
package bench

import (
	"errors"
	"testing"

	rice "github.com/vietpham102301/rice-http"
)

// noRecover and withRecover are deliberately identical apart from the deferred
// recover, so the difference between them is the mechanism and nothing else.
//
// This exists because comparing M5's BenchmarkChainDispatch0 against M4's
// recorded file compares two sessions, and between-session drift already
// misled this project once, in M4's middleware sweep. Two arms measured in one
// run is the only honest way to price a defer.
//
//go:noinline
func noRecover(n int) int { return n + 1 }

//go:noinline
func withRecover(n int) (out int) {
	defer func() {
		if r := recover(); r != nil {
			out = -1
		}
	}()
	return n + 1
}

func BenchmarkNoRecoverBaseline(b *testing.B) {
	sink := 0
	for i := 0; i < b.N; i++ {
		sink = noRecover(sink)
	}
	globalSink = sink
}

func BenchmarkDeferRecoverOverhead(b *testing.B) {
	sink := 0
	for i := 0; i < b.N; i++ {
		sink = withRecover(sink)
	}
	globalSink = sink
}

var globalSink int

func BenchmarkDispatch404(b *testing.B) {
	app := rice.New()
	app.GET("/users", func(c *rice.Ctx) error { return nil })
	h := app.FasthttpHandler()

	fctx := newRequestCtx("GET", "/missing")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

func BenchmarkDispatchHTTPError(b *testing.B) {
	app := rice.New()
	app.GET("/bad", func(c *rice.Ctx) error { return rice.NewHTTPError(400, "bad request") })
	h := app.FasthttpHandler()

	fctx := newRequestCtx("GET", "/bad")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

func BenchmarkDispatchPlainError(b *testing.B) {
	err := errors.New("ordinary failure")

	// A quiet handler: DefaultErrorHandler logs every unrecognised error, and a
	// benchmark that logs is measuring the logger.
	app := rice.New(rice.WithErrorHandler(func(c *rice.Ctx, err error) {}))
	app.GET("/err", func(c *rice.Ctx) error { return err })
	h := app.FasthttpHandler()

	fctx := newRequestCtx("GET", "/err")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkDispatchPanic prices the whole panic path including debug.Stack().
// It is expected to be microseconds. It is recorded so that the cost of a panic
// is a number in a file rather than folklore in a code review.
func BenchmarkDispatchPanic(b *testing.B) {
	// Quiet, for the same reason as above: this path logs a full stack trace
	// through DefaultErrorHandler on every single iteration.
	app := rice.New(rice.WithErrorHandler(func(c *rice.Ctx, err error) {}))
	app.GET("/boom", func(c *rice.Ctx) error { panic("benchmark panic") })
	h := app.FasthttpHandler()

	fctx := newRequestCtx("GET", "/boom")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}
```

`BenchmarkDispatch404` and `BenchmarkDispatchHTTPError` deliberately stay on the default handler. Neither logs, because both produce an `*HTTPError` and take the first branch — and if either ever starts logging, the benchmark output makes the regression obvious immediately.

Note that `BenchmarkDispatchPanic` still allocates a stack trace per iteration inside `handle`; the quiet handler only stops it being printed.

- [ ] **Step 5: Record the results**

Run: `make bench`

Check `scripts/bench.sh` for the exact invocation and output path. The results file must carry the same header the other files in `bench/results/` carry: milestone name, date, Go version, OS, CPU.

- [ ] **Step 6: Commit**

```bash
git add alloc_test.go bench/error_bench_test.go bench/results/M5-error-handling.txt
git commit -m "test: pin M5 allocation budgets and record error-path benchmarks"
```

---

### Task 8: Documentation, ADR-0008, and the retrospective

**Files:**
- Create: `docs/adr/0008-rice-recovers-panics-in-core.md`
- Create: `docs/milestones/M5-error-handling.md`
- Modify: `docs/03-core-concepts.md` §4 and §7
- Modify: `docs/02-architecture.md` — the package layout
- Modify: `docs/05-performance-model.md` — the budget table
- Modify: `docs/04-roadmap.md` — M5 status
- Modify: `README.md` — the status warning
- Modify: `docs/progress.md` — the journal
- Modify: `docs/adr/README.md` — the ADR index

**Interfaces:**
- Consumes: the measured figures from Task 7's results file.
- Produces: documentation only. No code changes.

- [ ] **Step 1: Correct the false claim in `docs/03-core-concepts.md` §7**

The section currently says:

> **Panics.** The core installs a panic hook on the fasthttp server so a panicking handler produces a 500 and does not take the connection down.

Both halves are wrong and the correction is the point of this step. fasthttp v1.73.0 has no `PanicHandler`; `grep -rn "PanicHandler" $(go env GOMODCACHE)/github.com/valyala/fasthttp@v1.73.0/*.go` returns nothing, and the module's only `recover()` is in `Response.writeBodyStream`. Rewrite the paragraph to say that rice installs a deferred recovery in its own dispatch path, that this is necessary because fasthttp offers no hook, and that without it a panicking handler takes down the process rather than the connection.

Also in §7: delete the sentence promising "the configured error hook", which no longer describes anything. Replace the `type ErrorHandler` block with the real signatures from the spec's "Public API added" section, including `PanicError` and `DefaultErrorHandler`.

In §4, add `func (a *App) Build()` if it is still missing, and `WithErrorHandler` to the `Option` list.

- [ ] **Step 2: Update `docs/02-architecture.md`**

Move `middleware/` out of the "What later milestones add" block and into the "What exists today" tree, with the description `optional, opt-in: recover` and a note that logger, requestid and timeout are still unbuilt. Change the heading from "(through M4)" to "(through M5)". Expand the `errors.go` line from `ErrNotFound` to `HTTPError, PanicError, ErrNotFound, ErrorHandler, DefaultErrorHandler`. Update the layer diagram's caption, which currently says `HTTPError` (M5) is named but not built.

- [ ] **Step 3: Update `docs/05-performance-model.md`**

Add four rows to the budget table, with the figures Task 7 measured:

```
| Recovery installed, nothing panics | 0 extra | MEASURED M5 |
| 404 through the funnel | 1 | MEASURED M5 |
| Handler returns a fresh HTTPError | 2 | MEASURED M5 |
| Panic recovered, stack captured | documented, not bounded | MEASURED M5 |
```

Every figure must be read from `bench/results/M5-error-handling.txt` and from the `AllocsPerRun` assertions, not from memory.

- [ ] **Step 4: Write ADR-0008**

Create `docs/adr/0008-rice-recovers-panics-in-core.md`, following the shape of the other ADRs — Status, Date, Context, Decision, Alternatives, Consequences.

It must record four things, because without them a later reader sees an unconditional `defer` on the hot path and removes it as an oversight:

1. fasthttp v1.73.0 has no `PanicHandler` and no equivalent. Cite the grep and `server.go:2621`.
2. An unrecovered handler panic kills the process. Cite the probe: exit status 2, and the stack showing `workerpool.go:225`.
3. The measured cost, from Task 7's `BenchmarkNoRecoverBaseline` versus `BenchmarkDeferRecoverOverhead`, and the `AllocsPerRun` result of 0 extra allocations. Report ranges beside medians.
4. That this is a deliberate exception to design principle 7, "the hot path pays no cost for unused features", and why the exception is justified.

The alternatives section must include the option that was rejected: recovery as opt-in `middleware.Recover` only, which is what Gin and Fiber do, and why rice cannot follow them here — both run on a base that does not die outright.

Add the entry to `docs/adr/README.md`.

- [ ] **Step 5: Update the roadmap and README**

In `docs/04-roadmap.md`, change M5's `☐` to `☑`. Verify the exit criteria listed there were actually met before doing so, and say in the task report which test covers each.

In `README.md`, the status block says "There is no error-handling funnel yet and no context pooling". Remove the funnel clause. Add a short section on error handling to the body, between "Middleware and groups" and "Two phases", showing `NewHTTPError` and `WithErrorHandler`. Compile any example you add before committing — every existing example in that file was compiled against the local module.

- [ ] **Step 6: Write the retrospective**

Create `docs/milestones/M5-error-handling.md`, following `docs/milestones/M4-middleware-and-groups.md` in shape: what was built, what was measured with the numbers in a table, what was learned, what surprised, and what is still not understood.

It must cover, at minimum:

- The milestone's question — "can one funnel handle every failure mode without special cases?" — and the answer, with the `grep` from Task 2 Step 7 as the evidence rather than an assertion.
- The probe finding, which is the milestone's real story: a documented safety net that never existed, in a library that offers no way to build the one that was described. Say plainly that the document was wrong for four milestones and nobody noticed, because nothing tested it.
- The measured cost of recovery, with ranges, and an honest statement of what the numbers do and do not resolve. Report `BenchmarkNoRecoverBaseline` against `BenchmarkDeferRecoverOverhead` as the primary figure, because both arms ran in one session. If you also quote M5's `BenchmarkChainDispatch0` against M4's recorded file, the drift caveat goes in the same sentence as the number, not in a footnote: they are two different sessions, and between-session drift already produced a wrong reading in M4.
- Every guard that was broken on purpose, with the failure text.
- Anything that surprised the implementer.

- [ ] **Step 7: Add the journal entry**

Append an entry to `docs/progress.md` in the established shape. Include the numbers, and any place where a figure in the retrospective was corrected.

- [ ] **Step 8: Verify every figure**

Re-derive every number quoted in the retrospective, the journal, the ADR and the performance model directly from `bench/results/M5-error-handling.txt`, sorting the samples before taking a minimum or a median. In M4 a range low was reported from the recording order rather than the sorted order, twice, and both times it was wrong.

- [ ] **Step 9: Run everything and commit**

Run: `make lint && make test && make cover && make bench`

```bash
git add docs/ README.md
git commit -m "docs: close out M5 with ADR-0008, the retrospective, and the corrected panic claim"
```

---

## Notes for the executor

**On the false documentation claim.** `docs/03-core-concepts.md` has described a fasthttp panic hook since M1. It does not exist. This is not a stale sentence to tidy — it is a documented safety guarantee that was never real, in a codebase whose own principle 8 is "every decision is recorded, including the ones that were wrong." Task 8 Step 1 is not a chore.

**On fault injection.** Every task has a step for it, and the reason is written in M4's retrospective at length: nine separate times in this project something named as a guard turned out not to guard what it claimed, and in one of those cases the fault injection meant to validate the guard injected no fault and looked exactly like a passing experiment. When you break something and it stays green, that is the finding — report it rather than moving on.

**On the log noise.** `TestMain` sends the standard logger to `io.Discard` for package `rice`. Package `middleware`'s tests have no such guard, so any test there that reaches `DefaultErrorHandler` with an unrecognised error will print. Give those tests a quiet `WithErrorHandler`, as Task 6's tests already do where it matters.
