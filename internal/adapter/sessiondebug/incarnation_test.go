package sessiondebug

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
)

type snapshotOnlyStore struct{ base *memstore.Store }

func (s snapshotOnlyStore) Save(ctx context.Context, current *session.Session) error {
	return s.base.Save(ctx, current)
}

func (s snapshotOnlyStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return s.base.Load(ctx, id)
}

func TestLegacyDelegationEventCannotAttachRecreatedChild(t *testing.T) {
	ctx := context.Background()
	base := memstore.New()
	store := snapshotOnlyStore{base: base}
	log := memstore.NewEventLog()
	owner := &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}
	created := time.Unix(1700000000, 0).UTC()
	root := session.New("root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, created)
	child, err := session.NewSubagent("child", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, created, root.ID, root.Incarnation(), "call")
	if err != nil {
		t.Fatal(err)
	}
	root.Owner, child.Owner = owner.Clone(), owner.Clone()
	if err := base.Save(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := base.Save(ctx, child); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(ctx, root.ID, session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{ParentCallID: "call", ChildID: string(child.ID), ChildIncarnation: child.Incarnation()}}); err != nil {
		t.Fatal(err)
	}
	if err := base.Delete(ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := session.NewSubagent(child.ID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, created, root.ID, root.Incarnation(), "call")
	if err != nil {
		t.Fatal(err)
	}
	replacement.Owner = owner.Clone()
	if err := base.Save(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.Incarnation() == child.Incarnation() {
		t.Fatal("replacement reused child incarnation")
	}

	graph := NewBound(root.ID, session.DebugTargetFingerprint(root), owner, true, store, log).(*inspectTool).scanLineage(ctx, root)
	if len(graph.Nodes) != 1 || graph.Nodes[0].Incarnation != string(child.Incarnation()) || graph.Nodes[0].Inspectable || graph.Nodes[0].Handle != "" {
		t.Fatalf("old event attached recreated child: %+v", graph.Nodes)
	}

	legacyLog := memstore.NewEventLog()
	if err := legacyLog.Append(ctx, root.ID, session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{ParentCallID: "call", ChildID: string(child.ID)}}); err != nil {
		t.Fatal(err)
	}
	legacy := NewBound(root.ID, session.DebugTargetFingerprint(root), owner, true, store, legacyLog).(*inspectTool).scanLineage(ctx, root)
	if len(legacy.Nodes) != 0 {
		t.Fatalf("legacy event without incarnation attached a child: %+v", legacy.Nodes)
	}
}
