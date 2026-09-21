# Binding and Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `binding.JSON[T](c)` — an opt-in package that decodes a JSON request body strictly into `T`, calls `T`'s `Validate()` if it has one, and returns an `*rice.HTTPError` the handler can return straight into rice's funnel.

**Architecture:** A new package `binding/` beside `middleware/`: same module, importing `rice`, one-way dependency. One exported generic function. Strictness comes from `json.Decoder` with `DisallowUnknownFields` plus a `dec.More()` check for trailing data. Validation is an optional interface discovered by type assertion through a pointer, never by reflection or struct tags.

**Tech Stack:** Go 1.25 generics, `encoding/json`, fasthttp v1.73.0 status constants. No new module dependency.

**Spec:** [docs/superpowers/specs/2026-09-20-binding-validation-design.md](../specs/2026-09-20-binding-validation-design.md)

## Global Constraints

- **Branch:** `binding-validation`, already created, already holding the spec commit. Do not commit to `main`.
- **Nothing in package `rice` changes.** This plan adds a package; it does not touch core. If a task appears to need a core change, stop and report it.
- **ADR-0006's boundary does not move.** Package `rice` still imports neither `reflect` nor `encoding/json` outside `c.JSON`. `binding` is a different package and may import `encoding/json`. `binding` must not import `reflect`.
- **Status constants come from fasthttp**, not `net/http`: `fasthttp.StatusBadRequest` (400) and `fasthttp.StatusUnprocessableEntity` (422). This matches `errors.go`, which uses `fasthttp.StatusNotFound` and `fasthttp.StatusInternalServerError`.
- **`encoding/json`'s error text must never reach the response body.** It is shaped by the caller's input. Only the `Validate()` branch puts a message in the response, and that message was written by the handler's author.
- **TDD.** Every test is written and seen failing before the code that satisfies it.
- **Test package is `binding_test`**, external, exactly as `middleware/recover_test.go` is `middleware_test`.
- **Run `make test` before each commit.**

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `binding/doc.go` | why the package is separate; the `Validate()` leak warning | 1 |
| `binding/binding.go` | `JSON[T]`, the decode branches, the `Validate` assertion | 1, 2 |
| `binding/binding_test.go` (`package binding_test`) | behaviour, including the dispatch helper | 1, 2 |
| `binding/alloc_test.go` (`package binding_test`) | the pinned allocation budget | 3 |
| `docs/adr/0011-*.md` (**new**), `docs/adr/README.md`, `docs/02-architecture.md`, `docs/04-roadmap.md`, `docs/05-performance-model.md`, `docs/progress.md` | documentation | 4 |

Task 2 depends on Task 1. Task 3 depends on Task 2. Task 4 depends on all.

## How these tests reach a `*rice.Ctx`

`Ctx` cannot be constructed from outside package `rice` — `reset` is unexported. External-package tests build an `App`, register a handler, and dispatch a `fasthttp.RequestCtx` through `app.FasthttpHandler()`, which calls `Build` for them. `middleware/recover_test.go:14-21` is the existing example. Every test in this plan uses that pattern, which means every test also exercises rice's error funnel — the response status and body are what the funnel wrote.

---

### Task 1: the package, and decoding

**Files:**
- Create: `binding/doc.go`, `binding/binding.go`, `binding/binding_test.go`

**Interfaces:**
- Consumes: `rice.New`, `rice.App.POST`, `rice.App.FasthttpHandler`, `rice.Ctx.Body`, `rice.HTTPError`.
- Produces: `func JSON[T any](c *rice.Ctx) (T, error)` — returns the zero value of `T` on any error; the error is always `*rice.HTTPError`.

- [ ] **Step 1: Write the failing tests**

Create `binding/binding_test.go`:

