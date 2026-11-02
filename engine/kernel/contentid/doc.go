// Package contentid provides optional exact-byte identity using the fixed
// CIDv1/DAG-CBOR/SHA-256 profile. Identity is not schema or semantic validation,
// history membership, acceptance, or authorization, even with a trusted CID.
//
// Hashing is synchronous and stateless. Callers bound input size before hashing
// and keep input stable during calls; operations neither retain nor mutate it.
// No CBOR parsing, normalization, or implicit resource policy is applied.
package contentid
