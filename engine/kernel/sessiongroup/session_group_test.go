package sessiongroup_test

import (
	"context"
	"errors"
	"testing"

	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
	"github.com/stacklok/mecatl/engine/kernel/sessiongroup"
	groupid "github.com/stacklok/mecatl/engine/kernel/sessiongroup/id"
)

func TestSkeletonCannotEstablishHosting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, canceled := range []bool{false, true} {
		if canceled {
			cancel()
		}
		g, err := sessiongroup.Open(ctx, groupid.NewGroupID())
		if g != nil || !errors.Is(err, sessiongroup.ErrNotImplemented) {
			t.Fatalf("Open created a placeholder owner: %v, %v", g, err)
		}
		for _, g := range []*sessiongroup.SessionGroup{nil, {}} {
			if g.GroupID() != (groupid.GroupID{}) {
				t.Fatal("unconstructed group has identity")
			}
			members, err := g.Members(ctx)
			if members != nil || !errors.Is(err, sessiongroup.ErrNotImplemented) {
				t.Fatalf("Members claimed membership: %v, %v", members, err)
			}
			loaded, err := g.LoadedRuntimes()
			if loaded != nil || !errors.Is(err, sessiongroup.ErrNotImplemented) {
				t.Fatalf("LoadedRuntimes claimed residency: %v, %v", loaded, err)
			}
			member := sessionid.NewSessionID()
			runtime, err := g.Load(ctx, member)
			if runtime != nil || !errors.Is(err, sessiongroup.ErrNotImplemented) {
				t.Fatalf("Load created a usable runtime: %v, %v", runtime, err)
			}
			if err := g.Unload(ctx, member); !errors.Is(err, sessiongroup.ErrNotImplemented) {
				t.Fatalf("Unload claimed success: %v", err)
			}
			if err := g.Close(ctx); !errors.Is(err, sessiongroup.ErrNotImplemented) {
				t.Fatalf("Close claimed drain/release: %v", err)
			}
		}
	}
}
