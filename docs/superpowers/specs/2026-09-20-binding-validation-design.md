# Binding and validation — Design

**Status:** approved, not yet implemented
**Date:** 2026-09-20
**Milestone:** none. Work after the roadmap — see [04-roadmap.md](../../04-roadmap.md), *Done after M8*
**Decision record:** ADR-0011 (new), "Binding is generic and validation is a method"
**Depends on:** `c.Body()` (the borrowed request body), `HTTPError` and the error funnel from M5,
and [ADR-0006](../../adr/0006-no-reflection-in-core.md), which permitted this package and
constrains its shape

## Goal

Turn a JSON request body into a typed value, and let the type say what makes it valid, in one
call that a handler can return the error from.

Today a handler writes `json.Unmarshal(c.Body(), &v)`, checks the error, maps it to a status, and
then validates by hand. That is three concerns in the handler and four lines before the work
starts. This package collapses it to one line while leaving every decision visible.

## Question this design answers

ADR-0006 said binding "may exist later as an opt-in package outside core, so that the cost appears
in the user's import list", and named generics as "the most promising future direction". This
design answers what that package actually looks like — and, more importantly, where the
convenience stops.

## The shape, at the call site

```go
type CreateUser struct {
	Email string `json:"email"`
	Age   int    `json:"age"`
}

// The rule lives here: readable, debuggable, testable on its own.
func (u CreateUser) Validate() error {
	if u.Email == "" {
		return errors.New("email is required")
	}
	if u.Age < 0 || u.Age > 150 {
		return errors.New("age out of range")
	}
	return nil
}

func handler(c *rice.Ctx) error {
	in, err := binding.JSON[CreateUser](c)
	if err != nil {
		return err // already an *rice.HTTPError with the right status
	}
	return c.JSON(201, create(in))
}
```

## What the probe found

Four behaviours of `encoding/json` that this design rests on, measured rather than assumed
(Go 1.25, `json.NewDecoder` with `DisallowUnknownFields`):

| Input | `Decode` returns | `errors.Is(err, io.EOF)` | `dec.More()` |
| --- | --- | --- | --- |
| `` (empty) | `EOF` | **true** | — |
| `"   \n"` (whitespace only) | `EOF` | **true** | — |
| `{"email":"a@b.c"}` | nil | — | false |
| `{"email":"a@b.c"}\n` | nil | — | **false** |
| `{"emial":"a@b.c"}` | `json: unknown field "emial"` | false | — |
| `{"email":` | `unexpected EOF` | **false** | — |
| `{"email":"a@b.c"}{"email":"x"}` | nil | — | **true** |
| `{"email":"a@b.c"} garbage` | nil | — | **true** |

Three of these rows are load-bearing:

1. **Whitespace-only counts as empty.** A body of `"\n"` gives `EOF`, so the empty-body branch
   catches it without a separate trim.
2. **Malformed input gives `unexpected EOF`, which does *not* match `errors.Is(err, io.EOF)`.**
   The empty-body branch therefore cannot swallow a truncated body and report it as "empty" — a
   confusion that would have sent every client hunting the wrong bug.
3. **A trailing newline does not trip `More()`.** Every real client ends its body with one, so a
   trailing-data check built on `More()` produces no false rejections.

## Non-goals

- **`Content-Type` checking.** Real clients omit the header or send `text/plain` constantly.
  Rejecting on it creates a class of 415s nobody predicts. If the body parses, it is accepted.
- **A body size limit.** `WithMaxBodySize` already enforces one at the transport, before a `Ctx`
  exists. A second limit here would be two sources of truth for one rule.
- **`binding.Query` or `binding.Header`.** `c.Query` and `c.Header` exist, and binding them
  without struct tags would be contrived.
- **Struct tags for validation, and any third-party validator.** See D1.
- **Zero allocations.** See D5. This package is the one place in rice that buys ergonomics with
  allocations, and it says so.

## Decisions

### D1 — Generics for the type, a method for the rules

`func JSON[T any](c *rice.Ctx) (T, error)`. No struct tags beyond `encoding/json`'s own, and no
dependency outside the standard library.

Rejected: **reflection plus `validate:"required,email"` tags**, which is what every other Go
framework does and what users expect. It loses on principle 2 — the rule stops being readable
from the call site, and a reader must consult a third-party library's documentation to learn what
`email` means. Rejected: **a fully reflection-free decoder the user writes per type**. It is the
purest answer to ADR-0006 and it is so verbose that nobody would import it, and a helper nobody
imports should not exist.

`encoding/json` uses reflection internally. That is the standard library's business, inside a
call this package makes; it is not reflection in rice's own code, and D7 states where the line
actually is.

### D2 — `Validate()` is optional, and is detected through a pointer

After decoding, the package tries `any(&out).(interface{ Validate() error })`.

The assertion goes through `&out` rather than `out` on purpose. A `Validate()` declared on the
value receiver is in the method set of both `T` and `*T`; one declared on the pointer receiver is
in the method set of `*T` only. Asserting on the value would silently skip the pointer-receiver
case, and "I wrote `Validate` and it never ran" is exactly the kind of silent failure this project
refuses to ship.

A type without `Validate()` decodes and returns. No panic, no warning, no registration step.

### D3 — Strict means two checks, not one

`DisallowUnknownFields()` rejects a misspelled field, so `{"emial": ...}` is a 400 rather than a
silently zero-valued `Email`.

That alone is a half-kept promise: `Decode` reads one value and stops, so `{"a":1}{"b":2}` would
pass. After a successful decode the package checks `dec.More()` and rejects anything left over.
The probe confirms a trailing newline does not trip it.

### D4 — Three error branches, and only one of them shows the cause

