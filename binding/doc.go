// Package binding turns a request body into a typed value.
//
// It is opt-in and it is not imported by package rice. What a user opts into
// here is the allocation and the API surface, not the dependency: encoding/json
// is already in rice's graph through c.JSON at the response edge, which
// ADR-0006 permits. What that ADR keeps out is encoding/json in rice's routing,
// context and middleware path, and a method on Ctx would have put it there. The
// import graph is the honest signal of what costs what, which is why this is a
// separate package. See
// docs/adr/0011-binding-is-generic-and-validation-is-a-method.md.
//
// The dependency runs one way: binding imports rice.
//
// # What the client sees
//
// Every error this package returns carries an *rice.HTTPError reachable with
// errors.As, so a handler returns it and rice's funnel writes the response. It
// is not always one at the top: an error from Validate is returned as Validate
// wrote it, wrapper and all. A body that is empty or unparseable is answered
// 400 with a fixed message, and the decoder's own error is kept in
// HTTPError.Err rather than written to the response: its text is shaped by the
// caller's input. Nothing writes that cause anywhere by default —
// DefaultErrorHandler logs unhandled errors and panics, not an *HTTPError — it
// is there for a custom ErrorHandler or a logging middleware to read.
//
// A value whose Validate method returns an error is answered 422 and that
// error's message IS written to the response, because the author of the handler
// wrote it; if Validate returns, or wraps, an *rice.HTTPError, that error is
// passed through with its own status instead. The match is errors.As, so an
// *rice.HTTPError anywhere in the chain wins — including one a collaborator
// wrapped several layers down, whose status the client then sees. This package
// cannot tell an author's message from a wrapped internal error: a Validate
// that returns fmt.Errorf("checking the database: %w", err) sends that text to
// the client. Keep Validate's messages about the request.
package binding
