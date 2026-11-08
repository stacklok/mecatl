# DTO operation generator

`dtogen` generates compositional, typed CBOR operations and whole-value
`Clone` and `Equal` methods, and concrete DTO-backed view accessors from Go
declarations. This contributor reference
owns the supported declarations, tags, registry, and generation workflow. The
[runtime package](../../engine/kernel/codec/doc.go) owns operational accounting,
input ownership, and low-level CBOR primitives. When changing the generator,
follow its [implementation guidance](AGENTS.md).

## Declare an owner

Each owner package declares one literal `go:generate` command. Its `-types`
list selects locally defined, nongeneric named roots. `-emit` explicitly selects
any nonempty combination of `cbor`, `clone`, `equal`, and `view`; order does not matter.
`-types` selects value/codec roots; `-views` selects projections independently and
is required exactly when `view` is selected. View-only destinations omit `-types`.
Only selected methods are emitted. CBOR methods cover persisted local named types;
Clone/Equal cover the complete local named closure, including excluded state.
Imported named types require their own owner declaration with the capabilities
needed by the consumer. The generator follows that declared owner closure and
writes prerequisite artifacts before the requesting package.
It never treats compatible handwritten methods as an opt-in declaration.

```go
//go:generate go -C ../../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/codec/internal/fixture -types Catalog,Sample,Link,Links -emit cbor,clone,equal -out zz_cbor_generated.go
```

For another package, adjust `-C` to reach the repository root, `-dir` to its
repository-relative directory, `-types` to its comma-separated local roots, and
`-out` to a package-local, non-test `.go` filename. Root order and repetitions
do not affect the output. A package has one generated output file, which
contains its selected local closure.

Commands use exactly this form:

```text
go -C <repository> run ./internal/dtogen/cmd/dtogen -dir <package> -types <roots> -emit cbor,clone,equal -out <filename>
```

Arguments must be literal and unquoted. Variable expansion, `-command` aliases,
and other invocation forms are unsupported so that `go generate` and the
freshness check read the same source registry.

Run the owning tasks from the repository root:

```sh
task generate:data        # also called by task generate
task generate:data:check  # nonmutating inventory and byte check used by CI
```

`generate:codecs` and `generate:codecs:check` remain forwarding compatibility tasks.

The loader uses the declaring package's module with `GOWORK=off`. It reads
source declarations without executing package initialization. A direct
invocation also discovers and generates declared prerequisite owners. Missing,
stale, or syntactically broken marked outputs can therefore be regenerated when
handwritten code already calls generated methods.

Generation renders and typechecks the complete selected owner closure before
installing any output. Each replacement is atomic. It refuses unmarked, symlink,
or nonregular output files. The old `cborgen` marker is accepted only for the seven
pre-rename `zz_cbor_generated.go` artifacts in the engine module; check mode still
rejects their obsolete bytes. New owners must not use that legacy marker. A
filesystem failure during installation may leave earlier files replaced; rerunning
repairs the closure. Concurrent editing or generation of one artifact is unsupported.
The generator does not provide filesystem confinement or crash-durability guarantees.

Check mode derives targets from the same directives, compares expected bytes,
and checks the inventory, including untracked Go files. Missing and stale
outputs, as well as marker-bearing artifacts for removed targets, fail the
check. Remove obsolete artifacts explicitly when retiring a declaration.
Source discovery skips `.git`, `.scratch`, `node_modules`, and `vendor`, and
never traverses directory symlinks.

## DTO-backed views

A destination owns one declaration and artifact, independently of the DTO source
owner. A view-only destination does not generate value operations on its backing:

```go
//go:generate go -C ../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/session -emit view -views views.json -out zz_views_generated.go
```

The session package uses this declaration with its complete `views.json` mapping.
Keep the view layouts handwritten, with private, nonembedded fields:

```go
import (
    "github.com/stacklok/mecatl/engine/kernel/session/format"
    "github.com/stacklok/mecatl/engine/kernel/views"
)

type MediaView struct { value *format.Media }

// InlineData reports tag-selected presence, including present-empty inline data.
// The returned bytes borrow stable backing; Copy explicitly detaches them.
func (v MediaView) InlineData() (views.Bytes, bool) {
    if v.value == nil || v.value.Source != format.MediaSourceInline {
        return views.Bytes{}, false
    }
    return views.BorrowBytes(v.value.InlineData), true
}
```

