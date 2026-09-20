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
// A value whose Validate method returns an error is answered 422 and that
// error's message IS written to the response, because the author of the handler
// wrote it; if Validate returns an *rice.HTTPError, it is passed through with
// its own status instead. This package cannot tell an author's message from a
// wrapped internal error: a Validate that returns fmt.Errorf("checking the
// database: %w", err) sends that text to the client. Keep Validate's messages
// about the request.
package binding
