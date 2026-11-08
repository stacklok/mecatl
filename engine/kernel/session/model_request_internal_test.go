package session

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

func TestModelRequestCanonicalPayloadFailurePrecedence(t *testing.T) {
	s := interactionFixture(t, "active")
	run := s.Snapshot().ActiveRun()
	valid := interactionRequest(s, 1).ModelRequest
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	defer b.Discard() //nolint:errcheck // The batch is deliberately left unsubmitted.
	before := b.admission
	oversized := make([]byte, s.policy.PayloadLimits.MaxByteStringBytes+1)
	for _, tc := range []struct {
		name string
		edit func(*RunID, *ModelRequest)
		want error
	}{
		{"inactive before sizing", func(r *RunID, v *ModelRequest) { *r = RunID{}; v.Recipe.Check = oversized }, ErrTransition},
		{"wrong run before validation", func(r *RunID, v *ModelRequest) { r.ordinal++; v.Recipe = ConstructionRecipe{} }, ErrTransition},
		{"sizing before invalid recipe", func(_ *RunID, v *ModelRequest) { v.Recipe.Algorithm = ""; v.Recipe.Check = oversized }, codec.ErrLimitExceeded},
		{"invalid recipe", func(_ *RunID, v *ModelRequest) { v.Recipe.Check = nil }, codec.ErrInvalidData},
		{"invalid source", func(_ *RunID, v *ModelRequest) { v.Source.CID = SourceHead{}.CID }, codec.ErrInvalidData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, v := run, valid
			tc.edit(&r, &v)
			id, err := b.AddModelRequest(r, v)
			if id != (ModelRequestID{}) || !errors.Is(err, tc.want) {
				t.Fatalf("failed request: %v, %v; want zero, %v", id, err, tc.want)
			}
			if b.admission != before || len(b.records) != 0 {
				t.Fatal("failed payload changed admission or retained a record")
			}
		})
	}
	id, err := b.AddModelRequest(run, valid)
	if err != nil || id != requestID(run.historyID, run.ordinal, 1) {
		t.Fatalf("failed additions consumed request identity: %v, %v", id, err)
	}
}
