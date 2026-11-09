//go:generate go -C ../../../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/codec/internal/fixture/value -emit cbor,clone,equal -out zz_cbor_generated.go

// Package value supplies imported, named supported shapes for generator fixtures.
package value
