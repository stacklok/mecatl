# ADR NNNN — <short decision title>

- Status: Proposed | Accepted | Superseded
- Date: YYYY-MM-DD
- Scope: <what part of the system this decision touches>
- Supersedes: <ADR NNNN, if any>
- Superseded by: <ADR NNNN, if any — added later, the only edit a frozen ADR takes>

## Context

<The forces at play: what problem, what constraints, what was tried or rejected.
Enough that a reader a year from now understands why this was even a question.>

## Decision

<What we decided, stated plainly. Imperative voice. The load-bearing part.>

## Consequences

<What becomes easier, what becomes harder, what we are now committed to. Include the
costs honestly — an ADR with only upsides is a sales pitch, not a record.>

## See also

<Links to the owning living architecture or public documentation, related ADRs,
 and an issue/PR when it supplies relevant context. See the lifecycle convention in
[ADR 0002](./0002-documentation-lifecycle.md) and current
[documentation change review](../development-process.md#documentation-change-review).>

---

<!--
Guidance (delete before committing):
- Create an ADR only for Architectural work that introduces or supersedes a genuinely durable
  public/API compatibility, persistence/data-ownership, security/trust, deployment/operator,
  module/system-boundary, or cross-subsystem-invariant decision. Classification follows the
  decision and blast radius, not diff size; see docs/development-process.md.
- Routine and Bounded work do not get ADRs merely to describe the change. Keep local rationale
  in the issue, PR, or acceptance plan; current behavior in living docs; repeatable procedure
  in skills; temporary execution state in .scratch.
- An ADR is a POINT-IN-TIME record. Once Accepted, you do not edit it to match new
  code. To change the decision, write a NEW ADR that Supersedes this one, and add a
  "Superseded by" line above. The supersede pointer is the only post-acceptance edit.
- **Status/work tracking**: use issues and PRs. Describe current limitations in the
  owning guide; do not add a second feature-status tracker or execution journal.
- Number monotonically: the next NNNN after the highest existing docs/adr/ file.
See docs/adr/0002-documentation-lifecycle.md for the full convention.
-->
