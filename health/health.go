package health

import (
	"context"
	"fmt"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// defaultTimeout bounds a Check whose Timeout is 0.
const defaultTimeout = time.Second

// errNotReady is the one error every refused readiness probe without a
// failing check returns: the App is starting or shutting down.
var errNotReady = &rice.HTTPError{Code: 503, Message: "not ready"}

// Live answers 200 "ok". It checks nothing: a process that can answer is
// alive, and a liveness probe that checked a dependency would have Kubernetes
// restart healthy pods whenever that dependency is down. It keeps answering
// while the App drains.
func Live() rice.Handler {
	return func(c *rice.Ctx) error {
		return c.String(200, "ok")
	}
}

// Check is one dependency readiness waits on. Fn receives a context bounded by
// Timeout, 0 meaning one second, and derived from the request's.
type Check struct {
	Name    string
	Timeout time.Duration
	Fn      func(ctx context.Context) error
}

// Ready answers 200 "ok" when app.Ready() and every check passes, and 503 "not
// ready" otherwise.
//
// While app is starting or shutting down it answers 503 at once, without
// running any check. Otherwise the checks run in order; the first that fails
// or outlasts its Timeout ends the probe with a 503 whose error, logged by the
// error funnel and never sent, names the check and wraps its cause. The client
// sees only "not ready", so the route does not publish what the service
// depends on.
//
// A check on a dependency every instance shares turns that dependency's outage
// into every instance leaving the load balancer at once, taking routes that
// did not need it with them. Check what this instance needs to serve most of
// its traffic, or nothing: Ready with no checks follows the App's lifecycle
// alone, which is what a rolling deploy needs.
//
// A nil app, or a Check with an empty Name, a nil Fn or a negative Timeout,
// panics.
func Ready(app *rice.App, checks ...Check) rice.Handler {
	if app == nil {
		panic("rice: health.Ready: app is nil")
	}
	for _, ch := range checks {
		switch {
		case ch.Name == "":
			panic("rice: health.Ready: a Check has an empty Name")
		case ch.Fn == nil:
			panic("rice: health.Ready: Check " + ch.Name + " has a nil Fn")
		case ch.Timeout < 0:
			panic("rice: health.Ready: Check " + ch.Name + " has a negative Timeout")
		}
	}
	checks = append([]Check(nil), checks...)

	return func(c *rice.Ctx) error {
		if !app.Ready() {
			return errNotReady
		}
		for _, ch := range checks {
			if err := run(c.Context(), ch); err != nil {
				return &rice.HTTPError{Code: 503, Message: "not ready", Err: fmt.Errorf("health: check %q: %w", ch.Name, err)}
			}
		}
		return c.String(200, "ok")
	}
}

// run calls ch.Fn under its timeout. A check that returns nil after its
// deadline passed still fails: it answered too late.
func run(parent context.Context, ch Check) error {
	timeout := ch.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if err := ch.Fn(ctx); err != nil {
		return err
	}
	return ctx.Err()
}
