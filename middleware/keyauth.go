package middleware

import (
	"context"

	rice "github.com/vietpham102301/rice-http"
)

// KeyAuthConfig is what KeyAuth needs. Key and Validate are required.
//
// Validate receives the request's context and the key, as a string the
// callback may keep, and behaves as BasicAuthConfig's does: the identity to
// store, whether the key is good, and an error when the check itself failed
// (answered with 500, whatever ok says).
//
// Look a key up by its hash — store SHA-256(key), never the key — or compare a
// fixed key with crypto/subtle's ConstantTimeCompare, never with ==. Validate
// must not log what it receives.
//
// Header "" reads Authorization: Bearer <key>. Any other value names a header
// whose whole value, trimmed, is the key, such as "X-API-Key"; it must be a
// valid header name. The key is never read from the query string, which ends
// up in access logs, proxies and browser history.
type KeyAuthConfig[T any] struct {
	Key      rice.Key[T]
	Validate func(ctx context.Context, key string) (T, bool, error)
	Header   string
}

// KeyAuth requires an API key, checks it with cfg.Validate, and stores the
// identity it returns under cfg.Key for the rest of the chain.
//
// A request without a key, with another scheme, or whose key Validate rejects
// gets 401; with the default header the response carries
// WWW-Authenticate: Bearer, and with a custom header no challenge, since none
// is defined for one. An error from Validate goes to the error funnel, which
// answers 500 unless a custom ErrorHandler decides otherwise.
//
// Placement is as for BasicAuth: inside CORS, Logger and otelrice, and on a
// group to protect part of an application. It does not throttle guessing; see
// the rate limiting middleware. See ADR-0021.
//
// A nil Validate, a zero Key or an invalid Header panics.
func KeyAuth[T any](cfg KeyAuthConfig[T]) rice.Middleware {
	if cfg.Validate == nil {
		panic("rice: middleware.KeyAuth: Validate is nil")
	}
	if cfg.Key == (rice.Key[T]{}) {
		panic("rice: middleware.KeyAuth: Key is the zero Key; make one with rice.NewKey")
	}
	if cfg.Header != "" && !isToken(cfg.Header) {
		panic("rice: middleware.KeyAuth: Header " + cfg.Header + " is not a valid header name")
	}
	bearer := cfg.Header == ""
	header, challenge := cfg.Header, ""
	if bearer {
		header, challenge = "Authorization", "Bearer"
	}
	validate, key := cfg.Validate, cfg.Key

	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			v := c.Header(header)
			if bearer {
				var ok bool
				if v, ok = cutScheme(v, "Bearer"); !ok {
					return unauthorized(c, challenge)
				}
			}
			v = trimOWS(v)
			if len(v) == 0 {
				return unauthorized(c, challenge)
			}

			id, ok, err := validate(c.Context(), string(v))
			if err != nil {
				return err
			}
			if !ok {
				return unauthorized(c, challenge)
			}
			key.Set(c, id)
			return next(c)
		}
	}
}
