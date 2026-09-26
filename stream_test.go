package rice_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// rawRequest sends one HTTP/1.1 request on a fresh connection and returns the
// connection and a reader over the raw response, so a test can read a streamed
// body piece by piece.
func rawRequest(t *testing.T, addr, method, path string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.Write([]byte(method + " " + path + " HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return conn, bufio.NewReader(conn)
}

// readUntil reads lines until what has been read contains want, and returns it.
// It fails the test if want has not arrived within d.
func readUntil(t *testing.T, r *bufio.Reader, want string, d time.Duration) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		var b strings.Builder
		for !strings.Contains(b.String(), want) {
			line, err := r.ReadString('\n')
			b.WriteString(line)
			if err != nil {
				break
			}
		}
		ch <- b.String()
	}()
	got := within(t, d, "reading "+strconvQuote(want), ch)
	if !strings.Contains(got, want) {
		t.Fatalf("connection ended before %q arrived; read %q", want, got)
	}
	return got
}

func strconvQuote(s string) string { return `"` + s + `"` }

// lockedBuffer is a log destination the stream's goroutine and the test can
// share without a data race.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// captureStreamLog sends the standard logger to a buffer for this test and back
// to io.Discard, where the package's TestMain keeps it, afterwards.
func captureStreamLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	return buf
}

// waitFor polls cond until it holds, failing the test after d.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %v", what, d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// shutdownOnCleanup shuts app down when the test ends, so no test leaves a
// stream or a server running into the next.
func shutdownOnCleanup(t *testing.T, app *rice.App) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
	})
}

