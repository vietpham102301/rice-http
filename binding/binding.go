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
}
