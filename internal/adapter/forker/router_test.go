package forker_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/forker"
)

func TestKindMergerRouterRoutesByParentEnvironmentRefKind(t *testing.T) {
	t.Parallel()
	local := &routingMerger{}
	microVM := &routingMerger{}
	router := forker.NewKindMergerRouter(local, map[session.EnvironmentKind]tool.EnvironmentMerger{"microvm": microVM})
	for _, tc := range []struct {
		kind session.EnvironmentKind
		want *routingMerger
	}{{session.EnvKindLocal, local}, {"microvm", microVM}} {
		parent := tool.MustEnvironment(session.EnvironmentRef{Kind: tc.kind, ID: "parent"}, memfs.NewWorkspace("/parent"), memledger.New(), nil)
		child := tool.MustEnvironment(session.EnvironmentRef{Kind: tc.kind, ID: "child"}, memfs.NewWorkspace("/child"), memledger.New(), nil)
		if err := router.Merge(context.Background(), child, parent); err != nil {
			t.Fatalf("Merge(%q): %v", tc.kind, err)
		}
		if tc.want.calls != 1 {
			t.Fatalf("Merge(%q) calls = %d", tc.kind, tc.want.calls)
		}
	}
}

type routingMerger struct{ calls int }

func (m *routingMerger) Merge(context.Context, tool.Environment, tool.Environment) error {
	m.calls++
	return nil
}

func TestKindRouterRoutesDelegationByEnvironmentRefKind(t *testing.T) {
	t.Parallel()
	local := &routingForker{id: "local"}
	microVM := &routingForker{id: "microvm"}
	router := forker.NewKindRouter(local, map[session.EnvironmentKind]tool.EnvironmentForker{
		"microvm": microVM,
	})

	for _, tc := range []struct {
		kind session.EnvironmentKind
		want string
	}{
		{kind: session.EnvKindLocal, want: "local"},
		{kind: "microvm", want: "microvm"},
	} {
		base := tool.MustEnvironment(session.EnvironmentRef{Kind: tc.kind, ID: "parent"}, memfs.NewWorkspace("/parent"), memledger.New(), nil)
		child, cleanup, _, err := router.Fork(context.Background(), base, "child")
		if err != nil {
			t.Fatalf("Fork(%q): %v", tc.kind, err)
		}
		if child.Ref().ID != tc.want {
			t.Fatalf("Fork(%q) child ref = %q, want route %q", tc.kind, child.Ref().ID, tc.want)
		}
		if err := cleanup(); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}

	unknown := tool.MustEnvironment(session.EnvironmentRef{Kind: "unknown-remote", ID: "parent"}, memfs.NewWorkspace("/parent"), memledger.New(), nil)
	if _, _, _, err := router.Fork(context.Background(), unknown, "child"); !errors.Is(err, forker.ErrUnsupportedEnvironmentKind) {
		t.Fatalf("unknown remote kind error = %v, want ErrUnsupportedEnvironmentKind", err)
	}
}

type routingForker struct{ id string }

func (f *routingForker) Fork(_ context.Context, base tool.Environment, _ string) (tool.Environment, func() error, string, error) {
	child := tool.MustEnvironment(session.EnvironmentRef{Kind: base.Ref().Kind, ID: f.id}, memfs.NewWorkspace("/"+f.id), memledger.New(), nil)
	return child, func() error { return nil }, "", nil
}
