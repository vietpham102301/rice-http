package rice

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// TestConnStateActiveAndIdleAreFree pins the per-request cost of D2's hook.
// fasthttp calls ConnState twice per request; those calls must neither
// allocate nor take the lock.
func TestConnStateActiveAndIdleAreFree(t *testing.T) {
	a := New()

	if got := testing.AllocsPerRun(1000, func() {
		a.connState(nil, fasthttp.StateActive)
		a.connState(nil, fasthttp.StateIdle)
	}); got != 0 {
		t.Errorf("connState(Active, Idle) allocates %v times, want 0", got)
	}

	a.connMu.Lock()
	defer a.connMu.Unlock()
	done := make(chan struct{})
	go func() {
		a.connState(nil, fasthttp.StateActive)
		a.connState(nil, fasthttp.StateIdle)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connState(Active, Idle) blocked on connMu")
	}
}

func TestConnStateTracksOpenConnections(t *testing.T) {
	a := New()
	c1, peer1 := net.Pipe()
	c2, peer2 := net.Pipe()
	c3, peer3 := net.Pipe()
	defer peer1.Close()
	defer peer2.Close()
	defer peer3.Close()

	a.connState(c1, fasthttp.StateNew)
	a.connState(c2, fasthttp.StateNew)
	a.connState(c3, fasthttp.StateNew)
	if len(a.conns) != 3 {
		t.Fatalf("tracking %d connections after three StateNew, want 3", len(a.conns))
	}

	a.connState(c1, fasthttp.StateClosed)
	a.connState(c2, fasthttp.StateHijacked)
	if len(a.conns) != 1 {
		t.Fatalf("tracking %d connections after a close and a hijack, want 1", len(a.conns))
	}
	if _, ok := a.conns[c3]; !ok {
		t.Fatal("the remaining tracked connection is not c3")
	}
}

func TestCloseConnsClosesTrackedAndLateConnections(t *testing.T) {
	a := New()
	tracked, trackedPeer := net.Pipe()
	defer trackedPeer.Close()
	a.connState(tracked, fasthttp.StateNew)

	a.closeConns()

	if _, err := trackedPeer.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("reading the peer of a force-closed connection: %v, want io.EOF", err)
	}

	// A connection accepted before the listener closed but reported after the
	// sweep is closed on arrival and never tracked.
	late, latePeer := net.Pipe()
	defer latePeer.Close()
	a.connState(late, fasthttp.StateNew)

	if _, err := latePeer.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("reading the peer of a late connection: %v, want io.EOF", err)
	}
	if _, ok := a.conns[late]; ok {
		t.Error("a connection reported after the sweep was tracked")
	}
}

// TestConnectionsAreUntrackedAfterACleanDrain: nothing leaks through the map.
// fasthttp reports StateClosed after it decrements its open count, so
// ShutdownWithContext can return a moment before the last removal; poll.
func TestConnectionsAreUntrackedAfterACleanDrain(t *testing.T) {
	a := New()
	a.GET("/", func(c *Ctx) error { return c.String(200, "up") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = a.Serve(ln) }()

	for i := 0; i < 5; i++ {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned %v, want nil", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		a.connMu.Lock()
		n := len(a.conns)
		a.connMu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections still tracked 1s after a clean drain", n)
		}
		time.Sleep(time.Millisecond)
	}
}
