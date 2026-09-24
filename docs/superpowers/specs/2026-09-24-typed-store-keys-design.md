# Typed store keys — Design

**Status:** approved, not yet implemented
**Date:** 2026-09-24
**Milestone:** none. Work after the roadmap — see [04-roadmap.md](../../04-roadmap.md), *Explicitly deferred*
**Decision record:** ADR-0016 (new), "Typed store keys replace Set and Get"
**Depends on:** the per-request store of M6 ([ADR-0005](../../adr/0005-context-pooling-and-borrow-contract.md),
[the M6 design](2026-09-18-m6-context-pooling-design.md), D4)

## Goal

Make the per-request store type-safe and collision-free: a value is stored and read through a key
that fixes its type, and two keys never share a slot unless they are the same key.

## Question this design answers

Can the store's key carry the value's type without adding a cost to the request path, and what
does replacing `Set(string, any)` cost the people already calling it?

## What exists

Read against rice at `35c1043`.

- `ctx_store.go` holds `entry{key string, val any}`, a slice pre-sized to `storeCapacity = 4`,
  and `Ctx.Set(key string, v any)` / `Ctx.Get(key string) (any, bool)`, each a linear scan.
  `resetStore` zeroes the entries before truncating, so a pooled `Ctx` keeps nothing alive.
- Two failure modes are left to run time. **Collision:** two middleware that pick the same string
  share a slot, silently. `middleware.RequestID` avoids it only by convention, with an unexported
  key namespaced `"rice/middleware.request-id"`. **Wrong type:** `Get` returns `any`, and a
  mistaken type assertion is found when it panics or when the `ok` form quietly returns false.
- Callers outside tests: `middleware/requestid.go` (`Set` and `Get`), and
  `bench/rice_bench_test.go`. Tests: `ctx_store_test.go`, `pool_test.go`, `pool_race_test.go`,
  `alloc_test.go`, and the `ricedebug` test that calls every exported method.
- The M6 design deferred typed keys because "the documented API was already `string`/`any`". This
  design reverses that deferral; the owner chose replacement over coexistence.

## Constraints from Go

- **A method cannot have its own type parameters.** `c.Get[T](k)` is not expressible, so the typed
  operation lives on the key (`k.Get(c)`) or in a package function (`rice.Get(c, k)`).
- **Storing a non-pointer value in an `any` boxes it.** A typed key does not change that: storing
  a `string` still costs one allocation at the call site, as `c.Set("id", s)` does today. Typed
  keys buy safety, not speed.

## Non-goals

- Removing the boxing allocation for non-pointer values. It needs a different store, not a
  different key.
- `Delete`, `MustGet`, enumerating the keys present, default values. The old API had none, and no
  caller has asked.
- Coexistence with `Set`/`Get`, or a deprecation period. The owner chose replacement: rice is before
  1.0, and one middleware in this repository uses the old API.

## Decisions

### D1 — the key carries the type; the operations are methods on it

```go
type Key[T any] struct{ id *keyID }

func NewKey[T any](name string) Key[T]

func (k Key[T]) Set(c *Ctx, v T)
func (k Key[T]) Get(c *Ctx) (T, bool)
func (k Key[T]) String() string
```

```go
var userKey = rice.NewKey[*User]("user") // once, at package level

userKey.Set(c, u)
u, ok := userKey.Get(c) // u is *User
```

The compiler rejects `userKey.Set(c, "a string")`, and `Get` returns `T` with no assertion at the
call site.

### D2 — identity is a pointer made by NewKey; the name is for people

`NewKey` allocates a `keyID{name}` and the key holds a pointer to it. Two calls to `NewKey` give two
distinct keys even with the same name and type, so two middleware that both choose `"user"` never
share a slot. Copying a `Key` copies the pointer: a copy is the same key. `Key` is a small value
type, passed and copied freely. `String()` returns the name, so `fmt` and log lines print something
readable rather than a pointer.

### D3 — what panics, at the call that is wrong

With the prefix `rice: `:

- `NewKey("")`. A key with no name has nothing to show when it is being debugged.
- `Set` or `Get` on a zero `Key[T]{}`, whose `id` is nil. Allowed, every zero key would share one
  slot — the collision this design exists to remove. The message says to create keys with
  `NewKey`.

### D4 — semantics carried over unchanged

- `Get` on a key with no value returns the zero `T` and `false`.
- A second `Set` on the same key replaces the first.
- A nil pointer is a value: `Get` returns `(nil, true)`.
- Borrowed: the value dies with the `Ctx` when the handler returns. Under `-tags ricedebug`, `Set`
  and `Get` on a released `Ctx` panic, like every other accessor; both call `c.poison.check()`
  first, before the nil-id check.
- Creating a key per request is not forbidden, but costs an allocation each time and can never be
  read back by the next call; the documentation says keys are package-level variables.

### D5 — the store keeps its shape; only the key's type changes

