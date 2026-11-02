package server

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/learning"
)

func TestFrozenLegacyEvidenceCannotRematerialize(t *testing.T) {
	svc := &Service{}
	for _, protocol := range []learning.EvidenceProtocol{"", "reflection-evidence/v0", "reflection-evidence/v1"} {
		record := learning.ProposalRecord{Candidate: learning.Candidate{Evidence: []learning.EvidenceRef{{Protocol: protocol}}}}
		if _, _, err := svc.rematerializeProposal(t.Context(), learning.ProposalPartition{}, record); !errors.Is(err, ErrFailedPrecondition) {
			t.Fatalf("legacy protocol %q accepted: %v", protocol, err)
		}
	}
}
