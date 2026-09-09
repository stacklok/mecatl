# ADR 0002 — Documentation lifecycle: living truth, frozen records, one tracker

- Status: Accepted
- Date: 2026-06-17
- Superseded by: ADR 0321 for user-facing usage and reference ownership
- Scope: the whole `docs/` tree — `README.md`, `docs/architecture.md`,
  `docs/usage.md`, `docs/tui.md`, `docs/design/*`, `docs/adr/*`, and `CLAUDE.md`'s
  guidance about them.

## Context

mecatl accumulated ~30 per-feature documents under `docs/design/` (mostly
SCREAMING-CASE "spike" files). Each one tried to be **two things at once**:

1. a **decision record** — "here is what we decided, and why, at a point in time", and
2. **living documentation** — "here is how the system works now".

Those jobs have opposite lifecycles. A decision record should be *immutable* (you
supersede it, you don't edit it). Living documentation must be *continuously
updated* or it lies. Bolting a mutable `Status:` header onto a rationale doc signs
the maintainer up to hand-edit a spike every time the code moves — and that
discipline reliably lapses, because the doc lives far from the code that changed.

The symptom was concrete. A June 2026 audit found `CLOUD-NATIVE.md` frozen at
"Phase 0" when Phase 3c had shipped, `MEMORY-TIERING.md` saying "DESIGN ONLY" when
the tier-0 index was live, and `PRODUCTION-READINESS.md` describing pre-Phase-2
resume behaviour. The same fact also lived in `CLAUDE.md`, `docs/architecture.md`,
**and** a design doc — three copies, three things to update, so they drifted apart.
The status idiom itself was inconsistent across docs (`> Status: **SHIPPED**`,
`DESIGN ONLY`, `**Status:** RETIRED`, `> **Historical …**`, or nothing).

## Decision

**Separate the lifecycles. One source of truth per fact.**

### Every document declares exactly one lifecycle

| Lifecycle | Where | Job | Maintenance |
|---|---|---|---|
| **Living truth** | `README.md`, `docs/architecture.md`, `docs/usage.md`, `docs/tui.md`, [`docs/architecture/mecatl.modelith.md`](../architecture/mecatl.modelith.md) (generated) | how the system works / is operated **now** | kept current; the single source for current behaviour |
| **Status tracker** | `docs/design/PRODUCTION-READINESS.md` | what is shipped / in-progress / deferred | the **only** place mutable status lives |
| **Decision record** | `docs/adr/NNNN-*.md` (new) and `docs/design/*.md` (existing spikes) | *why* a thing is shaped the way it is, captured at a point in time | **frozen**; supersede with a new ADR, never edit to match new code |
| **Research** | `docs/design/*RESEARCH*.md` | point-in-time study | frozen, dated |

### The rules

1. **Design records are frozen.** A `docs/design/*.md` spike captures the rationale
   at the time it was written. Once the feature ships, you do **not** rewrite the
   spike to track the new code — you update `docs/architecture.md` (current
   behaviour) and `PRODUCTION-READINESS.md` (status). Each design record carries a
   one-line **lifecycle banner** as its first content declaring its kind (Design
   record / Historical / Research note) and pointing at the living docs.

2. **Status lives in exactly one place.** `PRODUCTION-READINESS.md` is the single
   status tracker: a table of subsystem → status → design record → architecture
   section. No other design doc carries a `Status:` line. (This is enforced — see
   "Enforcement".)

3. **Current behaviour lives in `architecture.md`.** It is the living reference. A
   design record describes *why*; the architecture guide describes *what is true*.

4. **New design is a new record.** A new decision is a new `docs/adr/` entry (use
   `docs/adr/template.md`). When it ships, fold the current-state description into
   `architecture.md`; the ADR stays as the immutable "why". Superseding an old
   decision means a new ADR with a `Supersedes:` pointer and a `Superseded by:`
   back-pointer on the old one — never an in-place rewrite.

5. **Prefer verifiable truth to prose.** mecatl already gates docs against rot in
   ways most repos can't: the citation lint (`docs/lint`), the `matlatl` link/anchor
   gate, the `modelith` domain model, and generated `llms.txt`. Push facts that can
   rot into things a test checks; keep prose for the *why*.

### Enforcement

`docs/lint` gains a lifecycle gate (`CheckDesignDocLifecycle`) run under `task test`:

- Every `docs/design/*.md` (except the index `README.md`, the tracker
  `PRODUCTION-READINESS.md`, and the dense living reference `IMPLEMENTATION-NOTES.md`)
  must declare a lifecycle banner — one of the recognised kinds — near the top.
- No design doc except `PRODUCTION-READINESS.md` may contain a `Status:` tracker
  line. Status belongs in the tracker. (This is the exact idiom that rotted; making
  it a build failure makes the discipline structural instead of aspirational.)

## Consequences

**Positive.** The drift class we kept hand-fixing becomes impossible to reintroduce
silently. A reader (or agent) lands on a design doc and immediately knows it is a
frozen *why*, with a pointer to the live *what*. Status has one home. New design has
an obvious, low-friction home (an ADR) that doesn't create a future maintenance
liability.

**Costs.** A one-time reframing of the existing spikes (banner added, status moved
to the tracker). Maintainers must learn the reflex "shipped a feature? update
`architecture.md` + the tracker, not the spike." The existing SCREAMING-CASE design
docs are **not** renamed or relocated (that churn buys nothing and breaks
citations); only their *role* changes.

**Not done here (deliberately).** We did not merge `IMPLEMENTATION-NOTES.md` into
`architecture.md`, though they overlap; that is a larger call left for a future ADR.
`IMPLEMENTATION-NOTES.md` stays a living dense-reference companion for now.

## See also

- [`docs/design/README.md`](../design/README.md) — the lifecycle model + banner spec + citation convention.
- [`docs/design/PRODUCTION-READINESS.md`](../design/PRODUCTION-READINESS.md) — the status tracker.
- [`docs/architecture.md`](../architecture.md) — the living architecture reference.
