// Package codec provides shared operational budgets and strict CBOR primitives
// for statically generated persistence codecs, not a general-purpose serializer.
// It has no schema, semantic validation, reflection, identity ownership, or
// storage behavior. Generated owners define required and optional presence, exact
// field names, canonical text-key ordering (byte length, then lexical order),
// integer narrowing, and pure local validation. Null, floats, indefinite forms,
// and nonminimal headers are unsupported. The only tag exception is the fixed
// DAG-CBOR CID link exposed by CIDLink; callers cannot use tags generally.
// Identity owners validate the CID profile.
//
// Check, Encode, and Decode validate all positive global Limits once, before
// inspecting input. Nested generated methods share one Budget or Reader. A field
// cap is a positive literal or a positive machine maximum for an untagged value.
// Field caps constrain the immediate value, not its descendants, and supplement
// applicable global caps; they express resource policy, not domain validity.
//
// A fresh Budget traverses exactly one wire value, root depth zero. Call one Check
// method per node; pointers are not nodes. A container check reserves all immediate
// child nodes, including map keys, and declarations before allocation or sorting.
// CheckCIDLink is the fixed tag exception: it counts the tag at depth d, reserves
// and visits its byte-string child at d+1, and charges no collection declaration.
// The conservative estimate is nine bytes per node plus string/byte-string
// contents, on both Check and Decode. Thus even canonical input smaller than
// MaxBytes may be rejected. This bounds traversal and declarations, not exact heap
// usage, process-wide concurrency, or excluded in-memory fields.
//
// Reader combines the same accounting with strict owning scalar reads. Generated
// ReadCBOR methods allocate into fresh temporary storage and assign their receiver
// only after structural and local semantic success. Only root Decode calls Done,
// rejects trailing bytes, and returns zero on every failure. Encode checks the
// complete value before allocating output, then appends without repeating
// validation or accounting; failure returns nil output. Successful decoding proves
// neither authority, semantic compatibility, immutability, acceptance, nor
// durability.
//
// Input values and bytes must remain stable during a call. Independent operations
// and concurrent read-only policy reuse are safe. Budgets and readers are per-call
// mutable state and are not safe for concurrent use. Discard them after any error:
// no rollback is promised. Decoded strings and bytes own storage without cloning
// the whole input. No dependency-global policy is used.
package codec
