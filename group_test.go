package rice

import (
	"strings"
	"testing"

	"github.com/vietpham102301/rice-http/internal/router"
)

func TestGroupPrefixIsJoinedToTheRoutePath(t *testing.T) {
	var log []string

	app := New()
	g := app.Group("/api")
	g.GET("/users", traceHandler(&log))

	if got := dispatch(app, "/api/users"); got != 200 {
		t.Errorf("GET /api/users = %d, want 200", got)
	}
	if got := dispatch(app, "/users"); got != 404 {
		t.Errorf("GET /users = %d, want 404; the group prefix must be required", got)
	}
}

func TestNestedGroupPrefixesConcatenate(t *testing.T) {
	var log []string

	app := New()
	v1 := app.Group("/api").Group("/v1")
	v1.GET("/users", traceHandler(&log))

	if got := dispatch(app, "/api/v1/users"); got != 200 {
		t.Errorf("GET /api/v1/users = %d, want 200", got)
	}
}

// TestEmptyGroupPrefixIsAllowed covers the middleware-only group, which is a
// common shape: a set of routes that share middleware but not a path.
func TestEmptyGroupPrefixIsAllowed(t *testing.T) {
	var log []string

	app := New()
	g := app.Group("", traceMW(&log, "guard"))
	g.GET("/users", traceHandler(&log))

	if got := dispatch(app, "/users"); got != 200 {
		t.Fatalf("GET /users = %d, want 200", got)
	}
	if got := strings.Join(log, " "); got != "guard-in handler guard-out" {
		t.Errorf("trace = %q, want %q", got, "guard-in handler guard-out")
	}
}

func TestGroupRejectsAPrefixThatWouldJoinBadly(t *testing.T) {
	cases := []struct {
		prefix string
		phrase string
	}{
		{"/api/", "must not end with"},
		{"api", "must begin with"},
		{"api/", "must begin with"},
	}

	for _, c := range cases {
		app := New()
		v := mustPanic(t, "Group("+c.prefix+")", func() {
			app.Group(c.prefix)
		})

		msg, _ := v.(string)
		if !strings.Contains(msg, c.phrase) {
			t.Errorf("Group(%q) panicked with %q, which does not mention %q", c.prefix, msg, c.phrase)
		}
		if !strings.Contains(msg, c.prefix) {
			t.Errorf("Group(%q) panicked with %q, which does not name the offending prefix", c.prefix, msg)
		}
	}
}

func TestNestedGroupRejectsABadPrefixToo(t *testing.T) {
	app := New()
	g := app.Group("/api")

	mustPanic(t, `g.Group("/v1/")`, func() { g.Group("/v1/") })
	mustPanic(t, `g.Group("v1")`, func() { g.Group("v1") })
}

// TestGroupRootRegistersWithATrailingSlash records the consequence of ADR-0007
// declining trailing-slash equivalence: inside a group, "/" is the prefix plus a
// slash, which is a different route from the bare prefix.
func TestGroupRootRegistersWithATrailingSlash(t *testing.T) {
	var log []string

	app := New()
	g := app.Group("/api")
	g.GET("/", traceHandler(&log))

	if got := dispatch(app, "/api/"); got != 200 {
		t.Errorf("GET /api/ = %d, want 200", got)
	}
	if got := dispatch(app, "/api"); got != 404 {
		t.Errorf("GET /api = %d, want 404; ADR-0007 declined trailing-slash equivalence", got)
	}
}

func TestGroupMiddlewareRunsInsideApplicationMiddleware(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "app"))
	g := app.Group("/api", traceMW(&log, "group"))
	g.GET("/x", traceHandler(&log), traceMW(&log, "route"))

	dispatch(app, "/api/x")

	want := "app-in group-in route-in handler route-out group-out app-out"
	if got := strings.Join(log, " "); got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}

// TestThreeLevelOrder is the roadmap's exit criterion for nested groups, entry
// and unwind both.
func TestThreeLevelOrder(t *testing.T) {
	var log []string

	app := New()
	app.Use(traceMW(&log, "app"))
	outer := app.Group("/a", traceMW(&log, "outer"))
	inner := outer.Group("/b", traceMW(&log, "inner"))
	inner.GET("/c", traceHandler(&log), traceMW(&log, "route"))

	if got := dispatch(app, "/a/b/c"); got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}

	want := "app-in outer-in inner-in route-in handler route-out inner-out outer-out app-out"
	if got := strings.Join(log, " "); got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}

