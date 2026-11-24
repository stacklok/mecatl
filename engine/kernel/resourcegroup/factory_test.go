package resourcegroup_test

import (
	"context"
	"reflect"
	"testing"

	engineid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/resourcegroup"
)

// fakeID is a domain-owned comparable identity, not a session dependency.
type fakeID struct{ value engineid.ID }

func (i fakeID) ResourceID() engineid.ID { return i.value }

type fake struct{ id fakeID }

func (f *fake) ID() fakeID { return f.id }

type fakeFactory struct{}

func (*fakeFactory) ResourceType() reflect.Type           { panic("unexpected ResourceType") }
func (*fakeFactory) Kind() engineid.Kind                  { panic("unexpected Kind") }
func (*fakeFactory) Policy() resourcegroup.FactoryPolicy  { panic("unexpected Policy") }
func (*fakeFactory) DecodeID(engineid.ID) (fakeID, error) { panic("unexpected DecodeID") }
func (*fakeFactory) Load(*resourcegroup.Scope, fakeID) (resourcegroup.Loaded[*fake], error) {
	panic("unexpected Loader.Load")
}

var _ resourcegroup.Loader[*fake, fakeID] = (*fakeFactory)(nil)

func TestTypedHelpersAreNonoperational(t *testing.T) {
	key, err := engineid.Parse("TST1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, context.Background(), canceled} {
		for _, g := range []*resourcegroup.Group{nil, {}} {
			for _, id := range []fakeID{{}, {value: key}} {
				// Only T is explicit: I is inferred from the domain ID.
				if got, err := resourcegroup.Load[*fake](ctx, g, id); got != nil || err != resourcegroup.ErrNotImplemented {
					t.Fatalf("Load() = %v, %v", got, err)
				}
				// A domain Create supplies a typed closure, inferring I and F.
				got, err := resourcegroup.Create[*fake](ctx, g, id,
					func(*fakeFactory, *resourcegroup.Scope, fakeID) (resourcegroup.Loaded[*fake], error) {
						panic("unexpected Create callback")
					})
				if got != nil || err != resourcegroup.ErrNotImplemented {
					t.Fatalf("Create() = %v, %v", got, err)
				}
				got, err = resourcegroup.Create[*fake, fakeID, *fakeFactory](ctx, g, id, nil)
				if got != nil || err != resourcegroup.ErrNotImplemented {
					t.Fatalf("Create(nil) = %v, %v", got, err)
				}
			}
		}
	}
}
