package resourcegroup_test

import (
	"testing"

	engineid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/resourcegroup"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

func TestScopeSkeleton(t *testing.T) {
	for _, s := range []*resourcegroup.Scope{nil, {}} {
		if s.Group() != nil || s.GroupID() != (groupid.GroupID{}) || s.ResourceID() != (engineid.ID{}) {
			t.Fatal("nil/zero Scope identity is not zero")
		}
		if got, err := s.History(); got != nil || err != resourcegroup.ErrNotImplemented {
			t.Fatalf("History() = %v, %v", got, err)
		}
		if s.Context() != nil || s.Draining() != nil {
			t.Fatal("skeleton has a context or drain channel")
		}
		if got, ok := s.ShutdownBudget(); got != (resourcegroup.Budget{}) || ok {
			t.Fatalf("ShutdownBudget() = %+v, %v", got, ok)
		}
		if got, err := s.Admit(); got != resourcegroup.ModeUnknown || err != resourcegroup.ErrNotImplemented {
			t.Fatalf("Admit() = %v, %v", got, err)
		}
	}
}
