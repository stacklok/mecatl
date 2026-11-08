//go:generate go -C ../../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/codec/internal/fixture -types Catalog,Sample,Link,Links -emit cbor,clone,equal -out zz_cbor_generated.go

// Package fixture contains unrelated DTO families used to qualify generated codecs.
package fixture