func TestStreamSendsChunksBeforeTheCallbackReturns(t *testing.T) {
	release := make(chan struct{})
	app := rice.New()
	app.GET("/s", func(c *rice.Ctx) error {
		c.SetHeader("X-Before", "yes")
		return c.Stream(func(s *rice.Stream) error {
			if _, err := s.WriteString("first\n"); err != nil {
				return err
			}
			if err := s.Flush(); err != nil {
				return err
			}
			<-release
			_, err := s.Write([]byte("second\n"))
			return err
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/s")
	got := readUntil(t, r, "first", 2*time.Second)
	if !strings.Contains(got, "200 OK") || !strings.Contains(got, "X-Before: yes") {
		t.Errorf("status line or header missing before the first chunk: %q", got)
	}
	close(release)
	readUntil(t, r, "second", 2*time.Second)
	readUntil(t, r, "0\r\n", 2*time.Second) // the chunked body ends
}

func TestStreamNeverStartsWhenTheHandlerReturnsAnError(t *testing.T) {
	ran := make(chan struct{}, 1)
	app := rice.New()
	app.GET("/err", func(c *rice.Ctx) error {
		_ = c.Stream(func(s *rice.Stream) error { ran <- struct{}{}; return nil })
		return rice.NewHTTPError(418, "")
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/err")
	got := readUntil(t, r, "I'm a teapot", 2*time.Second)
	if !strings.Contains(got, "418") {
		t.Errorf("response %q, want the 418 the handler returned", got)
	}
	select {
	case <-ran:
		t.Error("the stream ran although the handler returned an error")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStreamNeverStartsForHEAD(t *testing.T) {
	ran := make(chan struct{}, 1)
	app := rice.New()
	app.HEAD("/s", func(c *rice.Ctx) error {
		c.SetHeader("X-Kind", "stream")
		return c.Stream(func(s *rice.Stream) error { ran <- struct{}{}; return nil })
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "HEAD", "/s")
	got := readUntil(t, r, "\r\n\r\n", 2*time.Second)
	if !strings.Contains(got, "X-Kind: stream") {
		t.Errorf("HEAD response %q is missing the handler's header", got)
	}
	select {
	case <-ran:
		t.Error("the stream ran for a HEAD request")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStreamPanicIsRecoveredAndLogged(t *testing.T) {
	logs := captureStreamLog(t)
	app := rice.New()
	app.GET("/p", func(c *rice.Ctx) error {
		return c.Stream(func(s *rice.Stream) error {
			_, _ = s.WriteString("partial\n")
			_ = s.Flush()
			panic("stream exploded")
		})
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/p")
	readUntil(t, r, "partial", 2*time.Second)
	readUntil(t, r, "0\r\n", 2*time.Second)
	waitFor(t, 2*time.Second, "the panic being logged", func() bool {
		return strings.Contains(logs.String(), "rice: stream panicked: stream exploded")
	})
	if !strings.Contains(logs.String(), "goroutine ") {
		t.Errorf("the panic log has no stack: %q", logs.String())
	}
}

func TestStreamErrorIsLogged(t *testing.T) {
	logs := captureStreamLog(t)
	app := rice.New()
	app.GET("/e", func(c *rice.Ctx) error {
		return c.Stream(func(s *rice.Stream) error { return errors.New("upstream went away") })
	})
	addr, _ := serve(t, app)
	shutdownOnCleanup(t, app)

	_, r := rawRequest(t, addr, "GET", "/e")
	readUntil(t, r, "0\r\n", 2*time.Second)
	waitFor(t, 2*time.Second, "the error being logged", func() bool {
		return strings.Contains(logs.String(), "rice: stream: upstream went away")
	})
}

func TestShutdownStopsAStreamThatHonoursItsContext(t *testing.T) {
	logs := captureStreamLog(t)
	app := rice.New()
	app.GET("/s", func(c *rice.Ctx) error {
		return c.Stream(func(s *rice.Stream) error {
			_, _ = s.WriteString("open\n")
			_ = s.Flush()
			<-s.Context().Done()
			return s.Context().Err()
		})
	})
	addr, _ := serve(t, app)

	_, r := rawRequest(t, addr, "GET", "/s")
	readUntil(t, r, "open", 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v, want a clean drain", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Shutdown took %v; the stream should have stopped as it began", d)
	}
	if strings.Contains(logs.String(), "rice: stream:") {
		t.Errorf("a stream ended by Shutdown was logged as an error: %q", logs.String())
	}
}

func TestShutdownForceClosesAStreamThatIgnoresItsContext(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	app := rice.New()
	app.GET("/s", func(c *rice.Ctx) error {
		return c.Stream(func(s *rice.Stream) error {
			_, _ = s.WriteString("open\n")
			_ = s.Flush()
			<-block
			return nil
		})
	})
	addr, _ := serve(t, app)

	_, r := rawRequest(t, addr, "GET", "/s")
	readUntil(t, r, "open", 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := app.Shutdown(ctx); !errors.Is(err, rice.ErrShutdownTimeout) {
		t.Errorf("Shutdown: %v, want ErrShutdownTimeout", err)
	}
}

func TestAStreamOpenedDuringShutdownStartsCancelled(t *testing.T) {
	inHandler := make(chan struct{})
	proceed := make(chan struct{})
	sawDone := make(chan bool, 1)
	app := rice.New()
	app.GET("/s", func(c *rice.Ctx) error {
		close(inHandler)
		<-proceed
		return c.Stream(func(s *rice.Stream) error {
			select {
			case <-s.Context().Done():
				sawDone <- true
			case <-time.After(time.Second):
				sawDone <- false
			}
			return nil
		})
	})
	addr, _ := serve(t, app)

	rawRequest(t, addr, "GET", "/s")
	within(t, 2*time.Second, "the handler starting", inHandler)

	shutdownErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		shutdownErr <- app.Shutdown(ctx)
	}()
	time.Sleep(50 * time.Millisecond) // let Shutdown begin and cancel the streams
	close(proceed)

	if !within(t, 2*time.Second, "the stream running", sawDone) {
		t.Error("a stream opened after Shutdown began did not see Done at once")
	}
	if err := within(t, 3*time.Second, "Shutdown returning", shutdownErr); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestStreamPanicsOnMisuse(t *testing.T) {
	cases := map[string]func(c *rice.Ctx){
		"nil callback": func(c *rice.Ctx) { _ = c.Stream(nil) },
		"called twice": func(c *rice.Ctx) {
			_ = c.Stream(func(*rice.Stream) error { return nil })
			_ = c.Stream(func(*rice.Stream) error { return nil })
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			recovered := make(chan any, 1)
			app := rice.New()
			app.GET("/s", func(c *rice.Ctx) error {
				defer func() { recovered <- recover() }()
				call(c)
				return nil
			})
			addr, _ := serve(t, app)
			shutdownOnCleanup(t, app)
			rawRequest(t, addr, "GET", "/s")
			r := within(t, 2*time.Second, "the handler running", recovered)
			if msg, _ := r.(string); !strings.HasPrefix(msg, "rice: ") {
				t.Errorf("recovered %v, want a rice: panic", r)
			}
		})
	}
}
