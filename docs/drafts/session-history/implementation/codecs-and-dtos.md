# Persistence codecs and DTO ownership

The selected implementation uses explicit Go persistence DTOs and handwritten
Atmos codecs behind `engine/kernel/history/format`. This keeps the public
contract independent of a codec library while making canonical encoding and
ownership reviewable for a small set of types. It is a design boundary, not a
claim that codec or storage implementations have shipped. The
[history-format report](history-format.md) explains the bytes and identities
that this boundary must preserve.

## Why handwritten Atmos codecs

Atmos provides a generic DAG-CBOR codec and exported validation-aware primitives
for direct typed decoding. Handwritten codecs can use those primitives without
adopting Lexicon or building a reflection mapper. This is proportional to the
few persistence types involved and makes field presence and ownership explicit.

Upstream fxamacker remains a credible general CBOR implementation. Its reflection
API maps ordinary Go DTOs, but a strict DAG-CBOR contract requires configuration
and checks beyond its options. Bounded decoding, lossless schema/profile
validation, canonical re-encoding, and byte equality can reject alternate
encodings. The extra encoding has a cost, and that strategy was not qualified
for arbitrary extension DTOs or custom marshalers.

The go-dasl/DRISL fork integrates more profile checks with reflection decoding,
but selecting it would also accept responsibility for its outstanding backports
and upstream tracking. That maintenance commitment, rather than an assertion
that reflection cannot work, is why it was excluded. A reflection bridge from
Go DTOs to Atmos's generic tree would introduce another mapper's null, numeric,
CID, and presence semantics. The selected handwritten approach avoids that
additional abstraction.

### Maintenance evidence, observed October 30, 2026

The investigation inspected go-dasl v0.8.0 and its pinned CBOR fork,
`github.com/hyphacoop/cbor/v2` at
`v2.0.0-20251007204234-2a4fa83e606e`, against fxamacker v2.10.0. At that time the
fork lacked upstream fixes, and proposed upstream DRISL support had not merged.
This is a dated dependency assessment, not a timeless claim about either project.

A local regression probe reproduced stale scalar reuse when decoding a map
containing `{a: 1, b: null}` into `map[string]int`: the fork retained `1` for
`b`, while upstream produced `0`. The generic `map[string]any` benchmark path
was not affected by that particular bug. Source comparison also identified
missing fixes involving conditional `time.Time` decode panics, recursive Go
container types, stale/interface state, and resource retention. Those additional
cases were not all locally reproduced; some other upstream fixes did not apply
under DRISL restrictions. No published CRITICAL advisory was found. The evidence
supports a correctness and maintenance concern, not a demonstrated critical
exploit or an inference of neglect.

## Strictness belongs to the complete codec

The Atmos API observations refer to v0.8.0, used in the October 30, 2026 experiments.
Helpers including `ReadHeader`, `ReadTextKey`, `ReadInt`, `ReadCIDLink`,
`CheckMapKeyOrder`, and `SkipValue` supply wire checks. A typed codec must compose
them correctly: traverse containers, check ordering, reject trailing data,
enforce resource limits, and validate the schema. A closed fixed-key schema can
require the exact canonical key sequence to reject reordered, duplicate, missing,
and unknown fields without a general map traversal. Reading a header does not
validate an entire object. Generic-codec experiments do not qualify every
handwritten path.

The codec contract combines exact canonical bytes with closed-envelope checks:
required fields, root/successor presence rules, supported envelope versions,
allowed CID profile, and integer boundaries. Unknown envelope fields are
rejected, while unknown record kind/version payloads remain opaque. Structural
validation supplies neither authorization nor safe interpretation of unfamiliar
payloads.

Resource bounds are explicit positive per-operation budgets for total commit
bytes, record count, kind bytes, and per-record payload bytes. There are no
implicit defaults or zero-as-unlimited semantics, and operations must not change
Atmos's package-global settings. Bounds apply before relevant allocations on
both encoding and decoding. `Validate` checks DTO data contracts without applying
budgets; codec operations additionally enforce policy and wire canonicality.
Numerical budgets remain a caller policy choice; this report does not prescribe
defaults.

Exact-byte fixtures and malformed/noncanonical cases must exercise the public
encode/decode boundary, including root binding, missing/null/empty distinctions,
unknown fields, limit boundaries, and ownership of strings and bytes.
Cross-implementation agreement is useful evidence but cannot replace those
checks.

## Public DTOs, private codec dependencies

`engine/kernel/history/format` owns public persistence data types, format
versions, and package-level encode/decode functions. Atmos types stay out of
exported signatures. Public commit CIDs are Mecatl-owned opaque, comparable
values; library-specific representations stay behind the codec boundary. The
initial CIDv1/DAG-CBOR/SHA-256 profile belongs to the format contract rather than
to a library's generic CID acceptance policy. The `atmos/cbor` package at v0.8.0
imports only the standard library, but its module requires Go 1.27.3. That minimum
applies to the entire engine module and propagates through local engine consumers;
it cannot be restricted to packages that import the codec.

History identity belongs in the small leaf `engine/kernel/history/id`, using the
existing `engine/id` resource utility. `history` re-exports `HistoryID` as an
alias and retains its constructor/parser wrappers. Format DTOs can use the
strong identity type without importing higher-level history contracts or
weakening the field to a string. Moving the defining type changes its Go
package/reflection identity while preserving ordinary source use and the text
contract.