`entry` becomes `{key *keyID, val any}`. The slice, `storeCapacity = 4`, the linear scan and
`resetStore`'s zeroing are unchanged; comparing pointers is cheaper than comparing strings.
`Key`, `NewKey`, `keyID` and the methods live in `ctx_store.go`, in package `rice`, so the methods
reach `c.poison` and `c.store`. `Ctx.Set` and `Ctx.Get` are removed; unexported `c.set(id, v)` and
`c.get(id)` keep the scan. No other exported name is added.

`Get` returns `e.val.(T)`. Only a `Set` through the same key writes that slot, so the assertion
cannot fail; a failure would be a bug in rice, and it panics rather than returning a wrong zero.

### D6 — callers move

- `middleware.RequestID`: `var requestIDKey = rice.NewKey[string]("rice/middleware.request-id")`.
  `RequestIDFrom` keeps its signature and loses its type assertion. The namespaced name is kept for
  readability; it no longer carries the collision guarantee.
- `bench/rice_bench_test.go` and every core test listed under *What exists* move to keys.

## Components

| File | Change |
| --- | --- |
| `ctx_store.go` | `keyID`, `Key[T]`, `NewKey`, the methods; `entry.key` becomes `*keyID`; `Set`/`Get` become unexported `set`/`get` |
| `ctx_store_test.go` | behaviour, rewritten for keys |
| `pool_test.go`, `pool_race_test.go`, `ricedebug_test.go` | move to keys |
| `alloc_test.go` | the store budgets, renamed and extended |
| `middleware/requestid.go` | `requestIDKey` becomes a `Key[string]` |
| `bench/rice_bench_test.go` | moves to keys |

## Allocation budget

Unchanged figures, renamed tests, one added row:

| Operation | Budget |
| --- | --- |
| `Key.Set` with a pointer value | 0 |
| `Key.Set` with a non-constant string (the caller's boxing) | 1 |
| `Key.Get` with a pointer `T` | 0 |
| `Key.Get` with a `string` `T` (new) | 0 |
| `middleware.RequestID`, generating an id | 2 (3 under `-race` on Linux, as today) |

`NewKey` allocates its `keyID` once per key and is not budgeted, like registration. **Every figure is
measured on darwin and Linux, with and without `-race`, before it is written into a document.** If a
figure moves, the measured one is pinned with its mechanism named, not rounded to the target.

## Testing

One test per rule, each failing when its rule is broken:

- `Set` then `Get` returns the value with its type; `Get` with nothing stored returns the zero value
  and `false`
- **two keys made by two `NewKey` calls with the same name and type hold separate values** — the
  collision test
- a copy of a key reads the same value
- a second `Set` replaces the first; a nil pointer is stored and read back as `(nil, true)`
- more than `storeCapacity` keys in one request all read back correctly
- a request on a pooled `Ctx` sees none of the previous request's values
- `NewKey("")` panics; `Set` and `Get` on a zero `Key` panic; each message carries `rice: `
- `String()` returns the name
- the pool race test, moved to keys
- the `ricedebug` test: `Set` and `Get` on a released `Ctx` panic

## Documentation

- **ADR-0016 (new)** — "Typed store keys replace Set and Get". Context: the two run-time failure
  modes and the M6 deferral. Decision: D1–D6. Alternatives, each with why it lost: package-level
  generic functions `rice.Set(c, k, v)` / `rice.Get(c, k)` (same mechanism, but `rice.Get` is a
  vague name at package level and reads unlike `c.Param`); user-declared key types in the
  `context.WithValue` style (`type userKey struct{}`), which prevent collisions but leave the
  value's type unchecked; keeping `Set`/`Get` beside keys (two ways to do one thing, and the string
  path still collides). Consequences: the breaking change and how to migrate; the boxing cost that
  does not change; the M6 deferral reversed.
- **`docs/03-core-concepts.md`** — rewrite *Per-request store*; the `RequestID` illustration and
  the paragraph after it move to a key.
- **`docs/05-performance-model.md`** — rename the store rows, add the `Get` string row, reword the
  `c.Set("k", s)` sentence.
- **`docs/04-roadmap.md`** — move the item from *Explicitly deferred* to *Done after M8*.
- **`docs/02-architecture.md`** — `ctx_store.go`'s line in the layout.
- **README, `middleware/doc.go`, `docs/06-glossary.md`** — wherever they name `Set` or `Get`.
- **`docs/progress.md`** — one entry.

## Exit criteria

1. `make test` and `make test-debug` green on darwin and in a Linux container.
2. Every rule in D2–D4 is pinned by a test that fails when it is broken.
3. Every budget is measured on both platforms before it is written.
4. `grep -rn 'c\.Set(\|c\.Get(' --include='*.go' .` returns nothing, and the same search over
   `README.md`, `docs/*.md`, `docs/adr/` and `middleware/doc.go` returns only historical records
   (the progress journal and ADRs that predate this one).

## Open questions

None.