`-views` names a package-local JSON file containing an array of projections. The
`backing` field names the private pointer in the local `view` layout; its Go type
selects the backing DTO (including its normal Go import/alias resolution). Each
backing field must appear exactly once. Choose one of these entries:

```json
[
  {
    "view": "MediaView",
    "backing": "value",
    "fields": [
      {"field": "MIMEType", "method": "MIMEType"},
      {"field": "Source", "exclude": true},
      {"field": "InlineData", "method": "InlineData", "override": "func() (views.Bytes, bool)"}
    ]
  }
]
```

This abbreviated mapping illustrates syntax; a real Media projection must also
classify every other field. Ordinary emitted fields ignore CBOR tags. An override
names a handwritten method with an **exact Go func type**, evaluated in the view
type declaration's file scope. Its value receiver, parameters, results and
variadic status must match; a different semantic return shape is allowed.
For example, a tag-selected URI override uses `"override":"func() (string, bool)"`.
Missing, stale, duplicate or unknown targets and unmarked method collisions fail.
Handwritten override documentation wins; the generator never emits its body/docs.
Signature validation proves the declared target and type, not the body's behavior;
review and test handwritten immutability, zero, presence and lifetime contracts.

Declare context-owned methods that have no single field projection separately:

```json
"context": [{"method":"RequestID", "signature":"func() ModelRequestID"}]
```

All handwritten view methods must be classified as overrides or context methods.
Additional private layout fields retain enclosing scope. Such a view can have
mechanical accessors, but the generator will not construct it as a child or a
collection element: handwritten navigation must carry that context. For example,
exclude an `Inputs` field and retain its scoped iterator as a context method, or
explicitly adapt that field through a handwritten override. No generated adapter
captures scope or materializes an intermediate slice.

Mechanical methods return the following concrete types:

| Backing field | Accessor result |
| --- | --- |
| Boolean, numeric, string (including named scalar) | Same scalar type |
| Pointer to scalar | `(T, bool)` |
| `[]byte` | `views.Bytes` |
| Required by-value struct | Local mapped child view |
| Pointer to mapped struct | `(ChildView, bool)` |
| Slice of mapped by-value structs | `views.SliceView[DTO, ChildView]` |

Pointers to bytes/slices additionally return a presence bool. Each zero view is
safe, with zero/false for missing optional values; present zero/empty values stay
present. Required child navigation takes the address of retained backing. Byte
and collection navigation is O(1), borrows stable backing, and does not clone
siblings. Byte `Copy` alone detaches. A context-free layout gets a private
`dtogenView<Type>` constructor that also serves as the noncapturing slice adapter.
Multiple context-free projections of the same child DTO are ambiguous and fail.
Unsupported shapes must be explicitly overridden or excluded, never exported as
mutable values. In particular, an immutable identity struct can use an explicit
scalar-returning override rather than exposing its storage.

Generated comments retain source type/field contracts, including provisional
notes, explicitly labelled as DTO semantics rather than accessor mutability.
Accessor comments add zero/presence, retained-backing, lifetime, copy and complexity
contracts. A fingerprint includes the mapping, backing shapes and source docs,
including excluded/overridden fields. A structural pointer-conversion assertion
also detects stale backing shapes at compile time where all types/fields are
accessible. Source/mapping/docs changes still require deterministic regeneration
and check even when an assertion is unavailable or a conversion ignores tags.

Absent or broken marked artifacts bootstrap in one invocation: typed load-only
view signatures join the value/codec signatures, then the complete selected owner
closure is rendered and strictly typechecked before any installation. This uses
`GOWORK=off` and never executes owner initialization, overrides, or other code.

## Supported Go types

The table and tag rules below describe the `cbor` capability. Without `cbor`,
Clone/Equal accept untagged declarations and ignore serialization tags, caps, and
custom marshal methods. They additionally support machine-sized integers, floats,
complex numbers, nonbyte arrays, scalar map keys, pointer chains, and embedding.
Both value operations still reject interfaces, functions, channels, generic and
recursive graphs, defined pointer roots, blank fields, and non-scalar map keys.
Local private fields participate; imported private state stays behind an explicit
owner's generated methods. No operation infers ownership from method names alone.

