# ADR-0007 — No trailing-slash redirection, no case-insensitive matching

Status: Accepted
Date: 2026-09-11

## Context

[ADR-0004](0004-radix-tree-router.md) left one question open for M3: whether the router
should support trailing-slash redirection (a request for `/users/` reaching the handler
registered at `/users`) and case-insensitive fallback matching (`/Users` reaching `/users`).
Both are defaults in httprouter, and through it in Gin. ADR-0004 called them "conveniences
that add branches to the lookup path" and asked for the decision to be made with a
measurement.

The measurement turned out not to be the thing that decides it, and that is worth stating
before the decision rather than after. Both features are *fallbacks*: they run only after
the ordinary walk has already failed. A request that matches a registered pattern never
reaches either branch, so neither costs a matching request anything, and the only requests
that pay are the ones already on their way to a 404. There is no hot-path trade-off for a
benchmark to arbitrate. The cost ADR-0004 anticipated is not where it expected it to be.

What is left is scope, and M3 is the milestone with the least of it to spare.

## Decision

rice supports neither. A request whose path differs from a registered pattern only by a
trailing slash, or only by case, produces `ErrNotFound` and a 404 through the same funnel as
any other miss. `/users` and `/users/` are two distinct patterns; an application that needs
both registers both.

Both remain available later, and only as opt-in behaviour rather than as a default. The
`Option` mechanism that would carry them arrives in M7.

## Alternatives

**Redirect on a trailing-slash mismatch.** httprouter's `RedirectTrailingSlash`: on a miss,
retry the lookup with the slash added or removed, and on a hit answer with a redirect rather
than the handler. Genuinely useful, and the behaviour most users arriving from another
framework expect. Rejected on scope. It is not one decision but three: 301 or 308 (301 lets
a client legally turn a POST into a GET, 308 preserves the method but is handled worse by old
clients), whether a redirect fires at all for a request that already carries a body, and what
the `Location` header contains when the request arrived through a proxy. Each has a
defensible answer, none is obvious, and all three widen the semantics M3 has to test
exhaustively while contributing nothing to the measurement M3 exists to produce — a
measurement taken against prefix splitting, parameter capture, wildcards, backtracking and
conflict detection, which ADR-0004 itself calls the hardest code in the project.

**Match the trailing-slash variant directly, with no redirect.** Cheaper than redirecting and
simpler to implement: treat `/users/` as `/users` and dispatch. Rejected for a reason that is
not cost either. It silently makes two distinct URLs one resource, which is a
canonicalisation decision with consequences for caching and for logging, not a routing
convenience. It is also the harder of the two to withdraw, because route tables written under
it would change meaning if it were ever removed.

**Case-insensitive fallback matching.** httprouter's `RedirectFixedPath`, which also fixes
case. Rejected on scope and on definition. It needs either a second case-folded tree, which
doubles the memory and the insert path, or a folded retry walk, which duplicates the hardest
function in the project in a second form. And "case-insensitive" is not free to define: ASCII
only, or full Unicode folding, with the dotless-i problem that follows. The failure it
rescues is a client sending a URL nobody published, which a 404 already describes accurately.

**Register both variants automatically at insert time.** No lookup-path change at all, which
makes it the cheapest option on paper. Rejected because it collides with conflict detection:
the auto-registered twin of one pattern can conflict with a pattern the user wrote, and the
resulting duplicate-route panic would name a pattern that appears nowhere in their code. That
is the same failure mode D8 of the M3 design rejected path normalisation for, and it is worse
here because it is invisible at the call site.

## Consequences

**Makes easy:** the lookup path stays exactly as ADR-0004 described it. No retry after a
failed walk, no second tree, no folding, and nothing on the miss path but the error funnel.
The matching rules remain fully described by the static > parameter > wildcard priority, so
the answer to "why did this request 404" is always readable from the route table.

**Makes hard:** an application that wants `/users` and `/users/` to behave identically must
register both patterns, or normalise the path in front of rice — which a reverse proxy does
well and is arguably where it belongs. Anyone migrating from httprouter, Gin or Echo loses a
default they may not know they were relying on, and finds out through a 404.

**Forecloses nothing.** Both features are additive, both live on the miss path, and neither
changes the outcome of a request that already matches. Either can be added later as an
`Option` without altering the meaning of a single route table written before it, which is
precisely why deferring them costs nothing but the convenience itself.

**Decided without the measurement ADR-0004 asked for.** Recorded plainly rather than left to
be noticed: ADR-0004 said "decide with a measurement", and no measurement was taken. Not
because measuring was inconvenient, but because on inspection there was nothing for a
measurement to decide — the branches sit entirely on the miss path, so their cost to a
matching request is zero by construction and no benchmark would have changed the answer.
ADR-0004 anticipated a hot-path cost trade-off that does not exist. The decision rests on
scope, and saying so is more useful than producing a number that would have decided nothing.
