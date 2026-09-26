package rice

import (
	"bufio"
	"context"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

// Stream is a response body written in pieces, after the handler has returned.
// Stream hands one to the callback; nothing in it belongs to the request's Ctx.
//
// Write and WriteString buffer; Flush sends what is buffered. Once the client
// has gone, each of them returns an error and the stream's context is
// cancelled. A Stream is safe for use by one goroutine at a time, plus rice's
// own heartbeat.
type Stream struct {
	mu     sync.Mutex
	w      *bufio.Writer
	ctx    context.Context
	cancel context.CancelFunc
}

// Write buffers p.
func (s *Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	n, err := s.w.Write(p)
	s.mu.Unlock()
	return n, s.fail(err)
}

// WriteString buffers v.
func (s *Stream) WriteString(v string) (int, error) {
	s.mu.Lock()
	n, err := s.w.WriteString(v)
	s.mu.Unlock()
	return n, s.fail(err)
}

// Flush sends what is buffered to the client.
func (s *Stream) Flush() error {
	s.mu.Lock()
	err := s.w.Flush()
	s.mu.Unlock()
	return s.fail(err)
}

// Context is cancelled when the stream must stop: Shutdown has begun, Shutdown
// force-closed the connections, the client has gone, or the callback returned.
//
// It is not c.Context(). That may be a middleware's, cancelled as soon as the
// handler returns, which is before the stream starts; so values and deadlines a
// middleware put there do not reach the stream. See ADR-0019.
func (s *Stream) Context() context.Context { return s.ctx }

// fail cancels the stream's context when a write has failed, which is how a
// departed client is noticed: fasthttp reports it only to the next write.
func (s *Stream) fail(err error) error {
	if err != nil {
		s.cancel()
	}
	return err
}

// Stream sends the response body in pieces: fn writes to s after the handler
// has returned. Set the status and headers before calling it; with no status set
// it is 200. Return its result from the handler:
//
//	return c.Stream(func(s *rice.Stream) error { ... })
//
// fn runs on its own goroutine, after this Ctx has been released, so it must not
// use c: read and copy what it needs from the request first. It does not run at
// all when the handler returns an error, a middleware settles the request with
// HandleError, the handler panics, or the request is HEAD.
//
// rice recovers a panic in fn and logs it with its stack. An error fn returns is
// logged unless the stream's context was already cancelled, which is how a
// stream normally ends. Neither reaches the ErrorHandler: the status has already
// been sent.
//
// Middleware sees the handler return before the stream is written: Logger's
// duration ends there, Timeout's deadline does not cover the stream, and
// middleware.Recover does not cover fn. See ADR-0019.
//
// A nil fn, or a second Stream or SSE in the same request, panics.
func (c *Ctx) Stream(fn func(s *Stream) error) error {
	c.poison.check()
	c.registerStream(0, fn)
	return nil
}

// registerStream records fn for handle to start after release, or records only
// that a stream was asked for when the request is HEAD.
func (c *Ctx) registerStream(heartbeat time.Duration, fn func(*Stream) error) {
	if fn == nil {
		panic("rice: Stream or SSE callback is nil")
	}
	if c.streamSet {
		panic("rice: Stream or SSE called twice for one request")
	}
	c.streamSet = true
	if c.fctx.IsHead() {
		// fasthttp would run the writer for HEAD with nobody reading, and an
		// event stream would never end. The headers are the whole answer.
		return
	}
	c.streamFn, c.streamHeartbeat = fn, heartbeat
}

// startStream hands fn to fasthttp as the body writer. handle calls it after
// releasing the Ctx, so fn can never run beside the handler.
func (a *App) startStream(fctx *fasthttp.RequestCtx, fn func(s *Stream) error, heartbeat time.Duration) {
	parent := a.streamCtx
	fctx.SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithCancel(parent)
		s := &Stream{w: w, ctx: ctx, cancel: cancel}

		var hbDone chan struct{}
		if heartbeat > 0 {
			hbDone = make(chan struct{})
			go s.heartbeat(heartbeat, hbDone)
		}

		// Runs last. The heartbeat must have stopped before this function
		// returns: fasthttp then puts w back in a pool, and a heartbeat still
		// writing would write into another response.
		defer func() {
			cancel()
			if hbDone != nil {
				<-hbDone
			}
			s.mu.Lock()
			_ = w.Flush()
			s.mu.Unlock()
		}()

		// fasthttp runs this function on a bare goroutine; a panic here would
		// end the process. See ADR-0019.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("rice: stream panicked: %v\n%s", r, debug.Stack())
			}
		}()

		if err := fn(s); err != nil && ctx.Err() == nil {
			log.Printf("rice: stream: %v", err)
		}
	})
}

// heartbeat writes an SSE comment every interval until the stream's context is
// cancelled. A failed write means the client has gone, and cancels the stream.
func (s *Stream) heartbeat(every time.Duration, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			_, _ = s.w.WriteString(":\n\n")
			err := s.w.Flush()
			s.mu.Unlock()
			if err != nil {
				s.cancel()
				return
			}
		}
	}
}
