package health_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/health"
)

// servingApp returns an App that is serving on an ephemeral port, so its
// Ready is true, and shuts it down when the test ends.
func servingApp(t *testing.T, opts ...rice.Option) *rice.App {
	t.Helper()
	app := rice.New(opts...)
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
	})
	deadline := time.Now().Add(2 * time.Second)
	for !app.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("app never became ready")
		}
		time.Sleep(time.Millisecond)
	}
	return app
}

// probe dispatches GET /p to h on a probe App of its own, with opts, and
// returns the response.
func probe(t *testing.T, h rice.Handler, opts ...rice.Option) *fasthttp.RequestCtx {
	t.Helper()
	p := rice.New(opts...)
	p.GET("/p", h)
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/p")
	p.FasthttpHandler()(fctx)
	return fctx
}

func status(fctx *fasthttp.RequestCtx) (int, string) {
	return fctx.Response.StatusCode(), string(fctx.Response.Body())
}

func TestLiveAlwaysAnswersOK(t *testing.T) {
	if code, body := status(probe(t, health.Live())); code != 200 || body != "ok" {
		t.Errorf("Live = %d %q, want 200 \"ok\"", code, body)
	}
}

func TestLiveAnswersOKWhileDraining(t *testing.T) {
	app := rice.New(rice.WithDrainDelay(time.Second))
	app.GET("/livez", health.Live())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Serve(ln) }()
	for !app.Ready() {
		time.Sleep(time.Millisecond)
	}
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(context.Background()) }()
	for app.Ready() {
		time.Sleep(time.Millisecond)
	}
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/livez")
	app.FasthttpHandler()(fctx)
	if code, _ := status(fctx); code != 200 {
		t.Errorf("Live while draining = %d, want 200: a draining process is alive", code)
	}
	<-done
}

func TestReadyIsNotReadyBeforeServingWithoutRunningChecks(t *testing.T) {
	ran := false
	app := rice.New()
	check := health.Check{Name: "db", Fn: func(context.Context) error { ran = true; return nil }}
	if code, body := status(probe(t, health.Ready(app, check))); code != 503 || body != "not ready" {
		t.Errorf("Ready before serving = %d %q, want 503 \"not ready\"", code, body)
	}
	if ran {
		t.Error("a check ran while the App was not ready")
	}
}

func TestReadyWhileDraining(t *testing.T) {
	app := servingApp(t, rice.WithDrainDelay(time.Second))
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(context.Background()) }()
	for app.Ready() {
		time.Sleep(time.Millisecond)
	}
	if code, _ := status(probe(t, health.Ready(app))); code != 503 {
		t.Errorf("Ready while draining = %d, want 503", code)
	}
	<-done
}

func TestReadyWithPassingChecks(t *testing.T) {
	app := servingApp(t)
	var order []string
	ok := func(name string) health.Check {
		return health.Check{Name: name, Fn: func(context.Context) error { order = append(order, name); return nil }}
	}
	if code, body := status(probe(t, health.Ready(app, ok("db"), ok("cache")))); code != 200 || body != "ok" {
		t.Errorf("Ready = %d %q, want 200 \"ok\"", code, body)
	}
	if strings.Join(order, ",") != "db,cache" {
		t.Errorf("checks ran %v, want db then cache", order)
	}
}

func TestReadyFailingCheck(t *testing.T) {
	app := servingApp(t)
	down := errors.New("connection refused")
	var seen error
	laterRan := false
	h := health.Ready(app,
		health.Check{Name: "db", Fn: func(context.Context) error { return down }},
		health.Check{Name: "cache", Fn: func(context.Context) error { laterRan = true; return nil }},
	)
	fctx := probe(t, h, rice.WithErrorHandler(func(c *rice.Ctx, err error) {
		seen = err
		rice.DefaultErrorHandler(c, err)
	}))
	if code, body := status(fctx); code != 503 || body != "not ready" {
		t.Errorf("Ready = %d %q, want 503 \"not ready\"", code, body)
	}
	if !errors.Is(seen, down) || !strings.Contains(seen.Error(), `"db"`) {
		t.Errorf("the funnel saw %v, want the cause wrapped with the check's name", seen)
	}
	if laterRan {
		t.Error("a check after the failing one ran")
	}
}

func TestReadyCheckTimeout(t *testing.T) {
	app := servingApp(t)
	var seen error
	var deadline time.Duration
	slow := health.Check{Name: "slow", Timeout: 50 * time.Millisecond, Fn: func(ctx context.Context) error {
		d, _ := ctx.Deadline()
		deadline = time.Until(d)
		<-ctx.Done()
		return ctx.Err()
	}}
	fctx := probe(t, health.Ready(app, slow), rice.WithErrorHandler(func(c *rice.Ctx, err error) {
		seen = err
		rice.DefaultErrorHandler(c, err)
	}))
	if code, _ := status(fctx); code != 503 {
		t.Errorf("a check past its Timeout gave %d, want 503", code)
	}
	if !errors.Is(seen, context.DeadlineExceeded) {
		t.Errorf("the funnel saw %v, want context.DeadlineExceeded", seen)
	}
	if deadline > 50*time.Millisecond {
		t.Errorf("the check's context had %v left, want at most its 50ms Timeout", deadline)
	}
}

func TestReadyCheckThatAnswersLateFails(t *testing.T) {
	app := servingApp(t)
	late := health.Check{Name: "late", Timeout: 20 * time.Millisecond, Fn: func(context.Context) error {
		time.Sleep(60 * time.Millisecond) // ignores its context, then reports success
		return nil
	}}
	if code, _ := status(probe(t, health.Ready(app, late))); code != 503 {
		t.Errorf("a check that answered after its Timeout gave %d, want 503", code)
	}
}

func TestReadyDefaultTimeoutIsOneSecond(t *testing.T) {
	app := servingApp(t)
	var left time.Duration
	c := health.Check{Name: "db", Fn: func(ctx context.Context) error {
		d, ok := ctx.Deadline()
		if !ok {
			t.Error("a check with Timeout 0 got no deadline")
		}
		left = time.Until(d)
		return nil
	}}
	probe(t, health.Ready(app, c))
	if left <= 900*time.Millisecond || left > time.Second {
		t.Errorf("a check with Timeout 0 had %v left, want about one second", left)
	}
}

func TestReadyPanicsOnACheckThatCannotWork(t *testing.T) {
	app := rice.New()
	fn := func(context.Context) error { return nil }
	cases := map[string]health.Check{
		"empty Name":       {Fn: fn},
		"nil Fn":           {Name: "db"},
		"negative Timeout": {Name: "db", Fn: fn, Timeout: -1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if msg, _ := recover().(string); !strings.HasPrefix(msg, "rice: health.Ready:") {
					t.Errorf("panic %q", msg)
				}
			}()
			health.Ready(app, c)
		})
	}
}

func TestReadyPanicsOnANilApp(t *testing.T) {
	defer func() {
		if msg, _ := recover().(string); !strings.HasPrefix(msg, "rice: health.Ready:") {
			t.Errorf("panic %q", msg)
		}
	}()
	health.Ready(nil)
}
