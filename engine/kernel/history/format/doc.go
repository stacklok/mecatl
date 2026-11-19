//go:generate go -C ../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/history/format -emit cbor,clone,equal -out zz_cbor_generated.go

// Package format owns the isolated kernel's persistence representation. Commit
// and Record are mutable persistence DTOs, separate from accepted-state
// aggregates. Their owner-generated CBOR methods compose through codec's shared
// operation state. Validate checks their data contract; CBOR operations
// additionally enforce canonical encoding and per-operation resource limits. The
// package does not establish history membership or acceptance.
//
// Version 1 envelopes require signed Unix-nanosecond timestamps on records and
// commits, plus a nonzero writer-instance ID on commits. Zero timestamps denote
// the Unix epoch; negative values and backwards clock jumps are valid. These
// diagnostic fields participate in commit identity without proving timing,
// ordering, or authority. Decoding rejects missing metadata rather than inferring it.
package format
