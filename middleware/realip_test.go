package middleware_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// ipApp answers GET /ip with the address rice attributes the request to.
func ipApp(trustedHops int) *rice.App {
	app := rice.New()
	app.Use(middleware.RealIP(trustedHops))
	app.GET("/ip", func(c *rice.Ctx) error { return c.String(200, c.ClientIP().String()) })
	return app
}

// dispatchFrom sends GET /ip as if from peer, the address of the proxy this
// service is connected to, with one X-Forwarded-For header line per xff value.
// It returns the address rice attributed the request to.
func dispatchFrom(t *testing.T, app *rice.App, peer string, xff ...string) string {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod("GET")
	req.SetRequestURI("/ip")
	for _, v := range xff {
		req.Header.Add("X-Forwarded-For", v)
	}
	fctx := &fasthttp.RequestCtx{}
	fctx.Init(&req, &net.TCPAddr{IP: net.ParseIP(peer), Port: 4000}, nil)
	app.FasthttpHandler()(fctx)
	return string(fctx.Response.Body())
}

func TestRealIPTakesTheEntryWrittenByTheTrustedProxy(t *testing.T) {
	// The client sent its own X-Forwarded-For claiming 198.51.100.7; the
	// trusted proxy appended the address it actually saw.
	got := dispatchFrom(t, ipApp(1), "10.0.0.2", "198.51.100.7, 203.0.113.9")
	if got != "203.0.113.9" {
		t.Errorf("address = %s, want 203.0.113.9", got)
	}
}

func TestRealIPCountsTwoTrustedHops(t *testing.T) {
	// CDN appended the client; the load balancer appended the CDN.
	got := dispatchFrom(t, ipApp(2), "10.0.0.2", "198.51.100.7, 203.0.113.9, 192.0.2.44")
	if got != "203.0.113.9" {
		t.Errorf("address = %s, want 203.0.113.9", got)
	}
}

// TestRealIPFallsBackWhenThereAreTooFewEntries pins that the middleware never
// reaches for the leftmost entry: that is the part the client wrote.
func TestRealIPFallsBackWhenThereAreTooFewEntries(t *testing.T) {
	got := dispatchFrom(t, ipApp(2), "10.0.0.2", "203.0.113.9")
	if got != "10.0.0.2" {
		t.Errorf("address = %s, want the connection's 10.0.0.2", got)
	}
}

