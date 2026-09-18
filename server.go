package rice

import (
	"context"
	"errors"
	"fmt"
	"net"
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
func (a *App) Serve(ln net.Listener) error {
	a.Build()

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
// The drain is fasthttp's: it closes idle keep-alive connections and polls for
// the rest every 100ms, so Shutdown may return up to that long after the last
// request finished.
func (a *App) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	ln := a.ln
	a.mu.Unlock()

	err := a.srv.ShutdownWithContext(ctx)
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		a.closeConns()
		err = fmt.Errorf("%w: %w", ErrShutdownTimeout, ctxErr)
	}

	// A Serve that published ln but had not yet handed it to fasthttp is
	// invisible to ShutdownWithContext, and would block forever. Closing ln here
	// makes fasthttp's Serve return nil. It must come after ShutdownWithContext:
	// closing first makes fasthttp's own close fail, and it reports that error.
	// When fasthttp did record ln this is a second close, and its error is noise.
	if ln != nil {
		_ = ln.Close()
	}
	return err
}
