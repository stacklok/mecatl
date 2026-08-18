# ADR 0226 — Session storage separates current state, indexed metadata, and maintenance

- Status: Proposed
- Date: 2026-08-17
- Scope: SessionStore snapshot durability, jsonlstore physical format, session discovery pagination, retention/migration jobs, and mecatui maintenance/adoption workflows

## Context

The local JSONL session store appends the complete aggregate snapshot after every Save. A
long conversation therefore grows approximately with the sum of every historical full
snapshot, even though Load needs only the newest record. Real per-workspace stores reached
12 GiB/807 snapshots and 3 GiB/157 snapshots. Before the bounded reverse reader landed,
listing allocated tens of gigabytes and took minutes; after it, each file is bounded by its
latest record, but every inventory page still scans every session and the client waits for all
pages.

Older snapshots also predate ADR 0217's durable session kind. They remain correctly
fail-closed as `unknown`, but that leaves an operator able to inspect an old top-level chat
without a safe way to make a writable continuation. Existing retention has no cohesive
plan/apply management surface, embedded mecatui policy is not operator-visible enough, and
physical compaction can easily be confused with destructive cleanup.

The design must preserve the engine's minimal SessionStore port, opaque session IDs, snapshot
fidelity, sidecar audit/event logs, caller ownership, session leases, and the rule that missing
metadata never grants a capability.

## Decision

1. **Current snapshots are bounded and atomically replaced.** The jsonlstore v2 session file
   contains one versioned current sessnap payload plus bounded inventory metadata. Save writes
   a same-directory temporary file, syncs it, atomically renames it, and syncs the directory
   where supported. The existing tool and event sidecars remain append-only. V1 stays readable;
   ordinary successful saves may lazily promote one session without rewriting the store.

2. **Inventory uses a derivative, rebuildable metadata catalog.** The catalog is not transcript
   authority. It stores only inventory-safe fields, uses deterministic `(modified_at DESC,
   session_id ASC)` keyset ordering, binds cursors to catalog generation and filters, validates
   external/shared-directory changes, and rebuilds from v2 headers or bounded v1 tail reads.
   A page performs work proportional to page size and never decodes conversation histories.

3. **Locking follows mutation identity.** Per-session/family mutation serialization protects
   snapshot/sidecar ordering; a narrow catalog lock protects index updates. Directory scans and
   transcript decoding never hold one store-wide mutex. A blocked inventory cannot delay an
   unrelated session save, load, or event append.

4. **Physical optimization and logical deletion are separate jobs.** Migration/compaction
   preserves every logical session and durable kind. Retention cleanup intentionally deletes
   sessions. Both are authenticated server-side plan/apply jobs with generation-bound plans,
   bounded batches, progress, cancellation, resumability, per-item errors, and mutation-time
   ownership/kind/state/liveness/lease revalidation. The TUI never edits store files.

5. **Legacy adoption is an explicit semantic operation.** An eligible owned `unknown` source
   is copied server-side into a new explicit-main session with a new opaque ID and audit
   relationship; the source stays unchanged. Reserved child/team/parallel/scheduled provenance
   is never eligible. Workspace/environment and provider/model bindings are explicit, and
   cross-provider replay state is stripped through the existing carryover discipline. Physical
   v1-to-v2 migration never changes kind.

6. **The TUI presents progressive, distinct workflows.** Sessions renders the first page before
   loading more. It labels legacy rows inspect-only and offers capability-driven **Adopt as
   chat** when eligible. **Optimize storage** is semantics-preserving; **Clean up sessions** is
   destructive and receives a separate high-friction confirmation. Job progress survives panel
   closure and cancellation stops future items rather than rolling back committed items.

7. **Automatic cleanup remains daemon-owned.** One shared retention planner drives automatic
   sweeps and manual dry-run/apply. Operator configuration exposes age/count limits and cadence;
   unknown records stay protected by default. systemd/launchd keep mecated running with stable
   paths; external cron/find/glob deletion is unsupported because it bypasses leases and live
   state.

## Consequences

Steady-state snapshot storage becomes proportional to current conversation size rather than
save count, and inventory latency becomes independent of transcript size. Historical stores can
be compacted incrementally with bounded temporary space. The design adds a catalog, maintenance
job state, management authorization, format compatibility, crash-injection tests, and explicit
cross-process coordination. Catalog data is derivative and may be rebuilt; transcript truth
remains the SessionStore snapshot. Migration and cleanup are partially-completing operations, so
status and cancellation wording must be exact. Backends that cannot provide these management
capabilities report them as unsupported rather than fabricating zero data.

## See also

- [ADR 0217](./0217-session-discovery-continuation.md) — durable kind, authoritative transcript, capability-driven inventory, and bounded public pages.
- [ADR 0027](./0027-cloud-native.md) — snapshot fidelity, event-log durability, resource inventory, and session leasing.
- [ADR 0104](./0104-session-family-physical-naming.md) — opaque IDs and canonical/legacy session-family ownership.
- [ADR 0005](./0005-driver-seams.md) — SessionStore and optional prunable/metadata driver contracts.
- [`docs/architecture.md`](../architecture.md) — living storage, service, and client behavior.
- [`docs/tui.md`](../tui.md) — living Sessions interaction behavior.
