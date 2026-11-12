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
loading reconstructs it without resolving references or executing work. The
[session example](session/example_test.go) shows submission, snapshot access,
and reconstruction.

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

## DTO generation

`dtogen` generates codecs, whole-value operations, and view accessors. See the
[generator guide](../../internal/dtogen/README.md) for declarations, supported
schemas, and field policies. Regenerate and check from the repository root:

```sh
task generate:data
task generate:data:check
```

## Focused checks

Run the focused offline checks from the repository root:

```sh
cd engine && go test -race ./id ./kernel/... ./arch ./adapter/history/...
```

Follow the scoped [implementation guidance](AGENTS.md) when changing this directory.
