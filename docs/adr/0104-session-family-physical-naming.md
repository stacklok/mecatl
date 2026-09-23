# ADR 0104 — Session families get a bounded, injective, non-reversible physical name

- Status: Accepted
- Date: 2026-08-12
- Scope: `internal/adapter/store/jsonlstore` — how a session id becomes a filename, and how ownership of an existing family is proven. No engine, port, or wire surface changes.

## Context

A `session.SessionID` is an **opaque** string. The engine never parses one, and
hosts mint them from text mecatl does not control: a subagent's id is
`"subagent-" + <provider tool-call id>`, a `resume:` id is model-authored, and an
embedding host can supply a namespaced compound id through `CreateSession`.

The JSONL store used to name each session's three files from
`legacySafeName` — a sanitizer mapping every rune outside `[A-Za-z0-9._-]` to
`_`. That transform is **lossy**, so `"a/b"` and `"a_b"` produced one filename.
Two distinct sessions then shared one append-only file and `Load`, which takes
the last line, returned whichever wrote most recently. Issue #441 rated this low
severity today — nothing escaped the store directory, and exploiting it requires
already knowing the id being collided with — but it made
`docs/agent-identity-model.md`'s claim that call ids are structurally safe false,
and a lossy identifier stops being harmless the moment something downstream reads
it as structured.

The first fix made the filename **reversible**: `sid-v1-` plus Raw URL-base64 of
the whole id. That removed the collision and introduced a worse failure. Base64
inflates 4/3 with no bound, so against `NAME_MAX` the longest usable id fell from
241 bytes to 175. Past that every filesystem call returned `ENAMETOOLONG`, which
is not `os.IsNotExist`, so a pre-existing session in that band became
**permanently unwritable** after an upgrade, and a long provider-supplied child id
was **silently never persisted** because `engine/agent/loop.go` (`save`) discarded
its error. A collision bug had been traded for a durability bug.

Reversibility also turned out to be unused. Its only consumer was a cross-check
that the filename belonged to the id it was read for — and that read the id from
the file's own contents anyway.

## Decision

**A canonical family name is bounded, injective, and NOT reversible.**
`internal/adapter/store/jsonlstore/resolve.go` (`encodeSessionToken`) emits
`sid-v1-<up to 40 sanitized chars>-<32 hex of SHA-256>` under a `sid-v1`
subdirectory, so the longest filename is 94 bytes for an id of **any** length.
This is the shape `internal/adapter/flocklease/flocklease.go` (`safeName`)
already used, widened from 64 to 128 bits of digest because a store collision
would mean two sessions sharing a family where a lease collision only means
false contention.

**The logical id is recovered from stored content, never from the filename.**
`engine/port/store.go` (`SessionMeta`) states this as a port-level obligation for
any backend whose key is a lossy transform of the id. A lossless key —
`internal/adapter/redisstore`, which keys verbatim — is free to be the source.

**Ownership and validity are separate questions.**
`resolve.go` (`canonicalOwnership`) answers only "is this family mine", and fails
closed **only** when a stored snapshot positively proves another owner. A torn or
empty latest line means ownership is *unproven*, not disproven: the token is
injective, so the family is ours whatever the bytes say. Conflating the two made
one crash-torn line break three unrelated operations — `Delete` returned an error
having removed nothing (violating `port.PrunableStore`'s idempotence, and
unprunable forever because `List` skips undecodable files), `EventLog.Read` failed
for a byte-perfect events file, and every write failed even though appending a
fresh snapshot is what repairs it.

**A legacy family is read and migrated only on an exact-id proof.**
`resolve.go` (`legacySnapshot`) accepts a pre-rewrite family only when its own
latest snapshot carries an id byte-equal to the requested one; absent and
belonging-to-someone-else are treated identically, and an unreadable one is an
error that callers must propagate rather than fold into "not ours".
`resolve.go` (`migrateLegacyFamily`) moves sidecars before the snapshot so an
interrupted migration leaves the family discoverable and retryable.

**A torn tail does not corrupt the next record.**
`internal/adapter/store/jsonlstore/jsonlstore.go` (`appendLine`) emits a
separating newline when the file does not end in one, bounding the damage from an
interrupted write to the single record it interrupted.

**Schedule and delivery filenames deliberately keep the lossy scheme.**
`legacySafeName` remains the stem for schedule and fire files, and
`internal/app/delivery_queue.go` (`deliverySafeName`) is an independent copy of
the same transform. Both pay for the lossiness with an ownership check against
the record's own contents rather than by renaming files. Migrating them is a
separate decision, not covered here.

## Consequences

**Easier.** Session ids are genuinely opaque and unbounded: no caller needs to
know a length limit, and no future consumer inherits one from this backend's
filesystem. Colliding ids are structurally impossible in the canonical namespace.
The readable prefix makes a store directory eyeball-able, which the base64 blob
had destroyed. The obligation is now stated once at the port and validated for
every backend by `engine/adapter/storeconformance`, so the next store cannot
reacquire a ceiling unnoticed.

**Harder, and accepted.** A filename no longer determines its logical id offline;
recovering one means reading the file, which is what
`docs/usage/troubleshooting.md` already instructs. The package carries two naming
schemes — the canonical token and `legacySafeName` — for as long as pre-rewrite
files may exist, and a reader must hold both. Ownership now costs a bounded tail
read per write rather than a bare `stat`. A hand-planted file whose stem does not
match its contents is skipped by listing and rejected by the ownership check
rather than repaired.

**Deliberately not done.** Issue #441 recommended validating ids where they are
constructed, with a loud diagnostic so a non-conforming gateway is visible. That
is a separate change with a different risk profile — it can *reject* work a
provider or model submitted — and it does not reach ids supplied by an embedding
host through the API, which a store-side fix does. The charset question it raises
belongs to whichever consumer first reads an id as structured; this decision
records that the store no longer does.

**Cost paid elsewhere.** Because no released version ever wrote a `sid-v1` file,
this replacement of the encoding carried no on-disk migration. That will not be
true of the next change to it.

## See also

- [ADR 0002](./0002-documentation-lifecycle.md) — the lifecycle convention this record follows.
- [ADR 0027](./0027-cloud-native.md) — the resource inventory and rehydrate-fidelity ledger; `Delete`'s removal order is a row there.
- [ADR 0020](./0020-diagnostics.md) — the injected `port.Diagnostics` seam the persistence WARN uses.
- `docs/architecture.md` and [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) — living descriptions of the store's behaviour.
- `docs/usage/troubleshooting.md` — the operator workflow for recovering ids from file contents.
