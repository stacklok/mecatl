# Extension records and optional materialization

This is a future implementation direction for the isolated session-history kernel,
not shipped behavior or authorization to add an extension registry. It captures
how third-party records could appear alongside core session facts without taking
part in the session lifecycle. Exact APIs and the union representation remain open.

## Authoritative bytes, optional interpretation

An extension owner should be able to append a versioned record and retrieve it
from a snapshot in its original chronological position. A reader without the
owner's codec should still be able to inspect the kind and schema version and
export the exact payload bytes. A reader with the codec could also obtain a typed
interpretation without repeatedly decoding the payload.

The existing history envelope already carries `Kind`, `KindVersion`, and opaque
`Payload` bytes, and preserves unknown kind/version pairs. Session decoding is
stricter: it accepts a closed set of core kinds and one supported version. Those
boundaries are implemented in
[`history/format/record.go`](../../../../engine/kernel/history/format/record.go)
and [`session/record.go`](../../../../engine/kernel/session/record.go).
Exposing extension records through session snapshots therefore requires an
explicit session contract, not just a different memory layout.

The proposed backing representation combines the canonical encoded envelope with
an optional complete decoded value. The envelope remains authoritative for
identity, persistence, and opaque export. Decoding or cache population must not
rewrite retained bytes through a codec's re-encoding. Canonical encoding rules
for newly authored extension payloads belong to their versioned owner contract;
the history envelope must not silently impose the core session payload schema.

Proposed and accepted facts should retain distinct handles while using the same
private materialized representation. Materialization establishes local validity,
not accepted membership or legal lifecycle ordering. A snapshot publishes accepted
handles only after verified application. Validation-derived caches belong in
admission sidecars rather than fields on the canonical materialized fact. See
[Snapshots, ownership, and cloning](ownership.md) for the related stable-view and
publication design.

## A registry supplies interpretation, not lifecycle authority

A candidate registry maps each exact kind/schema-version pair to owner-supplied
canonical encoding, bounded decoding/validation, and typed-value ownership
operations. Packages could provide registrations and typed access helpers; the
host would compose an explicit registry and freeze it before session use. This
avoids behavior that depends on mutable process-global maps or import-time
registration. Duplicate registrations and attempts to override core kinds should
fail during composition.

Core session records should keep first-class typed snapshot accessors and explicit
lifecycle interpretation. Registering an extension codec grants no ability to
change run state, resolve a pending model request, satisfy a tool-call obligation,
or alter input coverage. Extension records consume retention and resource budgets
and preserve order, but the core lifecycle reducer ignores their payloads.

An unknown extension kind and an unsupported core lifecycle version require
different treatment. An opaque extension can remain visible without affecting
reconstruction. Silently skipping an unfamiliar core transition could reconstruct
incorrect session state. Before implementation, define a reserved namespace or
another explicit classification rule that distinguishes the two independently of
whether a codec happens to be registered. Also define where extension additions
are legal; lifecycle neutrality alone does not grant append authority or decide
whether records can be added before creation, between runs, or during pending work.

## Cached values must preserve snapshot guarantees

An ordinary Go interface holding a concrete DTO pointer is a candidate for the
single decoded-value slot. Type assertions or type switches recover the variant;
unsafe pointer casts are unnecessary. This would permit extension-owned types
without declaring a field for each type inside the session package. It is not a
selected replacement for the current by-value union: compare retained size, GC
scanning, allocation and copying costs, and direct-view traversal before choosing.
The internal representation should not leak into persistence or public APIs.

A cached pointer cannot be exposed as mutable shared storage. The codec contract
needs either immutable typed handles or detached exports, including ownership of
nested slices and pointers. A private closed interface may suit core variants;
extension participation needs a deliberate ownership contract rather than a
marker method that merely asserts immutability.

No registered codec should still mean successful opaque access. A failed optional
interpretation should leave the raw record available and report a typed-access
error. Required validation is a different operation: failure can reject admission
or loading. The registry API must distinguish these roles before deciding whether
decoding is eager, lazy, or mandatory. Codec availability must not accidentally
change the meaning of an accepted core lifecycle fact.

Decoding needs explicit allocation/work limits in addition to payload-byte bounds.
Owner-provided in-process codec code remains trusted code, not a sandboxed plugin.
If materialization is lazy, concurrent snapshot readers require synchronized cache
initialization and a defined failure-caching policy. Typed interpretation also
provides no authorization, secret scrubbing, or safe-display guarantee.

## Scope of the current simplification

Unify core materialization without building a registry now. Keep the encoded
envelope independent of decoded variants, preserve the proposed/accepted handle
distinction, and avoid making the current union layout a public contract. This
leaves room for registered materialization without requiring extension semantics
in the current refactor.

Revisit this note with a concrete extension owner. Settle kind classification,
append eligibility, codec and ownership contracts, validation versus optional
interpretation, cache lifetime and budgets, and the typed-access API together.
Qualification should include opaque access without a codec, unchanged canonical
bytes, unsupported core versions, duplicate registration, decode failures,
concurrent reads, mutation isolation, and equivalent lifecycle reconstruction with
and without extension codecs.
