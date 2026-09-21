package binding_test

import (
	"errors"
	"fmt"
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

// wrappingValidator returns an *rice.HTTPError wrapped in a message of its
// own, which is the shape fmt.Errorf("...: %w", err) produces and the reason
// the passthrough uses errors.As rather than a type assertion.
type wrappingValidator struct {
	Email string `json:"email"`
}

func (v wrappingValidator) Validate() error {
	return fmt.Errorf("checking the email against the directory: %w",
		&rice.HTTPError{Code: 409, Message: "email already taken"})
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

// TestValidateMayWrapItsOwnStatus is the reason the passthrough is errors.As
// and not a type assertion: an author who adds context with fmt.Errorf still
// gets the status they chose. The error binding returns is the wrapper, so its
// dynamic type is *fmt.wrapError rather than *rice.HTTPError — what the
// package promises is that an *rice.HTTPError is reachable with errors.As, and
// rice's funnel finds it the same way.
func TestValidateMayWrapItsOwnStatus(t *testing.T) {
	var bindErr error
	app := rice.New()
	app.POST("/users", func(c *rice.Ctx) error {
		_, err := binding.JSON[wrappingValidator](c)
		bindErr = err
		if err != nil {
			return err
		}
		return c.String(201, "created")
	})

	fctx := dispatch(t, app, `{"email":"a@b.c"}`)

	if code := fctx.Response.StatusCode(); code != 409 {
		t.Errorf("status = %d, want 409: a wrapped *rice.HTTPError lost its status", code)
	}
	if b := string(fctx.Response.Body()); b != "email already taken" {
		t.Errorf("response = %q, want %q", b, "email already taken")
	}

	// The author's outer message survives in the chain, which is why the
	// wrapper is returned rather than the *rice.HTTPError it carries.
	if bindErr == nil {
		t.Fatal("JSON returned nil, want the wrapped error")
	}
	if !strings.Contains(bindErr.Error(), "checking the email against the directory") {
		t.Errorf("returned error = %v, want the author's outer message kept", bindErr)
	}
	var he *rice.HTTPError
	if !errors.As(bindErr, &he) {
		t.Fatalf("errors.As found no *rice.HTTPError in %T", bindErr)
	}
	if he.Code != 409 {
		t.Errorf("reachable HTTPError.Code = %d, want 409", he.Code)
	}
}
