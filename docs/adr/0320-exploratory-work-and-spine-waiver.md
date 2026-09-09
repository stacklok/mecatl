# ADR 0320 — Exploratory work and an explicit human waiver narrow the spine's default

- Status: Accepted
- Date: 2026-09-09
- Scope: when the acceptance-plan spine (ADR 0306) applies at all
- Supersedes: ADR 0306 where it states, without qualification, that all substantive
  interface-bearing work uses the spine

## Context

ADR 0306 (and `AGENTS.md`'s Workflow section, which restates it) declares unconditionally
that substantive interface-bearing work uses the human-reviewed acceptance-plan spine. In
practice this instruction binds the agent even when it was never asked for: a plain request
to build or try something gets read as requiring a Plan/Interface PR, a merged approved
baseline, and isolated TDD orchestration before a line of code may be written, regardless of
whether the requester actually wants that process for this piece of work.

Two real needs fall outside what the spine was built for. First, evaluating whether an idea
is worth building at all: a human may want a working local prototype to decide if a feature
earns its interface commitments, before paying for a Plan/Interface PR and a full
implementation PR. Forcing the plan first inverts the actual decision order — writing the
contract before knowing whether the thing behind it is any good. Second, a human directing
the session already holds ultimate authority in this workflow (`AGENTS.md`: "Humans merge
every PR; agents never merge to `main`") independent of which process produced the diff;
nothing currently lets that same human trade the plan/interface paperwork for taking on
review of the resulting diff directly, for a change they are willing to own end to end.

Neither need is served by weakening the spine's actual guarantee — that nothing merges to
`main` without human review — nor by giving the agent discretion to decide a feature "isn't
worth" the process, which was ADR 0295's exact failure mode. Both are served by giving the
*human* that discretion, explicitly, per request.

## Decision

### Exploratory work (spikes)

A human may explicitly request exploratory work: build something to find out if it works or
is worth pursuing, with no commitment yet to ship it. Exploratory work:

- proceeds without a `docs/acceptance/*.md` plan, without `/to-acceptance-plan`, and without
  `/plan-orchestrate`;
- lives on a disposable branch or worktree (`spike/<slug>` is conventional) or uncommitted in
  the working tree, at the requester's preference;
- is never merged into `main` and never becomes the subject of an Implementation PR as-is. If
  the human decides afterward to ship it, the feature re-enters the spine from its normal
  entry point (`/to-acceptance-plan`, Split or Combined as eligibility allows) — the spike may
  inform that plan's authoring, but the plan and its implementation are produced through the
  spine, not by relabeling the spike's commits;
- still respects every invariant in "Things That Will Bite You" (layering, the permission
  model, UTF-8 repair, and the rest) — the waiver here is procedural, never architectural.

This request must be explicit and per-task; the agent does not infer that a request is
exploratory from its own judgment about whether the feature is worth building.

### Explicit human waiver

Separately, the human directing a session may explicitly waive the spine for a specific piece
of work by stating so directly (for example: "skip the spine for this," "I'll review this one
directly, no acceptance plan"). A waiver:

- is scoped to the request it names; it is not a standing session default unless the human
  says so explicitly ("skip the spine for the rest of this session" is valid and binds until
  the human says otherwise or the session ends);
- lifts only the plan/interface PR ceremony (`docs/acceptance/*.md`, `/to-acceptance-plan`,
  `/plan-orchestrate`'s isolation and gating machinery) — it changes nothing else in
  `AGENTS.md`: not the layering rules, not the invariants in "Things That Will Bite You," not
  "humans merge every PR," not the verification commands (`task lint && task test`);
- does not retroactively apply to work already merged, and does not authorize the agent to
  merge to `main` — that authority never transfers;
- may be exercised for any reason the human gives, or none; the agent does not need to agree
  the feature is small enough, and does not push back once the waiver is stated.

An agent that continues to insist on the spine after an explicit waiver, or that treats an
exploratory request as though it must still produce a plan, is not following this ADR.

## Consequences

- The agent no longer reads `AGENTS.md`'s spine sentence as unconditionally binding on every
  substantive request; the default (spine applies) holds unless the human explicitly names
  one of the two carve-outs above.
- Nothing about the spine's actual guarantee changes: a plan still cannot merge to `main`
  without a human doing so, and neither carve-out lets an agent merge unreviewed work.
- This reintroduces some of the discretion ADR 0295 removed — but the discretion sits with
  the human requesting the work, not with the agent deciding what qualifies as "substantive,"
  which was ADR 0295's actual complaint.
- A spike that quietly becomes the shipped implementation without ever going back through the
  spine is a risk this ADR does not eliminate on its own; it relies on the human choosing to
  route real shipping work back through `/to-acceptance-plan` rather than merging spike
  commits directly. `main` branch protection (none configured today, per ADR 0319) would be
  the structural backstop if this becomes a problem in practice.

## See also

- [ADR 0306](./0306-human-reviewed-development-contracts.md) — the spine this ADR narrows.
- [ADR 0319](./0319-merge-is-plan-approval.md) — the neighboring simplification to the
  spine's approval mechanism.
- [Development process](../development-process.md) — the living workflow.
