package middleware

import (
	"crypto/rand"
	"encoding/hex"

	rice "github.com/vietpham102301/rice-http"
)

const (
	requestIDHeader = "X-Request-Id"

	// requestIDKey is the store key. It is unexported and namespaced so that no
	// handler's own c.Set can collide with it; read the id with RequestIDFrom.
	requestIDKey = "rice/middleware.request-id"
)

// RequestID gives every request an id, stores it for RequestIDFrom, and writes
// it to the X-Request-Id response header so a client can quote it.
//
// An incoming X-Request-Id is kept when it is 1 to 64 characters from
// [A-Za-z0-9_-]. That is how a gateway's id survives into this service's logs
// for cross-service tracing. Anything else is discarded and a new id is
// generated: 16 random bytes, hex-encoded. The character set is the security
// control — an id goes into every log line for its request, so one containing
// a newline would let a client forge log entries.
//
// The id is not put into c.Context: that costs an allocation on every request
// for a use most handlers never have. To propagate it to an outbound call,
// write
//
//	c.SetContext(context.WithValue(c.Context(), requestIDKey{}, middleware.RequestIDFrom(c)))
//
// with a key type of your own.
func RequestID() rice.Middleware {
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			var id string
			if in := c.Header(requestIDHeader); validRequestID(in) {
				id = string(in) // copied: the header's bytes are borrowed
			} else {
				id = newRequestID()
			}
			c.Set(requestIDKey, id)
			c.SetHeader(requestIDHeader, id)
			return next(c)
		}
	}
}

// RequestIDFrom returns the id RequestID assigned, or "" when RequestID is not
// installed.
func RequestIDFrom(c *rice.Ctx) string {
	v, ok := c.Get(requestIDKey)
	if !ok {
		return ""
	}
	id, _ := v.(string)
	return id
}

// validRequestID reports whether an incoming id is safe to put in a log line.
func validRequestID(id []byte) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, b := range id {
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '-', b == '_':
		default:
			return false
		}
	}
	return true
}

// newRequestID returns 16 random bytes, hex-encoded. crypto/rand.Read cannot
// fail as of Go 1.24: it crashes the program rather than return an error.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
