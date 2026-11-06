# Content-part and media representation cleanup

Joe approved this direction on 2026-11-05 after reviewing kernel context commit
`b40c45289e8f7bbec8d1826f50896b8e735c2021`. This is the next implementation slice;
it is not implemented in that commit. It clarifies the existing content contract
without adding capture-parity metadata or production integration. See the
[implementation index](README.md) and [capture-parity inventory](capture-parity.md)
for the surrounding work and deferred gaps.

## Separate the four questions

The current `ContentPart.Kind` both selects text versus media and classifies media.
Move image/audio/document classification into `Media`. Keep explicit branch
presence on the enclosing part, without a redundant outer discriminator.

| Question | Owning field |
| --- | --- |
| Is this part text or media? | Exactly one of `ContentPart.Text` and `ContentPart.Media` is non-nil. |
| What category of media is it? | `Media.Kind`: image, audio, or document. |
| Where are the bytes retained or referenced? | `Media.Source`: inline, remote, or provider. |
| What is the declared MIME type? | `Media.MIMEType`, for example `image/png`. |

The domain construction values should have this structure:

```go
type ContentPart struct {
    Text  *string
    Media *Media
}

type Media struct {
    Kind   MediaKind
    Source MediaSourceKind

    // Inline source only.
    InlineData []byte

    // Remote source only.
    RemoteURI string

    // Provider source only; all three required together.
    ProviderName        string
    ProviderScope       string
    ProviderReferenceID string

    // Metadata shared across source variants.
    MIMEType   string
    FormatHint string
    Filename   string
    Title      string
}
```

`Kind` and `Source` lead the media declaration, before the fields they govern.
Retain a flat tagged media value with visibly grouped fields. Do not restore nested
source wrappers or introduce an interface hierarchy. In the persistence DTOs, the
closed vocabulary fields can remain strings under the existing codec conventions.

## Names and validation

Use `MediaKindImage`, `MediaKindAudio`, and `MediaKindDocument` for the media-kind
constants, and `MediaSourceInline`, `MediaSourceRemote`, and `MediaSourceProvider`
for source constants. Keep the vocabulary owned by `session/format`, with domain
typed constants derived from it. Remove `ContentKind` and its constants once the
outer discriminator is gone; there is no media-kind value for text.

Apply these renames consistently to domain values, DTOs, conversions, validation,
views, examples, tests, and generated public APIs:

| Current name | Approved direction |
| --- | --- |
| `ContentPart.Kind` | Remove; branch presence selects text/media. |
| Image/audio/document content kind | `Media.Kind` with `MediaKind` domain type. |
| `MediaInline`, `MediaRemote`, `MediaProvider` | `MediaSourceInline`, `MediaSourceRemote`, `MediaSourceProvider`. |
| `MediaType` | `MIMEType`. |
| `Format` | `FormatHint`. |
| `Data` | `InlineData`. |
| `URI` | `RemoteURI`. |
| `Provider` | `ProviderName`. |
| `Scope` | `ProviderScope`. |
| `ReferenceID` | `ProviderReferenceID`. |

`ContentPart` requires exactly one non-nil branch. A non-nil text pointer preserves
explicit empty text. Neither branch, including the zero value, is invalid; both
branches are also invalid. The text pointer adds an allocation/ownership obligation
but avoids silently interpreting an uninitialized part as valid empty text.

`Media.Validate` owns media-kind validity and document-only metadata applicability,
so standalone media validation can enforce those rules without an enclosing part.
Source validation retains the existing requirements:

| Source | Required source fields | MIME type |
| --- | --- | --- |
| Inline | `InlineData`, including explicitly empty bytes. The source tag establishes presence. | Required. |
| Remote | Absolute `RemoteURI`. | Optional. |
| Provider | Complete `ProviderName`/`ProviderScope`/`ProviderReferenceID` tuple. | Optional. |

Fields belonging to other source variants must be empty. `FormatHint` remains an
optional exact hint for every source. `Filename` and `Title` remain optional and
document-only, regardless of source. References grant no access or dereference
authority. Retain exact strings and bytes; this is not MIME/schema-policy expansion.

## Inspection and ownership

Keep the existing `ContentPartView.Text() (string, bool)` and
`ContentPartView.Media() (MediaView, bool)` branch navigation. Remove the outer
`ContentPartView.Kind()` and add `MediaView.Kind() MediaKind`. Rename other media
accessors consistently with the fields above; retain explicit presence on
source-specific accessors and field-only owned copies for inline bytes.

Construction assignments share pointers and slices. Cloning and successful record
construction/admission must detach the text pointer as well as media storage.
Mutating a caller's string variable through its retained pointer after construction
must not change a record, accepted view, or later replay. Views remain type-owned,
colocated, immutable, and allocation-free for ordinary navigation. Preserve zero
view behavior, collection bounds panics, pinned identity lookup and concurrent reads.

## Implementation and verification boundary

Implement both `session` and `session/format` together with their direct callers.
Account for the text pointer and new media-kind field in preallocation size/depth/
node budgets. Keep validation ahead of governed allocations and preserve canonical,
bounded decoding and atomic failed admission.

This changes the experimental part/media schema, not merely Go spelling. Preserve
existing CBOR keys where their meaning remains the same, but do not claim complete
encoded-byte compatibility when moving the discriminator or changing text presence.
Follow the existing early-kernel policy: version 1, no compatibility-only migration
machinery or changelog bookkeeping. Generate codecs and public API snapshots through
the owning tasks; do not hand-edit them.

Regression checks should cover neither/both branches, explicit empty text, invalid
media kind/source, the complete source-field matrix, document-only metadata on
standalone media, text-pointer mutation isolation, exact metadata/bytes through
Submit/Apply/Load, zero/wrong-branch views, and no-copy navigation. Adapt existing
allocation/negative controls rather than removing them. Recheck value budgets,
limits, retry/reserve, scope-before-omission, and external replay atomicity at their
existing boundaries. Run focused kernel/format/architecture tests and required
generation/API/lint checks, broadening gates for actual integration effects.

No timestamps, owner/principal fields, lifecycle expansion, request reconstruction,
streaming bytes, generic view framework, or production cutover belongs in this
cleanup. The [capture-parity inventory](capture-parity.md) remains the place to
revisit missing basic facts and later-layer responsibilities without expanding
this slice. New implementation requires a subsequent explicit instruction; prior
commit/push/PR-update authority does not carry over automatically.
