package session

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

func TestModelRequestCanonicalPayloadFailurePrecedence(t *testing.T) {
	s := interactionFixture(t, "active")
	valid := interactionRequest(s, 1).authoring()
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	defer b.Discard() //nolint:errcheck // The batch is deliberately left unsubmitted.
	before := b.admission
	oversized := make([]byte, s.policy.PayloadLimits.MaxByteStringBytes+1)
	for _, tc := range []struct {
		name string
		edit func(*ModelRequest)
		want error
	}{
		{"inactive before sizing", func(v *ModelRequest) { v.RunID = RunID{}; v.Recipe.Check = oversized }, ErrTransition},
		{"wrong run before validation", func(v *ModelRequest) { v.RunID.ordinal++; v.Recipe = ConstructionRecipe{} }, ErrTransition},
		{"sizing before invalid recipe", func(v *ModelRequest) { v.Recipe.Algorithm = ""; v.Recipe.Check = oversized }, codec.ErrLimitExceeded},
		{"invalid recipe", func(v *ModelRequest) { v.Recipe.Check = nil }, codec.ErrInvalidData},
		{"invalid source", func(v *ModelRequest) { v.Source.CID = SourceHead{}.CID }, codec.ErrInvalidData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := valid
			tc.edit(&v)
			id, err := b.AddModelRequest(v)
			if id != (ModelRequestID{}) || !errors.Is(err, tc.want) {
				t.Fatalf("failed request: %v, %v; want zero, %v", id, err, tc.want)
			}
			if b.admission != before || len(b.records) != 0 {
				t.Fatal("failed payload changed admission or retained a record")
			}
		})
	}
	id, err := b.AddModelRequest(valid)
	if err != nil || id != requestID(valid.RunID.historyID, valid.RunID.ordinal, 1) {
		t.Fatalf("failed additions consumed request identity: %v, %v", id, err)
	}
}
