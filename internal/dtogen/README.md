# DTO operation generator

`dtogen` generates compositional, typed CBOR operations and whole-value
`Clone` and `Equal` methods, and concrete DTO-backed view accessors from Go
declarations. This contributor reference
owns the supported declarations, tags, registry, and generation workflow. The
[runtime package](../../engine/kernel/codec/doc.go) owns operational accounting,
input ownership, and low-level CBOR primitives. When changing the generator,
follow its [implementation guidance](AGENTS.md).

## Declare an owner

Each owner package declares one literal `go:generate` command and marks its local,
nongeneric named roots with `//dtogen:include` directly on their type declarations.
`-emit` explicitly selects any nonempty combination of `cbor`, `clone`, and `equal`,
or the standalone `view` capability. Value/codec declarations select DTO roots;
view-only declarations select local handwritten view handles. Only selected methods
are emitted. CBOR methods cover persisted local named types;
Clone/Equal cover the complete local named closure, including excluded state.
Imported named types require their own owner declaration with the capabilities
needed by the consumer. The generator follows that declared owner closure and
writes prerequisite artifacts before the requesting package.
`kernel/value.Value` and `kernel/value.Map` are explicit handwritten primitive
owners: the generator verifies their exact package/type identities, source
provenance, and operation signatures without traversing their private recursive
representation. Other handwritten methods do not opt a type into generation.

```go
//go:generate go -C ../../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/codec/internal/fixture -emit cbor,clone,equal -out zz_cbor_generated.go

// Catalog exercises nested containers and imported DTOs.
//
//dtogen:include
type Catalog struct {
    // ...
}
```

For another package, adjust `-C` to reach the repository root, `-dir` to its
repository-relative directory, and `-out` to a package-local, non-test `.go`
filename. Mark roots only: value operations follow the local named closure
automatically. A package has one generated output file containing its selected
local closure.

Markers are read only from active compiled handwritten files, excluding tests,
generated output and build-excluded files. Put each marker in the type's doc
comment (inside a grouped declaration, on the individual type). Duplicate,
malformed, misplaced or ambiguous markers fail generation; aliases cannot be
roots. Roots are sorted for deterministic output.

Packages without include markers can instead select roots with a nonempty
comma-separated `-types` list. Its order and repetitions do not affect output.
Combining `-types` with include markers is an error; explicit `-types=` is invalid.

Commands use this form, with `-types <roots>` only for explicit-list selection:

```text
go -C <repository> run ./internal/dtogen/cmd/dtogen -dir <package> -emit cbor,clone,equal -out <filename>
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

Generation renders and typechecks all selected packages before installing any
output. Validation, rendering, and typechecking failures leave existing output
bytes unchanged. Output files must be marked regular files, not symlinks. Each
replacement is atomic, but installation across files is not transactional: a
filesystem failure can leave earlier files replaced. Rerun generation after
resolving the failure. Concurrent editing or generation of one artifact is
unsupported. The generator provides neither filesystem confinement nor
crash-durability guarantees.

Check mode derives targets from the same directives, compares expected bytes,
and checks the inventory, including untracked Go files. Missing and stale
outputs, as well as marker-bearing artifacts for removed targets, fail the
check. Remove obsolete artifacts explicitly when retiring a declaration.
Source discovery skips `.git`, `.scratch`, `node_modules`, and `vendor`, and
never traverses directory symlinks.

## DTO-backed views

A destination owns one view-only declaration and artifact, independently of the
DTO source owner. Mark each selected local view type with `//dtogen:include`;
packages without markers can instead list handles with `-types`. The generator
does not scan names ending in `View` or generate value operations on their backing:

```go
//go:generate go -C ../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/session -emit view -out zz_views_generated.go
```

Keep layouts handwritten, with private, nonembedded fields. Each selected view
must have a private `value *DTO` field pointing to a named struct. Additional
private fields retain enclosing context.

The DTO owns one default field policy, independent of CBOR, Clone and Equal:

| DTO field tag | Default view behavior |
| --- | --- |
| Untagged | Generate a safe same-named getter |
| `view:"-"` | Omit the field |
| `view:"manual"` | Require a directly declared same-named handwritten value-receiver getter on every selected view of this DTO |

Unknown, malformed, duplicate and contradictory tags fail generation. Handwritten
getters take no arguments and return at least one value. Pointer receivers and
promoted methods do not satisfy manual policy. A handwritten method colliding
with an untagged field fails rather than silently replacing its generated getter.
Extra derived/navigation methods need no separate classification. Handwritten
documentation stays authoritative for manual methods; review and test their
ownership, zero, presence and lifetime contracts.

```go
// In the DTO owner:
type Entry struct {
    Label string
    Data []byte
}

// In the consuming view package:
// EntryView retains immutable entry backing.
//
//dtogen:include
type EntryView struct { value *format.Entry }
```

Mechanical methods return the following concrete types:

