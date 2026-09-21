# ADR-0011 — Binding is generic and validation is a method

Status: Accepted
Date: 2026-09-20

## Context

[ADR-0006](0006-no-reflection-in-core.md) kept `reflect` and `encoding/json` out of routing,
context handling and middleware, and left one door open: binding "may exist later as an opt-in
package outside core, so that the cost appears in the user's import list". It also named the
direction — generics — as "the most promising future direction", and stopped there. It said where
binding may live. It did not say what it looks like.

The first service that will use rice is a JSON API, and every handler in it starts the same way:
`json.Unmarshal(c.Body(), &v)`, a check, a status chosen by hand, then the validation rules
written inline. Three concerns and four lines before the handler's actual work begins, repeated
per route, with the status mapping re-decided each time. That is the pressure that forces a
choice now.

The choice is not whether to add binding. It is where the convenience stops, because the
convenient version of this feature is the one that made ADR-0006 necessary: `c.Bind(&v)` driven
by struct tags, where a reader of a handler cannot see what the rules are.

## Decision

One exported function, in a package outside core:

```go
package binding

func JSON[T any](c *rice.Ctx) (T, error)
```

No options, no constructor, no registry. The type is a type parameter, so the call site names it
and the compiler checks it.

**Strictness is two checks, not one.** The decoder runs with `DisallowUnknownFields`, so
`{"emial": "..."}` is an error rather than a silently zero-valued `Email`. That alone would be a
half-kept promise, because `Decode` reads one value and stops: `{"a":1}{"b":2}` would pass. After
a successful decode the package checks `dec.More()` and rejects anything left over.

**Validation is an ordinary method, found by assertion.** After decoding, the package tries
`any(&out).(interface{ Validate() error })`. The assertion goes through `&out` rather than `out`
deliberately: a `Validate` on a value receiver is in the method set of both `T` and `*T`, but one
on a pointer receiver is in the method set of `*T` only. Asserting on the value would silently
skip the pointer-receiver case, and "I wrote `Validate` and it never ran" is indistinguishable
from "my validation passed". A type without `Validate` decodes and returns — no panic, no
warning, no registration step.

**Three error branches, and only one of them shows the cause.**

| Case | Status | Body the client sees | `HTTPError.Err`, available to a logger |
| --- | --- | --- | --- |
| Empty or whitespace-only body | 400 | `empty request body` | `io.EOF` |
| Malformed JSON, unknown field, trailing data | 400 | `invalid JSON body` | the `encoding/json` error, or `errTrailingData` |
| `Validate` returned an error | 422 | that error's own message | the same error |

The cause in that last column is available, not written: `DefaultErrorHandler` logs unhandled
errors and panics, and never an `*HTTPError`. A custom `ErrorHandler` or a logging middleware
reads it; nothing does by default.

The first two use a fixed message because `encoding/json`'s text is shaped by the caller's input —
`invalid character 'x' looking for beginning of value` leaks parser detail, and M5 already holds a
test that a cause string never reaches a response body. The third is safe by construction: the
message was written by the handler's author, not produced from input. Empty is split from
malformed because it is the most common client mistake, and answering `invalid JSON body` to a
request that carried no body sends people looking in the wrong place.

422 rather than 400 for a failed `Validate`, because 400 means "I could not parse this" and 422
means "I parsed it and it breaks a rule", and a client can tell those apart without reading the
body. The override needs no option: if `Validate` returns, or wraps, an `*rice.HTTPError`, that
error is passed through unchanged, so an author who wants 409 for a particular rule returns one
and gets it. The match is `errors.As`, so an `*rice.HTTPError` anywhere in the chain is found —
including one a collaborator wrapped, whose status the client then sees. What is passed through
is the error as `Validate` wrote it, wrapper and all, so the author's outer message stays in the
chain: every error this package returns *carries* an `*rice.HTTPError` reachable with
`errors.As`, rather than being one.

## Alternatives

**Reflection plus `validate:"required,email"` struct tags and a third-party validator.** This is
what gin, echo and Fiber all do, it is what a user arriving from any of them expects, and it would
have been less code here than what was built. Rejected on design principle 2: the rule stops being
readable at the call site. A reader of a handler sees `binding.JSON[CreateUser](c)` and, to learn
what makes a `CreateUser` valid, must open the struct, read a tag, and then consult a third
library's documentation to find out what `email` means in that library's dialect — and the answer
differs between libraries. A `Validate() error` method is Go: a reader follows it, a debugger
steps into it, and a test calls it directly without a request. It also adds a module dependency to
a repository that has one, which is not the deciding argument but is the one a user sees first.

