# Content-addressed history format

The selected design represents history as a linear chain of content-addressed
commits, each containing an atomic, ordered group of records. This report
explains the identity and representation choices; the [codec and DTO report](codecs-and-dtos.md)
explains their Go implementation boundary. These are design decisions, not a
claim that the format or storage implementation has shipped.

## Content identity is separate from acceptance and authority

A `HistoryID` identifies one append-only collection. A commit CID identifies
exact encoded content, including its parent reference. The accepted head
identifies that history's accepted tip. A correctly hashed object can exist
without ever being accepted: acceptance belongs to the history/storage protocol,
not to a mutable field in the hashed object.

Including the parent makes changes to a predecessor change descendant identities.
The same proposal against the same parent has the same CID before submission,
which is useful for append retries and resolving uncertain outcomes. Object
lookup alone cannot establish acceptance or rejection; whether the CID replaces
a separate retry identifier belongs to the append/resolve contract.

Storage operations remain explicitly scoped by HistoryID for permissions,
sharding, and head selection. Neither a hash nor placement in a shard establishes
authorization or history membership. The [kernel contract](kernel.md) retains
acceptance gates and reconstruction from accepted facts.

## Bind the history at the root

The first ordinary commit is also the root; there is no empty genesis object or
special semantic root record. An empty history has no accepted commit.

| Commit | Canonical contents |
| --- | --- |
| Root | Format version, HistoryID, null parent, nonempty ordered records |
| Successor | Format version, parent commit CID, nonempty ordered records; HistoryID omitted |

Binding HistoryID once avoids repeating it in every commit. Parent hashes carry
that binding transitively. HistoryID is present exactly when parent is null;
omission and null are distinct encodings.

A detached successor does not directly identify its history. Membership requires
ancestry to the root or an already verified ancestor with trusted membership.
Append processing has a history scope and accepted parent; full reconstruction
can verify the chain from the root. Copying an object between shards cannot
change that binding.

No automatic timestamp, random nonce, writer identity, or operational storage
metadata is part of the selected envelope. Authoritative domain facts belong in
records; backend observations can remain outside identity bytes.

## Commits group reusable records

Linking individual records instead of commits would make record identities
independent of batching. Commit-level linkage was selected because the storage
protocol needs atomic ordered groups and no separate record CID requirement was
identified. A commit CID plus record index can identify a record when needed.
Changing the grouping changes commit identity; grouping is persisted information,
not a transient batching hint.

Records remain reusable DTOs without linkage fields. Their fields are `Kind`,
`KindVersion`, and `Payload`. `KindVersion` versions the kind's payload schema,
not a revision of an individual record instance. Third-party kind namespacing
and compact kind identifiers are deferred design scope, not an extension
registration framework.

Payloads are required opaque byte strings and need not contain CBOR. Empty bytes
are allowed; null is rejected. Nil and empty Go slices both encode as an empty
byte string. Record owners define payload schemas and interpretation; a shared
codec is a useful default rather than a restriction on opaque content.

Unknown kind/version pairs retain their exact payload bytes. Preserving and
hash-verifying them does not make them safe to replay or display. Missing
semantics cannot silently count as equivalent reconstruction, and private model
input or provider replay cannot become public merely because records use a
reusable DTO. Authorized projections remain separate from persistence values.

## Canonical bytes are a logical storage contract

History owns the canonical commit encoding used for hashing. Persistence
adapters choose their physical representation as long as they can reconstruct
the exact hashed bytes. A backend can store one blob, compress data, split
commits or records into rows, or build search indexes. Those layouts and indexes
do not become alternative authorities or hash inputs.

Letting each adapter choose identity bytes would make CIDs backend-dependent.
Changing those bytes also changes descendant parent references. Separating
canonical identity from physical storage permits backend optimization without
redefining the history.