| Backing field | Accessor result |
| --- | --- |
| Boolean, numeric, string (including named scalar) | Same scalar type |
| Pointer to scalar | `(T, bool)` |
| `[]byte` | `views.Bytes` |
| Required by-value struct | Selected context-free child view |
| Pointer to selected struct | `(ChildView, bool)` |
| Slice of selected by-value structs | `views.SliceView[DTO, ChildView]` |
| String-keyed map of safe scalars | `views.MapView[Key, Scalar, Scalar]` |
| String-keyed map of pointers to selected context-free DTOs | `views.MapView[Key, *DTO, ChildView]` |
| `value.Value` / `value.Map` | `value.View` / `value.MapView` |

Map getters retain the original map and adapt present entries lazily, in
unspecified order. Missing lookups never invoke the adapter. Pointer-valued DTO
maps borrow existing pointees; by-value DTO entries requiring wrappers and
context-bearing children require handwritten navigation. Required nil pointer
entries remain invalid for CBOR.

Pointers to bytes/slices additionally return a presence bool. Zero views are safe;
optional fields preserve present zero/empty values. Byte and collection navigation
is O(1) and borrows backing without cloning siblings. Byte `Copy` alone detaches;
see the [views contract](../../engine/kernel/views/doc.go) for borrowing and
lifetime requirements.

A view containing only `value *DTO` is context-free and can be constructed by
generated child getters. Selecting two context-free views for one DTO is
ambiguous and fails generation. Navigation into a child with additional context
must be handwritten or omitted. Handwritten collection adapters may capture
required context, which must remain stable along with the backing. Unsupported
untagged fields fail generation; use manual policy for opaque immutable values
that can safely be returned directly.

Regenerate and check after changing selected types, backing layouts, field tags,
or source documentation. Generated accessor comments include the DTO's type and
field documentation.

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
| `value.Value`, `value.Map` | Native structured values, delegated to the exact primitive owner |

The [structured-value package](../../engine/kernel/value/doc.go) supports explicit
null, booleans, UTF-8 text, exact integers in `[-2^63, 2^64-1]`, arrays, and
string-keyed maps. DTO fields accept ordinary mutable authoring values; admission
checks bounds before detaching storage. `value.ParseJSON` converts one complete
supported JSON root into mutable `value.Value` storage. It follows `encoding/json`
Unicode replacement behavior, rejects duplicate decoded object keys, and rejects
decimal/exponent number tokens. `value.ParseJSONObject` is its object-root wrapper
and returns `value.Map`.

```go
payload, err := value.ParseJSON([]byte(`[null,9007199254740993]`), limits)
object, err := value.ParseJSONObject([]byte(`{"enabled":true}`), limits)
```

`Value`, `Map`, `View`, and `MapView` marshal native JSON through the same
local-depth-128 bounded traversal. An admitted view can be passed directly to
`json.Marshal` without exporting its backing:

```go
import "encoding/json"

view, err := payload.Admit(limits)
payloadJSON, err := json.Marshal(view)
mapView, err := object.Admit(limits)
objectJSON, err := json.Marshal(mapView)
```

JSON is an interchange representation. `codec.Encode` continues to produce the
canonical CBOR bytes used for persisted value identity.

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
| `cbor:"name,omitempty"` | Pointer field: nil means absent and nonnil zero means present; or Boolean (including a named Boolean): false means absent and true means present. Missing Boolean fields decode as false. |
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
invalid. Optional nil pointers are omitted. Explicit CBOR null is valid only
within the structured-value primitive; it does not represent an ordinary DTO
pointer. Required nil slices and maps encode as empty collections; decoding initializes
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
invalid UTF-8, integer overflow, trailing data, floats, indefinite forms,
and unsupported simple values. Null is accepted only by structured-value
primitives. Opaque bytes remain uninterpreted.

`Clone` detaches mutable storage, including excluded fields, preserving nil and
empty collections. `Equal` includes excluded state, compares map membership and
ordered slices, treats nil and empty collections as equal, and distinguishes
absent pointers from present zero values. Neither operation validates values or
uses operational resource limits. Structured-value owners have a fixed local
depth ceiling of 128, with their root at zero, independently of enclosing wire
depth. Their `Clone` and `Equal` panic with `value.ErrDepthExceeded` if traversal
crosses that ceiling; generated parents propagate the panic. Equality can return
on a shallow mismatch without inspecting deeper branches. Fallible validation,
admission, JSON conversion, and decoding return errors instead.

`contentid.CID` is an owner-generated `,cid` type. Use `contentid.Sum(encoded)`
for an exact-byte identity. To check bytes against an expected identity, decode
the original bytes and call `contentid.Verify(encoded, expected)`. Verify does
not validate CBOR or establish authority.

## Diagnostics and focused checks

Load errors report handwritten Go diagnostics. Schema errors identify the owner,
type, and field, including missing mapping tags, unsupported shapes, invalid
literal caps, missing owner declarations, malformed validators, and generated
name collisions. Fix the declaration and regenerate; do not edit a marked
output by hand.

From the repository root:

```sh
GOWORK=off go test ./internal/dtogen/...
task generate:data:check
cd engine && GOWORK=off go test -race ./kernel/codec/...
```
