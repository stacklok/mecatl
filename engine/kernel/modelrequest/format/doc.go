//go:generate go -C ../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/modelrequest/format -emit cbor,clone,equal -out zz_cbor_generated.go

// Package format owns mutable logical model-request DTOs and chronological-v1
// parameters. RequestV1 has no envelope, version field, identity, or embedded CID;
// its interpreting algorithm supplies schema context. Canonical CBOR alone defines
// content identity. Nil and empty collections encode identically. Assignment shares
// mutable storage; generated Clone detaches it. Codec operations validate children
// under explicit resource limits before local semantic checks and output allocation.
// Direct Validate calls impose no operational resource policy.
package format
