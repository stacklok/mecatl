# ADR 0243 — Local JSONL durability boundaries

- Status: Accepted
- Date: 2026-08-27
- Scope: jsonlstore snapshots and event/tool sidecars (issue #469)

## Context

The local JSONL store is a plaintext, single-host persistence option. Earlier behavior
made the v2 snapshot replacement atomic within normal process operation, but did not
document or consistently enforce its crash boundaries. Append-only sidecars also need a
commit boundary: a process or power loss can leave an incomplete final write, while a
complete corrupt record must never be mistaken for a harmless torn tail.

The store already uses a stable per-session family lock for cooperating mecatl
processes. That coordination cannot control arbitrary external writers. Successful sync
and atomic-rename calls provide a host/power-loss guarantee only when the underlying
filesystem and storage stack honor those primitives; a capability probe verifies syscall
support, not media persistence (for example, tmpfs does not survive host power loss).

## Decision

Use a same-directory temporary file for every v2 current snapshot, fully write and sync
it when supported, atomically replace the current file, then sync the containing
directory when supported. Report the verified primitives through `SnapshotDurability`;
only atomic replacement plus file and directory sync is host-crash safe. An existing
canonical snapshot may still be saved with a reported weaker capability. The first Save
of a root-level legacy family must migrate it and therefore fails before mutation when
directory sync is unavailable. A snapshot operation reports an error if a supported
required step fails; a post-replace directory sync error is loud even though the new
snapshot is already authoritative.

Treat newline as the sidecar commit marker. Under the stable per-session family flock,
strict EventLog append requires both file and directory sync: it marshals and rejects
any record larger than the reader's shared 16 MiB newline-inclusive Scanner limit before
acquiring the family lock or mutating storage, repairs only an unterminated EOF fragment by
truncating to the last newline, writes one complete newline-terminated record, rejects short
writes, file-syncs before success, and syncs the directory after every append. ToolCall audit cannot report errors through its
port, so it instead attempts the same append and every available sync. When directory sync
is unavailable it does not force a legacy-family migration: it appends to the existing
readable sidecar, or to the sidecar matching the authoritative snapshot family when absent,
so a later capable migration preserves the full audit order. Open
canonical sidecars through an `os.Root` rooted at the canonical directory (and readable
legacy sidecars through the store root), keep inspection and mutation on that descriptor,
reject non-regular entries without blocking on FIFOs, and tighten every sidecar opened for
append to `0600` before reading or writing its contents. Likewise tighten each validated
regular legacy family file through a no-follow descriptor immediately before migration.
Event reads capture the bounded
complete-record prefix while holding that flock, then release it before yielding while
retaining the bounded descriptor/section view until iteration ends. They ignore only an
unterminated final fragment; blank, whitespace-only, malformed newline-terminated or
middle records, unknown format tags, malformed payloads, and I/O failures remain errors.

Use the same flock identity for snapshot, event, tool, delete, and legacy-promotion
mutations in cooperating jsonlstore processes. It is not a lock against arbitrary
external writers. Require the configured store path and every ancestor to be physical,
non-symlink directories; on macOS an operator must use the physical `/private/...`
spelling rather than a `/var/...` path that traverses the `/var` symlink. Durably publish
adapter-created directory hierarchy entries. Stage destructive mutations
sidecars-before-authority and sync every affected root/canonical directory at those
boundaries. Before explicit migration removes a verified v1 while v2 already exists,
first sync the v2 directory, then remove v1 and sync again. If a later step fails, sync
already-made progress before returning an error so a retry converges. Refuse EventLog
append, Delete/retention, and legacy destructive/move operations before mutation when
required sync capability is unavailable. This fail-closed posture can make retention and
other destructive maintenance unavailable on such filesystems.

Keep the live client relay chunk-streamed, but give its run-scoped durable recorder a
bounded-memory and bounded-record policy: coalesce message and reasoning deltas separately
into UTF-8-safe chunks of at most 1 MiB. This conservative payload ceiling remains below the
JSONL reader's 16 MiB record cap even when every input byte takes the worst-case six-byte
JSON escape. Typical turns still append one message and one reasoning record; oversized
turns append the minimum bounded number of chunks. A non-delta boundary flushes pending
chunks before it is appended. Each coalesced chunk and boundary is passed to
`EventLog.Append` exactly once and then discarded regardless of the result: an error may be
reported after the write became durable, so retrying could duplicate folded text. Failures
warn once per recorder but do not prevent later boundary/result append attempts. Process
death or an append failure can lose a chunk; the completed SessionStore snapshot remains
authoritative.

Keep v1 snapshots readable. Promote verified legacy families lazily on a later write;
operators need no manual migration.

## Consequences

A successful snapshot save has a precise host-crash guarantee only where all three
reported primitives are available and the underlying filesystem/storage honors their
successful completion. The probe establishes syscall support, not media persistence; it
cannot make volatile storage such as tmpfs survive power loss. On weaker filesystems,
an existing canonical snapshot Save can retain weaker behavior and atomic replacement
can still avoid a torn current snapshot. A root-level legacy family's first Save fails
before migration without directory sync. Strict EventLog append requires file and
directory sync, and Delete/retention and other destructive family operations fail closed
without directory sync, reducing availability rather than claiming durability. ToolCall
audit may instead leave an unsynced or partially synced best-effort record, or drop it on
another failure. A process crash can leave a temporary snapshot or an unterminated
sidecar tail; the next operation safely cleans or ignores only those narrow incomplete
artifacts. It cannot repair completed corruption.

Flocks serialize cooperating local mecatl processes without imposing a distributed lock
protocol or protecting data modified outside the adapter. Syncing and lock-held prefix
capture add I/O and contention, but readers release the family flock before yielding;
the lazy iterator retains only its bounded descriptor/section view until iteration ends.
Adapter-created sidecars are owner-only `0600`; append and migration also tighten any
legacy/current regular files they touch. Untouched pre-existing files retain their prior mode,
so operators must still secure the owner-only store tree.

## See also

- `internal/adapter/store/jsonlstore/jsonlstore.go` (`SnapshotDurability`)
- `internal/adapter/store/jsonlstore/jsonlstore.go` (`appendLine`)
- [`migrateLegacyFamily` in `resolve.go` at the decision commit](https://github.com/stacklok/mecatl/blob/ad1cfe3c89a640905b88fb69f9905df498ba5c9c/internal/adapter/store/jsonlstore/resolve.go)
- `docs/architecture/observability.md`
- `docs/design/IMPLEMENTATION-NOTES.md`
- [ADR 0027](./0027-cloud-native.md)
- [ADR 0104](./0104-session-family-physical-naming.md)
- [ADR 0226](./0226-session-storage-maintenance.md)
