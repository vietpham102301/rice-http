package middleware

import (
	"log/slog"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// Logger writes one log line per request to l: method, path, status, latency,
// the client's address and, when RequestID is installed, the request id.
//
// Install it outermost. It settles any error the chain returns with
// c.HandleError before logging, so the status it records is the one the client
// receives — including from a custom ErrorHandler — and it returns nil, having
// consumed the error. Nothing outside it sees that error.
//
// Install Recover directly inside it. A panic unwinds through Logger's frame
// before it can record anything, so without Recover beneath it a panicking
// request is not logged at all:
//
//	app.Use(
//		middleware.Logger(l),
//		middleware.Recover(),
//		middleware.RealIP(1),
//		middleware.RequestID(),
//	)
//
// It reads the address and the id after the chain returns, so although it is
// outermost it sees what RealIP and RequestID did inside it.
//
// It records the path and never the query string, which carries tokens and
// personal data. Status 500 and above is logged at ERROR, everything else at
// INFO. A nil logger panics.
func Logger(l *slog.Logger) rice.Middleware {
	if l == nil {
		panic("rice: middleware.Logger: logger is nil")
	}
	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			start := time.Now()
			if err := next(c); err != nil {
				c.HandleError(err)
			}

			status := c.RequestCtx().Response.StatusCode()
			level := slog.LevelInfo
			if status >= 500 {
				level = slog.LevelError
			}
			// HandleError above must run regardless of whether this level is
			// enabled: a disabled Logger must still settle the request, or a
			// downstream middleware relying on HandleError having run would
			// see an unsettled one.
			ctx := c.Context()
			if !l.Enabled(ctx, level) {
				return nil
			}

			attrs := [6]slog.Attr{
				slog.String("method", string(c.Method())),
				slog.String("path", string(c.Path())),
				slog.Int("status", status),
				slog.Duration("latency", time.Since(start)),
				slog.String("ip", c.ClientIP().String()),
			}
			n := 5
			if id := RequestIDFrom(c); id != "" {
				attrs[n] = slog.String("request_id", id)
				n++
			}
			l.LogAttrs(ctx, level, "request", attrs[:n]...)
			return nil
		}
	}
}
