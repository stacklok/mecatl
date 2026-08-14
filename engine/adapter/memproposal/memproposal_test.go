package memproposal_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/proposalconformance"
	"github.com/stacklok/mecatl/engine/learning"
)

func TestConformance(t *testing.T) {
	proposalconformance.Run(t, func(*testing.T) learning.ProposalRepository { return memproposal.New() })
}
