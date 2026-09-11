# M3 — Radix Tree Router: Design

**Status:** approved, not yet implemented
**Date:** 2026-09-10
**Milestone:** M3 in [docs/04-roadmap.md](../../04-roadmap.md)
**Decision record:** [ADR-0004](../../adr/0004-radix-tree-router.md)
**Depends on:** M2 (`internal/router.Tree[H]`, per-verb registration, the error funnel)

## Goal

Replace M2's exact-match map with a per-method radix tree that supports named
parameters (`/users/:id`) and a trailing catch-all (`/files/*path`), resolves
overlaps by a fixed total priority, rejects ambiguous registrations at startup,
and captures parameters without allocating.

This is the algorithmic core of the project. ADR-0004 already fixed the shape:
one tree per verb, `Lookup` filling a caller-supplied `*Params`, priority
**static > parameter > wildcard**, conflicts rejected at registration.

## Question this milestone answers

Where does the tree actually win, and where does it lose to the map?

M2 measured the map at 38.52 / 38.37 / 38.34 ns/op for 10 / 100 / 1000 routes —
flat to within 0.18 ns. A hash of a short string does not care how many routes
exist, so the honest expectation is that **the tree will not beat the map on
purely static routes, and may lose to it.** If that is the result, it is a
finding rather than a failure: it would mean the tree earns its keep on
parameters, which the map cannot express at all, and on shared prefixes, and
that static-only route sets are a case where the naive structure was already
right.

M2's numbers are what makes this answerable, which is why M2 existed.

## Non-goals

Out of scope, each with the milestone that owns it or the decision that excluded it.

- Trailing-slash redirection and case-insensitive fallback matching — **decided
  against**, see D7. ADR-0004 left this open for M3; M3 closes it.
- Middleware, groups, the `build()` phase — M4
- `HTTPError`, a configurable `ErrorHandler`, panic recovery — M5
- Context pooling, and sizing parameter storage from the observed maximum — M6
- Host-based routing and regular-expression routes — foreclosed by ADR-0004
- `405 Method Not Allowed` — still deferred, as in M2's D4

## Decisions

### D1: One parameter child and one wildcard child per node

```go
type node[H any] struct {
	prefix     string // the compressed static prefix this node owns
	handler    H
	hasHandler bool

	static   []*node[H] // static children, distinguished by first byte
	param    *node[H]   // at most one ":name" child
	wildcard *node[H]   // at most one "*name" child
	name     string     // parameter name; set only on param and wildcard nodes
}
```

`param` and `wildcard` are single pointers rather than slices, and that is what
makes conflict detection simple: a second parameter child with a different name
is rejected because there is nowhere to put it.

This is forced by logic, not chosen. `/users/:id` and `/users/:name` cannot both
be honoured — a request for `/users/42` would have to bind either `id` or `name`,
and nothing in the request says which. Registering both is a programmer error and
M3 rejects it at startup.

Static children stay a plain slice, scanned by comparing first bytes. httprouter
keeps a parallel `indices string` of first bytes to scan in one pass with better
cache locality. That is a real optimisation but it duplicates state that must be
kept in sync, which is a bug source in the hardest code in the project. M3 writes
the readable version and measures it. If the scan shows up in a profile, the
`indices` variant is an M8 candidate with a number to justify it.

### D2: Lookup backtracks, and this is the part most radix routers get wrong

Register `/users/new` and `/users/:id`. Request `/users/newx`:

1. The static branch matches `new`, leaving `x` unconsumed
2. That node has no child matching `x` and no handler for the remainder — the
   static branch fails
3. Lookup must **return to the parent and try the parameter child**, which
   captures `newx` and matches

Without that step, `/users/newx` returns 404 while `/users/:id` sits registered
and willing. httprouter carried exactly this bug and Gin inherited it.

