# Isolated execution kernel

This directory contains the isolated kernel experiment alongside the existing
runtime. `history/id` supplies the strongly typed `HistoryID` using the shared
[`id`](../id/id.go) utility. `history` re-exports that type and retains its
constructor/parser entry points. `contentid` supplies opaque, comparable commit
CIDs with strict text parsing and formatting. `history/format` supplies mutable
`Commit` and `Record` persistence DTOs. Their generated `Clone` methods detach
identity pointers and nested mutable storage; `Equal` compares data with nil and
empty slices equivalent while distinguishing absent and present pointers. Neither
operation validates or freezes the DTOs. `Validate` checks the data contract
independently of resource policy. Shared `codec.Encode` and `codec.Decode` enforce
canonical v1 DAG-CBOR and explicit positive resource limits; History hashes and
verifies exact bytes with `contentid`. Roots omit parent; successors omit history
ID, and present-zero identities and explicit null are invalid. Unknown record
kind/version payloads remain opaque; valid encoding proves neither membership
nor acceptance.

`history.History` implements DTO-facing append, retry resolution, and bounded
reads through an accepted head. `Identify` derives a proposal reference locally
for resolving uncertain appends. `Append` makes one conditional backend call.
Reads page forward between commit references; a zero `After` starts before the
first commit, and `Next` continues traversal through the fixed endpoint. Callers
choose a commit count; policy supplies the byte ceiling. History validates
canonical bytes and indexed identity and ordering when decoding stored commits.
Its byte-facing `history.Backend`
contract provides atomic expected-head appends, coherent indexed observations,
and reads bounded before byte materialization. Writer ownership and fencing are
outside this contract. The
[history adapters](../adapter/history/README.md) supply a process-local memory
backend and reusable conformance checks. The owning Go declarations describe
operation and ownership semantics. There is no execution facade here; production
entry points continue to use the existing engine.

`history` imports `history/format`, `history/id`, `contentid`, and `codec`;
format imports the identity owners and composes their generated methods over the
identity-free `codec` runtime. The history identity depends on `engine/id` and
`codec`. Format and `codec` use Atmos's `cbor` primitives; the runtime's remaining
dependency closure is standard-library-only.
The history identity leaf supplies strongly typed history identities to format
DTOs without importing higher-level history contracts. These packages do
not depend on the legacy session aggregate, model providers, or host composition.
The [kernel design note](../../docs/drafts/session-history/implementation/kernel.md)
provides historical design context. Follow the scoped
[implementation guidance](AGENTS.md) when changing this directory.

## Persistence codec tooling

`codec` supplies explicit operational budgets and strict owning CBOR primitives
for generated DTO codecs. `contentid` derives and verifies fixed-profile CIDs
from exact bytes. History's generated envelope composes the same runtime and
identity-owner methods; none of these packages integrates the isolated kernel
into production execution.

The [generator guide](../../internal/cborgen/README.md) owns supported Go types,
`cbor`/`cborlimit` tags, source-local `go:generate` declarations, generated APIs,
and semantic/identity composition. The [runtime package contract](codec/doc.go)
owns accounting and input/output lifetimes. From the repository root:

```sh
task generate:codecs
task generate:codecs:check
```

## Focused checks

Run the focused offline checks from the repository root:

```sh
cd engine && go test -race ./id ./kernel/... ./arch ./adapter/history/...
```
