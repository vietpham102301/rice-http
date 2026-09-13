package rice

import (
	"context"
	"errors"
	"net"
)

// ErrShutdownTimeout is returned by Shutdown when in-flight requests did not
// finish before the context deadline. The server keeps draining in the
// background: fasthttp's Shutdown has no deadline of its own, so rice can stop
// waiting but cannot stop the drain.
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
func (a *App) Serve(ln net.Listener) error {
	a.Build()

	a.mu.Lock()
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
// finish, or for ctx to be done, whichever comes first.
//
// M1 is blunt: it runs fasthttp's deadline-free Shutdown in a goroutine and
// races it against the context. M7 adds lifecycle hooks and finer draining.
func (a *App) Shutdown(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- a.srv.Shutdown() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ErrShutdownTimeout
	}
}
