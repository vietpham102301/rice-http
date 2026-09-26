package rice_test

import (
	"runtime"
	"strings"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

func TestSSEWritesTheEventStreamFormat(t *testing.T) {
	sendErr := make(chan error, 1)
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(0, func(s *rice.SSE) error {
			if err := s.Send(rice.Event{ID: "7", Event: "tick", Data: "a\nb\r\nc\rd", Retry: 1500 * time.Millisecond}); err != nil {
				return err
			}
			if err := s.Send(rice.Event{}); err != nil {
				return err
			}
			sendErr <- s.Send(rice.Event{ID: "x\ny", Data: "never"})
			_ = s.Send(rice.Event{Event: "done"})
			<-s.Context().Done()
			return nil
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/e")
	got := readUntil(t, r, "event: done", 2*time.Second)
	for _, want := range []string{
		"Content-Type: text/event-stream",
		"Cache-Control: no-cache",
		"id: 7\nevent: tick\nretry: 1500\ndata: a\ndata: b\ndata: c\ndata: d\n\n",
		"\ndata: \n\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("response is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "never") {
		t.Error("an event with a line break in its ID was written")
	}
	if err := within(t, time.Second, "the invalid Send", sendErr); err == nil {
		t.Error("Send accepted an ID containing a line break")
	}
}

func TestSSEIgnoresARetryUnderOneMillisecond(t *testing.T) {
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(0, func(s *rice.SSE) error {
			if err := s.Send(rice.Event{Data: "a", Retry: 500 * time.Microsecond}); err != nil {
				return err
			}
			return s.Send(rice.Event{Event: "done"})
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/e")
	got := readUntil(t, r, "event: done", 2*time.Second)
	if !strings.Contains(got, "\ndata: a\n\n") {
		t.Errorf("the event is missing:\n%s", got)
	}
	if strings.Contains(got, "retry:") {
		t.Errorf("a 500µs Retry wrote a retry line:\n%s", got)
	}
}

func TestSSERejectsFramingCharacters(t *testing.T) {
	results := make(chan []bool, 1)
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(0, func(s *rice.SSE) error {
			var errs []bool
			for _, ev := range []rice.Event{
				{ID: "a\rb"}, {ID: "a\nb"}, {ID: "a\x00b"},
				{Event: "a\rb"}, {Event: "a\nb"},
				{ID: "ok", Event: "ok", Data: "line\nline"},
			} {
				errs = append(errs, s.Send(ev) != nil)
			}
			results <- errs
			return nil
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	rawRequest(t, addr, "GET", "/e")
	got := within(t, 2*time.Second, "the sends", results)
	want := []bool{true, true, true, true, true, false}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d: error %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSSEHeartbeatArrives(t *testing.T) {
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(50*time.Millisecond, func(s *rice.SSE) error {
			<-s.Context().Done()
			return nil
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/e")
	readUntil(t, r, ":\n", time.Second)
}

func TestSSEPanicsOnANegativeHeartbeat(t *testing.T) {
	recovered := make(chan any, 1)
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		defer func() { recovered <- recover() }()
		return c.SSE(-time.Second, func(*rice.SSE) error { return nil })
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	rawRequest(t, addr, "GET", "/e")
	r := within(t, 2*time.Second, "the handler running", recovered)
	if msg, _ := r.(string); !strings.HasPrefix(msg, "rice: ") {
		t.Errorf("recovered %v, want a rice: panic", r)
	}
}

func TestSSEHeartbeatNoticesAClientThatLeft(t *testing.T) {
	const every = 50 * time.Millisecond
	cancelled := make(chan struct{})
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(every, func(s *rice.SSE) error {
			if err := s.Send(rice.Event{Data: "hello"}); err != nil {
				return err
			}
			<-s.Context().Done()
			close(cancelled)
			return nil
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	conn, r := rawRequest(t, addr, "GET", "/e")
	readUntil(t, r, "data: hello", 2*time.Second)
	conn.Close()
	// fasthttp learns of the close only from a write that fails; the first
	// heartbeat after the close may still fit in the kernel's buffers.
	within(t, 10*every+time.Second, "the stream noticing the client left", cancelled)
}

// heartbeatGoroutines counts running heartbeat goroutines by their frame.
func heartbeatGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "(*Stream).heartbeat(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

func TestSSEHeartbeatStopsWithTheStream(t *testing.T) {
	finished := make(chan struct{})
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(20*time.Millisecond, func(s *rice.SSE) error {
			defer close(finished)
			return s.Send(rice.Event{Data: "one"})
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/e")
	readUntil(t, r, "data: one", 2*time.Second)
	within(t, 2*time.Second, "the callback returning", finished)
	waitFor(t, 2*time.Second, "no heartbeat goroutine remaining", func() bool {
		return heartbeatGoroutines() == 0
	})
}

// TestSSEIsDeadOnceItsCallbackReturns is modelled on Task 1's
// TestStreamIsDeadOnceItsCallbackReturns in stream_test.go: an SSE handed out
// on a channel and retained past fn's return must not touch the retired
// Stream's bufio.Writer, which fasthttp may by then have put back in its pool
// to serve another response. Send must notice the same done flag Write,
// WriteString and Flush do, and must not allocate while noticing it.
func TestSSEIsDeadOnceItsCallbackReturns(t *testing.T) {
	handed := make(chan *rice.SSE, 1)
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.SSE(0, func(s *rice.SSE) error {
			handed <- s
			return nil
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/e")
	readUntil(t, r, "0\r\n", 2*time.Second) // the chunked body has fully ended

	s := within(t, 2*time.Second, "fn handing off its SSE", handed)

	var err error
	allocs := testing.AllocsPerRun(1000, func() {
		err = s.Send(rice.Event{Data: "late"})
	})
	if err == nil {
		t.Error("Send after the stream ended: got nil error, want one")
	}
	if allocs != 0 {
		t.Errorf("Send after the stream ended allocated %.1f objects per call, want 0", allocs)
	}
}
