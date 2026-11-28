package resourcegroup_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/resourcegroup"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

func TestManagerSkeleton(t *testing.T) {
	for _, config := range []resourcegroup.ManagerConfig{{}, validConfig()} {
		if got, err := resourcegroup.NewManager(config); got != nil || err != resourcegroup.ErrNotImplemented {
			t.Fatalf("NewManager() = %v, %v", got, err)
		}
	}
	id, err := groupid.ParseGroupID("GRP1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, m := range []*resourcegroup.Manager{nil, {}} {
		for _, factories := range [][]resourcegroup.Factory{
			nil, {nil}, {(*fakeFactory)(nil)}, {&fakeFactory{}, &fakeFactory{}},
		} {
			if err := m.Register(factories...); err != resourcegroup.ErrNotImplemented {
				t.Fatalf("Register() = %v", err)
			}
		}
		for _, ctx := range []context.Context{nil, context.Background(), canceled} {
			for _, key := range []groupid.GroupID{{}, id} {
				if got, err := m.Load(ctx, key); got != nil || err != resourcegroup.ErrNotImplemented {
					t.Fatalf("Load() = %v, %v", got, err)
				}
			}
		}
		if got, err := m.ResidentGroups(); got != nil || err != resourcegroup.ErrNotImplemented {
			t.Fatalf("ResidentGroups() = %v, %v", got, err)
		}
		for range 2 {
			if err := m.Drain(); err != resourcegroup.ErrNotImplemented {
				t.Fatalf("Drain() = %v", err)
			}
			if m.Done() != nil {
				t.Fatal("Done() is not nil")
			}
			if got, err := m.Report(); !reflect.DeepEqual(got, resourcegroup.ManagerShutdownReport{}) || err != resourcegroup.ErrNotImplemented {
				t.Fatalf("Report() = %+v, %v", got, err)
			}
		}
	}
}
