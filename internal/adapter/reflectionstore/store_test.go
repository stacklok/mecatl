package reflectionstore_test

import (
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/proposalconformance"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/internal/adapter/reflectionstore"
)

func TestConformance(t *testing.T) {
	proposalconformance.Run(t, func(t *testing.T) learning.ProposalRepository {
		s, e := reflectionstore.New(filepath.Join(t.TempDir(), "store"))
		if e != nil {
			t.Fatal(e)
		}
		return s
	})
}
