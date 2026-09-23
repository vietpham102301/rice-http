package middleware

import (
	"context"
	"errors"
	"time"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
)

// Timeout gives the rest of the chain a deadline d, on the context it passes
// to its database and HTTP clients, and answers 503 when that deadline is what
// made the request fail.
//
// It is cooperative. The deadline is attached to c.Context(), and work that
// honours its context — which database drivers and HTTP clients do — stops
// when it passes. A handler that ignores its context is not stopped: it runs
// to completion and its answer, however late, is returned. Timeout is a
// deadline the work is asked to honour, not a switch that cuts it off. See
// docs/adr/0014-timeout-is-cooperative.md for why it cannot be the latter as a
// middleware, and for the transport-level alternative.
//
// The chain's error becomes 503 Service Unavailable when all three hold: the
// chain returned an error; the deadline on the context this middleware created
// has passed — which under an outer Timeout may be that outer, shorter
// deadline rather than d; and the error is not already an *rice.HTTPError,
// since an author who chose a status keeps it. It asks that context rather
// than the error because many database drivers report a timeout in their own
// error type without unwrapping to context.DeadlineExceeded. The cause stays
// in HTTPError.Err; the client receives only the status text. A context
// cancelled rather than timed out is left alone.
//
// The context is cancelled the moment the chain returns, not when the deadline
// passes, so a goroutine that outlives the request must not use it.
//
// Installing it costs observability. DefaultErrorHandler logs only an error it
// cannot match as an *rice.HTTPError, and Logger records the status and the
// latency, never the error — so wrapping a plain error in a 503 turns a
// failure that was logged into one that is not. And because the condition is
// the context rather than the error, every error returned after the deadline
// is answered 503, including a genuine bug in a handler that ignored its
// context. An ErrorHandler that logs HTTPError.Err gets both back.
//
// Install it inside Logger, so the 503 is what Logger records. A route's own
// Timeout inside an application-wide one can only shorten the deadline, never
// lengthen it: a context's deadline is never later than its parent's.
//
// A d of zero or less panics.
func Timeout(d time.Duration) rice.Middleware {
	if d <= 0 {
		panic("rice: middleware.Timeout: duration must be positive")
	}
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			// Derived from c.Context(), not context.Background(), so the
			// force-close signal of ADR-0010 still reaches the handler.
			prev := c.Context()
			ctx, cancel := context.WithTimeout(prev, d)
			defer cancel() // release the timer now, not when the deadline passes
			c.SetContext(ctx)
			// The deadline belongs to the chain inside Timeout only. Restoring
			// the previous context keeps an outer middleware — Logger, which
			// hands c.Context() to slog — from receiving one that has expired.
			defer c.SetContext(prev)

			err := next(c)
			if err == nil || ctx.Err() != context.DeadlineExceeded {
				return err
			}
			var he *rice.HTTPError
			if errors.As(err, &he) {
				return err
			}
			return &rice.HTTPError{Code: fasthttp.StatusServiceUnavailable, Err: err}
		}
	}
}
