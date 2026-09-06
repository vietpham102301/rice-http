package rice

import (
	"strings"
	"testing"
)

// registeredVerbs pairs each helper with the verb it must register under, so
// that a copy-paste error in one helper is caught rather than assumed absent.
func registeredVerbs() []struct {
	verb string
	call func(a *App, path string, h Handler)
} {
	return []struct {
		verb string
		call func(a *App, path string, h Handler)
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

		if _, ok := app.lookup([]byte(c.verb), []byte("/x")); !ok {
			t.Errorf("%s helper did not register a route reachable by %s", c.verb, c.verb)
		}

		for _, other := range registeredVerbs() {
			if other.verb == c.verb {
				continue
			}
			if _, ok := app.lookup([]byte(other.verb), []byte("/x")); ok {
				t.Errorf("%s helper also registered the route under %s", c.verb, other.verb)
			}
		}
	}
}

func TestLookupMissesAnUnregisteredPath(t *testing.T) {
	app := New()
	app.GET("/users", func(c *Ctx) error { return nil })

	if _, ok := app.lookup([]byte("GET"), []byte("/absent")); ok {
		t.Error("lookup found a path that was never registered")
	}
}

func TestHandleSupportsAnUncommonVerb(t *testing.T) {
	app := New()
	app.Handle("PROPFIND", "/dav", func(c *Ctx) error { return nil })

	if _, ok := app.lookup([]byte("PROPFIND"), []byte("/dav")); !ok {
		t.Error("an uncommon verb registered with Handle was not reachable")
	}
	if _, ok := app.lookup([]byte("GET"), []byte("/dav")); ok {
		t.Error("an uncommon verb's route leaked into a common verb's tree")
	}
}

func TestLookupOnAnUncommonVerbWithNoneRegistered(t *testing.T) {
	app := New()
	app.GET("/x", func(c *Ctx) error { return nil })

	// rare is still nil here; lookup must not panic on it.
	if _, ok := app.lookup([]byte("PROPFIND"), []byte("/x")); ok {
		t.Error("lookup found a route for a verb that was never registered")
	}
}

func TestLookupOnAnUncommonVerbNotInTheRareMap(t *testing.T) {
	app := New()
	app.Handle("PROPFIND", "/dav", func(c *Ctx) error { return nil })

	// rare now exists and contains PROPFIND. lookup for a different uncommon
	// verb (TRACE) takes the map-probe branch, not the nil short-circuit.
	if _, ok := app.lookup([]byte("TRACE"), []byte("/dav")); ok {
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

	if _, ok := app.lookup([]byte("GET"), []byte("/users")); !ok {
		t.Error("GET /users disappeared after POST /users was registered")
	}
	if _, ok := app.lookup([]byte("POST"), []byte("/users")); !ok {
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

	h, ok := app.lookup([]byte("GET"), []byte("/x"))
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
