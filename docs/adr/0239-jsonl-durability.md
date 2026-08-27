# ADR 0239 — Local JSONL durability boundaries

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
only atomic replacement plus file and directory sync is host-crash safe. A snapshot
operation reports an error if a supported required step fails; a post-replace directory
sync error is loud even though the new snapshot is already authoritative.

Treat newline as the sidecar commit marker. Under the stable per-session family flock,
event and tool append repair only an unterminated EOF fragment by truncating to the last
newline, write one complete newline-terminated record, reject short writes, file-sync
before success, and directory-sync after every successful append. Open canonical
sidecars through an `os.Root` rooted at the canonical directory (and readable legacy
sidecars through the store root), keep inspection and mutation on that descriptor, and
reject non-regular entries without blocking on FIFOs. Event reads capture the bounded
complete-record prefix while holding that flock, then release it before yielding while
retaining the bounded descriptor/section view until iteration ends. They ignore only an
unterminated final fragment; blank, whitespace-only, malformed newline-terminated or
middle records, unknown format tags, malformed payloads, and I/O failures remain errors.

Use the same flock identity for snapshot, event, tool, delete, and legacy-promotion
mutations in cooperating jsonlstore processes. It is not a lock against arbitrary
external writers. Durably publish adapter-created directory hierarchy entries. Stage
destructive mutations sidecars-before-authority and sync every affected root/canonical
directory at those boundaries. Before explicit migration removes a verified v1 while v2
already exists, first sync the v2 directory, then remove v1 and sync again. If a later
step fails, sync already-made progress before returning an error so a retry converges.
Refuse EventLog append and destructive/move operations before mutation when required
directory sync is unavailable. Snapshot Save retains its existing weaker-capability
behavior; ToolCall remains best-effort and may drop a record because its port returns no
error.

Keep v1 snapshots readable. Promote verified legacy families lazily on a later write;
operators need no manual migration.

## Consequences

A successful snapshot save has a precise host-crash guarantee only where all three
reported primitives are available and the underlying filesystem/storage honors their
successful completion. The probe establishes syscall support; it cannot make volatile
storage such as tmpfs survive power loss. On weaker filesystems, atomic replacement can
still avoid a torn current snapshot, but neither event append nor destructive family
operations claim equivalent durability: they fail closed when required directory sync
cannot be established, while Snapshot Save reports the weaker capability and ToolCall
may drop its best-effort record. A process crash can leave a temporary snapshot or an
unterminated sidecar tail; the next operation safely cleans or ignores only those narrow
incomplete artifacts. It cannot repair completed corruption.

Flocks serialize cooperating local mecatl processes without imposing a distributed lock
protocol or protecting data modified outside the adapter. Syncing and lock-held prefix
capture add I/O and contention, but readers release the family flock before yielding;
the lazy iterator retains only its bounded descriptor/section view until iteration ends.
Owner-only `0600` sidecars preserve the plaintext-store privacy posture.

## See also

- `internal/adapter/store/jsonlstore/jsonlstore.go` (`SnapshotDurability`)
- `internal/adapter/store/jsonlstore/jsonlstore.go` (`appendLine`)
- `internal/adapter/store/jsonlstore/resolve.go` (`migrateLegacyFamily`)
- `docs/architecture/observability.md`
- `docs/design/IMPLEMENTATION-NOTES.md`
- [ADR 0027](./0027-cloud-native.md)
- [ADR 0104](./0104-session-family-physical-naming.md)
- [ADR 0226](./0226-session-storage-maintenance.md)
