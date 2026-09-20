# 01 — Design Principles

Nine rules. Each one is falsifiable: it says what would violate it. When two principles
conflict, the lower number wins, and the conflict gets an ADR.

---

## 1. Allocation is a design constraint, not an optimisation pass

Every public API on the hot path carries a documented allocation budget, and the budget is
enforced by a test using `testing.AllocsPerRun`. A change that raises a budget is a design
change and needs justification in the pull request, the same way an API break would.

*Violated by:* adding a feature and planning to "profile it later". Later never has the
same leverage — by then the allocation is baked into the API shape, and removing it is a
breaking change instead of a design choice.

*Consequence:* some ergonomics are refused. `c.Params()` returning a `map[string]string`
is convenient and costs a map allocation per request, so it does not exist.

---

## 2. No reflection, no struct tags, no code generation in core

Package `rice` imports neither `reflect` nor `encoding/json` in its routing, context or
middleware path. Serialisation helpers that must use `encoding/json` do so at the edge,
in one file, and their cost is documented rather than hidden.

*Violated by:* a `c.Bind(&v)` that walks struct tags. That helper is welcome, but it lives
in a side package that users opt into, so that the cost is visible in the import list.

*Why:* reflection is where framework behaviour becomes unexplainable. The moment routing
depends on tags, the reader can no longer predict what happens by reading the call site.

---

## 3. Explicit lifetimes — the borrow contract

Everything reachable from a `*Ctx` is **borrowed**, not owned, with one named exception:
`Context()` returns a `context.Context` owned by the `App`, not the request, which stays
valid after the handler returns. See
[ADR-0010](adr/0010-request-context-cancels-at-force-close.md). Every other value reached
through a `*Ctx` is valid only until the handler returns. That includes the `*Ctx` itself,
every `[]byte` obtained from it, every route parameter, every header value, and the request
body slice.

Code that needs a value to outlive the handler must copy it, and the framework provides an
obvious way to do so for every borrowed value.

*Violated by:* spawning a goroutine that closes over `c` and reads it after return. The
framework cannot prevent this at compile time, so it must (a) say so in every doc that
hands out a borrowed value and (b) detect it at run time under the `ricedebug` build tag
by poisoning released contexts. See [ADR-0005](adr/0005-context-pooling-and-borrow-contract.md).

*Why this is principle 3 and not principle 9:* object reuse is what buys principle 1. The
contract is the price of the performance, and hiding the price would be dishonest.

---

## 4. Composition over configuration

Behaviour is added by wrapping, not by setting flags. There is no `app.EnableX(true)`.
There is a middleware that does X, and you install it or you do not.

*Violated by:* an options struct that grows a boolean per feature. Each boolean is a
branch on the hot path that every user pays for and most users do not want.

*Corollary:* the core has no built-in logging, no built-in recovery, no built-in CORS.
Those are middlewares, and a user who installs none pays for none.

---

## 5. Errors are returned, never panicked

The handler signature is `func(*Ctx) error`. A handler signals failure by returning. The
framework routes every returned error through a single, replaceable `ErrorHandler`, so
there is exactly one place in the program where an error becomes a status code.

Panics are treated as bugs, not as control flow. The core wires fasthttp's panic hook so
that a panic produces a 500 and does not kill the connection, but it does not offer
`panic(rice.ErrNotFound)` as an idiom.

*Violated by:* `c.AbortWithStatus(404)` style APIs, where the handler mutates shared state
and the caller must remember to `return` immediately afterwards. That pattern makes
control flow invisible and is the source of a well-known class of Gin bugs.

---

## 6. Small public surface, deep internals

Package `rice` exports the smallest set of names that makes the framework usable. The
radix tree, the pool, the byte-conversion helpers and the chain compiler live under
`internal/` where they can be rewritten without breaking anyone.

*Test:* if a type is exported, there is a paragraph about it in
[03-core-concepts.md](03-core-concepts.md). If writing that paragraph is hard, the type
probably should not be exported.

---

## 7. The hot path branches as little as possible, and pays no cost for unused features

Work that can happen once, at route registration or at server start, does not happen per
request. Middleware chains are compiled into a single closure at build time, not walked
with an index at run time. Route parameter slots are sized when the route is registered,
not discovered per request.

*Violated by:* a per-request `if app.tracingEnabled { ... }`. Either tracing is a
middleware the user installed, in which case the branch does not exist for anyone else,
or it does not exist at all.

---

## 8. Every decision is recorded, including the ones that were wrong

An ADR is written when a choice has more than one defensible answer. It states the
context, the decision, the alternatives rejected, and the consequences accepted. ADRs are
never rewritten to look smarter in hindsight; they are superseded.

*Why:* the artefact of this project is understanding, and understanding lives in the
comparison between what was chosen and what was not. A repo of only correct final answers
teaches much less than a repo that shows the fork in the road.

---

## 9. Tests and benchmarks are deliverables, not chores

A milestone is not done when the code works. It is done when there is a test proving it
works, a benchmark recording what it costs, and a journal entry saying what was learned.
The benchmark numbers are committed so that regressions are visible as diffs.

*Violated by:* "I'll add benchmarks at the end." At the end there is no baseline to
compare against, and the number that matters — the one from before the change — is gone.
