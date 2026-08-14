# ADR 0108 — Session discovery uses durable kind metadata and an authoritative transcript

- Status: Accepted
- Date: 2026-08-14
- Scope: session creation metadata, SessionStore snapshots/meta projections, HarnessService session inventory/transcript surfaces, and mecatui session discovery/continuation

## Context

A mecatl session is one durable conversation aggregate, but several trusted producers create
sessions with different operator semantics: interactive main chats, autonomous scheduled
fires, Subagent children, Parallel branches, and team members. The engine correctly treats
`session.SessionID` as opaque; ADR 0104 reinforces that contract by using a bounded,
non-reversible physical store name. Mecatui nevertheless infers operator semantics from
`subagent-`, `parallel-`, and `team-` prefixes. That duplicates an engine/agent convention
in the client, cannot describe relationships, and makes custom child prefixes look like
continuable main chats.

Two persistence planes answer different questions. A SessionStore snapshot is the
authoritative model-visible conversation used to continue a session. EventLog is an
append-only activity/audit timeline: append failure warns rather than aborting a run, an
unknown id reads as an empty stream, and EOF carries no completeness attestation. Therefore
EventLog replay cannot prove that the human sees all context the model will receive. The
current `/sessions` path can attach to the snapshot after event replay fails, creating that
asymmetric view.

Continuation also is not one operation. A main chat can receive another prompt through the
run-entry recovery funnel; a scheduled session may be driven only by the scheduler purpose;
a Subagent continues only through its `resume:` contract; a Parallel branch is inspect-only;
and team members are re-driven only by their Supervisor.

## Decision

### 1. Persist a closed session-kind and relationship schema

Every newly-created session carries one inert, provider-neutral `SessionKind`:

| Kind | Trusted producer | Required relationship | Direct chat continuation |
|---|---|---|---|
| `main` | public create/fork/carryover surfaces | none | yes |
| `scheduled` | scheduler fire creation | schedule name; origin session when one exists | no |
| `subagent` | `SubagentTool` | parent session id + call id | no; `Subagent(resume:)` only |
| `parallel_branch` | `ParallelTool` | parent session id + call id + branch index | no |
| `team_member` | `Supervisor` | team id + member name; parent session id when tool-driven | no |
| `unknown` | legacy/custom data only | none | no automatic grant |

The schema is validated at construction/restore: fields forbidden for a kind are empty;
required fields must be present. Peer forks, reasoning-effort forks, and model carryovers
remain `main` and deliberately gain no lineage field, preserving ADR 0065's scope cut.
Public create requests cannot stamp or override child/scheduled metadata; only trusted
server/composition and engine/agent producers do so.

Stores round-trip these labels and expose them through metadata listing. The labels do not
decide ownership, but **server-owned kind is an input to the direct-run safety gate**:
explicit delegation and scheduled kinds are denied on the public chat path regardless of ID
spelling. The historical prefix guard remains as defense in depth for legacy snapshots with
missing/conflicting metadata. Missing, invalid, or conflicting metadata never grants a
capability.

### 2. Keep SessionID opaque; centralize legacy compatibility

No client classifies a session from its ID. Exactly one server helper projects legacy
snapshots that lack kind metadata from the historical built-in prefixes. Explicit valid
metadata controls display classification; for safety, either an explicit non-main kind or a
historical child/scheduled prefix denies public chat continuation. The compatibility helper
never rewrites on read. A false-positive legacy prefix is an accepted fail-closed cost; the
operator can inspect but not directly continue that row.

### 3. Separate authoritative transcript from optional activity replay

Add a public snapshot-derived transcript surface. It performs one ownership-checked
`SessionStore.Load` and streams/projects that coherent aggregate's `Conversation.Messages`
plus session metadata already carried by the loaded snapshot. No second metadata read is
claimed to be the same revision. This transcript is the exact human-displayable conversation
the next model request will use: user, assistant, and tool messages after compaction,
excluding provider-private opaque replay blobs that are not human content. Unknown or
corrupt snapshots return an error; empty idle sessions are explicitly complete with zero
messages. Transcript reads are pure: they never resolve an Environment, rebuild an engine,
acquire a lease, or mutate/persist the session.

Mecatui uses this authoritative transcript for both continuation and inspection. The
existing EventLog stream remains an optional **activity replay** (tool/delegation/status
history) whose availability and completeness are reported separately and never gate or
justify chat continuation. EOF on EventLog is never labelled a complete transcript.

### 4. Expose capabilities and closed reason codes

Inventory reports independent capability fields for authoritative transcript availability,
optional activity replay, public-chat continuation, and inspection. Disabled actions carry
a closed reason code (`inspect_only_kind`, `awaiting_approval`, `active_elsewhere`,
`transcript_unavailable`, `environment_unavailable`, or `unknown`), not prose clients parse.
Caller-visible details are sanitized. Foreign, absent, ownerless-under-enforcement, and
pruned sessions remain one indistinguishable NotFound class and never appear as special rows.

