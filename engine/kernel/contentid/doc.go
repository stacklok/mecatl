//go:generate go -C ../../.. run ./internal/cborgen/cmd/cborgen -dir engine/kernel/contentid -types CID -out zz_cbor_generated.go

// Package contentid provides optional exact-byte identity using the fixed
// CIDv1/DAG-CBOR/SHA-256 profile. CID is an owner-generated CBOR value whose
// fixed link framing uses codec's identity-free primitives. Identity is not
// schema or semantic validation, history membership, acceptance, or
// authorization, even with a trusted CID.
//
// Hashing is synchronous and stateless. Callers bound input size before hashing
// and keep input stable during calls; operations neither retain nor mutate it.
// Hashing and identity parsing apply no CBOR parsing, normalization, or implicit
// resource policy. Generated CBOR operations use explicit codec limits.
package contentid
