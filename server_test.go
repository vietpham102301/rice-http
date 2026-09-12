package rice_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// waitForAddr polls until the app has bound a listener, or fails the test.
func waitForAddr(t *testing.T, app *rice.App) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if addr := app.Addr(); addr != "" {
			return addr
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("server did not bind a listener within 2s")
	return ""
}

// get issues a real HTTP request with the standard library client, which proves
// rice speaks HTTP to something that is not fasthttp.
func get(t *testing.T, addr, path string) (int, string) {
	t.Helper()
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, string(body)
}

func TestServeAnswersARealRequestOnAnEphemeralPort(t *testing.T) {
	app := rice.New()
	app.GET("/world", func(c *rice.Ctx) error {
		return c.String(200, "hello "+string(c.Path()))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	status, body := get(t, addr, "/world")
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if body != "hello /world" {
		t.Errorf("body = %q, want %q", body, "hello /world")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}

func TestRunBindsTheGivenAddress(t *testing.T) {
	app := rice.New()
	app.GET("/", func(c *rice.Ctx) error { return c.String(200, "up") })

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run("127.0.0.1:0") }()

	addr := waitForAddr(t, app)

	status, body := get(t, addr, "/")
	if status != 200 || body != "up" {
		t.Errorf("got status %d body %q, want 200 %q", status, body, "up")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
	if err := <-errCh; err != nil {
		t.Errorf("Run returned %v after shutdown, want nil", err)
	}
}

func TestRunReturnsAnErrorOnAnUnbindableAddress(t *testing.T) {
	app := rice.New()

	// Port 1 requires privileges this test does not have.
	if err := app.Run("127.0.0.1:1"); err == nil {
		t.Error("Run returned nil for an unbindable address, want an error")
	}
}

func TestShutdownStopsAcceptingNewConnections(t *testing.T) {
	app := rice.New()
	app.GET("/", func(c *rice.Ctx) error { return c.String(200, "up") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)
	if status, _ := get(t, addr, "/"); status != 200 {
		t.Fatalf("server was not up before shutdown: status %d", status)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned %v, want nil", err)
	}

	client := &http.Client{Timeout: time.Second}
	if _, err := client.Get("http://" + addr + "/"); err == nil {
		t.Error("a request succeeded after shutdown, want a connection error")
	}
}

func TestShutdownReturnsErrShutdownTimeoutWhenTheDeadlinePasses(t *testing.T) {
	release := make(chan struct{})
	inFlight := make(chan struct{})

	app := rice.New()
	app.GET("/slow", func(c *rice.Ctx) error {
		close(inFlight)
		<-release // hold the request open past the shutdown deadline
		return c.String(200, "finally")
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get("http://" + addr + "/slow")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	<-inFlight // the handler is now blocked

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = app.Shutdown(ctx)
	close(release) // let the handler finish so the goroutine does not leak

	if err != rice.ErrShutdownTimeout {
		t.Errorf("Shutdown returned %v, want ErrShutdownTimeout", err)
	}
}

func TestAddrIsEmptyBeforeServing(t *testing.T) {
	app := rice.New()
	if got := app.Addr(); got != "" {
		t.Errorf("Addr() = %q before serving, want empty string", got)
	}
}

func TestServeReturns404ForAnUnregisteredPath(t *testing.T) {
	app := rice.New()
	app.GET("/known", func(c *rice.Ctx) error { return c.String(200, "known") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	if status, body := get(t, addr, "/known"); status != 200 || body != "known" {
		t.Errorf("registered route: got %d %q, want 200 %q", status, body, "known")
	}

	status, body := get(t, addr, "/unknown")
	if status != 404 {
		t.Errorf("unregistered route: status = %d, want 404", status)
	}
	if body != "Not Found" {
		t.Errorf("unregistered route: body = %q, want %q", body, "Not Found")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}

func TestServeAnswersAParameterisedRoute(t *testing.T) {
	app := rice.New()
	app.GET("/users/:id", func(c *rice.Ctx) error {
		return c.String(200, "user "+c.ParamString("id"))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	status, body := get(t, addr, "/users/42")
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if body != "user 42" {
		t.Errorf("body = %q, want %q", body, "user 42")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}

func TestServeAnswersAGroupedMiddlewaredRoute(t *testing.T) {
	app := rice.New()

	app.Use(func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			c.SetHeader("X-App", "1")
			return next(c)
		}
	})

	g := app.Group("/api", func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			c.SetHeader("X-Group", "1")
			return next(c)
		}
	})
	g.GET("/users/:id", func(c *rice.Ctx) error {
		return c.String(200, "user "+c.ParamString("id"))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	resp, err := http.Get("http://" + addr + "/api/users/42")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := string(body); got != "user 42" {
		t.Errorf("body = %q, want %q", got, "user 42")
	}
	if got := resp.Header.Get("X-App"); got != "1" {
		t.Errorf("X-App = %q; application middleware did not run over a socket", got)
	}
	if got := resp.Header.Get("X-Group"); got != "1" {
		t.Errorf("X-Group = %q; group middleware did not run over a socket", got)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}

func TestServeAnswersAWildcardRoute(t *testing.T) {
	app := rice.New()
	app.GET("/files/*path", func(c *rice.Ctx) error {
		return c.String(200, "file "+c.ParamString("path"))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	status, body := get(t, addr, "/files/a/b/c.txt")
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if body != "file a/b/c.txt" {
		t.Errorf("body = %q, want %q", body, "file a/b/c.txt")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}

// TestServeMatchesAPercentEncodedRequestAgainstADecodedPattern is the request-side
// half of the rule Task 2 enforces at registration: fasthttp decodes the path
// before rice sees it, so the decoded pattern is the one that matches.
func TestServeMatchesAPercentEncodedRequestAgainstADecodedPattern(t *testing.T) {
	app := rice.New()
	app.GET("/café", func(c *rice.Ctx) error { return c.String(200, "decoded") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	status, body := get(t, addr, "/caf%C3%A9")
	if status != 200 || body != "decoded" {
		t.Errorf("got %d %q, want 200 %q", status, body, "decoded")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}
