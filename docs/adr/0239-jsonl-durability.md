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
processes. That coordination cannot control arbitrary external writers, and a filesystem
that does not support file or directory sync cannot honestly promise host-crash safety.

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
before success, and directory-sync first publication. Event reads capture the bounded
complete-record prefix while holding that flock, then release it before yielding. They
ignore only an unterminated final fragment; malformed newline-terminated or middle
records, unknown format tags, malformed payloads, and I/O failures remain errors.

Use the same flock identity for snapshot, event, tool, delete, and legacy-promotion
mutations in cooperating jsonlstore processes. It is not a lock against arbitrary
external writers. Stage destructive mutations sidecars-before-authority and sync every
affected root/canonical directory at those boundaries. If a later step fails, sync
already-made progress before returning an error so a retry converges. Refuse destructive
operations before mutation when directory sync is unavailable.

Keep v1 snapshots readable. Promote verified legacy families lazily on a later write;
operators need no manual migration.

## Consequences

A successful snapshot save has a precise host-crash guarantee only where all three
reported primitives are available. On weaker filesystems, atomic replacement can still
avoid a torn current snapshot, but neither snapshots nor destructive family operations
claim equivalent power-loss durability. A process crash can leave a temporary snapshot
or an unterminated sidecar tail; the next operation safely cleans or ignores only those
narrow incomplete artifacts. It cannot repair completed corruption.

Flocks serialize cooperating local mecatl processes without imposing a distributed lock
protocol or protecting data modified outside the adapter. Syncing and lock-held prefix
capture add I/O and contention, but permit append readers to yield without retaining a
file handle or blocking writers. Owner-only `0600` sidecars preserve the plaintext-store
privacy posture.

## See also

- `internal/adapter/store/jsonlstore/jsonlstore.go` (`SnapshotDurability`)
- `internal/adapter/store/jsonlstore/jsonlstore.go` (`appendLine`)
- `internal/adapter/store/jsonlstore/resolve.go` (`migrateLegacyFamily`)
- `docs/architecture/observability.md`
- `docs/design/IMPLEMENTATION-NOTES.md`
- [ADR 0027](./0027-cloud-native.md)
- [ADR 0104](./0104-session-family-physical-naming.md)
- [ADR 0226](./0226-session-storage-maintenance.md)
