# ADR 0336 — Draft-aware session inventory

- Status: Proposed
- Date: 2026-09-14
- Scope: session discovery metadata, startup latest-resume selection, and mecatui session inventory
- Supersedes: ADR 0217 Decision 4 only for automatic latest-selection eligibility and the additional inventory projection; all other ADR 0217 decisions remain in force
- Superseded by: None

## Context

A session is durably created before its first user prompt and remains stored after Close. This
acknowledges a resource that can survive restart and retain its placement and model selection.
It also leaves valid empty main sessions in inventory. They can win `mecatui --resume-latest`
and clutter `/sessions` as ordinary untitled chats. Delayed persistence or destructive Close
would break acknowledged-resource and exact-resume semantics.

## Decision

Add one content-free activity projection for every valid persisted conversation:

| Value | Meaning |
|---|---|
| `draft` | No genuine user prompt exists. |
| `active` | At least one genuine user prompt exists. |
| `unknown` | Discovery metadata cannot prove either result. |

The classifier uses `engine/session/title.go` (`IsGenuineUserPrompt`), so a
harness-authored compaction summary cannot activate a draft. It returns draft or active after a
successful inspection. Unknown is reserved for missing, corrupt, unavailable, or unproved
metadata; title, turns, lifecycle state, and ID spelling are not evidence.

Normal Create/Save derives the scalar into cheap discovery metadata. It is carried by JSONL and
Redis metadata rows, and alongside an opaque remote-driver snapshot; it is not duplicated in the
canonical snapshot. Missing legacy projection stays unknown; normal later writes establish it.
No backfill is introduced in this increment.

Mecatui keeps its existing complete inventory fetch and locally groups rows: Chats shows active
and unknown main sessions, while Drafts shows draft main sessions as `New — no messages`.
Scheduled, child, other, and storage-health views stay unchanged. Do not add server-side
kind/activity filters, new cursor semantics, secondary indexes, or migration merely for this
view.

When `session_activity_inventory` is advertised through
`GetCompatibilityInfoResponse.features`, `--resume-latest` selects active eligible main sessions
only. It still rejects a selected row whose authoritative transcript is empty. Exact-ID resume
remains activity-agnostic. Without that feature, newer mecatui keeps the historical mixed Chats
view and only its narrow empty-transcript latest-resume guard; older clients retain historical
behavior.

Listing, Close, failed resume, and stale observations never delete drafts. Draft actions retain
existing server-side authorization and revalidation.

## Consequences

The engine gains an Added/minor activity classifier and discovery-metadata field; public and
driver summary fields plus the driver Save/Create scalar are additive. The exact interfaces,
compatibility matrix, and verification live in the [acceptance plan](../acceptance/draft-aware-session-inventory.md).

Inventory remains owner-authorized and content-free. Activity grants neither continuation nor
deletion authority. This preserves ADR 0217's immediate-persistence, non-destructive Close,
authoritative transcript, and session-kind rules. Draft retention/deletion is separate work with
its own serialization, liveness, and conditional-deletion decision.

## See also

- [ADR 0217](0217-session-discovery-continuation.md) — session taxonomy, transcripts, and continuation
- [ADR 0038](0038-event-sourced-rehydration.md) — snapshot authority versus event-log replay
- [ADR 0248](0248-sdk-compatibility-and-error-contract.md) — additive feature negotiation
- [ADR 0291](0291-server-owned-session-placement.md) — path-free public session projections
