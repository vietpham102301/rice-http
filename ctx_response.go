package rice

import "encoding/json"

// MIMETextPlainUTF8 is the content type String sets on the response.
const MIMETextPlainUTF8 = "text/plain; charset=utf-8"

// MIMEApplicationJSON is the content type JSON sets on the response. It carries
// no charset parameter: JSON is UTF-8 by definition (RFC 8259).
const MIMEApplicationJSON = "application/json"

// Status sets the response status code and returns the context for chaining.
func (c *Ctx) Status(code int) *Ctx {
	c.poison.check()
	c.fctx.SetStatusCode(code)
	return c
}

// SetHeader sets a response header, replacing any existing value for the key.
func (c *Ctx) SetHeader(key, value string) {
	c.poison.check()
	c.fctx.Response.Header.Set(key, value)
}

// SetContentType sets the Content-Type response header.
func (c *Ctx) SetContentType(value string) {
	c.poison.check()
	c.fctx.SetContentType(value)
}

// String writes a plaintext response body and sets the content type.
//
// It allocates nothing on a warm response buffer, which is the steady state on
// a live server. See docs/05-performance-model.md.
func (c *Ctx) String(code int, s string) error {
	c.poison.check()
	c.fctx.SetStatusCode(code)
	c.fctx.SetContentType(MIMETextPlainUTF8)
	c.fctx.SetBodyString(s)
	return nil
}

// Bytes writes a raw response body and leaves the content type alone, so the
// caller can set one with SetContentType or let fasthttp default it.
//
// The bytes are copied into the response buffer, so the caller may reuse the
// slice after the call returns.
func (c *Ctx) Bytes(code int, b []byte) error {
	c.poison.check()
	c.fctx.SetStatusCode(code)
	c.fctx.SetBody(b)
	return nil
}

// NoContent sends the given status code with no body: 204 after a successful
// DELETE, 205, 304.
//
// It discards anything already written to the body, and sends no Content-Type:
// a status that carries no body must not describe one.
func (c *Ctx) NoContent(code int) error {
	c.poison.check()
	c.fctx.SetStatusCode(code)
	c.fctx.Response.ResetBody()
	c.fctx.Response.Header.SetContentType("")
	return nil
}

// JSON encodes v with encoding/json and writes it as the response body, with
// the given status and the application/json content type.
//
// It is the one place core uses encoding/json (ADR-0006), and it allocates: the
// boxing of v and the encoded bytes, plus whatever v's shape costs the encoder.
// A handler that needs zero allocations encodes the bytes itself and calls
// Bytes. See docs/05-performance-model.md.
//
// If encoding fails, the error is returned and the response is left exactly as
// it was, so the error handler writes the response and no half-written body
// goes out.
func (c *Ctx) JSON(code int, v any) error {
	c.poison.check()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.fctx.SetStatusCode(code)
	c.fctx.SetContentType(MIMEApplicationJSON)
	c.fctx.SetBody(b)
	return nil
}