Working-state types remain separate from mutable persistence DTOs. `Commit` and
`Record` expose concrete `Clone` and `Equal` methods: cloning detaches all nested
mutable storage, while equality compares fields and ordered opaque payload bytes,
with nil and empty slices equivalent. Neither operation validates the format or
freezes its values. Marker interfaces establish none of these guarantees; concrete
methods make the operations discoverable without requiring a shared interface.
Higher-level history code depends on the format contract rather than on whether
codecs are handwritten or generated. Internal packages are useful where they
establish a real dependency boundary, not as a requirement to create one package
per file.

### Owning operations detach storage

Encode/decode operations own their returned storage. Decoded strings and byte
slices must not alias the input; encoded bytes must not alias DTO storage.
Low-level Atmos text helpers can borrow strings from input, so owning decoding
must explicitly detach them as well as payload bytes. Callers must not mutate
DTOs concurrently with encoding.

These guarantees make values independent across the codec boundary; they do not
make exported DTO slices immutable. Stable publication, cloning, and borrowed
access retain the separate contracts in the [ownership report](ownership.md).
A future borrowed/view decoder would be a distinct API rather than a silent
weakening of owning decoding.

### Why package functions leave room for generation

Package-level encode/decode functions avoid coupling DTOs to codec methods.
Concrete `Clone` and `Equal` methods favor discoverability over unrestricted
future aliases to generated structs. Go cannot attach methods to an alias of a
type defined in another package, so a generated replacement must supply compatible
methods or retain Mecatl-defined DTOs with explicit conversion. Aliases also
cannot substitute individual generated field types. Generator customization,
conversion, or an explicit Go API change may therefore be necessary; source
compatibility with an arbitrary future generated schema is not promised.

Lexicon can generate typed Go records, JSON/CBOR methods, unions, and client
functions. A schema could also support documentation, structural validation,
and descriptive inspection. Those are reasons to reconsider generation when
representative extension needs justify its conventions; they are not proof that
it automatically meets Mecatl's canonicality, semantic, or ownership contracts.
A codec replacement must preserve canonical bytes, rejection behavior, and
ownership guarantees rather than silently redefine stored identities.

Extension owners still communicate across versions, exports, reconstruction,
and missing-handler cases even when one implementation normally owns a kind.
The useful CRD analogy is platform-owned storage/lifecycle with owner-defined
payload interpretation. It does not require schema discovery, admission
webhooks, or a schema-distribution framework.

## What the experiments establish

The October 30, 2026 experiments compared fxamacker v2.10.0 with go-cid v0.6.2,
go-dasl v0.8.0, and Atmos v0.8.0. Five common nonnil commit fixtures produced
identical encoded bytes and SHA-256 hashes across candidates. Payloads included
NUL, invalid UTF-8 bytes, and representation-sensitive JSON. Tested owning paths
detached payload bytes and record-kind strings. These results establish agreement
and ownership for those fixtures, not exhaustive conformance.

Integrated generic validation avoided a second encoding for large reads. For a
64 KiB payload, median validated reads were about 18.9 microseconds with
fxamacker plus canonical equality, 10.3 with DRISL, and 8.9 with Atmos. This
compares available integration strategies, not equally optimized algorithms;
Atmos used generic values plus DTO projection, not the selected handwritten
codec. Its generic encoder also required DTO-to-map conversion.

Small-record reconstruction gave a different comparison. Loading 1,000 linked
commits with one 128-byte payload each, checking hashes/ancestry, and retaining
records took about 3.1 ms with fxamacker's direct DTO plus canonical equality and
4.2 ms with Atmos's generic validation/projection. This supports measuring the
actual workload rather than inferring load performance from large blobs. Bursts
of many small records were a workload hypothesis, not an observed production
bottleneck.

Measurements used Linux/amd64 on an Intel i7-1165G7, Go 1.27.3, GOMAXPROCS=1,
CPU affinity 2, and five 200 ms samples per benchmark. CPU frequency and other
host activity were uncontrolled. Burst timings were noisy and included neither
storage I/O nor real reducers. Cumulative allocated bytes were not peak live
memory. No handwritten Atmos codec, reflection bridge, no-copy optimization, or
generated Atmos DTO path was performance-qualified. Source inspection of
Indigo, cbor-gen, and glex was ecosystem research, not benchmark evidence.

The selection rests on explicit codec and maintenance responsibilities, not a
claim of an end-to-end performance winner, complete generated-code coverage, or
security qualification.

## References

- Atmos v0.8.0 [generic decoder](https://github.com/jcalabro/atmos/blob/v0.8.0/cbor/decoding.go), [direct-decoding helpers](https://github.com/jcalabro/atmos/blob/v0.8.0/cbor/decode_helpers.go), and [Lexicon generator](https://github.com/jcalabro/atmos/blob/v0.8.0/lexgen/cbor.go).
- [fxamacker v2.10.0](https://github.com/fxamacker/cbor/releases/tag/v2.10.0), [go-cid v0.6.2](https://github.com/ipfs/go-cid/releases/tag/v0.6.2), and [go-dasl v0.8.0](https://github.com/hyphacoop/go-dasl/tree/v0.8.0).
- [DRISL upstream proposal #698](https://github.com/fxamacker/cbor/issues/698), [closed float PR #701](https://github.com/fxamacker/cbor/pull/701), and [fork tracking #4](https://github.com/hyphacoop/go-dasl/issues/4).
- Upstream [v2.9.3 panic fix](https://github.com/fxamacker/cbor/releases/tag/v2.9.3), [v2.9.6 correctness fixes](https://github.com/fxamacker/cbor/releases/tag/v2.9.6), and [typed-map fix #855](https://github.com/fxamacker/cbor/pull/855).
