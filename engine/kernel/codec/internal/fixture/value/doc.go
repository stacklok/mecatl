//go:generate go -C ../../../../../.. run ./internal/cborgen/cmd/cborgen -dir engine/kernel/codec/internal/fixture/value -types Octet,Key,Blob,Detail -out zz_cbor_generated.go

// Package value supplies imported, named supported shapes for generator fixtures.
package value