Rows are advisory. Static adoption validates ownership, kind, transcript availability, and
metadata that requires no runtime attachment. It never calls `EnvironmentResolver`, rebuilds
an engine, or reserves a lease while an idle TUI is open. The first prompt resolves the
persisted profile/model/EnvironmentRef and revalidates state while acquiring the ordinary
run-entry lock/lease atomically. A conflict leaves the adopted transcript visible/read-only
and offers retry/back; no second session is silently created.

### 5. Split public chat drive from scheduler drive

Public gRPC/HTTP/mecatui prompts enter a chat-purpose `StartRunContent` path that accepts
`main` only (plus the legacy compatibility rules). The scheduler uses a distinct trusted
service/composition entry purpose that accepts `scheduled` only. Neither purpose is a public
request field, so callers cannot claim scheduler authority. Both converge on the same engine
run-entry implementation after the kind gate; the scheduler's existing `sched--` legacy
fallback remains available only through its trusted path.

### 6. Inspection is non-destructive and the vocabulary is explicit

Viewing a child or scheduled transcript is an overlay over the current main chat. Closing it
restores the previous chat and never rebinds the prompt target.

Mecatui calls interactive aggregates **Chats**, autonomous schedule aggregates **Scheduled
runs**, and delegated aggregates **Child runs**. “Continue” appends a prompt to a chat;
“Inspect” reads a transcript without changing the prompt target; “Fork” is ADR 0065's peer
session; “Resume Subagent” remains `Subagent(resume:)`.

### 7. Listing has a cursor-bounded response where supported

The public list accepts a bounded page size and opaque cursor, ordered by
`(modified_at DESC, session_id ASC)` for deterministic ties. A new optional metadata-pager
port capability lets stores return bounded pages; every in-tree durable store and
remote-driver adapter implements it and shares a conformance suite. Adapters may scan their
backend to form a page in v1—the guarantee is bounded response/memory at the service/client
boundary, not constant-time storage traversal. Pages use keyset continuation and are
best-effort under concurrent saves: new/updated sessions may move to an earlier page, but a
cursor never causes an out-of-bounds response or ownership leak. A store without the
capability reports listing unavailable rather than returning an unbounded response.
Ownership filtering occurs before page formation and counts. A truly bounded indexed scan,
with index rebuild/cleanup and ADR-0027 resource inventory, is a later backend optimization.

### 8. Display handles are representations, not alternate IDs

The full opaque, valid-UTF-8 ID is copied byte-exact through the clipboard. Terminal
surfaces render a safe reversible quoted form for the full ID. Compact row/header handles
are a display-only digest of the full ID, expanded on collision within the visible page;
they are never accepted by server APIs. A persisted invalid-UTF-8 ID is corrupt and cannot
cross the protobuf string surface: it returns the same safe typed failure as a corrupt
snapshot rather than being repaired into a different ID. Exit emits one documented, quoted
single-line handoff on stderr after the alternate screen is restored.

## Consequences

Session snapshots, optional metadata-pager capabilities, public summaries, and transcript
RPCs gain additive fields/surfaces. Existing `session.New` and required `SessionStore`
method signatures remain unchanged; producers stamp validated labels through additive,
intention-revealing methods/options. Engine compatibility changes are `Added` only—never
`Changed` or `Removed`—and follow the API gate/CHANGELOG process. Every store/source adapter
must round-trip the same schema; it cannot live only in mecatui or JSONL.

Snapshot transcript projection is deliberately less visually rich than EventLog activity
replay: it shows the exact current conversation, not every transient event or pre-compaction
message. That is the correct continuation contract because it matches model-visible context.
Clients may offer the optional activity timeline separately.

Cursor-bounded pagination adds store/driver work and bounds responses/client memory while
allowing a v1 adapter to scan its backend. Unsupported external stores lose inventory until
they implement the optional pager; direct Get/Start by exact ID remains unchanged. A truly
indexed bounded scan is deferred rather than implied by the wire cursor.

Legacy snapshots remain visible through one server compatibility projection. Custom hosts
must stamp kind metadata for rich discovery; `unknown` remains inspect-only/fail-closed.

## See also

- [ADR 0104](./0104-session-family-physical-naming.md) — SessionID is opaque and its physical store name is non-reversible.
- [ADR 0038](./0038-event-sourced-rehydration.md) — snapshot rehydration versus event-log reconstruction.
- [ADR 0065](./0065-conversation-fork.md) — peer fork versus same-session continuation.
- [ADR 0015](./0015-background-subagents.md) — delegated child handles and lifecycle.
- [ADR 0027](./0027-cloud-native.md) — snapshot/log resource and rehydration contracts.
- [`docs/architecture.md`](../architecture.md) — living session/store/wire behavior.
- [`docs/tui.md`](../tui.md) — living mecatui interaction behavior.
