# Writing Mecatl user documentation

This directory contains the human-facing documentation for [Mecatl](../README.md).
It is rendered by the Docusaurus site in `website/`; content belongs here, not in
that infrastructure directory. See [`website/AGENTS.md`](../website/AGENTS.md) for
site mechanics and supported commands.

## Source material

When authoring pages, start with:

- `docs/architecture/*.md` — behavioral descriptions of each subsystem
- `docs/usage/*.md` — operator flags, configuration, and worked examples
- `docs/adr/*.md` — the *why* behind each design decision

Do not write from `AGENTS.md` or `CLAUDE.md` directly. They are internal contracts
with dense implementation detail. Synthesize from the architecture and usage docs,
which are already written at the right level of abstraction.

## Feature pages

A feature page explains a user journey, not an implementation inventory. Start with
the outcome a user gets, then use this order when it applies:

1. What the feature helps the user do.
2. Availability and prerequisites: deployment, model, trust, storage, or
   configuration gates.
3. A short happy-path usage example.
4. Configuration and permissions.
5. Limits and deployment boundaries.
6. Next steps and related pages.

Keep shared-core behavior on feature pages. Put operational differences between
`mecated`, `mecak8s`, and embedded `mecatui` in the deployment pages and the
[capability matrix](/features/capability-matrix.md). `mecatui` is a terminal skin
over an embedded or connected server, not a separate agent implementation.
`mecak8s` is storage-free locally by design: its durable state is externalized
rather than absent.

Every availability claim needs current evidence from the composition and public
surface that actually ships it: flags or configuration, capability advertisement,
wire handlers, and deployed adapters. A port, proto, or constructor alone does not
prove a feature is available. Describe a current omission as a gap only when there
is evidence it is not an intentional product boundary; otherwise state the
observable behavior without speculating.

Do not put working plans, authoring ledgers, or internal implementation queues in
`user-docs/`. Track them in issues, project planning, or an acceptance plan.
