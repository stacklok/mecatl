package memskill_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/skillconformance"
	"github.com/stacklok/mecatl/engine/learning"
)

type evaluatedOnlyRepository struct{ learning.SkillRepository }

func TestConformance(t *testing.T) {
	factory := func(*testing.T) learning.SkillRepository { return memskill.New() }
	skillconformance.Run(t, factory)
	skillconformance.RunValidatedActivation(t, factory)
}

func TestBaseConformanceDoesNotRequireValidatedActivation(t *testing.T) {
	skillconformance.Run(t, func(*testing.T) learning.SkillRepository {
		return evaluatedOnlyRepository{SkillRepository: memskill.New()}
	})
}