// TestParentUseAfterChildCreationStillApplies is the reason a Group holds a
// parent pointer instead of copying its parent's middleware. If it snapshotted,
// this middleware would silently not wrap the child's routes — the same bug
// ADR-0003 exists to prevent, one level down, and the middleware people add late
// is usually authentication.
func TestParentUseAfterChildCreationStillApplies(t *testing.T) {
	var log []string

	app := New()
	parent := app.Group("/api")
	child := parent.Group("/v1")
	child.GET("/x", traceHandler(&log))

	parent.Use(traceMW(&log, "late")) // after the child exists and has routes

	dispatch(app, "/api/v1/x")

	if got := strings.Join(log, " "); got != "late-in handler late-out" {
		t.Errorf("trace = %q; middleware added to a parent after the child was created must still wrap the child's routes", got)
	}
}

// TestSiblingGroupsDoNotShareMiddleware is the slice-aliasing guard. Two children
// of one parent must not see each other's middleware, which a shared backing
// array would cause.
func TestSiblingGroupsDoNotShareMiddleware(t *testing.T) {
	var log []string

	app := New()
	parent := app.Group("/api")

	left := parent.Group("/left", traceMW(&log, "left"))
	right := parent.Group("/right", traceMW(&log, "right"))

	left.GET("/x", traceHandler(&log))
	right.GET("/x", traceHandler(&log))

	dispatch(app, "/api/left/x")
	if got := strings.Join(log, " "); got != "left-in handler left-out" {
		t.Errorf("left route trace = %q, want only left's middleware", got)
	}

	log = nil
	dispatch(app, "/api/right/x")
	if got := strings.Join(log, " "); got != "right-in handler right-out" {
		t.Errorf("right route trace = %q, want only right's middleware", got)
	}
}

// TestGroupDoesNotAliasTheCallersSlice is the same hazard as the App-level one:
// keeping the variadic rather than copying lets a caller's later mutation reach
// in.
//
// The mutation has to be an in-place overwrite of an element already in range,
// not a further append: an append past the alias's own length writes into
// spare capacity the alias's own reads never look beyond, so it would pass
// whether or not the slice was actually copied — proving nothing. See
// TestRouteMiddlewareDoesNotAliasTheCallersSlice in build_test.go for the same
// guard against the same non-guard.
func TestGroupDoesNotAliasTheCallersSlice(t *testing.T) {
	var log []string

	callerSlice := make([]Middleware, 1)
	callerSlice[0] = traceMW(&log, "A")

	app := New()
	g := app.Group("/api", callerSlice...)
	g.GET("/x", traceHandler(&log))

	// The caller keeps using their slice after registering, overwriting the
	// element it already passed in.
	callerSlice[0] = traceMW(&log, "INTRUDER")

	dispatch(app, "/api/x")

	if got := strings.Join(log, " "); got != "A-in handler A-out" {
		t.Errorf("trace = %q; the caller's later mutation must not reach the group", got)
	}
}

func TestGroupRegistrationAfterBuildPanics(t *testing.T) {
	app := New()
	g := app.Group("/api")
	g.GET("/x", func(c *Ctx) error { return nil })
	app.Build()

	v := mustPanic(t, "group GET after Build", func() {
		g.GET("/y", func(c *Ctx) error { return nil })
	})

	if msg, _ := v.(string); !strings.Contains(msg, "after Build") {
		t.Errorf("panic %q should say the registration came after Build", v)
	}
}

func TestGroupUseAfterBuildPanics(t *testing.T) {
	app := New()
	g := app.Group("/api")
	g.GET("/x", func(c *Ctx) error { return nil })
	app.Build()

	mustPanic(t, "group Use after Build", func() {
		g.Use(func(next Handler) Handler { return next })
	})
}

func TestEveryGroupVerbHelperRegistersUnderItsOwnVerb(t *testing.T) {
	verbs := []struct {
		name string
		call func(g *Group, path string, h Handler)
	}{
		{"GET", func(g *Group, p string, h Handler) { g.GET(p, h) }},
		{"POST", func(g *Group, p string, h Handler) { g.POST(p, h) }},
		{"PUT", func(g *Group, p string, h Handler) { g.PUT(p, h) }},
		{"PATCH", func(g *Group, p string, h Handler) { g.PATCH(p, h) }},
		{"DELETE", func(g *Group, p string, h Handler) { g.DELETE(p, h) }},
		{"HEAD", func(g *Group, p string, h Handler) { g.HEAD(p, h) }},
		{"OPTIONS", func(g *Group, p string, h Handler) { g.OPTIONS(p, h) }},
	}

	for _, v := range verbs {
		app := New()
		g := app.Group("/api")
		v.call(g, "/x", func(c *Ctx) error { return nil })

		var p router.Params
		if _, ok := app.lookup([]byte(v.name), []byte("/api/x"), &p); !ok {
			t.Errorf("%s helper did not register a route reachable by %s at /api/x", v.name, v.name)
		}
		for _, other := range verbs {
			if other.name == v.name {
				continue
			}
			if _, ok := app.lookup([]byte(other.name), []byte("/api/x"), &p); ok {
				t.Errorf("%s helper also registered the route under %s", v.name, other.name)
			}
		}
	}
}