| Case | Status | Body the client sees | `HTTPError.Err` (logs only) |
| --- | --- | --- | --- |
| Empty body | 400 | `empty request body` | `io.EOF` |
| Malformed JSON, unknown field, trailing data | 400 | `invalid JSON body` | the real `encoding/json` error |
| `Validate()` returned an error | 422 | that error's own message | the same error |

The first two use a fixed message because `encoding/json`'s text is **shaped by the caller's
input** — `invalid character 'x' looking for beginning of value` leaks parser detail, and M5
already holds a test that a cause string never reaches the response body. The third is safe *by
construction*: the message was written by the handler's author, not produced from input.

Empty is split from malformed because it is the most common client mistake, and answering
`invalid JSON body` to a request that carried no body sends people looking in the wrong place.

**`Validate()`'s message is sent to the client verbatim, and the package cannot check that.** An
author who writes `fmt.Errorf("checking user in db: %w", err)` leaks their internals. This is
documented, not detected, and the documentation says so plainly rather than implying a guarantee.

### D5 — 422 for a failed `Validate()`, and the author may override it

400 means "I could not parse this"; 422 means "I parsed it and it breaks a rule". A client can
tell the two apart without reading the body.

The override needs no option: if `Validate()` returns an `*rice.HTTPError`, the package passes it
through unchanged. An author who wants 409 Conflict or 403 for a particular rule returns one, and
gets it.

422 is defined by RFC 4918 rather than RFC 9110, so some intermediaries treat it as unfamiliar.
That is the known cost of this decision and it belongs in ADR-0011's consequences.

### D6 — Pinned allocations, not a zero budget

`DisallowUnknownFields` exists only on `json.Decoder`, so the package allocates a
`bytes.Reader` and a `json.Decoder` per call, plus the decoded value and whatever `encoding/json`
charges for `T`'s shape. `json.Decoder` has no `Reset`, so it cannot be pooled.

The budget is therefore a **measured number, pinned exactly** for a named fixture, the way
`c.JSON`'s is — so an allocation that gets cheaper fails the test and the documented figure is
corrected rather than left stale. The escape hatch is `c.Body()` and a hand-written decode, and
the documentation names it.

### D7 — The package is `binding/`, beside `middleware/`, and ADR-0006's line has not moved

Same module, importing `rice`, one-way dependency, with a `doc.go` that explains why it is a
separate package — the shape `middleware/doc.go` already established.

ADR-0006 says "Package `rice` imports neither `reflect` nor `encoding/json` anywhere in routing,
context handling or middleware". Package `binding` is not package `rice`. The line is where it
was, and ADR-0011 must say so explicitly, because a reader who finds `encoding/json` in this
repository's import graph will otherwise conclude the rule was quietly relaxed.

## Components

| File | Responsibility |
| --- | --- |
| `binding/binding.go` | `JSON[T]`, the three error branches, the `Validate` assertion |
| `binding/doc.go` | why this package is separate, and the leak warning from D4 |
| `binding/binding_test.go` | behaviour |
| `binding/alloc_test.go` | the pinned budget |

## Public API added

```go
package binding

func JSON[T any](c *rice.Ctx) (T, error)
```

One exported function. No options, no constructor, no registry.

## Allocation budget

One row, in a new *Opt-in packages* section of
[05-performance-model.md](../../05-performance-model.md) — separate from the `Ctx` table, because
nothing here is on the hot path and mixing them would imply this cost is paid by everyone.

| Operation | Budget | Enforced by |
| --- | --- | --- |
| `binding.JSON` with the `CreateUser` fixture | measured, pinned exactly | `TestAllocBudgetJSONBinding` |

The number is filled in when it is measured. The implementation must not guess it, and the
documentation must name the fixture it was measured with, since the figure depends on `T`'s shape.

## Testing

**The two that would fail silently:**

- `Validate()` on a **value receiver** runs.
- `Validate()` on a **pointer receiver** runs. These are separate tests because one assertion
  style passes the first and skips the second.

**The rest:**

- a type with no `Validate()` decodes and returns, with no panic
- `Validate()` returning an `*rice.HTTPError` passes through with its own status
- an unknown field is a 400
- trailing data (`{}{}`) is a 400, and a trailing newline is not
- an empty body, and a whitespace-only body, are both 400 `empty request body`
- malformed JSON is a 400 `invalid JSON body`, **and the cause string does not appear in the
  response body** — the M5 test, applied here
- `Validate()`'s error is a 422 carrying its own message
- one end-to-end test through a real app, proving the error reaches rice's funnel and is written
  by it, rather than merely being returned

## Documentation

- **ADR-0011 (new)** — "Binding is generic and validation is a method". Records the shape, names
  the two rejected alternatives with why each lost, states the 422 cost, and states explicitly
  that ADR-0006's boundary is unchanged.
- **`binding/doc.go`** — modelled on `middleware/doc.go`, including D4's leak warning.
- **`02-architecture.md`** — `binding/` in the layout.
- **`04-roadmap.md`** — remove "Request binding and validation, as an opt-in side package" from
  *Explicitly deferred*, and add an entry under *Done after M8*.
- **`05-performance-model.md`** — the new *Opt-in packages* section and its one row.
- **`progress.md`** — one entry when the work lands.

## Exit criteria

1. `make test` green, including the two receiver-shape tests.
2. The budget test pins an exact measured number, and `05-performance-model.md` carries that
   number and the fixture it was measured with.
3. ADR-0011 written and indexed, stating that ADR-0006's boundary did not move.
4. "Request binding and validation" no longer appears on the deferred list.

## Open questions

None. The decisions above were each taken deliberately; the one acknowledged risk — an author
leaking internals through `Validate()`'s message — is documented rather than solved, because no
mechanism available to this package can distinguish an author's message from a wrapped internal
error.
