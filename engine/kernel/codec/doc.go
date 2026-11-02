// Package codec provides operational budgets and strict CBOR primitives for
// statically generated persistence codecs, not a general-purpose serializer.
// It has no schema, domain-validation hooks, reflection, or storage behavior.
// Generated callers own required/optional presence, exact field names, canonical
// text-key ordering (byte length, then lexical order), and integer narrowing.
// Null, floats, tags, indefinite forms, and nonminimal headers are unsupported.
//
// Each operation first validates global Limits and resolves every schema-known
// Named reference, even for absent optional fields, before inspecting its input.
// Unreferenced dictionary entries are ignored. A field cap is a positive literal,
// resolved named value, or the applicable global maximum for an untagged field.
// Apply optional field caps only when present. Field caps supplement global caps;
// they express resource policy, not schema or domain validity.
//
// A fresh Budget traverses exactly one wire value, root depth zero. Call one Check
// method per node; pointers are not nodes. A container check reserves all immediate
// child nodes (including map keys) and declarations before allocation or sorting.
// Visit children at depth+1. The conservative estimate is nine bytes per node plus
// string/byte-string contents, on both Check and Decode. Thus even canonical input
// smaller than MaxBytes may be rejected. This bounds traversal and declarations,
// not exact heap usage, process-wide concurrency, or excluded in-memory fields.
//
// Reader combines the same accounting with strict owning scalar reads. Generated
// decoders check schema/presence/order and destination ranges before narrowing,
// allocate collections only after successful headers, and call Done at the end.
// They return zero DTOs on failure; encoders return nil output on failure. Encode
// must perform its own checks, not rely on an earlier Check. No owner semantics
// are invoked automatically. Successful decoding proves neither authority nor
// semantic compatibility, immutability, acceptance, or durability.
//
// Input values, bytes, and policy dictionaries must remain stable during a call.
// Independent operations and concurrent read-only policy reuse are safe. Budgets
// and readers are per-call mutable state, not safe for concurrent use. Discard
// them after any error; no rollback is promised. Decoded strings and bytes own
// storage without cloning the whole input. No dependency-global policy is used.
package codec
