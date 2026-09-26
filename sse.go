package rice

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SSE is a server-sent event stream. SSE hands one to the callback.
type SSE struct{ s *Stream }

// Event is one server-sent event. Empty fields are left out, except Data: an
// event always carries at least one data line, so a browser dispatches it.
// Retry is sent in whole milliseconds, truncated; a Retry under one millisecond
// is left out too.
type Event struct {
	ID    string
	Event string
	Data  string
	Retry time.Duration
}

// errInvalidEvent is Send's answer to an ID or event name that would break the
// stream's framing.
var errInvalidEvent = errors.New("rice: SSE event ID or name contains a line break or NUL")

// SSE streams server-sent events. It sets Content-Type: text/event-stream and
// Cache-Control: no-cache, then behaves as Stream: fn runs after the handler has
// returned, must not use c, and does not run for HEAD or when the request was
// answered with an error.
//
// While fn runs, rice writes a comment line every heartbeat, whether or not
// events are flowing. That keeps proxies from closing an idle connection and is
// how a client that left is noticed: fasthttp reports a closed connection only
// to a write. Zero disables it; a negative heartbeat panics.
//
// WithWriteTimeout, when set, limits the whole stream, not each write. Behind
// nginx, disable proxy buffering for event streams: set X-Accel-Buffering: no
// before calling SSE, or configure proxy_buffering off.
//
// Read Last-Event-ID, and anything else fn needs, before calling SSE:
//
//	last := string(c.Header("Last-Event-ID"))
//	return c.SSE(15*time.Second, func(s *rice.SSE) error { ... })
func (c *Ctx) SSE(heartbeat time.Duration, fn func(s *SSE) error) error {
	c.poison.check()
	if heartbeat < 0 {
		panic("rice: SSE heartbeat is negative; use 0 to disable it")
	}
	if fn == nil {
		panic("rice: Stream or SSE callback is nil")
	}
	c.fctx.Response.Header.SetContentType("text/event-stream")
	c.fctx.Response.Header.Set("Cache-Control", "no-cache")
	c.registerStream(heartbeat, func(s *Stream) error { return fn(&SSE{s: s}) })
	return nil
}

// Context is the stream's context; see Stream.Context.
func (x *SSE) Context() context.Context { return x.s.ctx }

// Send writes one event and flushes it. It allocates nothing.
//
// Data is split into one data line per line; \r\n, \r and \n all end a line. An
// ID containing \r, \n or NUL, or an event name containing \r or \n, would change
// the stream's framing: Send writes nothing and returns an error. It returns an
// error, too, once the client has gone, or once fn has returned: the underlying
// bufio.Writer may by then be serving another response.
func (x *SSE) Send(ev Event) error {
	if strings.ContainsAny(ev.ID, "\r\n\x00") || strings.ContainsAny(ev.Event, "\r\n") {
		return errInvalidEvent
	}
	s := x.s
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return errStreamClosed
	}
	w := s.w
	if ev.ID != "" {
		w.WriteString("id: ")
		w.WriteString(ev.ID)
		w.WriteByte('\n')
	}
	if ev.Event != "" {
		w.WriteString("event: ")
		w.WriteString(ev.Event)
		w.WriteByte('\n')
	}
	if ms := ev.Retry.Milliseconds(); ms > 0 {
		var b [20]byte
		w.WriteString("retry: ")
		// WriteByte, not Write: Write can hand its slice to the underlying
		// io.Writer, which would move b to the heap.
		for _, d := range strconv.AppendInt(b[:0], ms, 10) {
			w.WriteByte(d)
		}
		w.WriteByte('\n')
	}
	data := ev.Data
	for {
		i := strings.IndexAny(data, "\r\n")
		line := data
		if i >= 0 {
			line = data[:i]
		}
		w.WriteString("data: ")
		w.WriteString(line)
		w.WriteByte('\n')
		if i < 0 {
			break
		}
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			i++
		}
		data = data[i+1:]
	}
	w.WriteByte('\n')
	// bufio.Writer's errors are sticky, so Flush reports any earlier failure too.
	err := w.Flush()
	s.mu.Unlock()
	return s.fail(err)
}
