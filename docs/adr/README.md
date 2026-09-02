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
