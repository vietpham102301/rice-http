package middleware

import (
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
)

// CORSConfig is what CORS needs to know. Only Origins is required.
//
// Origins are compared byte-for-byte with the Origin header a browser sends:
// lower case, scheme://host, a port only when it is not the scheme's default,
// no path and no trailing slash. "https://app.example.com/" never matches
// anything, and CORS panics on it. There is no wildcard and no pattern; each
// origin is listed.
//
// AllowMethods defaults to GET, HEAD, POST, PUT, PATCH and DELETE — the verbs
// a JSON API uses — when empty. AllowHeaders has no default: the browser's
// safelist does not include Authorization or Content-Type: application/json,
// so a JSON API lists both. ExposeHeaders names the response headers a script
// may read beyond the safelist; X-Request-Id lets a client quote its id.
// MaxAge is sent in whole seconds and omitted when zero, in which case the
// browser caches a preflight for five seconds. AllowCredentials adds
// Access-Control-Allow-Credentials: true to every response for an allowed
// origin, which a browser requires before it will send cookies.
type CORSConfig struct {
	Origins          []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	MaxAge           time.Duration
	AllowCredentials bool
}

// header is one compiled response header.
type header struct{ key, value string }

var defaultAllowMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}

// CORS lets a browser application served from one of cfg.Origins call this
// service: it answers the browser's preflight and puts the CORS headers on
// every other response, including a 401, a 404 and a 500.
//
// Install it with app.Use. A browser's preflight is an OPTIONS request for a
// path that usually has no OPTIONS route — a miss — and only application
// middleware runs on a miss (ADR-0012). A CORS installed on a group or a route
// decorates real responses and never sees a preflight, so the browser fails on
// its first non-simple request. Install it inside Logger, so the preflight's
// 204 is logged, and before any middleware that answers 401 or 403, so those
// answers carry the headers and the browser reports the status rather than a
// CORS failure.
//
// It states a policy and leaves the browser to enforce it. On a preflight it
// sends the configured Access-Control-Allow-Methods and -Headers; the browser
// compares the method and headers it intends to send against them and blocks
// the request itself when they are not listed. Access-Control-Request-Method
// and -Headers are never parsed here. The one check the server cannot delegate
// is the origin, and that is the only one made: an origin not in the list
// receives no CORS headers. A real request from it is still served — the
// browser blocks the script from reading the result — and a preflight from it
// is still answered 204, with the absence of headers as the answer.
//
// Every header is written before the rest of the chain runs, and rice's error
// funnel does not reset headers, so they survive whatever the chain returns.
// A custom ErrorHandler that resets the response drops them.
//
// Vary: Origin is added to every response, with or without an Origin header,
// so a shared cache never serves one origin's response to another.
//
// A configuration that cannot work panics at construction: see CORSConfig.
func CORS(cfg CORSConfig) rice.Middleware {
	origins, real, preflight := compileCORS(cfg)
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			h := &c.RequestCtx().Response.Header
			// Every response, with or without an Origin: a shared cache must
			// learn from the response that had none that the next one may
			// differ. Add, not Set, so a handler's own Vary survives.
			h.Add("Vary", "Origin")

			origin := c.Header("Origin")
			if len(origin) == 0 {
				return next(c)
			}
			allowed := matchOrigin(origins, origin)

			if isPreflight(c) {
				if allowed != "" {
					h.Set("Access-Control-Allow-Origin", allowed)
					for _, p := range preflight {
						h.Set(p.key, p.value)
					}
				}
				// Matched or not: the absence of headers is the "no". The
				// chain never sees a preflight, so a route's own OPTIONS
				// handler answers only ordinary OPTIONS requests.
				return c.NoContent(fasthttp.StatusNoContent)
			}

			if allowed != "" {
				// The configured string, equal to the header's bytes: no
				// []byte-to-string conversion and no allocation.
				h.Set("Access-Control-Allow-Origin", allowed)
				for _, p := range real {
					h.Set(p.key, p.value)
				}
			}
			return next(c)
		}
	}
}

// matchOrigin returns the configured origin equal to the request's, or "".
// The comparison is exact: no case folding, no prefix, no normalisation.
// string(origin) == o compiles to a comparison, not a conversion.
func matchOrigin(origins []string, origin []byte) string {
	for _, o := range origins {
		if string(origin) == o {
			return o
		}
	}
	return ""
}

// compileCORS validates cfg and does every piece of work that can be done
// once: it copies the origins, joins the lists, renders MaxAge, and builds the
// fixed header pairs for a real response and for a preflight. An empty field
// produces no pair, so the per-request path has no branch on configuration.
func compileCORS(cfg CORSConfig) (origins []string, real, preflight []header) {
	if len(cfg.Origins) == 0 {
		panic("rice: middleware.CORS: Origins is empty")
	}
	origins = make([]string, len(cfg.Origins))
	for i, o := range cfg.Origins {
		if !validOrigin(o) {
			panic("rice: middleware.CORS: origin " + strconv.Quote(o) + " is not scheme://host[:port] in lower case")
		}
		origins[i] = o
	}
	if cfg.MaxAge < 0 {
		panic("rice: middleware.CORS: MaxAge must not be negative")
	}
	methods := cfg.AllowMethods
	if len(methods) == 0 {
		methods = defaultAllowMethods
	}

	preflight = append(preflight, header{"Access-Control-Allow-Methods", joinList("AllowMethods", methods)})
	if v := joinList("AllowHeaders", cfg.AllowHeaders); v != "" {
		preflight = append(preflight, header{"Access-Control-Allow-Headers", v})
	}
	if cfg.MaxAge > 0 {
		preflight = append(preflight, header{"Access-Control-Max-Age", strconv.FormatInt(int64(cfg.MaxAge/time.Second), 10)})
	}
	if cfg.AllowCredentials {
		cred := header{"Access-Control-Allow-Credentials", "true"}
		preflight = append(preflight, cred)
		real = append(real, cred)
	}
	if v := joinList("ExposeHeaders", cfg.ExposeHeaders); v != "" {
		real = append(real, header{"Access-Control-Expose-Headers", v})
	}
	return origins, real, preflight
}

// validOrigin reports whether o has the shape a browser puts in Origin:
// scheme://host[:port], lower case, with no path, query or fragment. A
// trailing slash is a path. "*" and "null" have no scheme and fail.
func validOrigin(o string) bool {
	scheme, rest, ok := strings.Cut(o, "://")
	if !ok || scheme == "" || rest == "" {
		return false
	}
	if strings.ContainsAny(rest, "/?# \t") {
		return false
	}
	for i := 0; i < len(o); i++ {
		if b := o[i]; b >= 'A' && b <= 'Z' || b < 0x21 {
			return false
		}
	}
	return true
}

// joinList joins items as "A, B, C", or returns "" for none. An empty item, or
// one containing a comma or whitespace, would corrupt the joined value and
// panics naming the field.
func joinList(field string, items []string) string {
	for _, it := range items {
		if it == "" || strings.ContainsAny(it, ", \t") {
			panic("rice: middleware.CORS: " + field + " entry " + strconv.Quote(it) + " must not be empty or contain a comma or whitespace")
		}
	}
	return strings.Join(items, ", ")
}

// isPreflight reports whether this is a browser's preflight: OPTIONS with an
// Access-Control-Request-Method. The caller has already checked Origin. The
// header's value is not read; only its presence says what the request is.
func isPreflight(c *rice.Ctx) bool {
	return string(c.Method()) == fasthttp.MethodOptions &&
		len(c.Header("Access-Control-Request-Method")) > 0
}
