# Persistence codec generator

`cborgen` generates typed CBOR codecs and whole-value `Clone`/`Equal` methods
from Go DTO declarations. This contributor reference owns the supported types,
struct tags, and generation workflow. The [runtime package](../../engine/kernel/codec/doc.go)
owns budget accounting and low-level decoding contracts.

## Declare and generate a DTO

Select locally defined, nongeneric struct roots in the package that owns them.
Every persisted field needs an explicit mapping tag:

```go
type Document struct {
    Label string            `cbor:"label" cborlimit:"bytes=256"`
    Notes []string          `cbor:"notes" cborlimit:"items=@max_notes"`
    Hint  *string           `cbor:"hint,omitempty"`
    Cache map[string][]byte `cbor:"-"`
}
```

Put a literal `go:generate` directive in a handwritten Go source file in that
package. The fixture package has this working declaration:

```go
//go:generate go -C ../../../../.. run ./internal/cborgen/cmd/cborgen -dir engine/kernel/codec/internal/fixture -types Catalog,Sample -out zz_cbor_generated.go
```

For another package, adjust `-C` to reach the repository root, `-dir` to its
repository-relative directory, and `-types` to its comma-separated roots. `-out`
is a package-local, non-test `.go` filename, without a leading `.` or `_`.
Root order and repetitions do not change output. Each package has one generated
output file containing all selected roots and one private `cborgen` implementation
type.

Declarations use exactly `go -C <repository> run
./internal/cborgen/cmd/cborgen -dir <package> -types <roots> -out <filename>`.
Arguments must be literal and unquoted; variable expansion, `-command` aliases,
and other invocation forms are unsupported by repository discovery. This keeps
the declaration executable by `go generate` and readable by the freshness check
without a second target registry.

Run these owning tasks from the repository root:

```sh
task generate:codecs        # also called by task generate
task generate:codecs:check  # nonmutating inventory and byte check used by CI
```

The Taskfile invokes the root-module generator across source modules. The
loader uses the declaring package's module with `GOWORK=off`, preserving the
remaining Go build context, including build flags and checksum verification.
It reads declarations without executing owner package initialization.

Generation validates and formats the complete artifact before replacing it
with a temporary-file/atomic-rename operation. It refuses existing unmarked,
symlink, or nonregular output files. Missing, stale, or syntactically broken
marked output can be regenerated even when handwritten code calls generated
functions and methods. Handwritten diagnostics still fail generation.
Concurrent editing or generation of the same artifact is unsupported; the
generator provides neither filesystem confinement nor crash-durability guarantees.

Checking derives targets from the same directives and compares expected bytes
and inventory, including untracked Go files. Missing or stale outputs and extra
marker-bearing files fail checking, including artifacts of removed targets.
Remove obsolete artifacts explicitly when retiring a declaration. Orphan checks
run only in check mode; they do not block generation of unrelated packages.
Source discovery skips `.git`, `.scratch`, `node_modules`, and `vendor`, and does
not traverse directory symlinks.

## Supported Go types

| Go type | Persisted representation |
| --- | --- |
| `bool` | Boolean |
| `string` | UTF-8 text string |
| `int8`, `int16`, `int32`, `int64` | Minimal signed integer, checked against destination range |
| `uint8`, `uint16`, `uint32`, `uint64` | Minimal unsigned integer, checked against destination range |
| `[]byte`, `[N]byte` | Byte string; fixed array requires exact length |
| Other slices | Ordered arrays |
| Structs | Maps with explicitly tagged text keys |
| Maps with string-underlying keys | Text-key maps with supported values |
| Pointers | Indirection to a supported nonpointer value |

Named equivalents and reachable aliases are supported. Byte-string elements can
have a named `uint8`-underlying type. Reachable imported DTOs are supported when
every field needed for whole-value cloning and equality is accessible. Imported
or alias roots cannot receive generated owner methods and are rejected.

Generation rejects machine-sized integers (`int`, `uint`, `uintptr`), floats,
interfaces, functions, channels, nonbyte fixed arrays, nonstring map keys,
double pointers, embedding, generic types, and recursive type graphs. These
restrictions also apply to excluded fields because `Clone` and `Equal` visit
the whole DTO. Local unexported fields can be excluded; unexported persisted
fields and inaccessible imported fields are rejected.

Persisted types must not implement custom CBOR, text, or binary serialization
contracts: `MarshalCBOR`/`UnmarshalCBOR` in byte or streaming form, or
`MarshalText`/`UnmarshalText` and `MarshalBinary`/`UnmarshalBinary` in byte form.
Unrelated domain methods with different signatures are allowed. Generated
operations never invoke `Validate`, custom `Clone`/`Equal`, or other owner hooks.

## Tag reference

Tags use Go struct-tag syntax. Duplicate `cbor` or `cborlimit` entries and
malformed struct tags are rejected. Unrelated tags are allowed but do not affect
mapping; there is no JSON fallback.

### `cbor`

The complete value grammar is:

```text
mapping = "-" | name | name ",omitempty"
```

`name` is nonempty valid UTF-8, contains no comma, and is not `-`. Names are exact,
case-sensitive, and unique within the struct. No implicit Go field names apply.
Unknown or repeated options, empty names, and missing mapping tags are errors.

| Form | Meaning |
| --- | --- |
| `cbor:"name"` | Required field, including zero and empty values |
| `cbor:"name,omitempty"` | Pointer field only; nil means absent, nonnil zero means present |
| `cbor:"-"` | Excluded from persistence; decode leaves the field zero |

