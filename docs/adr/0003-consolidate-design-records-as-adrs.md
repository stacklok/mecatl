# ADR 0003 — Consolidate design records as ADRs

- Status: Accepted
- Date: 2026-06-17
- Scope: the `docs/design/*` design records and the `docs/adr/` scheme.
- Amends: [ADR 0002](./0002-documentation-lifecycle.md) (the design-record half).

## Context

ADR 0002 set up two parallel homes for "the why": the existing `docs/design/*`
spikes (frozen design records, banner-tagged, status-stripped) **and** `docs/adr/`
for new decisions. In practice that split is confusing — a reader hunting for the
rationale of a feature has to know whether it predates or postdates the ADR scheme,
and "design record vs ADR" is a distinction without a useful difference (both are
frozen, point-in-time records of *why*). Maintaining two numbering/location schemes
for the same kind of artifact is overhead with no payoff.

## Decision

**One scheme: all decision/design records are numbered ADRs under `docs/adr/`.**

- The 26 former `docs/design/*.md` records were moved to `docs/adr/0004–0029-*.md`
  (content preserved; every inbound link, citation, and code-comment reference
  updated). [`docs/adr/README.md`](./README.md) is the index.
- `docs/design/` keeps only the **non-records**: the live status tracker
  ([Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md)) and the dense
  living implementation reference
  ([IMPLEMENTATION-NOTES.md](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md)), plus the
  documentation/citation conventions in [its README](../design/README.md).
- The single-source-of-truth split from ADR 0002 **still holds**: current behaviour
  in `docs/architecture.md`, mutable status in `PRODUCTION-READINESS.md`, the *why*
  in ADRs.
- New decisions are new ADRs (copy [`template.md`](./template.md)); superseding is a
  new ADR with `Supersedes:` / `Superseded by:` pointers, never an in-place rewrite.

This **amends ADR 0002**: its "design records are a separate `docs/design/*` tier,
banner-tagged, with a no-`Status:` lint gate" mechanism is retired. ADRs legitimately
carry a `Status:` line (Accepted / Superseded), so the no-`Status:` lifecycle gate is
replaced by an ADR-shape check (every ADR has a `Status:` and `Date:`). The citation
lint now also covers `docs/adr/*.md`.

## Consequences

**Positive.** One place to look for any rationale; one numbering scheme; the
"design record vs ADR" ambiguity is gone. ADRs are the cross-tool-recognised standard
shape for this, so the records are now in a form contributors expect.

**Costs.** A one-time high-churn migration: 26 file renames plus a global rewrite of
every doc link, repo-root citation, and code-comment reference (done in this change).
The old `docs/design/` record paths are dead — anything off-repo that linked them must
update. The SCREAMING-CASE names became kebab ADR slugs (e.g. `MULTI-PROVIDER.md` →
`0016-multi-provider.md`).

**Note.** This reverses ADR 0002's explicit "don't relocate the design docs" call.
That call optimised for zero churn; this one optimises for a single coherent scheme,
judged worth the one-time cost.

## See also

- [`docs/adr/README.md`](./README.md) — the ADR index.
- [ADR 0002](./0002-documentation-lifecycle.md) — the lifecycle model this amends.
