package rice

import (
	"strings"
	"testing"

	"github.com/vietpham102301/rice-http/internal/router"
)

// registeredVerbs pairs each helper with the verb it must register under, so
// that a copy-paste error in one helper is caught rather than assumed absent.
func registeredVerbs() []struct {
	verb string
	call func(a *App, path string, h Handler, mw ...Middleware)
} {
	return []struct {
		verb string
		call func(a *App, path string, h Handler, mw ...Middleware)
	}{
		{"GET", (*App).GET},
		{"POST", (*App).POST},
		{"PUT", (*App).PUT},
		{"PATCH", (*App).PATCH},
		{"DELETE", (*App).DELETE},
		{"HEAD", (*App).HEAD},
		{"OPTIONS", (*App).OPTIONS},
	}
}

func TestEachVerbHelperRegistersUnderItsOwnVerb(t *testing.T) {
	for _, c := range registeredVerbs() {
		app := New()
		c.call(app, "/x", func(ctx *Ctx) error { return nil })

		var p router.Params
		if _, ok := app.lookup([]byte(c.verb), []byte("/x"), &p); !ok {
			t.Errorf("%s helper did not register a route reachable by %s", c.verb, c.verb)
		}

		for _, other := range registeredVerbs() {
			if other.verb == c.verb {
				continue
			}
			if _, ok := app.lookup([]byte(other.verb), []byte("/x"), &p); ok {
				t.Errorf("%s helper also registered the route under %s", c.verb, other.verb)
			}
		}
	}
}

func TestLookupMissesAnUnregisteredPath(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })

	var p router.Params
	if _, ok := app.lookup([]byte("GET"), []byte("/absent"), &p); ok {
		t.Error("lookup found a path that was never registered")
	}
}

func TestHandleSupportsAnUncommonVerb(t *testing.T) {
	app := New()
	app.Handle("PROPFIND", "/dav", func(c *Ctx) error { return nil })

	var p router.Params
	if _, ok := app.lookup([]byte("PROPFIND"), []byte("/dav"), &p); !ok {
		t.Error("an uncommon verb registered with Handle was not reachable")
	}
	if _, ok := app.lookup([]byte("GET"), []byte("/dav"), &p); ok {
		t.Error("an uncommon verb's route leaked into a common verb's tree")
	}
}

func TestLookupOnAnUncommonVerbWithNoneRegistered(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error { return nil })

	// rare is still nil here; lookup must not panic on it.
	var p router.Params
	if _, ok := app.lookup([]byte("PROPFIND"), []byte("/x"), &p); ok {
		t.Error("lookup found a route for a verb that was never registered")
	}
}

func TestLookupOnAnUncommonVerbNotInTheRareMap(t *testing.T) {
	app := New()
	app.Handle("PROPFIND", "/dav", func(c *Ctx) error { return nil })

	// rare now exists and contains PROPFIND. lookup for a different uncommon
	// verb (TRACE) takes the map-probe branch, not the nil short-circuit.
	var p router.Params
	if _, ok := app.lookup([]byte("TRACE"), []byte("/dav"), &p); ok {
		t.Error("lookup found a route for an uncommon verb that was not registered")
	}
}

// mustPanic runs fn and returns the panic value, failing the test if fn returns
// normally.
func mustPanic(t *testing.T, name string, fn func()) any {
	t.Helper()

	var got any
	func() {
		defer func() { got = recover() }()
		fn()
	}()

	if got == nil {
		t.Fatalf("%s did not panic, want a panic", name)
	}
	return got
}

func TestRegistrationPanicsOnAnEmptyPath(t *testing.T) {
	app := New()
	mustPanic(t, `GET("")`, func() {
		app.GET("", func(c *Ctx) error { return nil })
	})
}

func TestRegistrationPanicsOnAnEmptyMethod(t *testing.T) {
	app := New()
	v := mustPanic(t, `Handle("", "/x")`, func() {
		app.Handle("", "/x", func(c *Ctx) error { return nil })
	})

	if msg, _ := v.(string); !strings.Contains(msg, "/x") {
		t.Errorf("panic message %q does not name the offending path", v)
	}
}

func TestRegistrationPanicsOnALowercaseMethod(t *testing.T) {
	app := New()
	v := mustPanic(t, `Handle("get", "/y")`, func() {
		app.Handle("get", "/y", func(c *Ctx) error { return nil })
	})

	msg, _ := v.(string)
	if !strings.Contains(msg, "get") || !strings.Contains(msg, "/y") {
		t.Errorf("panic message %q should name both the offending verb and the path", v)
	}
}