| Go type | Persisted representation |
| --- | --- |
| `bool` | Boolean |
| `string` | UTF-8 text string |
| `int8`, `int16`, `int32`, `int64` | Minimal signed integer, checked against destination range |
| `uint8`, `uint16`, `uint32`, `uint64` | Minimal unsigned integer, checked against destination range |
| `[]byte`, `[N]byte` | Byte string; fixed arrays require an exact length |
| Other slices | Ordered arrays |
| Structs | Maps with explicitly tagged text keys |
| Maps with string-underlying keys | Text-key maps with supported values |
| Pointers | Indirection to a supported nonpointer value |

A local defined type may use any supported underlying type. An alias uses its
underlying owner's generated methods and cannot be a registry root. Imported
named types compose through their declared owner's methods. Named byte elements
are supported, except that packed named bytes cannot declare a validator.

Generation rejects machine-sized integers (`int`, `uint`, `uintptr`), floats,
interfaces, functions, channels, nonbyte fixed arrays, nonstring map keys,
double pointers, embedding, generic types, defined pointer types, and recursive
type graphs on persisted edges. Clone/Equal still visit every excluded field;
excluded subtrees need no CBOR mapping and may use the additional non-CBOR shapes.
A persisted private field is supported only in the package that owns and
generates its type. An inaccessible imported field is rejected.

Custom CBOR, text, and binary serialization contracts are rejected rather than
called. This includes byte and streaming `MarshalCBOR`/`UnmarshalCBOR`, byte
`MarshalText`/`UnmarshalText`, and byte `MarshalBinary`/`UnmarshalBinary`.
Other domain methods do not affect generation.

## Tag reference

Tags use Go struct-tag syntax. Duplicate `cbor` or `cborlimit` entries and
malformed struct tags are rejected. Unrelated tags do not affect mapping.

### `cbor`

Ordinary struct fields use this grammar:

```text
mapping = "-" | name | name ",omitempty"
```

`name` is nonempty valid UTF-8, contains no comma, and is neither `-` nor
implicit. Names are exact, case-sensitive, and unique within an ordinary struct.

| Form | Meaning |
| --- | --- |
| `cbor:"name"` | Required field, including zero and empty values |
| `cbor:"name,omitempty"` | Pointer field only; nil means absent, and nonnil zero means present |
| `cbor:"-"` | Excluded from persistence; it remains part of `Clone` and `Equal` |
| `cbor:",value"` | Transparent representation field |
| `cbor:",cid"` | Fixed DAG-CBOR CID-link representation field |

A `,value` type has exactly one persisted representation field. All other
fields must be explicitly excluded. The wrapper has no map node: generated
methods compose its represented value at the same depth. A `cborlimit` on that
field follows the represented string, byte-string, array, or map kind.

A `,cid` type has exactly one persisted `[36]byte` representation field and no
field cap. It emits the fixed DAG-CBOR tag-42 link with a zero prefix. The
runtime accounts the framing and returns raw bytes; the type owner validates the
CID profile. `,cid` is not a general tag facility.

Required nil pointers, including nil pointer elements in slices or maps, are
invalid. Optional nil pointers are omitted. Explicit CBOR null is invalid.
Required nil slices and maps encode as empty collections; decoding initializes
empty collections. Excluded subtrees need no mapping tags, but their storage
still participates in `Clone` and `Equal`. A budget tag in an excluded subtree
is rejected.

### `cborlimit`

A persisted field can have one immediate literal resource cap:

```text
budget  = ("bytes" | "items") "=" decimal
decimal = ASCII digits representing a positive uint64
```

| Option | Applicable immediate value, after pointer indirection |
| --- | --- |
| `bytes` | UTF-8 text byte length, byte-slice length, or fixed byte-array length |
| `items` | Nonbyte slice element count or map pair count |

Struct maps, CID links, numeric and Boolean scalars, and excluded fields cannot
carry a cap. A cap applies only to the immediate representation, not descendants.
Generated code gives each child its own field cap or the positive machine
maximum. Runtime global limits still apply to every applicable value.

## Local validation

A named type can declare one local value-receiver validator immediately above
its type declaration:

```go
//dtogen:validate validateCBOR
type ResourceID struct {
	value string `cbor:",value" cborlimit:"bytes=25"`
}

func (v ResourceID) validateCBOR() error {
	return nil
}
```

The method must have signature `func (T) name() error`. It runs after generated
child traversal on `CheckCBOR`, and after child reads but before `ReadCBOR`
assigns its receiver. It must be local, pure, non-normalizing, and
non-recursive. It must not perform CBOR traversal or operational resource
policy. Generation rejects unknown, duplicate, malformed, or ambiguous
directives.

