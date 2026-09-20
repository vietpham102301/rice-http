package binding_test

import (
	"errors"
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
