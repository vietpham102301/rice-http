package rice

// MIMETextPlainUTF8 is the content type String sets on the response.
const MIMETextPlainUTF8 = "text/plain; charset=utf-8"

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
