package rice

// Middleware wraps a Handler and returns one that runs around it.
//
// This is the decorator shape rather than an index walk with a cursor on the
// context. Stopping the chain is an ordinary return rather than a rule to
// remember, and no per-request state is needed to track position — which is why
// Ctx gained no field for middleware. See ADR-0003.
//
//	func Logging(next rice.Handler) rice.Handler {
//		return func(c *rice.Ctx) error {
//			started := time.Now()
//			err := next(c)
//			log.Printf("%s %s %s", c.Method(), c.Path(), time.Since(started))
//			return err
//		}
//	}
//
// Middleware runs in a fixed order: application middleware first, then each
// group from outermost to innermost, then the route's own, then the handler. The
// unwind runs in reverse. On a request that matches no route, only application
// middleware runs.
type Middleware func(next Handler) Handler