`Lookup` therefore tries static children, then the parameter child, then the
wildcard child, and on failure unwinds to the next alternative at the previous
node. Recursion expresses this most clearly and costs nothing: stack frames are
not heap allocations, and depth is bounded by the number of path segments.

The test that catches a missing backtrack is exactly the case above. It is
mandatory.

### D3: Parameter storage is a fixed inline array of eight

```go
const MaxParams = 8

// Param is one captured route parameter. Key is owned and comes from the
// registered pattern; Value is borrowed and points into fasthttp's buffer.
type Param struct {
	Key   string
	Value []byte
}

type Params struct {
	slots [MaxParams]Param
	n     int
}

func (p *Params) Reset() // exported: rice calls it from Ctx.reset across the package boundary
func (p *Params) Get(name string) []byte // linear scan; n is at most 8
func (p *Params) Len() int
func (p *Params) At(i int) Param
```

A linear scan over at most eight entries beats a map decisively at this size and
allocates nothing. `Ctx` holds a `Params` **by value**, so parameter storage
exists wherever the `Ctx` does and needs no separate allocation — which is the
entire reason ADR-0005 specified `Lookup(path, *Params)` instead of returning a
slice.

Registering a route with more than eight parameters panics at startup. The
pattern is parsed at registration, so the count is known then; there is no reason
to discover it at request time.

The alternative — sizing a slice from the maximum parameter count across all
registered routes — is better in the long run and is what M6 will do once a pool
exists to hold it. In M3 it would cost a second allocation per request, on the
milestone whose budget requires lookup to allocate nothing.

**The cost, stated plainly:** `Param` is 40 bytes, so `Params` is 328 and `Ctx`
grows from 16 bytes to roughly 344. Allocations per request stay at one; bytes
per request rise about twenty-onefold, and the benchmark will show it. M6's pool
makes the size irrelevant, but M3 records the number rather than hiding it.

> **Added during the final review (2026-09-11).** This decision weighed a fixed
> inline array against a pool-sized slice and never considered a third option:
> shrinking `Param` itself. `Key string` plus `Value []byte` is 40 bytes because
> both fields are independent views with their own pointer and length. Storing
> `(nameIdx uint16, start, end uint32)` instead — an index into the route's own
> name list, plus start/end offsets into the request path — is **12 bytes**, not the
> 10 its fields sum to: Go aligns the two `uint32`s, which pads the `uint16`. So
> eight slots cost **96 bytes** rather than 328. Both figures were measured with
> `unsafe.Sizeof`, after the first version of this note stated 10 and 80 from
> arithmetic that ignored alignment. The borrow contract is
> unchanged and capture still costs one allocation; nearly all of the ~43 ns this
> milestone's retrospective attributes to `Ctx` growth would be avoided without
> waiting for M6's pool.
>
> This was not caught in time to change M3, and it should not be: M3's budget was
> already spent proving the tree works at all, and reopening `Param`'s layout here
> would have widened scope on the milestone ADR-0004 already called the hardest
> code in the project. It is recorded here because M6 is unwritten and its stated
> plan — sizing storage from the observed maximum while keeping 40-byte entries —
> would spend a pool allocation to remove the fixed-eight limit while leaving most
> of this win on the table. This belongs to M6's design, not M3's, and the note
> exists so M6 inherits the option rather than reinventing it.

### D4: The Ctx is now allocated before the lookup, partially reverting M2's D6

M2's D6 moved the lookup ahead of the `Ctx` allocation so that a request matching
nothing would not pay for a context no handler ever read. M3 cannot keep that
order. `Lookup` fills a `*Params`, `Params` lives on the `Ctx`, so the `Ctx` must
exist before the lookup runs.

```go
func (a *App) handle(fctx *fasthttp.RequestCtx) {
	c := &Ctx{}
	c.reset(a, fctx)

	h, ok := a.lookup(fctx.Method(), fctx.Path(), &c.params)
	if !ok {
		a.handleError(c, ErrNotFound)
		return
	}

	if err := h(c); err != nil {
		a.handleError(c, err)
	}
}
```

