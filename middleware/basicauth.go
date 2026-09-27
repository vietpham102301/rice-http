package middleware

import (
	"bytes"
	"context"
	"encoding/base64"

	rice "github.com/vietpham102301/rice-http"
)

// BasicAuthConfig is what BasicAuth needs. Key and Validate are required.
//
// Validate receives the request's context — carrying a Timeout's deadline or
// an otelrice span when those are installed — and the user and password, as
// strings the callback may keep. It returns the identity to store under Key and
// whether the credentials are good; a non-nil error means the check itself
// failed (the store is down, say) and answers 500, whatever ok says.
//
// Validate compares secrets, not rice: compare a password against a stored
// bcrypt or argon2 hash, or a fixed secret with crypto/subtle's
// ConstantTimeCompare, never with ==. It must not log what it receives.
//
// Realm names the protection space in the WWW-Authenticate challenge; ""
// means "Restricted". It may not contain a quote, a backslash or a control
// byte.
type BasicAuthConfig[T any] struct {
	Key      rice.Key[T]
	Validate func(ctx context.Context, user, password string) (T, bool, error)
	Realm    string
}

// BasicAuth requires HTTP Basic credentials, checks them with cfg.Validate,
// and stores the identity it returns under cfg.Key for the rest of the chain:
//
//	u, _ := userKey.Get(c)
//
// A request without credentials, with another scheme, with credentials that
// are not base64 or have no colon, or that Validate rejects, gets 401 with
// WWW-Authenticate: Basic realm="…", charset="UTF-8"; all of these look the
// same to the client. An error from Validate goes to the error funnel, which
// answers 500 unless a custom ErrorHandler decides otherwise.
//
// Basic credentials are base64, which anyone on the wire can read: use it
// only behind TLS. It does not throttle guessing; see the rate limiting
// middleware.
//
// Install it inside CORS, so a preflight — which carries no credentials — is
// answered before it, and inside Logger and otelrice, so 401s are logged and
// traced. Install it on a group to protect part of an application; with
// app.Use it also answers a route miss with 401 (ADR-0012), so an
// unauthenticated client learns nothing about which routes exist. See
// ADR-0021.
//
// A nil Validate, a zero Key or an invalid Realm panics.
func BasicAuth[T any](cfg BasicAuthConfig[T]) rice.Middleware {
	if cfg.Validate == nil {
		panic("rice: middleware.BasicAuth: Validate is nil")
	}
	if cfg.Key == (rice.Key[T]{}) {
		panic("rice: middleware.BasicAuth: Key is the zero Key; make one with rice.NewKey")
	}
	realm := cfg.Realm
	if realm == "" {
		realm = "Restricted"
	}
	if !validRealm(realm) {
		panic("rice: middleware.BasicAuth: Realm may not contain a quote, a backslash or a control byte")
	}
	challenge := `Basic realm="` + realm + `", charset="UTF-8"`
	validate, key := cfg.Validate, cfg.Key

	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			enc, ok := cutScheme(c.Header("Authorization"), "Basic")
			if !ok {
				return unauthorized(c, challenge)
			}
			enc = trimOWS(enc)
			dec := make([]byte, base64.StdEncoding.DecodedLen(len(enc)))
			n, err := base64.StdEncoding.Decode(dec, enc)
			if err != nil {
				return unauthorized(c, challenge)
			}
			dec = dec[:n]
			i := bytes.IndexByte(dec, ':')
			if i < 0 {
				return unauthorized(c, challenge)
			}

			id, ok, err := validate(c.Context(), string(dec[:i]), string(dec[i+1:]))
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
