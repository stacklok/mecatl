# SQLite history adapter

`history/sqlite` implements `history.Backend` for independent kernel histories
on a trusted, application-owned local filesystem. It is intended for local
Linux and macOS storage. The adapter has no production integration and does not
complete issue #2666.

## Lifecycle

Call `New` with a private, application-owned root directory. The constructor
prepares that root, while histories open lazily on their first operation.
`Backend` retains at most one physical SQLite connection for each accessed
history. Call `Close` when the application is done with the backend. `Close`
stops new operations, waits for admitted operations, closes retained handles,
and is safe to call concurrently or repeatedly. A clean close is not required
for an acknowledged commit to survive process loss.

```go
backend, err := sqlite.New("/var/lib/example/history")
if err != nil {
	return err
}
defer backend.Close()
```

The root must remain private to the application and its cooperating processes.
This adapter does not harden storage against hostile modification, symlink or
hard-link attacks, or network filesystems.

## Storage layout and format

Each history has a directory named by its validated `HistoryID` beneath the
root. Its database is `history.sqlite`; SQLite manages the adjacent `-wal` and
`-shm` files. A stable `<HistoryID>.lock` file in the root coordinates
initialization.

The adapter stores one identity and one SQLite schema:

- `PRAGMA application_id` is `0x4d484953` (`MHIS`) and `user_version` is `1`.
- The `identity` table contains exactly one `HistoryID`.
- The strict `commits` table contains a positive sequence number, a 36-byte CID,
  and a nonempty payload. The highest sequence number is the history head.

A first attachment, including one opened for a read of an existing history,
takes the initialization lock. It validates the database identity, version, and
schema, then synchronizes the history directory and root before retaining the
handle. New databases are prepared in a temporary sibling directory, closed and
synchronized, then renamed into the final namespace. An existing final database
is never replaced. Missing reads do not create a database or lock.

Payloads are complete DRISL-CBOR items supplied by `history.History`. The
adapter retains and returns their exact bytes. It does not interpret payload
schemas, ancestry, or CID meaning; `history.History` owns those checks.

## Durability and errors

Every physical connection verifies WAL mode with `synchronous=FULL`,
`fullfsync=ON`, `checkpoint_fullfsync=ON`, normal local-file locking,
`read_uncommitted=OFF`, and a 5-second SQLite busy timeout. The adapter keeps
SQLite's standard automatic WAL checkpoint setting of 1,000 pages. These are
per-connection settings, including connections used after reopen or recovery.

A successful `Append` return means its SQLite transaction committed under those
settings. This is ordinary SQLite durability on the supported local filesystem;
it is not a custom power-loss guarantee or evidence for network filesystems.
An append error returns zero evidence and acceptance can be unknown, for
example when commit acknowledgement fails after SQLite accepted the transaction.
Do not automatically resubmit the proposal. Use `history.History.Resolve` with
the proposal identity to determine whether it was accepted.

## Inspection, support, and limits

Inspect a closed history with SQLite tooling only when the complete database
state is available. Do not copy a live `history.sqlite` file by itself: WAL mode
can require its `-wal` and `-shm` files. The adapter provides no live-backup
feature.

The driver is local to this adapter: `modernc.org/sqlite` v1.62.0 with
`modernc.org/libc` v1.80.1. A replacement driver must requalify the adapter's
behavior.

The adapter does not provide an authoritative history catalog, history deletion,
root or delegation lifecycle, execution ownership, fencing, distributed writer
coordination, or production wiring. The currently published `engine` v0.18.1
does not include kernel history, so this adapter cannot yet pass standalone or
publication qualification against its pinned engine dependency.

## Verification evidence and limits

The adapter tests exercise backend conformance, reopen behavior, unknown commit
resolution, and passive kernel session `Submit` and `Load` through the real
adapter. Subprocess tests exercise independent adapter processes, publication
coordination, and recovery paths.

The fault-injecting SQLite VFS tests run only on Linux `amd64` and are tied to
the listed sqlite/libc versions. They provide process and I/O-error evidence,
not simulated power-loss evidence. Native macOS execution has not been tested.
Cross-builds are build evidence, not runtime qualification. Benchmarks use
`b.TempDir()` on the filesystem selected by `TMPDIR`; tmpfs results do not
measure physical-device latency or throughput. Physical-storage runs include
standard checkpoints but do not establish power-loss behavior.
