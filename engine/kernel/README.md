# Isolated execution kernel

This directory contains the isolated kernel experiment alongside the production
runtime. It is not integrated into production execution.

## Structure

[`history`](history/doc.go) owns append-only history acceptance and bounded reads.
It treats record payloads as opaque; the [`Backend`](history/backend.go) contract
supplies atomic conditional storage. The [history adapters](../adapter/history/README.md)
provide a memory backend and reusable conformance checks.

[`session`](session/doc.go) interprets accepted history as lifecycle state and
retained context. It validates ordered batches before submission and publishes
state only after storage acceptance. Immutable snapshots pin accepted state;
[`CommitView`](session/commit_view.go) pins one writable batch's accepted prefix
and its proposed records for inspection and request construction. Proposed records
remain provisional after capture and confer neither acceptance nor execution
authority. Loading reconstructs accepted state without resolving references or
executing work. Independent model-usage facts retain fixed correlation and complete
replaceable reports without changing execution state. `Snapshot.Usages` explicitly
joins first definitions and latest reports across the whole snapshot, including
withdrawn identities and late reports for ended runs. This is best-effort showback,
not proof of spend or complete coverage. The [session example](session/example_test.go)
shows submission, snapshot access, and reconstruction; the
[usage example](modelrequest/usage_example_test.go) shows accounting corrections,
withdrawal/restoration, and a coverage-aware subtotal rebuilt from storage.

[`modelrequest`](modelrequest/doc.go) constructs immutable provider-neutral logical
requests from a pinned session source and reconstructs accepted commitments from
their retained recipes. Construction does not invoke a model. A request commitment
must be accepted by `session` before an execution boundary can use it. The
[model-request example](modelrequest/example_test.go) shows the complete authoring
and reconstruction flow.

Each layer's `format` package owns mutable persistence DTOs, generated codecs,
and payload-local validation. Aggregates own cross-record legality; valid encoding
alone proves neither lifecycle legality nor storage acceptance.

[`views`](views/doc.go) supplies read-only borrowed containers for inspecting
immutable backing owned by the caller. Borrowing neither freezes data nor
synchronizes access; the owner must keep backing stable for derived views.
Before changing these containers, read the [views implementation guidance](views/AGENTS.md).

[`codec`](codec/doc.go) provides bounded canonical encoding and decoding;
[`contentid`](contentid/cid.go) identifies exact encoded bytes. Resource policy is
explicit and separate from schema validity. The owning Go declarations document
limits, ownership, retry, and concurrency contracts.

## Diagnostic history metadata

Session construction and loading require a [`Config`](session/config.go) with a
nonzero writer-instance ID, reused across sessions for that writer's lifetime.
The optional clock defaults to system time. Session stamps each record at
successful local admission and each commit at first submission preparation;
frozen retries retain the exact timestamps, ID, bytes, and CID. Loading preserves
stored metadata without consulting the clock.

[`RecordView`](session/record_view.go) exposes recording time and optional enclosing
accepted [`CommitMetadata`](session/commit_metadata.go) in constant time. Captured
proposed records retain their timestamps but never acquire accepted metadata.
These diagnostic values establish neither authority nor action timing, storage
receipt time, or clock ordering. They affect history identity while leaving
logical model-request bytes and identity unchanged. Version 1 history envelopes
require all metadata fields; missing values fail decoding without migration or
inferred historical data.

## DTO generation

`dtogen` generates codecs, whole-value operations, and view accessors. See the
[generator guide](../../internal/dtogen/README.md) for declarations, supported
schemas, and field policies. Regenerate and check from the repository root:

```sh
task generate:data
task generate:data:check
```

## Focused checks

Run the accounting/construction examples and regressions, or the broader kernel
checks, from the repository root:

```sh
go -C engine test ./kernel/session ./kernel/modelrequest -run 'Usage|Example'
cd engine && go test -race ./id ./kernel/... ./arch ./adapter/history/...
```

Follow the scoped [implementation guidance](AGENTS.md) when changing this directory.
