# Isolated execution kernel

This directory contains the isolated kernel experiment alongside the production
runtime. It is not integrated into production execution.

## Structure

[`history`](history/doc.go) owns append-only history acceptance and bounded reads.
It treats record payloads as opaque; the [`Backend`](history/backend.go) contract
supplies atomic conditional storage. The [history adapters](../adapter/history/README.md)
provide a memory backend and reusable conformance checks.

[`session`](session/doc.go) interprets accepted history as lifecycle state and
retained context. The [session example](session/example_test.go) shows submission,
snapshot access, and reconstruction; the
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

[`views`](views/doc.go) supplies read-only borrowed containers. Its package
contract describes backing ownership and lifetime. Before changing these
containers, read the [views implementation guidance](views/AGENTS.md).

[`codec`](codec/doc.go) provides bounded canonical encoding and decoding;
[`contentid`](contentid/cid.go) identifies exact encoded bytes. Resource policy is
explicit and separate from schema validity. The owning Go declarations document
limits, ownership, retry, and concurrency contracts.

Diagnostic history metadata is documented in the
[session contract](session/doc.go) and the
[history envelope format](history/format/doc.go).

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
