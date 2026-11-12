// Package codec provides shared operational budgets and strict CBOR primitives
// for statically generated persistence codecs. Schema owners define field mapping,
// presence, integer ranges, and semantic validation; this runtime owns wire-format
// checks and resource accounting, not identity or storage policy.
//
// The supported CBOR subset excludes floats, indefinite forms, and nonminimal
// headers. Explicit null is available to native primitive owners; ordinary DTO
// pointer fields remain required non-null values when present. Map keys use
// canonical text ordering (byte length, then lexical order).
// The fixed DAG-CBOR CID link is the only supported tag; identity owners validate
// its CID profile.
//
// Operations require explicit [Limits]. Accounting conservatively bounds traversal
// and collection declarations, not exact heap usage or process-wide concurrency.
// Even canonical input smaller than MaxBytes may exceed its accounting budget.
// Field caps constrain immediate values, supplementing global limits rather than
// establishing domain validity. See [Budget], [Marshaler], and [Unmarshaler] for
// compositional traversal and failure contracts.
//
// Input values and bytes must remain stable during a call. Decoded mutable storage
// is owned by the result. Independent operations may run concurrently; budgets and
// readers are per-call mutable state. Discard them after an error. Successful
// decoding establishes neither authority, acceptance, nor durability.
package codec
