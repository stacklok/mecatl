//go:generate go -C ../../../../.. run ./internal/cborgen/cmd/cborgen -dir engine/kernel/codec/internal/fixture -types Catalog,Sample -out zz_cbor_generated.go

// Package fixture contains unrelated DTO families used to qualify generated codecs.
package fixture