func TestRegistrationPanicsOnAPathWithoutALeadingSlash(t *testing.T) {
	app := New()
	v := mustPanic(t, `GET("users")`, func() {
		app.GET("users", func(c *Ctx) error { return nil })
	})

	if msg, _ := v.(string); !strings.Contains(msg, "users") {
		t.Errorf("panic message %q does not name the offending path", v)
	}
}

func TestRegistrationPanicsOnANilHandler(t *testing.T) {
	app := New()
	mustPanic(t, "GET with nil handler", func() {
		app.GET("/x", nil)
	})
}

func TestRegistrationPanicsOnADuplicateRoute(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })

	v := mustPanic(t, "duplicate GET /users", func() {
		app.GET("/users", func(c *Ctx) error { return nil })
	})

	msg, _ := v.(string)
	if !strings.Contains(msg, "/users") || !strings.Contains(msg, "GET") {
		t.Errorf("panic message %q should name both the verb and the path", v)
	}
}

// TestTheSamePathUnderDifferentVerbsIsNotADuplicate is the case the duplicate
// check must not over-reach on: REST APIs register GET and POST on one path
// constantly.
func TestTheSamePathUnderDifferentVerbsIsNotADuplicate(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })
	app.POST("/users", func(c *Ctx) error { return nil })

	var p router.Params
	if _, ok := app.lookup([]byte("GET"), []byte("/users"), &p); !ok {
		t.Error("GET /users disappeared after POST /users was registered")
	}
	if _, ok := app.lookup([]byte("POST"), []byte("/users"), &p); !ok {
		t.Error("POST /users was not registered")
	}
}

func TestLookupReturnsTheHandlerThatWasRegistered(t *testing.T) {
	app := New()

	marker := "not called"
	app.GET("/x", func(c *Ctx) error {
		marker = "called"
		return nil
	})

	var p router.Params
	h, ok := app.lookup([]byte("GET"), []byte("/x"), &p)
	if !ok {
		t.Fatal("lookup did not find the route")
	}
	if err := h(nil); err != nil {
		t.Fatalf("handler returned %v, want nil", err)
	}
	if marker != "called" {
		t.Error("lookup returned a different handler than the one registered")
	}
}

func TestAppRoutesAParameterisedPattern(t *testing.T) {
	app := New()
	app.GET("/users/:id", func(c *Ctx) error { return nil })

	var p router.Params
	if _, ok := app.lookup([]byte("GET"), []byte("/users/42"), &p); !ok {
		t.Fatal("lookup did not match a parameterised route")
	}
	if got := string(p.Get("id")); got != "42" {
		t.Errorf("captured id = %q, want %q", got, "42")
	}
}

func TestAppRoutesAWildcardPattern(t *testing.T) {
	app := New()
	app.GET("/files/*path", func(c *Ctx) error { return nil })

	var p router.Params
	if _, ok := app.lookup([]byte("GET"), []byte("/files/a/b.txt"), &p); !ok {
		t.Fatal("lookup did not match a wildcard route")
	}
	if got := string(p.Get("path")); got != "a/b.txt" {
		t.Errorf("captured path = %q, want %q", got, "a/b.txt")
	}
}

// TestRegistrationPanicsOnANilMiddleware is Item 3's guard for App.register (the
// single path all sixteen verb helpers funnel through): a nil middleware in the
// route's own mw slice must panic here, named rice: and by index, rather than
// surfacing as a bare nil dereference inside internal/chain at Build.
func TestRegistrationPanicsOnANilMiddleware(t *testing.T) {
	app := New()

	v := mustPanic(t, "GET with a nil middleware", func() {
		app.GET("/x", func(c *Ctx) error { return nil }, nil)
		app.Build()
	})

	msg, _ := v.(string)
	if !strings.HasPrefix(msg, "rice:") {
		t.Errorf("panic %v is not rice:-prefixed", v)
	}
	if !strings.Contains(msg, "0") {
		t.Errorf("panic %q does not name the offending index", msg)
	}
}

func TestRegistrationPanicsOnAnUnmatchablePattern(t *testing.T) {
	cases := []string{"/a//b", "/a/./b", "/caf%C3%A9", "/users/:", "/files/*p/edit", "/a/:id/b/:id"}

	for _, pattern := range cases {
		app := New()
		v := mustPanic(t, "GET("+pattern+")", func() {
			app.GET(pattern, func(c *Ctx) error { return nil })
		})
		if msg, _ := v.(string); !strings.Contains(msg, pattern) {
			t.Errorf("panic for %q does not name the pattern: %v", pattern, v)
		}
	}
}
