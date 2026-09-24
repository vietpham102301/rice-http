package rice

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestKeyGetWithNothingStored(t *testing.T) {
	c := &Ctx{}
	k := NewKey[string]("missing")

	v, ok := k.Get(c)
	if v != "" || ok {
		t.Errorf("Get = %q, %v; want \"\", false", v, ok)
	}
}

func TestKeySetThenGet(t *testing.T) {
	c := &Ctx{}
	k := NewKey[string]("user")
	k.Set(c, "alice")

	v, ok := k.Get(c) // v is a string: no assertion at the call site
	if !ok || v != "alice" {
		t.Errorf("Get = %q, %v; want alice, true", v, ok)
	}
}

// TestTwoKeysWithTheSameNameNeverCollide is the reason keys exist. Two
// middleware that both choose "user" get two slots, whatever the types.
func TestTwoKeysWithTheSameNameNeverCollide(t *testing.T) {
	c := &Ctx{}
	a := NewKey[string]("user")
	b := NewKey[string]("user")
	n := NewKey[int]("user")
	a.Set(c, "alice")
	b.Set(c, "bob")
	n.Set(c, 7)

	if v, _ := a.Get(c); v != "alice" {
		t.Errorf("first key = %q, want alice", v)
	}
	if v, _ := b.Get(c); v != "bob" {
		t.Errorf("second key with the same name = %q, want bob", v)
	}
	if v, _ := n.Get(c); v != 7 {
		t.Errorf("key with the same name and another type = %d, want 7", v)
	}
}

func TestACopiedKeyIsTheSameKey(t *testing.T) {
	c := &Ctx{}
	k := NewKey[int]("n")
	copied := k
	k.Set(c, 7)

	if v, ok := copied.Get(c); !ok || v != 7 {
		t.Errorf("copy.Get = %d, %v; want 7, true", v, ok)
	}
}

func TestKeySetReplacesAnEarlierValue(t *testing.T) {
	c := &Ctx{}
	k := NewKey[int]("k")
	k.Set(c, 1)
	k.Set(c, 2)

	if v, _ := k.Get(c); v != 2 {
		t.Errorf("Get = %d, want 2", v)
	}
	if n := len(c.store); n != 1 {
		t.Errorf("store holds %d entries after setting one key twice, want 1", n)
	}
}

func TestKeyStoresANilPointer(t *testing.T) {
	c := &Ctx{}
	k := NewKey[*int]("p")
	k.Set(c, nil)

	if v, ok := k.Get(c); v != nil || !ok {
		t.Errorf("Get = %v, %v; want nil, true", v, ok)
	}
}

// TestKeyStoresANilInterfaceValue is the case a bare v.(T) gets wrong: a nil
// error stored in an any is a nil any, and asserting a nil any to an interface
// type panics.
func TestKeyStoresANilInterfaceValue(t *testing.T) {
	c := &Ctx{}
	k := NewKey[error]("err")
	k.Set(c, nil)

	if v, ok := k.Get(c); v != nil || !ok {
		t.Errorf("Get = %v, %v; want nil, true", v, ok)
	}
}

// TestTheStoreHoldsMoreThanItsInitialCapacity crosses storeCapacity, the
// documented cliff, and checks nothing is lost on the way over it.
func TestTheStoreHoldsMoreThanItsInitialCapacity(t *testing.T) {
	c := &Ctx{}
	keys := make([]Key[int], storeCapacity+2)
	for i := range keys {
		keys[i] = NewKey[int]("k" + strconv.Itoa(i))
		keys[i].Set(c, i)
	}

	for i, k := range keys {
		if v, ok := k.Get(c); !ok || v != i {
			t.Errorf("key %d = %d, %v; want %d, true", i, v, ok, i)
		}
	}
}

// TestMiddlewareCanPassAValueToTheHandler is the use the store exists for.
func TestMiddlewareCanPassAValueToTheHandler(t *testing.T) {
	requestID := NewKey[string]("request_id")
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			requestID.Set(c, "abc")
			return next(c)
		}
	})
	app.GET("/x", func(c *Ctx) error {
		v, _ := requestID.Get(c)
		return c.String(200, v)
	})

	fctx := dispatchCtx(app, "GET", "/x")

	if got := string(fctx.Response.Body()); got != "abc" {
		t.Errorf("body = %q, want %q", got, "abc")
	}
}

func TestARequestSeesNoStoreEntriesFromThePreviousOne(t *testing.T) {
	k := NewKey[string]("k")
	var leaked bool
	app := New()
	app.GET("/set", func(c *Ctx) error { k.Set(c, "v"); return nil })
	app.GET("/get", func(c *Ctx) error { _, leaked = k.Get(c); return nil })
	app.Build()

	for _, uri := range []string{"/set", "/get"} {
		fctx := &fasthttp.RequestCtx{}
		fctx.Request.Header.SetMethod("GET")
		fctx.Request.SetRequestURI(uri)
		app.handle(fctx)
	}

	if leaked {
		t.Error("a store entry set by one request was visible to the next")
	}
}

func TestKeyStringIsItsName(t *testing.T) {
	if got := NewKey[int]("user").String(); got != "user" {
		t.Errorf("String() = %q, want user", got)
	}
	// A zero Key prints as empty rather than panicking, so fmt and log lines
	// that show a key never crash on one.
	var zero Key[int]
	if got := zero.String(); got != "" {
		t.Errorf("zero Key String() = %q, want empty", got)
	}
}

// wantKeyPanic runs fn and checks it panics with a rice: message naming NewKey.
func wantKeyPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		msg, _ := recover().(string)
		if !strings.HasPrefix(msg, "rice: ") || !strings.Contains(msg, "NewKey") {
			t.Errorf("panic = %q, want a rice: message naming NewKey", msg)
		}
	}()
	fn()
}

func TestNewKeyPanicsOnAnEmptyName(t *testing.T) {
	wantKeyPanic(t, func() { NewKey[int]("") })
}

// TestAZeroKeyPanics: every zero Key has a nil id, so allowing one would give
// all of them one shared slot — the collision keys exist to remove.
func TestAZeroKeyPanics(t *testing.T) {
	c := &Ctx{}
	var k Key[int]
	t.Run("Set", func(t *testing.T) { wantKeyPanic(t, func() { k.Set(c, 1) }) })
	t.Run("Get", func(t *testing.T) { wantKeyPanic(t, func() { k.Get(c) }) })
}

// TestKeysOfDifferentTypesDoNotConvert pins that the type a key fixes cannot
// be undone by a conversion. Without a field that mentions T, every Key has the
// same underlying type, Key[int](aStringKey) compiles, and Get panics at run
// time with the wrong-type failure keys exist to remove. reflect is confined
// to this test; ADR-0006 governs production code.
func TestKeysOfDifferentTypesDoNotConvert(t *testing.T) {
	from := reflect.TypeOf(Key[string]{})
	to := reflect.TypeOf(Key[int]{})
	if from.ConvertibleTo(to) {
		t.Error("Key[string] converts to Key[int]; the key no longer fixes its type")
	}
	if !from.Comparable() {
		t.Error("Key is not comparable; it can no longer be a map key or compared with ==")
	}
}