// TestRealIPReadsTheShapesLoadBalancersWrite covers the entries RealIP
// accepts. Some load balancers append the address they saw with its port, and
// an IPv6 address with a port must be bracketed to be told apart. The port is
// dropped: ClientIP reports an address, not a socket.
func TestRealIPReadsTheShapesLoadBalancersWrite(t *testing.T) {
	for _, tc := range []struct{ entry, want string }{
		{"203.0.113.9", "203.0.113.9"},
		{"203.0.113.9:4711", "203.0.113.9"},
		{"2001:db8::1", "2001:db8::1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"[2001:db8::1]:4711", "2001:db8::1"},
		{" [2001:db8::1]:4711 ", "2001:db8::1"},
		{"203.0.113.9:65535", "203.0.113.9"},
		{"[::ffff:203.0.113.9]", "203.0.113.9"}, // IPv4-mapped, written as IPv6
	} {
		got := dispatchFrom(t, ipApp(1), "10.0.0.2", tc.entry)
		if got != tc.want {
			t.Errorf("%q: address = %s, want %s", tc.entry, got, tc.want)
		}
	}
}

// TestRealIPReadsAnUnbracketedIPv6AddressWhole pins that an entry with more
// than one colon and no brackets is never split at its last colon. The
// address below is a valid IPv6 address; reading "4711" as a port would
// attribute the request to 2001:db8::1, an address nobody wrote.
func TestRealIPReadsAnUnbracketedIPv6AddressWhole(t *testing.T) {
	got := dispatchFrom(t, ipApp(1), "10.0.0.2", "2001:db8::1:4711")
	if got != "2001:db8::1:4711" {
		t.Errorf("address = %s, want 2001:db8::1:4711 read whole", got)
	}
}

// TestRealIPFallsBackOnAnEntryThatIsNotAnIP pins every malformed shape: each
// falls back to the connection's address rather than to a guess.
func TestRealIPFallsBackOnAnEntryThatIsNotAnIP(t *testing.T) {
	for _, entry := range []string{
		"not-an-ip",
		"",
		"[203.0.113.9]",      // brackets are for IPv6 only
		"[203.0.113.9]:4711", // likewise
		"[2001:db8::1",       // unclosed
		"2001:db8::1]",       // unopened
		"[2001:db8::1]x",     // junk after the bracket
		"[2001:db8::1]:",     // empty port
		"[2001:db8::1]:0",    // port zero
		"[]",
		"[]:4711",
		"203.0.113.9:",
		"203.0.113.9:0",
		"203.0.113.9:65536",
		"203.0.113.9:99999999999999999999",
		"203.0.113.9:47a1",
		"203.0.113.9:+4711",
		"203.0.113.9: 4711",
		":4711",
		"example.com:4711",
	} {
		got := dispatchFrom(t, ipApp(1), "10.0.0.2", entry)
		if got != "10.0.0.2" {
			t.Errorf("%q: address = %s, want the connection's 10.0.0.2", entry, got)
		}
	}
}

func TestRealIPFallsBackWithNoHeader(t *testing.T) {
	got := dispatchFrom(t, ipApp(1), "10.0.0.2")
	if got != "10.0.0.2" {
		t.Errorf("address = %s, want the connection's 10.0.0.2", got)
	}
}

// TestRealIPReadsEveryHeaderLine covers a request carrying the header twice.
// fasthttp's Peek returns only the first line; the two lines are one list, and
// counting two hops from the right must cross from the second into the first.
func TestRealIPReadsEveryHeaderLine(t *testing.T) {
	got := dispatchFrom(t, ipApp(2), "10.0.0.2", "203.0.113.9", "192.0.2.44")
	if got != "203.0.113.9" {
		t.Errorf("address = %s, want 203.0.113.9", got)
	}
}

func TestRealIPPanicsOnFewerThanOneHop(t *testing.T) {
	for _, n := range []int{0, -1} {
		func() {
			defer func() {
				r := recover()
				s, ok := r.(string)
				if !ok || !strings.HasPrefix(s, "rice: ") {
					t.Errorf("RealIP(%d) panicked with %v, want a string starting %q", n, r, "rice: ")
				}
			}()
			middleware.RealIP(n)
		}()
	}
}

// serveApp starts app on an ephemeral port and returns its address.
func serveApp(t *testing.T, app *rice.App) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()
	// Not t.Context(): it is cancelled just before Cleanup functions run, which
	// would hand Shutdown a context that has already ended and force-close.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
	})
	deadline := time.Now().Add(2 * time.Second)
	for app.Addr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("app did not start serving")
		}
		time.Sleep(time.Millisecond)
	}
	return ln.Addr().String()
}

// TestRealIPDoesNotLeakAnAddressAcrossKeepAliveRequests is the reason RealIP
// sets the address on every request. fasthttp serves every request on a
// connection from one RequestCtx and clears a rewritten address only when the
// connection closes, so a middleware that skipped the rewrite would attribute
// the second request below to the first request's client. Behind a reverse
// proxy, those are different clients sharing one upstream connection.
func TestRealIPDoesNotLeakAnAddressAcrossKeepAliveRequests(t *testing.T) {
	addr := serveApp(t, ipApp(1))

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)

	get := func(extra string) string {
		t.Helper()
		fmt.Fprintf(conn, "GET /ip HTTP/1.1\r\nHost: rice\r\n%s\r\n", extra)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	if got := get("X-Forwarded-For: 203.0.113.9\r\n"); got != "203.0.113.9" {
		t.Fatalf("first request: address = %s, want 203.0.113.9", got)
	}
	if got := get(""); got != "127.0.0.1" {
		t.Errorf("second request on the same connection: address = %s, want 127.0.0.1 — the first request's address leaked", got)
	}
}
