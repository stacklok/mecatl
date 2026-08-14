package memskill_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/skillconformance"
	"github.com/stacklok/mecatl/engine/learning"
)

func TestConformance(t *testing.T) {
	skillconformance.Run(t, func(*testing.T) learning.SkillRepository { return memskill.New() })
}
