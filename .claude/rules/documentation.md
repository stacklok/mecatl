---
matlatl: orphan-intentional
paths:
  - "**/*.md"
  - "**/*.mdx"
---
# Documentation ownership and lifecycle

Before writing, identify the reader's task and the existing owning page. Update
that page in place; link to it instead of repeating the same behavior in several
documents. Follow the documentation change review in docs/development-process.md.

Living guides describe verified, implemented behavior. Keep proposed behavior in
acceptance plans, durable rationale in ADRs, and implementation history in PRs or
Git. A merged plan does not prove a feature shipped. Legitimate release notes and
public API changelogs remain release artifacts, not substitutes for current docs.
Track actionable work in issues and PRs; keep current operator limitations in
their owning guides, not a parallel feature-status ledger.

Replace stale explanations rather than appending corrections. Delete obsolete or
redundant material; migrate only verified, non-obvious knowledge missing from its
owner. Do not create catch-all implementation notes, per-issue status narratives,
or a new page merely because a change needs documenting.

Agent instructions contain actionable corrections, not subsystem encyclopedias.
Do not turn every bug fix into an always-loaded rule. Link/citation checks prove
reachability, not behavioral truth; verify claims against code and tests.