```go
package binding_test

import (
	"strings"
	"testing"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/binding"
)

// createUser is the fixture every test in this package binds into. It has no
// Validate method; the types that do are declared in the tests that need them.
type createUser struct {
	Email string `json:"email"`
	Age   int    `json:"age"`
}

// bindApp returns an App whose POST /users binds a createUser, records what
// binding returned, and answers 201 on success. The recorded values are what
// the assertions read; the response is what rice's funnel wrote.
func bindApp(got *createUser, bindErr *error) *rice.App {
	app := rice.New()
	app.POST("/users", func(c *rice.Ctx) error {
		in, err := binding.JSON[createUser](c)
		*got, *bindErr = in, err
		if err != nil {
			return err
		}
		return c.String(201, "created")
	})
	return app
}

// dispatch sends body to POST /users and returns the response fasthttp holds.
// FasthttpHandler calls Build, so the App needs no preparation.
func dispatch(t *testing.T, app *rice.App, body string) *fasthttp.RequestCtx {
	t.Helper()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("POST")
	fctx.Request.SetRequestURI("/users")
	fctx.Request.SetBodyString(body)
	app.FasthttpHandler()(fctx)
	return fctx
}

func TestJSONDecodesAValidBody(t *testing.T) {
	var got createUser
	var bindErr error
	fctx := dispatch(t, bindApp(&got, &bindErr), `{"email":"a@b.c","age":30}`)

	if bindErr != nil {
		t.Fatalf("JSON returned %v, want nil", bindErr)
	}
	if got.Email != "a@b.c" || got.Age != 30 {
		t.Errorf("decoded %+v, want {Email:a@b.c Age:30}", got)
	}
	if code := fctx.Response.StatusCode(); code != 201 {
		t.Errorf("status = %d, want 201", code)
	}
}

// TestJSONRejectsAnEmptyBody keeps the most common client mistake from being
// answered with "invalid JSON body", which sends people looking in the wrong
// place. A whitespace-only body reports io.EOF too, so it lands here as well.
func TestJSONRejectsAnEmptyBody(t *testing.T) {
	for _, body := range []string{"", "   \n"} {
		var got createUser
		var bindErr error
		fctx := dispatch(t, bindApp(&got, &bindErr), body)

		if code := fctx.Response.StatusCode(); code != 400 {
			t.Errorf("body %q: status = %d, want 400", body, code)
		}
		if b := string(fctx.Response.Body()); b != "empty request body" {
			t.Errorf("body %q: response = %q, want %q", body, b, "empty request body")
		}
	}
}

func TestJSONRejectsMalformedAndUnknownAndTrailing(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"malformed", `{"email":`},
		{"unknown field", `{"emial":"a@b.c"}`},
		{"trailing value", `{"email":"a@b.c"}{"email":"x"}`},
		{"trailing junk", `{"email":"a@b.c"} garbage`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got createUser
			var bindErr error
			fctx := dispatch(t, bindApp(&got, &bindErr), tc.body)

			if code := fctx.Response.StatusCode(); code != 400 {
				t.Errorf("status = %d, want 400", code)
			}
			if b := string(fctx.Response.Body()); b != "invalid JSON body" {
				t.Errorf("response = %q, want %q", b, "invalid JSON body")
			}
		})
	}
}

// TestJSONAcceptsATrailingNewline guards the trailing-data check against the
// shape every real client sends. dec.More() does not trip on it, and a check
// that rejected it would reject almost every request.
func TestJSONAcceptsATrailingNewline(t *testing.T) {
	var got createUser
	var bindErr error
	fctx := dispatch(t, bindApp(&got, &bindErr), `{"email":"a@b.c","age":30}`+"\n")

	if bindErr != nil {
		t.Fatalf("JSON returned %v, want nil", bindErr)
	}
	if code := fctx.Response.StatusCode(); code != 201 {
		t.Errorf("status = %d, want 201", code)
	}
}

// TestJSONDoesNotLeakTheDecoderError is the M5 rule applied here: the cause is
// reachable for logs and never written to the response. encoding/json's text is
// shaped by the caller's input, so putting it in the body would hand an
// attacker a view of the parser.
func TestJSONDoesNotLeakTheDecoderError(t *testing.T) {
	var got createUser
	var bindErr error
	fctx := dispatch(t, bindApp(&got, &bindErr), `{"emial":"a@b.c"}`)

	body := string(fctx.Response.Body())
	for _, leak := range []string{"emial", "unknown field", "json:"} {
		if strings.Contains(body, leak) {
			t.Errorf("response body %q contains %q, want the cause kept out of it", body, leak)
		}
	}

	var he *rice.HTTPError
	if !errors.As(bindErr, &he) {
		t.Fatalf("JSON returned %T, want an *rice.HTTPError", bindErr)
	}
	if he.Err == nil {
		t.Error("HTTPError.Err is nil, want the decoder's error kept for logs")
	}
	if !strings.Contains(he.Err.Error(), "emial") {
		t.Errorf("HTTPError.Err = %v, want the decoder's own message", he.Err)
	}
}

// TestJSONReturnsTheZeroValueOnError keeps a caller who ignores the error from
// seeing a half-decoded struct.
func TestJSONReturnsTheZeroValueOnError(t *testing.T) {
	var got createUser
	var bindErr error
	dispatch(t, bindApp(&got, &bindErr), `{"email":"a@b.c","age":"not a number"}`)

	if bindErr == nil {
		t.Fatal("JSON returned nil, want an error")
	}
	if got != (createUser{}) {
		t.Errorf("returned %+v on error, want the zero value", got)
	}
}
```

Add `"errors"` to the import block — `TestJSONDoesNotLeakTheDecoderError` uses `errors.As`, and Task 2's validator fixtures use `errors.New`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./binding/... 2>&1 | head -20`
Expected: the package does not build — `no required module provides package .../binding` or, once the directory exists, `undefined: binding.JSON`.

- [ ] **Step 3: Write `binding/doc.go`**

```go
// Package binding turns a request body into a typed value.
//
// It is opt-in and it is not imported by package rice. Importing rice must not
// drag in encoding/json, and the import graph is the honest signal of what
// costs what — which is why this is a separate package rather than a method on
// Ctx. See docs/adr/0011-binding-is-generic-and-validation-is-a-method.md.
//
// The dependency runs one way: binding imports rice.
//
// # What the client sees
//
// Every error this package returns is an *rice.HTTPError, so a handler returns
// it and rice's funnel writes the response. A body that is empty or unparseable
// is answered 400 with a fixed message, and the decoder's own error is kept in
// HTTPError.Err for the log rather than written to the response: its text is
// shaped by the caller's input.
//
// A value whose Validate method returns an error is answered 422, and that
// error's message IS written to the response, because the author of the handler
// wrote it. This package cannot tell an author's message from a wrapped
// internal error: a Validate that returns fmt.Errorf("checking the database:
// %w", err) sends that text to the client. Keep Validate's messages about the
// request.
package binding
```

- [ ] **Step 4: Write `binding/binding.go`**

```go
package binding

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
)

// errTrailingData is the cause recorded when a body holds more than one JSON
// value. It is never written to the response; it exists so the log says which
// of the 400s this was.
var errTrailingData = errors.New("binding: trailing data after the JSON value")

// JSON decodes the request body into T and returns it.
//
// Decoding is strict: a field the struct does not declare is an error, and so
// is anything left in the body after the first value. A misspelled field name
// is a 400 rather than a silently zero-valued struct field.
//
// If T, or *T, has a Validate() error method, it runs after decoding. An error
// from it is answered 422 and its message is written to the response; if it
// returns an *rice.HTTPError, that error is passed through untouched, so a
// handler that wants 409 for a particular rule returns one.
//
// Every error returned is an *rice.HTTPError: return it and rice's funnel
// writes the response. On any error the returned T is its zero value.
//
// It allocates. See docs/05-performance-model.md; a handler that must not
// allocate decodes c.Body() itself.
func JSON[T any](c *rice.Ctx) (T, error) {
	var out, zero T

	dec := json.NewDecoder(bytes.NewReader(c.Body()))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&out); err != nil {
		// A body that is empty or only whitespace reports io.EOF. A truncated
		// one reports io.ErrUnexpectedEOF, which does not match here, so a
		// half-sent body is never reported as an absent one.
		if errors.Is(err, io.EOF) {
			return zero, &rice.HTTPError{
				Code:    fasthttp.StatusBadRequest,
				Message: "empty request body",
				Err:     err,
			}
		}
		return zero, &rice.HTTPError{
			Code:    fasthttp.StatusBadRequest,
			Message: "invalid JSON body",
			Err:     err,
		}
	}

	// Decode reads one value and stops, so without this {"a":1}{"b":2} would
	// pass. A trailing newline does not trip More.
	if dec.More() {
		return zero, &rice.HTTPError{
			Code:    fasthttp.StatusBadRequest,
			Message: "invalid JSON body",
			Err:     errTrailingData,
		}
	}

	return out, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `make test`
Expected: green, including the seven new tests.

- [ ] **Step 6: Commit**

```bash
git add binding/
git commit -m "binding: add JSON[T], a strict decoder behind one call"
```

Use whatever `Co-Authored-By` trailer your session instructions give you.

---

### Task 2: `Validate()`

**Files:**
- Modify: `binding/binding.go` (append to `JSON`, after the `dec.More()` block)
- Modify: `binding/binding_test.go`

**Interfaces:**
- Consumes: `JSON[T]` from Task 1.
- Produces: no new exported symbol. The behaviour: a type with `Validate() error` on either receiver has it called; a returned `*rice.HTTPError` passes through; any other error becomes 422 with that error's message.

- [ ] **Step 1: Write the failing tests**

Append to `binding/binding_test.go`:

```go
// valueValidator declares Validate on the value receiver; pointerValidator
// declares it on the pointer receiver. Both must run. These are two tests
// because a type assertion on the value would pass the first and silently skip
// the second, and "I wrote Validate and it never ran" is the failure this pair
// exists to make impossible.
type valueValidator struct {
	Email string `json:"email"`
}

func (v valueValidator) Validate() error {
	if v.Email == "" {
		return errors.New("email is required")
	}
	return nil
}

type pointerValidator struct {
	Email string `json:"email"`
}

func (v *pointerValidator) Validate() error {
	if v.Email == "" {
		return errors.New("email is required")
	}
	return nil
}

// statusValidator returns an *rice.HTTPError of its own, which must survive
// untouched rather than being wrapped in a 422.
type statusValidator struct {
	Email string `json:"email"`
}

func (v statusValidator) Validate() error {
	return &rice.HTTPError{Code: 409, Message: "email already taken"}
}

func TestValidateRunsOnAValueReceiver(t *testing.T) {
	app := rice.New()
	app.POST("/users", func(c *rice.Ctx) error {
		_, err := binding.JSON[valueValidator](c)
		if err != nil {
			return err
		}
		return c.String(201, "created")
	})

	fctx := dispatch(t, app, `{"email":""}`)

	if code := fctx.Response.StatusCode(); code != 422 {
		t.Errorf("status = %d, want 422", code)
	}
	if b := string(fctx.Response.Body()); b != "email is required" {
		t.Errorf("response = %q, want %q", b, "email is required")
	}
}

func TestValidateRunsOnAPointerReceiver(t *testing.T) {
	app := rice.New()
	app.POST("/users", func(c *rice.Ctx) error {
		_, err := binding.JSON[pointerValidator](c)
		if err != nil {
			return err
		}
		return c.String(201, "created")
	})

	fctx := dispatch(t, app, `{"email":""}`)

	if code := fctx.Response.StatusCode(); code != 422 {
		t.Errorf("status = %d, want 422: a pointer-receiver Validate was skipped", code)
	}
	if b := string(fctx.Response.Body()); b != "email is required" {
		t.Errorf("response = %q, want %q", b, "email is required")
	}
}

func TestValidatePassingLetsTheHandlerProceed(t *testing.T) {
	app := rice.New()
	app.POST("/users", func(c *rice.Ctx) error {
		in, err := binding.JSON[valueValidator](c)
		if err != nil {
			return err
		}
		return c.String(201, in.Email)
	})

	fctx := dispatch(t, app, `{"email":"a@b.c"}`)

	if code := fctx.Response.StatusCode(); code != 201 {
		t.Errorf("status = %d, want 201", code)
	}
	if b := string(fctx.Response.Body()); b != "a@b.c" {
		t.Errorf("response = %q, want %q", b, "a@b.c")
	}
}

// TestATypeWithoutValidateIsAccepted pins that validation is optional: no
// registration, no panic, no warning.
func TestATypeWithoutValidateIsAccepted(t *testing.T) {
	var got createUser
	var bindErr error
	fctx := dispatch(t, bindApp(&got, &bindErr), `{"email":"a@b.c","age":30}`)

	if bindErr != nil {
		t.Fatalf("JSON returned %v, want nil", bindErr)
	}
	if code := fctx.Response.StatusCode(); code != 201 {
		t.Errorf("status = %d, want 201", code)
	}
}

// TestValidateMayChooseItsOwnStatus is what makes 422 a default rather than a
// rule, without adding an option to the package.
func TestValidateMayChooseItsOwnStatus(t *testing.T) {
	app := rice.New()
	app.POST("/users", func(c *rice.Ctx) error {
		_, err := binding.JSON[statusValidator](c)
		if err != nil {
			return err
		}
		return c.String(201, "created")
	})

	fctx := dispatch(t, app, `{"email":"a@b.c"}`)

	if code := fctx.Response.StatusCode(); code != 409 {
		t.Errorf("status = %d, want 409: Validate's own HTTPError was overwritten", code)
	}
	if b := string(fctx.Response.Body()); b != "email already taken" {
		t.Errorf("response = %q, want %q", b, "email already taken")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./binding/... -run Validate 2>&1 | head -20`
Expected: `TestValidateRunsOnAValueReceiver`, `TestValidateRunsOnAPointerReceiver` and `TestValidateMayChooseItsOwnStatus` fail with `status = 201, want 422` (and `want 409`) — `JSON` does not call `Validate` yet, so a body that should be rejected succeeds. `TestValidatePassingLetsTheHandlerProceed` and `TestATypeWithoutValidateIsAccepted` pass already; they are the guards that Task 2 does not overreach.

- [ ] **Step 3: Implement**

Append to `JSON` in `binding/binding.go`, replacing the final `return out, nil`:

```go
	// The assertion goes through &out, not out. A Validate declared on the
	// value receiver is in the method set of both T and *T; one declared on the
	// pointer receiver is in the method set of *T only. Asserting on the value
	// would silently skip the pointer-receiver case.
	if v, ok := any(&out).(interface{ Validate() error }); ok {
		if err := v.Validate(); err != nil {
			// An author who returns an *rice.HTTPError has chosen a status.
			// Honour it rather than burying it in a 422.
			var he *rice.HTTPError
			if errors.As(err, &he) {
				return zero, err
			}
			// The message is written to the response. It is safe because the
			// author wrote it, not because this package checked it.
			return zero, &rice.HTTPError{
				Code:    fasthttp.StatusUnprocessableEntity,
				Message: err.Error(),
				Err:     err,
			}
		}
	}

	return out, nil
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test`
Expected: green, including all five new tests.

- [ ] **Step 5: Commit**

```bash
git add binding/
git commit -m "binding: run Validate when the bound type has one"
```

---

### Task 3: the allocation budget

**Files:**
- Create: `binding/alloc_test.go` (`package binding_test`)

**Interfaces:**
- Consumes: `JSON[T]` and the `createUser` fixture from Task 1.
- Produces: the measured number, which Task 4 writes into `docs/05-performance-model.md`.

- [ ] **Step 1: Write the measurement**

`binding`'s tests cannot use package `rice`'s `budget` helper — it is unexported. This file carries its own, and it measures inside a handler, because a `*rice.Ctx` is only valid there.

Create `binding/alloc_test.go`:

```go
package binding_test

import (
	"testing"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/binding"
)

// TestAllocBudgetJSONBinding pins binding.JSON's cost exactly, the way
// TestAllocBudgetJSON pins c.JSON's in package rice: this package buys
// ergonomics with allocations and says how many. Pinned rather than bounded, so
// a cheaper encoder fails here and the documented figure is corrected instead
// of going stale.
//
// The figure is for the createUser fixture. A larger or deeper type costs
// whatever encoding/json charges for its shape.
//
// The measurement runs inside the handler because a *rice.Ctx is valid only
// there. AllocsPerRun's own bookkeeping is not attributed to the closure.
func TestAllocBudgetJSONBinding(t *testing.T) {
	const want = 0 // replaced in step 2 with the measured figure

	var got float64
	app := rice.New()
	app.POST("/users", func(c *rice.Ctx) error {
		_, _ = binding.JSON[createUser](c) // warm
		got = testing.AllocsPerRun(1000, func() {
			_, _ = binding.JSON[createUser](c)
		})
		return nil
	})

	dispatch(t, app, `{"email":"a@b.c","age":30}`)

	if got != want {
		t.Errorf("binding.JSON allocated %.1f objects per call, want exactly %.0f", got, want)
	}
}
```

- [ ] **Step 2: Measure, then pin**

Run: `go test ./binding/ -run TestAllocBudgetJSONBinding -v`
Expected: FAIL, reporting the real figure — `binding.JSON allocated N.0 objects per call, want exactly 0`.

Read N from that output and set `want = N`. **Do not round it, and do not guess it before running.** Record the exact failure line in your report; Task 4 puts the figure in the performance model.

- [ ] **Step 3: Run the test to verify it passes**

Run: `go test ./binding/ -run TestAllocBudgetJSONBinding -v`
Expected: PASS.

- [ ] **Step 4: Confirm the pin is real in both directions**

A pinned budget must fail when the number moves either way. Temporarily change `want` to `N-1`, run, and confirm it fails; change it to `N+1`, run, confirm it fails; restore `N`. Record both outputs in your report. This takes the place of the break-on-purpose step used elsewhere in this repository, and it is cheaper and more direct.

- [ ] **Step 5: Run the full suite and commit**

Run: `make test`

```bash
git add binding/alloc_test.go
git commit -m "binding: pin JSON's allocation cost for the createUser fixture"
```

---

### Task 4: ADR-0011 and the documentation

**Files:**
- Create: `docs/adr/0011-binding-is-generic-and-validation-is-a-method.md`
- Modify: `docs/adr/README.md`, `docs/02-architecture.md`, `docs/04-roadmap.md`, `docs/05-performance-model.md`, `docs/progress.md`

**Interfaces:**
- Consumes: the measured allocation figure from Task 3, and the fixture it was measured with.
- Produces: nothing code depends on.

- [ ] **Step 1: Write ADR-0011**

Follow `docs/adr/README.md`'s format exactly — `Context`, `Decision`, `Alternatives`, `Consequences` — with `Status: Accepted` and `Date: 2026-09-20`. Read `docs/adr/0010-request-context-cancels-at-force-close.md` first for the house voice. It must contain:

- **Context:** ADR-0006 permitted binding as an opt-in package and named generics "the most promising future direction". This settles the shape.
- **Decision:** `binding.JSON[T](c) (T, error)` in a package outside core; strictness via `DisallowUnknownFields` plus a trailing-data check; validation via an optional `Validate() error` discovered by asserting through `*T`; 400 for decode failures with a fixed message, 422 for validation with the author's message, and an `*rice.HTTPError` from `Validate` passed through unchanged.
- **Alternatives**, each with why it lost: **reflection plus `validate:"..."` struct tags and a third-party validator** — what every other Go framework does and what users expect, rejected on principle 2 because the rule stops being readable at the call site and a reader must consult another library's documentation; **a fully reflection-free decoder written per type** — the purest reading of ADR-0006, rejected because it is verbose enough that nobody would import it, and a helper nobody imports should not exist.
- **Consequences.** Makes easy: one line at the call site, rules that are ordinary Go code a reader can follow and a test can exercise directly, no new module dependency. Makes hard: the author writes `Validate` by hand, and **an author who wraps an internal error in `Validate`'s message sends that text to the client — this package cannot tell the two apart, so it is documented rather than detected**. The cost of 422: it is defined by RFC 4918 rather than RFC 9110, so some intermediaries treat it as unfamiliar.
- **A statement that ADR-0006's boundary has not moved:** package `rice` still imports neither `reflect` nor `encoding/json` outside `c.JSON`. `binding` is a different package, and its `encoding/json` import is visible in the user's import list, which is exactly what ADR-0006 asked for.
- **A revisit-if line:** if `encoding/json` gains a strict decode that does not require a `Decoder`, the allocation figure and D6's reasoning are re-read.

Add one row to the index table in `docs/adr/README.md`:

```markdown
| [0011](0011-binding-is-generic-and-validation-is-a-method.md) | Binding is generic and validation is a method | Accepted |
```

- [ ] **Step 2: Update the roadmap**

In `docs/04-roadmap.md`, **remove** this line from *Explicitly deferred* (it is at line 157 at the time of writing; confirm before deleting):

```markdown
- Request binding and validation, as an opt-in side package
```

Add an entry under *Done after M8* naming the package, `binding.JSON[T]`, ADR-0011, and the scope that was deliberately left out: no `Content-Type` check, no body-size limit of its own, no query or header binding.

- [ ] **Step 3: Update the performance model**

In `docs/05-performance-model.md`, add a new section — *Opt-in packages* — **after** the `Ctx` tables, not inside them. Nothing in `binding` is on the hot path, and a row among the `Ctx` rows would imply every user pays this.

```markdown
### Opt-in packages

| Operation | Budget | Status | Enforced by |
| --- | --- | --- | --- |
| `binding.JSON` with the `createUser` fixture | <N>, exactly | MEASURED after M8 | `TestAllocBudgetJSONBinding` |
```

Replace `<N>` with the figure Task 3 measured. Below the table, say in prose: why it allocates at all (`DisallowUnknownFields` exists only on `json.Decoder`, which cannot be pooled because it has no `Reset`), that the figure is for a named fixture and a different shape costs differently, that it is pinned in both directions rather than bounded, and that the escape hatch is `c.Body()` with a hand-written decode.

- [ ] **Step 4: Update the architecture layout**

In `docs/02-architecture.md`, add `binding/` to the package layout beside `middleware/`, with a one-line description naming its one-way dependency on `rice`.

- [ ] **Step 5: Write the progress entry**

Prepend to `docs/progress.md`, above the newest entry, using the file's `Did` / `Learned` / `Measured` / `Next` shape, dated the day the work lands, milestone `post-M8`. It must record:

- **Did:** the package, the one exported function, the strictness, the optional `Validate`, ADR-0011, and the scope deliberately left out.
- **Learned:** the three findings the design probe produced, which are what the error branches rest on — a whitespace-only body reports `io.EOF` exactly as an empty one does; a truncated body reports `io.ErrUnexpectedEOF`, which does **not** match `errors.Is(err, io.EOF)`, so the empty-body branch cannot swallow a half-sent body and misreport it; and a trailing newline does not trip `dec.More()`, so the trailing-data check produces no false rejections on real clients. Also that the pointer-receiver assertion is the one mistake here that would fail silently — a `Validate` that never runs looks exactly like a `Validate` that passed.
- **Measured:** the pinned allocation figure and the fixture, and the coverage from `make cover`. If coverage dropped, say so.
- **Next:** the group-B middleware set — Logger, RequestID, Timeout, CORS.

- [ ] **Step 6: Verify and commit**

Run: `make test && go test -race ./... && go test -tags ricedebug ./... && make lint && make cover`

Record the coverage figure for the progress entry.

```bash
git add docs/
git commit -m "docs: ADR-0011 and the docs for the binding package"
```

---

## Self-Review

**Spec coverage.** D1 → Task 1 step 4 and Task 2 step 3. D2 → Task 2's two receiver tests and the `&out` assertion. D3 → Task 1's `dec.More()` block, its trailing-data cases, and the trailing-newline guard. D4 → Task 1's three error branches and the leak test, plus `doc.go`'s warning. D5 → Task 2's 422 and the `statusValidator` passthrough test. D6 → Task 3 in full. D7 → `binding/doc.go` in Task 1 and ADR-0011 in Task 4. Non-goals → no task adds a `Content-Type` check, a size limit, or query binding. Testing section → Tasks 1 and 2 cover all eleven listed cases; the spec's "one end-to-end test through a real app" is satisfied structurally, since every test here dispatches through `FasthttpHandler` and asserts on what the funnel wrote — noted explicitly in *How these tests reach a `*rice.Ctx`*. Documentation → Task 4. Exit criteria → Task 4 step 6.

**Placeholder scan.** The one number this plan cannot contain is `binding.JSON`'s allocation count, because inventing it is precisely the failure mode Task 3 exists to prevent. Task 3 step 2 is written as an instruction to measure, with the literal `want = 0` placed so the first run fails and prints the real figure. Every other step carries its actual content.

**Type consistency.** `JSON[T any](c *rice.Ctx) (T, error)` is spelled identically in Tasks 1, 2, 3 and 4. The fixture is `createUser` in Tasks 1, 3 and 4 (lower-case: it is test-local and never exported). `errTrailingData`, `valueValidator`, `pointerValidator` and `statusValidator` each appear in exactly one task. The helpers `bindApp` and `dispatch` are defined in Task 1 and used unchanged in Tasks 2 and 3. `TestAllocBudgetJSONBinding` is spelled the same in Task 3 and in Task 4's performance-model row.
