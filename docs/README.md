# rice-http Design Docs

`rice` is a small HTTP framework built on top of [fasthttp](https://github.com/valyala/fasthttp).
It exists to be *understood*, not to win benchmarks — though it takes allocation counts
seriously, because that is one of the two things this project is meant to teach.

Module path: `github.com/vietpham102301/rice-http`
Package name: `rice`

## Learning goals

1. **Framework architecture and developer experience.** How a router, a middleware
   chain, a request context and a server lifecycle fit together, and where the seams
   between them belong.
2. **Zero-allocation performance.** How to design an API whose hot path allocates
   nothing, how to prove it, and how to keep it that way.

Everything in this repository is subordinate to those two goals. When a choice makes the
system faster but harder to explain, the explanation wins and the trade-off gets an ADR.

## How to read these docs

Read them in order the first time. After that, use them as reference.

| Doc | What it answers |
| --- | --- |
| [00-overview.md](00-overview.md) | What is rice, what is it not, who is it for |
| [01-design-principles.md](01-design-principles.md) | The rules every decision is judged against |
| [02-architecture.md](02-architecture.md) | Package layout, layers, request lifecycle |
| [03-core-concepts.md](03-core-concepts.md) | App, Ctx, Handler, Middleware, Router, Group, Error |
| [04-roadmap.md](04-roadmap.md) | Milestones M0 to M8 with exit criteria |
| [05-performance-model.md](05-performance-model.md) | Allocation budgets and how they are enforced |
| [06-glossary.md](06-glossary.md) | Terms used with a specific meaning here |
| [07-retrospective.md](07-retrospective.md) | What the project learned, what surprised it, what it would change |
| [adr/](adr/) | One file per irreversible-ish decision, with the reasoning |
| [progress.md](progress.md) | Dated journal: what was built, what was learned |
| [milestones/](milestones/) | Per-milestone design notes and retrospectives |

## Document conventions

- **ADRs are append-only.** A decision is never edited to say something else. It is
  superseded by a new ADR that links back to it. The wrong turns are part of the value.
- **The progress journal is append-only too.** Newest entry at the top. Every entry is
  dated and names the milestone it belongs to.
- **Numbered docs are living documents.** They describe the system as currently designed.
  When they change materially, the change is noted in `progress.md` with the reason.
- Code that does not exist yet is written in the future tense or marked `PLANNED`.
  Nothing in these docs should imply working code until the corresponding milestone
  is marked done in the roadmap.
