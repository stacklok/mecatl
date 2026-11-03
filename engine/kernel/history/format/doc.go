//go:generate go -C ../../../.. run ./internal/cborgen/cmd/cborgen -dir engine/kernel/history/format -types Commit,Record -out zz_cbor_generated.go

// Package format owns the isolated kernel's persistence representation. Commit
// and Record are mutable persistence DTOs, separate from accepted-state
// aggregates. Their owner-generated CBOR methods compose through codec's shared
// operation state. Validate checks their data contract; CBOR operations
// additionally enforce canonical encoding and per-operation resource limits. The
// package does not establish history membership or acceptance.
package format
