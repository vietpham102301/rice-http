package rice_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// rawPOST writes a POST with a body of n bytes and a Content-Length header over
// a raw connection, and returns the response status and body.
//
// It writes the body from a goroutine and reads the response concurrently,
// because a server that rejects the body answers before reading it and may
// close the connection while the client is still writing: a client that only
// read after writing would see a broken pipe instead of the status.
func rawPOST(t *testing.T, addr string, n int, extraHeaders string) (int, string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	go func() {
		fmt.Fprintf(conn, "POST /upload HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n%s\r\n", n, extraHeaders)
		chunk := []byte(strings.Repeat("a", 64*1024))
		for left := n; left > 0; left -= len(chunk) {
			if left < len(chunk) {
				chunk = chunk[:left]
			}
			if _, err := conn.Write(chunk); err != nil {
				return
			}
		}
	}()

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// uploadApp serves POST /upload, answering with the body length and counting
// how many times the handler ran.
func uploadApp(ran *atomic.Int32, opts ...rice.Option) *rice.App {
	app := rice.New(opts...)
	app.POST("/upload", func(c *rice.Ctx) error {
		ran.Add(1)
		return c.String(200, fmt.Sprint(len(c.Body())))
	})
	return app
}

func TestWithMaxBodySizeAcceptsABodyExactlyAtTheLimit(t *testing.T) {
	var ran atomic.Int32
	app := uploadApp(&ran, rice.WithMaxBodySize(1024))
	addr, _ := serve(t, app)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	status, body := rawPOST(t, addr, 1024, "")
	if status != 200 || body != "1024" {
		t.Errorf("a 1024-byte body at a 1024-byte limit: got %d %q, want 200 %q", status, body, "1024")
	}
}

func TestWithMaxBodySizeAnswers413OneByteOverAndSkipsTheHandler(t *testing.T) {
	var ran atomic.Int32
	app := uploadApp(&ran, rice.WithMaxBodySize(1024))
	addr, _ := serve(t, app)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	status, _ := rawPOST(t, addr, 1025, "")
	if status != 413 {
		t.Errorf("a 1025-byte body at a 1024-byte limit: status %d, want 413", status)
	}
	if n := ran.Load(); n != 0 {
		t.Errorf("the handler ran %d times for a rejected body, want 0", n)
	}
}

// TestTheDefaultBodyLimitAnswers413 covers an App with no option: fasthttp's
// own 4 MiB limit applies, and before rice mapped the error it answered 400
// "Error when parsing request", telling the client its request was malformed.
func TestTheDefaultBodyLimitAnswers413(t *testing.T) {
	var ran atomic.Int32
	app := uploadApp(&ran)
	addr, _ := serve(t, app)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	status, _ := rawPOST(t, addr, 4*1024*1024+1, "")
	if status != 413 {
		t.Errorf("a body one byte over the default 4 MiB: status %d, want 413", status)
	}
}

// TestTransportErrorsOtherThanBodySizeKeepFasthttpsStatuses guards the branches
// rice's transport error handler copies from fasthttp's default.
func TestTransportErrorsOtherThanBodySizeKeepFasthttpsStatuses(t *testing.T) {
	var ran atomic.Int32
	app := uploadApp(&ran)
	addr, _ := serve(t, app)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	// Larger than fasthttp's default 4096-byte read buffer.
	huge := "X-Big: " + strings.Repeat("b", 8*1024) + "\r\n"
	if status, _ := rawPOST(t, addr, 0, huge); status != 431 {
		t.Errorf("an 8 KiB header: status %d, want 431", status)
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(conn, "POST /upload HTTP/1.1\r\nHost: x\r\nContent-Length: nope\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("a malformed Content-Length: status %d, want 400", resp.StatusCode)
	}
}

// TestAStalledRequestStillAnswers408 is the timeout branch: a request cut off by
// the read timeout gets fasthttp's 408, not the 400 the default branch would give.
func TestAStalledRequestStillAnswers408(t *testing.T) {
	app := rice.New(rice.WithReadTimeout(100 * time.Millisecond))
	app.GET("/", func(c *rice.Ctx) error { return c.String(200, "up") })
	addr, _ := serve(t, app)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\nabc")

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 408 {
		t.Errorf("a body stalled past the read timeout: status %d, want 408", resp.StatusCode)
	}
}
