package rice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// ErrShutdownTimeout is returned by Shutdown when in-flight requests did not
// finish before the context ended. The returned error also wraps the context's
// own error, so errors.Is matches context.DeadlineExceeded or context.Canceled
// as well.
//
// When Shutdown returns it, the connections those requests were using have
// been closed: nothing more is served. The handlers themselves run to
// completion, because a goroutine cannot be stopped; their responses are lost.
var ErrShutdownTimeout = errors.New("rice: shutdown timed out")

// Run binds addr and serves until Shutdown is called.
//
// It blocks. Use "127.0.0.1:0" to bind an ephemeral port and read the result
// back with Addr. It builds before binding, so a bad configuration fails before
// a port is taken.
func (a *App) Run(addr string) error {
	a.Build()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return a.Serve(ln)
}

// Serve serves on an existing listener and blocks until Shutdown is called. It
// builds first, so it is safe to call directly without going through Run.
//
// An App serves once. If Shutdown has already been called, Serve closes ln and
// returns nil without serving.
//
// OnStart hooks run first, before any connection is accepted. If one fails,
// Serve closes ln and returns its error.
func (a *App) Serve(ln net.Listener) error {
	a.Build()

	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		_ = ln.Close()
		return nil
	}

	if err := a.runStart(); err != nil {
		_ = ln.Close()
		return err
	}

	// Shutdown may have run while the hooks did. Publishing ln and checking
	// closed under one lock is what lets Shutdown find it.
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	a.ln = ln
	a.mu.Unlock()

	return a.srv.Serve(ln)
}

// RunContext binds addr and serves until ctx is done, then shuts down, giving
// in-flight requests up to grace to finish. It returns once serving has
// stopped: nil after a clean shutdown, an error wrapping ErrShutdownTimeout if
// grace ran out, or the error that stopped serving early, such as a failed
// bind or OnStart hook.
//
// It is the building block for signal handling, which rice leaves to the
// standard library so the program chooses the signals:
//
//	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
//	defer stop()
//	err := app.RunContext(ctx, ":8080", 10*time.Second)
//
// A grace of zero cuts every in-flight request off at once. A negative grace
// panics.
//
// If Shutdown is called elsewhere, RunContext waits for it to drain and run
// the OnShutdown hooks, then returns nil; that Shutdown's result goes to its
// own caller. If ctx ends during that wait, RunContext shuts down with grace
// as usual, which cuts the other Shutdown's drain short at grace.
//
// RunContext can outlast grace in one case: when ctx ends while an OnStart
// hook is running. Serve does not return until that hook does, and RunContext
// waits for Serve.
func (a *App) RunContext(ctx context.Context, addr string, grace time.Duration) error {
	if grace < 0 {
		panic("rice: RunContext: grace is negative")
	}
	a.Build()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.Serve(ln) }()

	select {
	case err := <-serveErr:
		if err != nil {
			return err
		}
		// Serve returns nil only once a Shutdown has begun, so one was called
		// elsewhere. Returning now would let main exit mid-drain.
		select {
		case <-a.shutdownDone:
			return nil
		case <-ctx.Done():
			return a.shutdownWithin(grace)
		}
	case <-ctx.Done():
	}

	shutdownErr := a.shutdownWithin(grace)
	return errors.Join(shutdownErr, <-serveErr)
}

// shutdownWithin calls Shutdown with grace to drain. The ctx is not derived
// from RunContext's: that is already done, and the grace period is new time.
func (a *App) shutdownWithin(grace time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	return a.Shutdown(ctx)
}

// Addr returns the bound address, or the empty string if the App is not serving.
func (a *App) Addr() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.ln == nil {
		return ""
	}
	return a.ln.Addr().String()
}

// Shutdown stops accepting new connections and waits for in-flight requests to
// finish, or for ctx to end, whichever comes first. It returns nil after a clean
// drain and an error wrapping ErrShutdownTimeout when ctx ended first.
//
// Nothing is served after Shutdown returns, whatever it returns. When ctx ends
// before the drain does, Shutdown closes every connection still open, busy or
// not; fasthttp alone would let a busy keep-alive connection go on serving new
// requests. The handlers on those connections run to completion, because a
// goroutine cannot be stopped, and their responses are lost: when such a
// handler returns, its write fails and fasthttp logs one "error when serving
// connection ... use of closed network connection" line for that connection.
// See docs/adr/0009-shutdown-force-closes-at-deadline.md.
//
// The drain is fasthttp's: it closes idle keep-alive connections and polls for
// the rest every 100ms, so Shutdown may return up to that long after the last
// request finished. It usually waits one poll even when only idle connections
// are open, because fasthttp counts a closed connection as gone only once its
// serving goroutine has exited.
//
// OnShutdown hooks then run, after the drain and any force-close; their errors
// are joined with the drain's into the result. See OnShutdown.
//
// Shutdown is safe to call more than once and from several goroutines. Calls
// take turns: one drains, force-closes if its ctx ends, and runs the hooks
// before the next starts, so a call that waited its turn returns only after
// all of that. A later call finds nothing left to drain, returns nil, and runs
// no hooks. A call whose ctx ends while it waits does not wait on: it closes
// every open connection, as at its own deadline, and returns an error wrapping
// ErrShutdownTimeout. The call it was waiting for goes on, and returns once
// the handlers it was draining have finished.
func (a *App) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	ln := a.ln
	a.mu.Unlock()

	// Take the turn. The first select prefers it: a first Shutdown whose ctx
	// has already ended must still close the listener and run the hooks.
	select {
	case a.shutdownSem <- struct{}{}:
	default:
		select {
		case a.shutdownSem <- struct{}{}:
		case <-ctx.Done():
			a.closeConns()
			return fmt.Errorf("%w: %w", ErrShutdownTimeout, ctx.Err())
		}
	}
	defer func() { <-a.shutdownSem }()

	err := a.srv.ShutdownWithContext(ctx)
	if err == nil && ln != nil {
		// Serve publishes ln before fasthttp records it. A ShutdownWithContext
		// landing between the two finds no listener and returns nil without
		// draining. Closing ln here makes fasthttp's Serve return nil rather than
		// block. It must come after ShutdownWithContext: closing first makes
		// fasthttp's own close fail, and it reports that error. When fasthttp
		// did record ln this is a second close, and its error is noise.
		_ = ln.Close()

		// If fasthttp recorded ln after the drain above looked, it may have
		// accepted connections that nothing drained, and its stop flag is
		// reset, so they would go on serving. A second ShutdownWithContext
		// finds ln recorded and drains them; fasthttp's open count includes
		// its Serve loop until that returns, so none is missed. Otherwise it
		// finds no listener and returns nil at once. Its only other error is
		// from closing ln again, which is noise.
		if err2 := a.srv.ShutdownWithContext(ctx); ctx.Err() != nil && errors.Is(err2, ctx.Err()) {
			err = err2
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		a.closeConns()
		err = fmt.Errorf("%w: %w", ErrShutdownTimeout, ctxErr)
	}

	hookErrs := a.runShutdown(ctx)
	if len(hookErrs) == 0 {
		return err
	}
	return errors.Join(append([]error{err}, hookErrs...)...)
}
