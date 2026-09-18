package rice

import "context"

// OnStart registers fn to run when the App starts serving: after the listener
// is bound and before any connection is accepted. Hooks run in registration
// order. The first error stops the sequence, closes the listener, and is
// returned by Serve or Run; no connection is ever accepted.
//
// Use it for work that must succeed before traffic arrives, such as checking a
// database is reachable.
func (a *App) OnStart(fn func() error) {
	if fn == nil {
		panic("rice: OnStart: hook is nil")
	}
	if a.built {
		panic("rice: cannot call OnStart after Build; hooks must be registered before serving begins")
	}
	a.onStart = append(a.onStart, fn)
}

// OnShutdown registers fn to run when Shutdown is called, after in-flight
// requests have drained, or been cut off at the deadline. Hooks run in reverse
// registration order, as defer does, so a resource opened first is released
// last. Each receives the ctx passed to Shutdown.
//
// Every hook runs even when an earlier one fails or the drain timed out, and
// Shutdown returns all their errors joined with its own. Hooks run once, in the
// first Shutdown to take its turn; a concurrent Shutdown that waited its turn
// returns after them, and one whose ctx ended while it waited returns without
// waiting for them. A panicking hook is not recovered.
//
// After a timed-out drain, a handler that was cut off may still be running: a
// goroutine cannot be stopped. A hook releasing something handlers use must
// allow for that.
//
// When the drain timed out, the ctx a hook receives is already done: a hook
// that needs time of its own must not derive it from ctx.
//
// Shutdown does not wait for a running OnStart hook. If it is called while one
// is running, the OnShutdown hooks run, and Shutdown returns, before that
// OnStart hook does; anything the OnStart hook opens after that point is never
// released by an OnShutdown hook.
func (a *App) OnShutdown(fn func(context.Context) error) {
	if fn == nil {
		panic("rice: OnShutdown: hook is nil")
	}
	if a.built {
		panic("rice: cannot call OnShutdown after Build; hooks must be registered before serving begins")
	}
	a.onShutdown = append(a.onShutdown, fn)
}

// runStart runs the OnStart hooks in order and returns the first error.
func (a *App) runStart() error {
	for _, fn := range a.onStart {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}

// runShutdown runs the OnShutdown hooks in reverse order, once per App, and
// returns every error they reported. It closes shutdownDone when they are done,
// even if one panics.
func (a *App) runShutdown(ctx context.Context) []error {
	var errs []error
	a.shutdownOnce.Do(func() {
		defer close(a.shutdownDone)
		for i := len(a.onShutdown) - 1; i >= 0; i-- {
			if err := a.onShutdown[i](ctx); err != nil {
				errs = append(errs, err)
			}
		}
	})
	return errs
}