**A fully reflection-free decoder, written per type.** The purest reading of ADR-0006: the user
implements `DecodeJSON(b []byte) error` for each input type, or generates it, and nothing in the
path walks a type at runtime. It is the fastest option and the only one that would put the
allocation figure below at zero. Rejected because the resulting call site is verbose enough that
nobody would import it — the whole point of this package is to replace four lines with one, and a
version that replaces four lines with forty of generated code is a code generator, which ADR-0006
already rejected as a second project. A helper nobody imports should not exist.

**`c.Bind(&v)` as a method on `Ctx`.** Not seriously considered, and named here because it is what
a reader will ask about. It would put `encoding/json` in core's import graph for every user
including the ones who never call it, which is the exact cost ADR-0006 drew its line to avoid.

## Consequences

**Makes easy.** One line at the call site, with the type visible in it. Validation rules that are
ordinary Go code: a reader follows them, and a test exercises `CreateUser{}.Validate()` directly
with no request, no server and no framework. A handler returns the error unchanged and rice's
funnel writes the response, so the status mapping is decided once here instead of per route. No
new module dependency.

**Makes hard: the author writes `Validate` by hand.** There is no `required` and no `email`. A
field that must be non-empty gets an `if`. For a struct with twenty fields that is twenty `if`s,
and the tag-based version really is shorter. This is the sacrifice, and it is the same one
ADR-0006 made.

**An author who wraps an internal error in `Validate`'s message sends that text to the client.**
`fmt.Errorf("checking user in db: %w", err)` returned from `Validate` becomes a 422 body. This
package cannot tell an author's message from a wrapped internal error — the only signal available
is the type, and both are `error`. So it is documented rather than detected: `binding/doc.go` says
it plainly and the rule is "keep `Validate`'s messages about the request". A guarantee that cannot
be enforced is better stated as a warning than implied by silence.

**The cost of 422.** It is defined by RFC 4918, the WebDAV extension, not by RFC 9110, so some
intermediaries and client libraries treat it as an unfamiliar status. A proxy that buckets
unknown 4xx, or a generated client that switches on a known set, may handle it less gracefully
than a 400. That is the price of the distinction between "unparseable" and "invalid", and it is
paid knowingly.

**It allocates, and the number is pinned rather than bounded.** `DisallowUnknownFields` exists
only on `json.Decoder`, not on `json.Unmarshal`, and `json.Decoder` has no `Reset`, so it cannot
be pooled: strictness is what costs the allocations. `binding.JSON` with the test suite's
`createUser` fixture allocates exactly 9 objects per call, and the budget test fails if the figure
moves in either direction, so an encoder that gets cheaper corrects the documentation rather than
leaving it stale. What those 9 objects are has not been determined — see
[05-performance-model.md](../05-performance-model.md). The escape hatch is the one ADR-0006 named:
`c.Body()` and a hand-written decode.

**ADR-0006's boundary has not moved.** Package `rice` still imports neither `reflect` nor
`encoding/json` anywhere in routing, context handling or middleware; `c.JSON` at the response edge
remains the one exception, exactly as ADR-0006 wrote it. Package `binding` is a different package.
Its `encoding/json` import appears in the import list of any user who chooses it and in nobody
else's, which is precisely the condition ADR-0006 attached to this package existing. `encoding/json`
uses reflection internally — that is the standard library's business inside a call this package
makes, and it is not reflection in rice's own code. This paragraph exists because a reader who
greps the rice module for `encoding/json` will find it in two places — a third, `bench/compare/`,
is a benchmark harness in a separate module — and could reasonably conclude
the rule was quietly relaxed. It was not.

**Forecloses little.** `binding.Query` and `binding.Header` are deliberately absent — `c.Query`
and `c.Header` exist, and binding them without struct tags would be contrived — but nothing here
prevents them. Neither a `Content-Type` check nor a body-size limit is added: real clients omit the
header constantly, and `WithMaxBodySize` already enforces a limit at the transport before a `Ctx`
exists, so a second one here would be two sources of truth for one rule.

**Revisit if `encoding/json` gains a strict decode that does not require a `Decoder`.** The
allocation figure and the whole of the pinned-budget reasoning rest on `DisallowUnknownFields`
living on `json.Decoder` and on that decoder having no `Reset`. If a future standard library
offers an unknown-field check through a one-shot call, this decision is re-read: the figure would
drop and the escape hatch might stop being needed.