## Whole-type overrides

Attach one directive per handwritten operation to its defined type:

```go
import (
    "bytes"
    "slices"
)

//dtogen:override Clone
//dtogen:override Equal
type Payload struct { Data []byte }

func (v Payload) Clone() Payload { return Payload{Data: slices.Clone(v.Data)} }
func (v Payload) Equal(other Payload) bool { return bytes.Equal(v.Data, other.Data) }
```

The implementations must be directly declared value-receiver methods with exactly
`func (T) Clone() T` and `func (T) Equal(T) bool` signatures. Each override requires
its corresponding `-emit` capability and a target in the selected local type
closure. Duplicate, misplaced, stale, missing, and malformed overrides fail before
installation; undeclared method collisions still fail. CBOR overrides are not
supported. Overrides do not relax the typed graph or bounded CBOR shape policies.

No default public method or traversal body is generated for an overridden
operation. Generated parents dispatch through the handwritten method, including
local and imported by-value, pointer, and slice children across owner packages.
The author remains responsible for the implementation's ownership and equality
semantics. Handwritten methods are retained during load-only bootstrap, including
when generated outputs are absent or broken.

## Generated API

With `-emit cbor,clone,equal`, every persisted local named type `T` receives:

```go
func (v T) CheckCBOR(b *codec.Budget, depth, fieldMax int) error
func (v T) AppendCBOR(dst []byte) []byte
func (v *T) ReadCBOR(r *codec.Reader, depth, fieldMax int) error
func (v T) Clone() T
func (v T) Equal(other T) bool
```

`CheckCBOR`, `AppendCBOR`, and `ReadCBOR` compose across owners using one
shared `Budget`, `Reader`, and output buffer. `fieldMax` is a positive cap for
the immediate wire value. It does not cap descendants. `CheckCBOR` checks both
field and global limits before allocation or copying, and then runs the owner's
local validator. `AppendCBOR` is a prechecked operation: its caller must have
checked the same stable value. It performs no validation or accounting.

`ReadCBOR` reads into fresh temporary storage and assigns the receiver only
after structural and local semantic success. On failure, the receiver and its
reachable storage remain unchanged. The reader's cursor and accounting do not
roll back, so discard it after an error. Nested reads do not reject trailing
bytes; root decoding does.

The whole-value helpers construct and validate shared operation state:

```go
encoded, err := codec.Encode(value, limits)
if err != nil {
	return err
}
decoded, err := codec.Decode[MyType](encoded, limits)
if err != nil {
	return err
}
```

`codec.Check` validates without allocating output. `codec.Encode` checks once,
then appends once, returning nil bytes on failure. `codec.Decode[T]` infers
`*T` as the generated decoding implementation, rejects trailing bytes, and
returns zero `T` on every failure. All positive `codec.Limits` fields are
required. Global limits are validated once per whole-value operation.

Canonical encoding uses definite lengths, shortest headers, and text keys sorted
by UTF-8 byte length and then lexical bytes. Decoding rejects duplicates,
unordered keys, unknown and missing fields, malformed or nonminimal input,
invalid UTF-8, integer overflow, null, trailing data, floats, indefinite forms,
and unsupported simple values. Opaque bytes remain uninterpreted.

`Clone` detaches mutable storage, including excluded fields, preserving nil and
empty collections. `Equal` includes excluded state, compares map membership and
ordered slices, treats nil and empty collections as equal, and distinguishes
absent pointers from present zero values. Neither operation validates values or
uses resource limits.

`contentid.CID` is an owner-generated `,cid` type. Use `contentid.Sum(encoded)`
for an exact-byte identity. To check bytes against an expected identity, decode
the original bytes and call `contentid.Verify(encoded, expected)`. Verify does
not validate CBOR or establish authority.

## Diagnostics and focused checks

Load errors report handwritten Go diagnostics. Schema errors identify the owner,
type, and field, including missing mapping tags, unsupported shapes, invalid
literal caps, missing owner declarations, malformed validators, and generated
name collisions. Fix the declaration and regenerate; do not edit a marked
output by hand. Generation errors preserve prior output bytes.

From the repository root:

```sh
GOWORK=off go test ./internal/dtogen/...
task generate:data:check
cd engine && GOWORK=off go test -race ./kernel/codec/...
```
