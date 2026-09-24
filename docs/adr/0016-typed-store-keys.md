# ADR-0016 — Typed store keys replace Set and Get

Status: Accepted
Date: 2026-09-24

## Context

Since M6 the per-request store has been `c.Set(key string, v any)` and `c.Get(key string) (any, bool)`
over a slice of key/value pairs pre-sized to four ([ADR-0005](0005-context-pooling-and-borrow-contract.md)).
That API leaves two failures to run time.

- **Collision.** Two middleware that choose the same string share one slot, and neither is told.
  `middleware.RequestID` avoided it only by convention: an unexported key namespaced
  `"rice/middleware.request-id"`, which protects nothing from a caller that happens to type the
  same string.
- **Wrong type.** `Get` returns `any`. A mistaken assertion is found when it panics, or not at all
  when the `, ok` form quietly returns false and the handler carries on without its value.

The M6 design deferred typed keys because "the documented API was already `string`/`any`". That
reason concerned a document, not a user; the owner chose to replace the old API rather than add
beside it, since rice is before 1.0 and one middleware in the repository called it.

Go fixes one part of the shape. A method cannot declare type parameters of its own, so
`c.Get[T](k)` is not expressible: the typed operation has to live on the key or in a package-level
function.

## Decision

The store is reached only through a typed key.

```go
type Key[T any] struct{ /* unexported */ }

func NewKey[T any](name string) Key[T]

func (k Key[T]) Set(c *Ctx, v T)
func (k Key[T]) Get(c *Ctx) (T, bool)
func (k Key[T]) String() string
```

```go
var userKey = rice.NewKey[*User]("user") // once, at package level

userKey.Set(c, u)
u, ok := userKey.Get(c) // u is a *User
```

- **The key fixes the type.** `userKey.Set(c, "a string")` does not compile, and `Get` returns `T`.
  `Key` carries a zero-size field `_ [0]*T` ahead of its identity, so `Key[int]` and
  `Key[string]` have different underlying types and `Key[int](aStringKey)` does not compile either.
  Without it every instantiation shares one underlying type, the conversion compiles, and `Get`
  panics on the wrong type at run time — the failure this decision removes. The field adds no
  size and keeps `Key` comparable, so a key can be compared with `==` or used as a map key.
- **Identity is a pointer `NewKey` allocates; the name is for people.** Two `NewKey` calls give two
  keys that never share a slot, even with the same name and type. A copy of a `Key` is the same
  key. `String` returns the name.
- **What panics, with the prefix `rice: `:** `NewKey("")`, and `Set` or `Get` on a zero `Key[T]{}`.
  Every zero key has a nil id; allowing one would give all of them a single shared slot, the
  collision keys exist to remove.
- **Semantics carried over:** `Get` with nothing stored returns the zero `T` and `false`; a second
  `Set` replaces the first; a nil value is a value. `Get` returns a stored nil of an interface `T`
  as `(nil, true)` rather than asserting a nil `any`, which would panic.
- **Borrowed, as before.** `Set` and `Get` call `c.poison.check()` first, so under
  `-tags ricedebug` a released `Ctx` panics with the use-after-release panic even when the key is
  zero.
- **The store keeps its shape.** Entries become `{key *keyID, val any}`; the slice, its capacity of
  four, the linear scan and the zeroing on release are unchanged. `Ctx.Set` and `Ctx.Get` are
  removed.
- **`middleware.RequestID`** stores its id under `rice.NewKey[string]("rice/middleware.request-id")`.
  Its slot can no longer be reached by anyone who guesses the name; `RequestIDFrom` is the only
  reader.

## Alternatives

**Package-level generic functions, `rice.Set(c, k, v)` and `rice.Get(c, k)`.** The same mechanism —
a typed key and a pointer identity — with the operation spelled differently. Rejected because
`rice.Get` and `rice.Set` are vague names at package level, where they read as if they set or get
something about rice itself, and because the call reads unlike every other per-request accessor,
which hangs off its subject: `c.Param`, `c.Header`, and now `userKey.Get(c)`.

**User-declared key types, in the style of `context.WithValue`.** `type userKey struct{}` and a
store keyed by `any`. It prevents collisions, since two packages cannot declare the same type. It
does not fix the value's type: the key says nothing about what is stored under it, so `Get` still
returns `any` or makes the caller name a type argument that the compiler cannot check against
`Set`'s. It solves one of the two failures.

**Keeping `Set` and `Get` beside keys.** Nothing already written stops compiling. Rejected because
it leaves two ways to do one thing, and the string path keeps both failures for anyone who reaches
for it first — the same reasoning that rejected a separate `app.Pre` in
[ADR-0012](0012-application-middleware-runs-on-route-misses.md).

## Consequences

**Makes easy.** A value passed from middleware to handler is typed end to end, and two packages
cannot overwrite each other's values by choosing the same name. A handler reads `u, ok :=
userKey.Get(c)` with no assertion.

**Breaking.** Code calling `c.Set` or `c.Get` no longer compiles, and the compiler names each line.
The migration is mechanical: one package-level `NewKey` per string key, then `k.Set(c, v)` and
`k.Get(c)` without the assertion.

**Does not change the cost.** Storing a non-pointer value still boxes it into the store's `any` at
the call site: `Key.Set` with a non-constant string costs one allocation, exactly as `c.Set` did,
and a pointer costs none. Typed keys buy safety, not speed; removing the boxing would need a
different store. `NewKey` allocates its identity once per key and is not budgeted, like
registration. A key created per request pays that each time and can never be read back by the
next call, which is why keys are package-level variables.

**A test had to be added, not extended.** `TestEveryCtxMethodPanicsAfterRelease` walks the method
set of `*Ctx` by reflection so that a new accessor without a poison check fails without anyone
extending a list. `Key`'s methods are methods on `Key`, not on `*Ctx`, and that walk cannot see
them; `TestKeyMethodsPanicAfterRelease` covers them, including a zero key, to show the
use-after-release panic comes first.

**Forecloses little.** A `Delete`, a `MustGet`, or a store that avoids boxing can be added later
without changing what is here.