Required nil pointers, including nil pointer elements in slices/maps, are invalid.
Required nil slices/maps encode as empty collections; decode initializes empty
collections. A nonnil pointer to a nil collection encodes a present empty
collection. Explicit CBOR null is rejected.

Excluded subtrees need no mapping tags, but their storage still participates in
`Clone` and `Equal`. Budget metadata anywhere in an excluded subtree is rejected
rather than silently ignored.

### `cborlimit`

A field may have one immediate resource maximum:

```text
budget  = ("bytes" | "items") "=" maximum
maximum = decimal | "@" identifier
identifier = [a-zA-Z_][a-zA-Z0-9_]*
```

`decimal` contains only ASCII digits and must represent a positive `uint64` value.
Leading zeros are accepted. Signs, whitespace, zero, overflowing literals, empty
references, expressions, unknown options, and repeated/comma-separated budgets
are rejected.

| Option | Applicable value, following pointer indirection |
| --- | --- |
| `bytes` | UTF-8 text byte length, byte-slice length, or fixed byte-array length |
| `items` | Nonbyte slice element count or map pair count |

Structs, scalar numbers, booleans, and excluded fields cannot carry these caps.
The cap applies only to that field's immediate length/count, not its subtree or
each element. Global and aggregate limits still apply and cannot be relaxed by
a field cap. A literal is built into generated code; `@name` resolves an exact,
case-sensitive key in `codec.Limits.Named`.

Every schema-referenced name for the selected root must have a positive runtime
value, even beneath an absent optional pointer. Each operation resolves its
finite reference set once before inspecting input. Unreferenced entries are
ignored. The dictionary must remain stable during the call; generated code does
not mutate, retain, copy, or globally cache it. Concurrent read-only reuse is safe.

## Generated API and acceptance

For each selected root `T`, generation adds:

```go
func CheckT(v T, limits codec.Limits) error
func EncodeT(v T, limits codec.Limits) ([]byte, error)
func DecodeT(data []byte, limits codec.Limits) (T, error)
func (v T) Clone() T
func (v T) Equal(other T) bool
```

Private helpers are numeric methods on one stateless implementation type per
output. Type-mapping comments identify the Go values they handle. Handwritten
collisions with generated entry points, owner methods, implementation names, or
required import/type identifiers fail generation rather than selecting another
name.

`CheckT` checks structure and operational policy before sorting or output
allocation. Supply every numeric `codec.Limits` field as positive; there are no
defaults or unlimited values. Before encoding, compose `CheckT` → owner semantics
→ `EncodeT`. Encoding repeats its own checks. On input, compose `DecodeT` → owner
semantics → acceptance. Failure returns nil encoded bytes or a zero DTO. Successful
results own persisted mutable storage and decoded strings.

Errors support `errors.Is` with `codec.ErrInvalidLimits`,
`codec.ErrLimitExceeded`, and `codec.ErrInvalidData`. Operational limits are
separate from schema and domain validity: tightening a cap can reject existing
values without changing encoded bytes. See the [runtime accounting contract](../../engine/kernel/codec/doc.go)
for conservative size estimates, allocation admission, and input lifetimes.

Encoding uses definite lengths, shortest headers, and text keys sorted by UTF-8
byte length then lexical bytes. Decoding rejects duplicates, unordered keys,
unknown/missing fields, malformed/nonminimal input, invalid UTF-8, integer
overflow, null, trailing data, floats, tags/CIDs, indefinite forms, and unsupported
simple values. Opaque byte contents remain uninterpreted. This strict subset is
not unrestricted DAG-CBOR or a drop-in reflective serializer. Changes to tags,
field presence, or accepted types require a stored-data compatibility assessment;
adding a field is not automatically compatible with existing strict decoders.

`Clone` detaches all mutable storage, including excluded fields, while preserving
nil/empty collections and pointer absence. It does not preserve capacity or
sharing topology. `Equal` includes excluded fields, compares map membership and
ordered slices, treats nil/empty collections equally, and distinguishes absent
pointers from present-zero values. Both operate without validation or limits,
including on codec-invalid values. `CheckT` ignores excluded storage and does not
bound a later whole-value clone. Callers own in-memory admission and input stability.

Content identity is opt-in through [`contentid`](../../engine/kernel/contentid/cid.go).
Use `contentid.Sum(encoded)` on emitted bytes. On input, compose `DecodeT` →
`contentid.Verify(original, expected)` → owner semantics → acceptance. Verify the
original input, never a reencoding; an unset expected CID is an error. The fixed
CIDv1/DAG-CBOR/SHA-256 helper hashes exact bytes without normalization, CBOR
validation, or size policy. Bound input first. Matching identity alone proves
neither semantic validity nor authority. The [compiled owner example](../../engine/kernel/codec/contentid_composition_test.go)
demonstrates this composition.

## Diagnostics and focused checks

Load errors report Go diagnostics from handwritten sources. Schema errors identify
the owning type/field and reason, such as a missing mapping tag, unsupported
integer, invalid field budget, or generated-name collision. Fix the declaration
and regenerate; do not repair marked output by hand or remove the marker to hide
staleness. Generation errors preserve prior output bytes.

From the repository root:

```sh
GOWORK=off go test ./internal/cborgen/...
task generate:codecs:check
cd engine && GOWORK=off go test -race ./kernel/codec/...
```
