package middleware

import (
	"runtime/debug"

	rice "github.com/vietpham102301/rice-http"
)

// Recover converts a panic below it into a *rice.PanicError returned normally.
//
// rice already recovers panics in its core, so this is not what keeps a
// panicking handler from killing the process — that happens with or without it.
// What this adds is where the conversion happens. Core recovery sits outside
// the entire chain, so by the time it runs every middleware frame has unwound.
// A middleware written as
//
//	err := next(c)
//	record(err)
//	return err
//
// never reaches its record call when the handler panics. With Recover installed
// beneath it, the panic arrives as an ordinary return value and the rest of the
// chain behaves normally.
//
// Install it as the outermost middleware you want to protect, and put anything
// that must observe failures outside it.
func Recover() rice.Middleware {
	return func(next rice.Handler) rice.Handler {
		// The named return is the whole mechanism: the deferred function
		// overwrites it, which is how a panic becomes a returned error.
		return func(c *rice.Ctx) (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = &rice.PanicError{Value: r, Stack: debug.Stack()}
				}
			}()

			return next(c)
		}
	}
}