This is not a regression in disguise; it is the borrow contract's design working
as intended. Parameter storage living on the borrowed handle is what buys
zero-allocation capture, and the price is that the handle must be constructed
first.

**A consequence worth naming, because it will look like a regression:** M2
measured the 404 path at zero allocations, because escape analysis kept the
miss-path `Ctx` on the stack. M2's retrospective predicted that M5's configurable
`ErrorHandler` would take that zero away. **M3 takes it away first, for an
entirely different reason** — the `Ctx` now escapes on both paths because
`&c.params` is passed to `lookup`. The prediction that the zero would not last
was right; the mechanism named for it was not the one that arrived. M3 records
this in ADR-0005 and in the retrospective, because it is the clearest available
evidence for why that zero was documented rather than pinned by a test.

### D5: Insert splits on the longest common prefix

Insertion walks from the root comparing the pattern's next static run against the
current node's `prefix`:

- Common prefix equals the node's prefix, and pattern remains — descend
- Common prefix equals both — this node is the target; a handler already present
  means a duplicate
- Common prefix is shorter than the node's prefix — **split**: the node keeps the
  common part, a new child takes the node's remainder along with its children and
  handler, and a second child takes the pattern's remainder
- `:` begins a parameter segment, consumed to the next `/`
- `*` begins a wildcard, which must run to the end of the pattern

Splitting is the intricate part and ADR-0004 says so. The test suite covers
splits at the first byte, at an interior byte, at the last byte of a prefix, and
splits that must themselves split again.

### D6: Registration rejects anything ambiguous or unreachable

Rejected, with a message naming both patterns where two are involved:

