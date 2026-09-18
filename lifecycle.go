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
// Shutdown returns all their errors joined with its own. Hooks run on the first
// Shutdown only. A panicking hook is not recovered.
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
// returns every error they reported.
func (a *App) runShutdown(ctx context.Context) []error {
	var errs []error
	a.shutdownOnce.Do(func() {
		for i := len(a.onShutdown) - 1; i >= 0; i-- {
			if err := a.onShutdown[i](ctx); err != nil {
				errs = append(errs, err)
			}
		}
	})
	return errs
}
