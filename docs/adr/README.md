# Architecture Decision Records

One file per decision that had more than one defensible answer.

## Rules

1. **Append-only.** An ADR is never edited to change its decision. If the decision changes,
   write a new ADR, mark the old one `Superseded by ADR-NNNN`, and link both ways.
   Typo fixes and added links are fine.
2. **Name the alternatives.** An ADR that lists only the chosen option is a changelog entry,
   not a decision record. The value is in the comparison.
3. **State the cost.** Every decision buys something and pays for something. The payment
   goes in "Consequences", including the bad ones.
4. **Written when the decision is made,** not reconstructed later. A retroactive ADR says
   so in its status line.

## Format

```
# ADR-NNNN — Title
Status: Proposed | Accepted | Superseded by ADR-NNNN
Date: YYYY-MM-DD

## Context      — what forced a choice
## Decision     — what was chosen, stated flatly
## Alternatives — each option, with why it lost
## Consequences — what this makes easy, what it makes hard, what it forecloses
```

## Index

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-use-fasthttp-as-transport.md) | Use fasthttp as the transport | Accepted |
| [0002](0002-handler-returns-error.md) | Handlers return an error | Accepted |
| [0003](0003-middleware-as-prebuilt-closure-chain.md) | Middleware as a pre-built closure chain | Accepted |
| [0004](0004-radix-tree-router.md) | Per-method radix tree router | Accepted |
| [0005](0005-context-pooling-and-borrow-contract.md) | Pool the context, publish a borrow contract | Accepted |
| [0006](0006-no-reflection-in-core.md) | No reflection in core | Accepted |
| [0007](0007-no-trailing-slash-or-case-insensitive-matching.md) | No trailing-slash redirection, no case-insensitive matching | Accepted |
| [0008](0008-rice-recovers-panics-in-core.md) | rice recovers panics in core | Accepted |
| [0009](0009-shutdown-force-closes-at-deadline.md) | Shutdown force-closes connections at the deadline | Accepted |
| [0010](0010-request-context-cancels-at-force-close.md) | The request context cancels at force-close, not at disconnect | Accepted |
| [0011](0011-binding-is-generic-and-validation-is-a-method.md) | Binding is generic and validation is a method | Accepted |
| [0012](0012-application-middleware-runs-on-route-misses.md) | Application middleware runs on route misses | Accepted |
| [0013](0013-middleware-can-settle-a-request.md) | Middleware can settle a request with `c.HandleError` | Accepted |
| [0014](0014-timeout-is-cooperative.md) | Timeout is cooperative | Accepted |
| [0015](0015-cors-states-a-policy.md) | CORS states a policy; the browser enforces it | Accepted |
| [0016](0016-typed-store-keys.md) | Typed store keys replace Set and Get | Accepted |
| [0017](0017-static-files-wrap-fasthttp-fs.md) | Static files wrap fasthttp.FS behind the error funnel | Accepted |
| [0018](0018-accepts-negotiates-by-q-and-adds-vary.md) | Accepts picks by the client's q, breaks ties by the server's order, and adds Vary | Accepted |
| [0019](0019-streams-run-after-the-handler.md) | Streams run after the handler, on their own context, and stop when Shutdown begins | Accepted |