| Rejected | Why |
| --- | --- |
| The same pattern twice for one verb | One handler could never run (M2's D8) |
| Two parameter names at one position (`/u/:id` vs `/u/:name`) | Nothing in a request says which to bind (D1) |
| Two wildcard names at one position | Same reason |
| A wildcard not at the end (`/f/*p/edit`) | A catch-all consumes the remainder by definition |
| An empty parameter name (`/users/:`) | Nothing to look the value up by |
| A repeated parameter name in one pattern (`/a/:id/b/:id`) | The second capture would shadow the first |
| More than `MaxParams` parameters | Storage is fixed at eight (D3) |
| An empty path segment (`/a//b`) | Never matches: `fctx.Path()` collapses it (D8) |
| A `.` or `..` segment | Never matches: `fctx.Path()` resolves it (D8) |
| A percent-encoded pattern (`/caf%C3%A9`) | Never matches: `fctx.Path()` decodes it (D8) |

Accepted, resolved by the priority in ADR-0004 rather than rejected:

| Accepted | Resolution |
| --- | --- |
| `/users/new` and `/users/:id` | `/users/new` takes the static branch; everything else binds `id` |
| `/files/:name` and `/files/*path` | A single segment binds `name`; deeper paths take the wildcard |
| `/files/*path` and `/files/a/b` | `/files/a/b` takes the static branch |

The priority being total is what allows these to coexist. A design that rejected
overlap instead of ordering it would reject route sets every REST API contains.

### D7: No trailing-slash redirection, no case-insensitive fallback

ADR-0004 left both open for M3 and asked for a decision recorded as a follow-up
ADR. M3 declines both, and the reasoning is not cost: both would sit on the miss
path and neither would touch a matching request.

The reason is scope. Prefix splitting, parameter capture, wildcard handling,
backtracking and conflict detection are, in ADR-0004's own words, the hardest
code in the project. Adding redirect semantics — which also means choosing
between 301 and 308, and deciding whether a redirect fires for a request that
already carries a body — widens the semantics M3 must test exhaustively while
contributing nothing to the measurement M3 exists to produce.

Recorded as a follow-up ADR, not as silence. Should either return, it should
return opt-in, and the `Option` mechanism for that arrives in M7.

### D8: Patterns must be given in the form `fctx.Path()` produces

M2's final review found that `/a//b`, `/a/./b` and `/caf%C3%A9` all register
cleanly and then never match, because fasthttp collapses empty segments, resolves
dot segments, and percent-decodes before rice ever sees the path. Matching
happens in decoded, normalised space; registration did not.

M3 rejects those patterns at registration rather than normalising them, for the
same reason M2's D8 panics on a duplicate: a route that can never run is a
programmer error, and the error belongs at the call site that made it.

Normalising instead would be friendlier and worse. `app.GET("/a//b", h1)`
followed by `app.GET("/a/b", h2)` would silently become a duplicate-route panic
naming a pattern neither line contains.

Panic messages name the offending pattern and the form to use instead:

    rice: route path /a//b has an empty segment; register /a/b
    rice: route path /caf%C3%A9 is percent-encoded; register the decoded form /café

### D9: M2's map is frozen inside the benchmark package

`bench/results/README.md` states that numbers from different stamps are not
comparable and that the older label must be re-run on the current machine.
Deleting M2's map would make that impossible for every milestone after this one.

So the map implementation is copied — roughly fifteen lines — into
`bench/mapbaseline_test.go` as a type local to package `bench`, used only for
measurement and imported by nothing. Every recording from M3 onward reports the
map and the tree side by side, produced by the same binary on the same machine in
the same session.

It is a deliberate duplicate and will be labelled as one, including an
instruction not to "fix" it: its value is that it does not change.

## Components

| Unit | Responsibility | Depends on |
| --- | --- | --- |
| `internal/router.Params` | Fixed-capacity parameter capture, name lookup by linear scan | nothing |
| `internal/router` pattern parsing | Split a pattern into static, parameter and wildcard segments; reject malformed and un-normalised patterns | nothing |
| `internal/router.node[H]` | One tree node: static prefix, one parameter child, one wildcard child | nothing |
| `internal/router.Tree[H].Insert` | Longest-common-prefix insertion with splitting; conflict rejection | `node`, pattern parsing |
| `internal/router.Tree[H].Lookup` | Priority-ordered walk with backtracking, filling `*Params` | `node`, `Params` |
| `rice.Ctx` | Gains `params Params` by value, plus `Param` and `ParamString` | `router.Params` |
| `rice.App.lookup` | Verb to tree, then delegate, threading the caller's `*Params` | `router` |
| `rice.App.handle` | Construct `Ctx`, look up, dispatch or funnel | all of the above |

`internal/router` remains generic over the handler type and imports nothing from
`rice`, per the layering rule. `Params` is an internal type; it never appears in
rice's public API, because `Param` and `ParamString` return `[]byte` and `string`.

## Public API added

```go
func (c *Ctx) Param(name string) []byte       // borrowed; empty if absent
func (c *Ctx) ParamString(name string) string // copies; one allocation; safe to keep
```

Both are already committed in `docs/03-core-concepts.md`, including their
allocation budgets. The naming rule holds: the byte-returning accessor is free and
borrowed, the string-returning one copies and is yours.

## Allocation budgets

Rows this milestone moves from TARGET to measured in
`docs/05-performance-model.md`:

| Operation | Budget | Enforced by |
| --- | --- | --- |
| Router lookup, 1 parameter | 0 | `AllocsPerRun` in `alloc_test.go` |
| `c.Param` | 0 | `AllocsPerRun` |
| `c.ParamString` | 1 | `AllocsPerRun` |

The existing table bundles three accessors into one row, `c.Param`, `c.Query`,
`c.Header` | 0 | TARGET (M3). **That row must be split**, not relabelled. M3
delivers `c.Param` only; marking the row MEASURED M3 would assert that `c.Query`
and `c.Header` are measured when neither exists. `c.Param` becomes its own
MEASURED M3 row and the other two keep TARGET.

`Router lookup, 5 parameters` stays TARGET (M6): its row is annotated "needs slot
sizing", and slot sizing is M6's work.

A gap this surfaced, recorded rather than fixed here: **`c.Query` and `c.Header`
belong to no milestone.** `docs/03-core-concepts.md` commits to both and
`docs/05-performance-model.md` budgets them, but no entry in
`docs/04-roadmap.md` claims them. They are trivial wrappers over fasthttp and
adding them to M3 would be unrelated scope creep, so M3 leaves them alone and
notes that the roadmap needs an owner for them.

The end-to-end row stays at one allocation. Its byte figure changes and the
retrospective states the new number.

## Testing

**Pattern parsing and validation** — every rejected case in D6 gets a test
asserting the panic and that the message names the offending pattern.

**Insertion** — table-driven: splits at the first byte, an interior byte and the
final byte of a prefix; a split that itself must split; insertion order
independence, meaning the same route set inserted in several orders produces the
same lookup results.

**Lookup** — hit, miss, and for each of the accepted overlaps in D6 the
resolution the table claims. Plus, mandatorily, the backtracking case from D2:
`/users/new` and `/users/:id` registered, `/users/newx` requested, expecting the
parameter branch to match.

**Parameters** — capture of one, several, and exactly `MaxParams`; absent name
returns empty; wildcard captures the remainder including slashes; a value read as
`[]byte` aliases the request buffer while `ParamString` survives it.

**Depth** — a deeply nested route set, to show recursion depth is bounded by the
path and not by the tree's size.

**Allocation budgets** — as tabled above, each asserting its precondition before
measuring, following the pattern M2 established.

**Integration** — a parameterised route answered over a real socket, and a
wildcard route serving a nested path.

## Benchmarks

Recorded with `make bench-record LABEL=M3-radix-tree-router`.

| Benchmark | Purpose |
| --- | --- |
| `BenchmarkMapLookup10/100/1000` | The frozen M2 map, same run, same machine (D9) |
| `BenchmarkTreeLookup10/100/1000` | The tree on the same static route set |
| `BenchmarkTreeLookup1Param` | The case the map cannot express |
| `BenchmarkTreeLookup5Params` | Parameter capture at depth |
| `BenchmarkTreeLookupWildcard` | Catch-all matching |
| `BenchmarkTreeLookupBacktrack` | The `/users/newx` path, which fails one branch before succeeding |
| `BenchmarkTreeMiss` | The 404 path, 1000 routes registered |

The map-versus-tree pair on identical static routes is the milestone's headline.
The backtracking benchmark exists because it is the worst case the design admits,
and a worst case nobody measured is a worst case nobody knows.

## Exit criteria

- Table-driven tests covering prefix splits, priority ordering, backtracking,
  conflicting registrations and deep nesting
- `AllocsPerRun` asserts 0 for lookup, including with parameters captured
- Benchmarks recorded and compared directly against the frozen map in the same run
- The three budget rows above move to measured; `Router lookup, 5 parameters`
  stays TARGET (M6)
- ADR-0004's open question closed by a follow-up ADR recording D7
- ADR-0005 gains a note recording that M3, not M5, ended the accidental
  zero-allocation 404 path, and why
- Retrospective in `docs/milestones/M3-radix-tree-router.md`, stating plainly
  whether the tree beat the map and what that implies
- Journal entry in `docs/progress.md`

## Open questions

- Whether the tree beats the map on static routes. That is this milestone's
  question and its benchmarks answer it. The design's expectation is recorded
  above so the result cannot be reinterpreted after the fact.
- Whether the static-child scan wants httprouter's `indices` layout. Deferred to
  M8 and only with a profile to justify it (D1).
- Roughly nine nanoseconds of M2's routing cost were never explained. M3 changes
  the lookup path completely, so the figure is not directly carried over — but the
  habit of recording an unexplained number is now two milestones old, and M3
  should either explain its own cost or say that it did not.
