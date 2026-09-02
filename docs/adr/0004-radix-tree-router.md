# ADR-0004 — Per-method radix tree router

Status: Accepted
Date: 2026-09-02

## Context

Routing is on the hot path of every request. It has to support static segments, named
parameters (`/users/:id`) and a catch-all (`/static/*path`), and it has to resolve them
without allocating. It also has to give a predictable answer when two patterns could both
match.

## Decision

One radix tree per HTTP method, held in a fixed-size array indexed by a method constant with
a map fallback for uncommon verbs. `Lookup` fills a caller-supplied `*Params` rather than
returning a new slice. Match priority is fixed and total: **static > parameter > wildcard**.
Conflicting registrations are rejected at registration time with an error naming both
patterns.

Milestone M2 ships a deliberately naive map-based router first, so that M3's tree can be
compared against a real measured baseline rather than against an assumption.

## Alternatives

**Hash map of full paths.** Constant-time, trivial to write and to reason about. Rejected
as the final design because it cannot express parameters at all without a separate fallback
mechanism, and hashing the path means walking the whole string where a tree often decides on
the first few bytes. Kept as M2 precisely to produce the comparison number.

**Linear scan of compiled regular expressions.** Maximum pattern expressiveness. Rejected
outright: cost grows linearly with route count, regexp matching allocates, and it makes the
matching rules unpredictable for the reader.

**Trie with one node per path segment.** Simpler to implement than a radix tree, since there
is no prefix splitting. Rejected because it costs a pointer chase per segment and a node per
segment, where a radix tree collapses shared prefixes into single nodes. The prefix-splitting
logic is genuinely the hardest code in the project, which is an argument *for* writing it
given the learning goal.

**One tree with method as the first segment.** Fewer trees, uniform structure. Rejected
because it puts a string comparison of the verb on the hot path where an array index would
do, and it makes "405 Method Not Allowed" harder to answer than "404".

## Consequences

**Makes easy:** zero-allocation lookup; parameters as byte views into fasthttp's buffer;
method dispatch as an array index; unambiguous match resolution with no runtime tie-breaking.

**Makes hard:** insertion with common-prefix splitting is intricate and needs a thorough
table-driven test suite covering splits, priority, trailing slashes and deep nesting.
Conflict detection has to be written carefully or it will reject valid route sets.

**Forecloses:** regular-expression routes, and host-based routing (which would need another
dimension above the method array).

**Open question for M3:** whether to support trailing-slash redirection and case-insensitive
fallback matching. Both are conveniences that add branches to the lookup path. Decide with a
measurement, and record the outcome here as a follow-up ADR.
