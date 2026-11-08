# Isolated execution kernel

This directory contains the isolated kernel experiment alongside the production
runtime. It is not integrated into production execution.

## Structure

[`history`](history/doc.go) owns append-only history acceptance and bounded reads.
It treats record payloads as opaque; the [`Backend`](history/backend.go) contract
supplies atomic conditional storage. The [history adapters](../adapter/history/README.md)
provide a memory backend and reusable conformance checks.

[`session`](session/doc.go) interprets accepted history as lifecycle state and
retained context. It preserves rich authored input separately from prepared content
and its provenance. Instructions, advertised tool catalogs, and model bindings are
independent session-scoped replacements. Model requests commit to an accepted
source and construction recipe. Atomic responses retain ordered output, requested
calls, and private provider data; responses and observed invocation failures can
carry reported usage and safe correlation evidence. Explicit run endings preserve
unresolved work. The session validates ordered batches
before submission and updates accepted state only after storage acceptance. Immutable
snapshots pin state and context selections; loading reconstructs them without
resolving source files or media references, invoking models, or executing tools.
The [session example](session/example_test.go) shows submission, snapshot access,
and reconstruction.

Each layer's `format` package owns its mutable persistence DTOs, generated codecs,
and payload-local validation. The aggregate owns cross-record legality; valid
encoding alone proves neither lifecycle legality nor storage acceptance. Session
payload names alias canonical format declarations. Commit methods take full
scope separately and reject nonzero synthesized context/allocation fields before
materializing a local DTO copy. Nested numeric references resolve within the
explicit destination run; extracting a component preserves no origin proof.
Occurrence IDs support validated parent/component reconstruction, which neither
allocates an occurrence nor establishes membership or authority.

[`views`](views/doc.go) supplies read-only borrowed Bytes and SliceView containers.
Follow its [runtime guidance](views/AGENTS.md) when changing these containers.
Session views pin immutable accepted backing; wrapping and navigation do not copy
or freeze it. Response Items and content Parts adapt elements on demand without
an intermediate slice. Recipe, call, provider-data and media byte getters borrow
leaves; callers explicitly use Copy for independently owned bytes. Scope-bearing
identities and derived call enumeration remain session-owned adapters.

[`codec`](codec/doc.go) provides bounded canonical encoding and decoding;
[`contentid`](contentid/cid.go) identifies exact encoded bytes. Resource policy is
explicit and separate from schema validity. The owning Go declarations document
limits, ownership, retry, and concurrency contracts.

## DTO generation

See the [generator guide](../../internal/dtogen/README.md) for supported schemas
and code generation, and the [codec package contract](codec/doc.go) for resource
accounting and buffer lifetimes. Each destination owns one handwritten generation
declaration and artifact. Session's [view declaration](session/doc.go) selects
local DTO-backed handles. Untagged DTO fields get safe same-named getters;
`view:"manual"` requires a handwritten getter and `view:"-"` omits the field.
Layouts and scope/tag-selected semantics remain handwritten; generated mechanical
accessors use only noncapturing adapters. From the repository root:

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
