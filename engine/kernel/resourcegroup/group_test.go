package resourcegroup_test

import (
	"context"
	"reflect"
	"testing"

	engineid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/groupstorage"
	"github.com/stacklok/mecatl/engine/kernel/resourcegroup"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

func TestGroupSkeleton(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, g := range []*resourcegroup.Group{nil, {}} {
		if got := g.GroupID(); got != (groupid.GroupID{}) {
			t.Fatalf("GroupID() = %v", got)
		}
		for _, ctx := range []context.Context{nil, context.Background(), canceled} {
			for _, kind := range []engineid.Kind{"", "bad", "TST"} {
				for _, req := range []groupstorage.MemberPageRequest{{}, {Limit: -1}, {Limit: 1}} {
					got, err := g.Members(ctx, kind, req)
					if !reflect.DeepEqual(got, groupstorage.MemberPage{}) || err != resourcegroup.ErrNotImplemented {
						t.Fatalf("Members() = %+v, %v", got, err)
					}
				}
			}
		}
		if got, err := g.ResidentResources(); got != nil || err != resourcegroup.ErrNotImplemented {
			t.Fatalf("ResidentResources() = %v, %v", got, err)
		}
		for range 2 {
			if err := g.Drain(); err != resourcegroup.ErrNotImplemented {
				t.Fatalf("Drain() = %v", err)
			}
			if got, err := g.Status(); got != (resourcegroup.LifecycleStatus{}) || err != resourcegroup.ErrNotImplemented {
				t.Fatalf("Status() = %+v, %v", got, err)
			}
			if g.Done() != nil {
				t.Fatal("Done() is not nil")
			}
			if got, err := g.Report(); !reflect.DeepEqual(got, resourcegroup.ShutdownReport{}) || err != resourcegroup.ErrNotImplemented {
				t.Fatalf("Report() = %+v, %v", got, err)
			}
		}
	}
}
