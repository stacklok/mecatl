package fixtureview

import "github.com/stacklok/mecatl/engine/kernel/internal/valuefixture"

//go:generate go -C ../../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/internal/valuefixture/fixtureview -emit view -out zz_views_generated.go

// EnvelopeView borrows an envelope and retains its stable backing for its lifetime.
// Its zero value is empty and safe. It neither freezes nor synchronizes storage.
//
//dtogen:include
type EnvelopeView struct{ value *valuefixture.Envelope }

// Borrow retains v without copying or validation in O(1). v and all descendants
// must remain stable for every derived view's lifetime; no freezing or locking occurs.
func Borrow(v *valuefixture.Envelope) EnvelopeView { return EnvelopeView{value: v} }

// ChildView borrows a child and its descendants for its lifetime. Zero is safe;
// it neither freezes nor synchronizes the backing, which must remain stable.
//
//dtogen:include
type ChildView struct{ value *valuefixture.Child }

// LeafView borrows one leaf's stable backing for its lifetime. Zero is safe;
// it neither freezes nor synchronizes storage.
//
//dtogen:include
type LeafView struct{ value *valuefixture.Leaf }