The initial profile is CIDv1, DAG-CBOR codec `0x71`, and SHA-256. Public CIDs are
Mecatl-owned opaque, comparable values; library-specific CID types stay inside
the codec boundary. Standard CIDs and a narrow DAG-CBOR codec do not require
adopting the IPLD graph/schema/storage stack, ATProto services, or Git's branch
and merge model.

### Why deterministic CBOR

CBOR provides native byte strings and integers in a compact structured format.
Opaque tool arguments and provider replay can retain exact bytes, including
representation-sensitive JSON spelling. General CBOR permits alternate integer
encodings and map orders, so a deterministic profile and explicit application
presence rules are necessary. Noncanonical authoritative input is rejected
rather than silently normalized into another identity.

DAG-CBOR supplies an established content-addressing profile with string map keys
and native CID links. A CID link uses tag 42 around a byte string containing a
leading zero byte followed by the binary CID. DRISL closely aligns for these
envelope values and uses the same `0x71` identifier; it does not require a new
Mecatl codec identifier. Profile names alone do not guarantee strict decoding;
[codec validation](codecs-and-dtos.md#strictness-belongs-to-the-complete-codec)
remains an implementation responsibility.

The main alternatives were canonical JSON (JCS), Protobuf, and a custom binary
envelope. JCS offers readable, broadly supported data, but needs a binary
convention and uses an IEEE-754 number model; ordinary Go JSON marshaling is not
JCS. Protobuf's deterministic marshaling is not a stable canonical encoding
across implementations and time. A custom envelope would leave Mecatl owning a
format specification without solving shared payload tooling. Canonical Cap'n
Proto adds a compiler/schema ecosystem without a demonstrated need for its
access model; MessagePack and ASN.1 DER offered no compensating fit advantage.

### Envelope evolution and compact keys

Commit fields are `FormatVersion`, `HistoryID`, `Parent`, and `Records`.
`FormatVersion` versions the canonical commit/record envelope, not database
compression or physical layout. The selected starting version is 1; unsupported
envelope versions and unknown commit/record envelope fields are rejected.
Versions are positive integers within the signed-64-bit range.

Compact text keys retain the string-keyed DAG-CBOR model while reducing repeated
names. Commit keys are `v`, `h`, `p`, and `r`; record keys are `k` for kind,
`v` for kind version, and `p` for payload. These maps are separate, so reuse
across maps is unambiguous. Kind names are nonempty UTF-8 strings; no namespace
grammar is imposed by the envelope.
Descriptive Go names need not become long wire keys. Integer labels leave the
chosen string-keyed model; positional arrays make evolution more restrictive.

Human-readable inspection is a one-way projection, not another hashing input.
It needs explicit redaction, truncation, and opaque-payload handling rather than
incidental full-record `String` or logging output.

## A proof index can remain derived

A Merkle Mountain Range can provide compact inclusion proofs over commit CIDs,
where a parent chain requires walking intervening commits for distant ancestry.
It does not make full state reconstruction sublinear or prove acceptance from an
arbitrary accumulator root. Proofs across a trust boundary need an authenticated
root and size, with an explicit checkpoint/acceptance contract.

A rebuildable index can be added without rewriting commit CIDs. Keeping it
derived avoids introducing another authoritative structure before there is a
concrete proof requirement.

## References

- [CBOR, RFC 8949](https://www.rfc-editor.org/rfc/rfc8949.html), [JCS, RFC 8785](https://www.rfc-editor.org/rfc/rfc8785.html), and [DAG-CBOR](https://ipld.io/specs/codecs/dag-cbor/spec/).
- [Protobuf serialization is not canonical](https://protobuf.dev/programming-guides/serialization-not-canonical/) and [Cap'n Proto canonicalization](https://capnproto.org/encoding.html#canonicalization).
- [Git objects](https://git-scm.com/book/en/v2/Git-Internals-Git-Objects), [DRISL](https://dasl.ing/drisl.html), and [ATProto repositories](https://atproto.com/specs/repository).
