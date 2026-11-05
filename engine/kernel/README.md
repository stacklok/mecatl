# Isolated execution kernel

This directory contains the isolated kernel experiment alongside the production
runtime. It is not integrated into production execution.

## Structure

[`history`](history/doc.go) owns append-only history acceptance and bounded reads.
It treats record payloads as opaque; the [`Backend`](history/backend.go) contract
supplies atomic conditional storage. The [history adapters](../adapter/history/README.md)
provide a memory backend and reusable conformance checks.

[`session`](session/doc.go) interprets accepted history as lifecycle state. It
validates ordered batches before submission and updates accepted state only after
storage acceptance. Immutable snapshots expose that state. Loading reconstructs
state through the same application path. The [session example](session/example_test.go)
shows submission, snapshot access, and reconstruction.

Each layer's `format` package owns its mutable persistence DTOs, generated codecs,
and payload-local validation. The aggregate owns cross-record legality; valid
encoding alone proves neither lifecycle legality nor storage acceptance. Public
session values remain separate from persistence DTOs so verified history context
can supply scope omitted from payloads.

[`codec`](codec/doc.go) provides bounded canonical encoding and decoding;
[`contentid`](contentid/cid.go) identifies exact encoded bytes. Resource policy is
explicit and separate from schema validity. The owning Go declarations document
limits, ownership, retry, and concurrency contracts.

## Persistence codec tooling

See the [generator guide](../../internal/cborgen/README.md) for supported schemas
and code generation, and the [codec package contract](codec/doc.go) for resource
accounting and buffer lifetimes. From the repository root:

```sh
task generate:codecs
task generate:codecs:check
```

## Focused checks

Run the focused offline checks from the repository root:

```sh
cd engine && go test -race ./id ./kernel/... ./arch ./adapter/history/...
```

Follow the scoped [implementation guidance](AGENTS.md) when changing this directory.
