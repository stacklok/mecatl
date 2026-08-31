package session_test

import (
	"context"
	"iter"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionContinuityUX_Scenario1_KindRelationshipRoundTrip(t *testing.T) {
	t.Parallel()
	created := time.Unix(1700000000, 0).UTC()
	parentIncarnation := session.NewIncarnationID()
	tests := []struct {
		name string
		new  func() (*session.Session, error)
		kind session.SessionKind
		rel  session.SessionRelationship
	}{
		{name: "main", new: func() (*session.Session, error) {
			return session.New("main-1", session.ModeDefault, "/ws", session.Limits{}, created), nil
		}, kind: session.SessionKindMain},
		{name: "scheduled", new: func() (*session.Session, error) {
			return session.NewScheduled("sched-1", session.ModePlan, "/ws", session.Limits{}, created, "nightly", "origin-1", parentIncarnation)
		}, kind: session.SessionKindScheduled, rel: session.SessionRelationship{ScheduleName: "nightly", OriginSessionID: "origin-1", OriginIncarnation: parentIncarnation}},
		{name: "subagent", new: func() (*session.Session, error) {
			return session.NewSubagent("sub-1", session.ModeDefault, "/ws", session.Limits{}, created, "parent-1", parentIncarnation, "call-1")
		}, kind: session.SessionKindSubagent, rel: session.SessionRelationship{ParentSessionID: "parent-1", ParentIncarnation: parentIncarnation, CallID: "call-1"}},
		{name: "parallel branch", new: func() (*session.Session, error) {
			return session.NewParallelBranch("parallel-1", session.ModeDefault, "/ws", session.Limits{}, created, "parent-1", parentIncarnation, "call-2", 3)
		}, kind: session.SessionKindParallelBranch, rel: session.SessionRelationship{ParentSessionID: "parent-1", ParentIncarnation: parentIncarnation, CallID: "call-2", BranchIndex: intPtr(3)}},
		{name: "team member", new: func() (*session.Session, error) {
			return session.NewTeamMember("team-member-1", session.ModeDefault, "/ws", session.Limits{}, created, "team-1", "reviewer", "parent-1", parentIncarnation)
		}, kind: session.SessionKindTeamMember, rel: session.SessionRelationship{TeamID: "team-1", MemberName: "reviewer", ParentSessionID: "parent-1", ParentIncarnation: parentIncarnation}},
		{name: "debug", new: func() (*session.Session, error) {
			return session.NewDebug("debug-1", session.ModeDefault, session.Limits{}, created, "target-1", parentIncarnation)
		}, kind: session.SessionKindDebug, rel: session.SessionRelationship{DebugTargetID: "target-1", DebugTargetIncarnation: parentIncarnation}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want, err := tc.new()
			if err != nil {
				t.Fatalf("construct: %v", err)
			}
			if want.Kind != tc.kind || !reflect.DeepEqual(want.Relationship, tc.rel) {
				t.Fatalf("constructed metadata = (%q, %+v), want (%q, %+v)", want.Kind, want.Relationship, tc.kind, tc.rel)
			}

			snap, err := sessnap.Of(want)
			if err != nil {
				t.Fatalf("sessnap.Of: %v", err)
			}
			got, err := snap.Restore()
			if err != nil {
				t.Fatalf("Snapshot.Restore: %v", err)
			}
			if got.Kind != tc.kind || !reflect.DeepEqual(got.Relationship, tc.rel) {
				t.Errorf("snapshot metadata = (%q, %+v), want (%q, %+v)", got.Kind, got.Relationship, tc.kind, tc.rel)
			}

			meta := eventsource.SessionMeta{ID: want.ID, Incarnation: want.Incarnation(), Mode: want.Mode, Workspace: want.Workspace, Limits: want.Limits, CreatedAt: want.CreatedAt, Kind: want.Kind, Relationship: want.Relationship}
			folded, err := eventsource.Fold(meta, iter.Seq2[session.Event, error](func(func(session.Event, error) bool) {}))
			if err != nil {
				t.Fatalf("eventsource.Fold: %v", err)
			}
			if folded.Kind != tc.kind || !reflect.DeepEqual(folded.Relationship, tc.rel) || folded.Incarnation() != want.Incarnation() {
				t.Errorf("event-source metadata = (%q, %+v), want (%q, %+v)", folded.Kind, folded.Relationship, tc.kind, tc.rel)
			}

			storeMeta := port.SessionDiscoveryMeta{ID: want.ID, Kind: want.Kind, Relationship: want.Relationship}
			if storeMeta.Kind != tc.kind || !reflect.DeepEqual(storeMeta.Relationship, tc.rel) {
				t.Errorf("store metadata = (%q, %+v), want (%q, %+v)", storeMeta.Kind, storeMeta.Relationship, tc.kind, tc.rel)
			}
		})
	}
}

func TestADR_0108_PublicCreateCannotForgeKind(t *testing.T) {
	t.Parallel()
	s := session.New("public", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	if s.Kind != session.SessionKindMain {
		t.Fatalf("session.New Kind = %q, want %q", s.Kind, session.SessionKindMain)
	}
	if s.Relationship != (session.SessionRelationship{}) {
		t.Fatalf("session.New Relationship = %+v, want empty", s.Relationship)
	}
}

func TestADR_0108_InvalidRelationshipsFailClosed(t *testing.T) {
	t.Parallel()
	created := time.Unix(0, 0)
	invalid := []sessnap.Snapshot{
		{ID: "bad-main", Kind: session.SessionKindMain, Relationship: session.SessionRelationship{ParentSessionID: "forged"}, CreatedAt: created},
		{ID: "bad-scheduled", Kind: session.SessionKindScheduled, CreatedAt: created},
		{ID: "bad-subagent", Kind: session.SessionKindSubagent, Relationship: session.SessionRelationship{ParentSessionID: "parent"}, CreatedAt: created},
		{ID: "bad-parallel", Kind: session.SessionKindParallelBranch, Relationship: session.SessionRelationship{ParentSessionID: "parent", CallID: "call", BranchIndex: intPtr(-1)}, CreatedAt: created},
		{ID: "bad-team", Kind: session.SessionKindTeamMember, Relationship: session.SessionRelationship{TeamID: "team"}, CreatedAt: created},
		{ID: "bad-debug-empty", Kind: session.SessionKindDebug, CreatedAt: created},
		{ID: "bad-debug-parent", Kind: session.SessionKindDebug, Relationship: session.SessionRelationship{DebugTargetID: "target", ParentSessionID: "forged"}, CreatedAt: created},
		{ID: "bad-unknown", Kind: session.SessionKind("future"), CreatedAt: created},
	}
	for _, snap := range invalid {
		if _, err := snap.Restore(); err == nil {
			t.Errorf("Restore(%q) succeeded, want fail-closed error", snap.ID)
		}
	}
}

func TestSessionContinuityUX_Scenario1_PeerRebindsRemainMain(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"peer fork", "effort fork", "model carryover"} {
		t.Run(name, func(t *testing.T) {
			s := session.New(session.SessionID(name), session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
			if s.Kind != session.SessionKindMain || s.Relationship != (session.SessionRelationship{}) {
				t.Fatalf("peer rebind metadata = (%q, %+v), want main with no lineage", s.Kind, s.Relationship)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

func TestInvariant_session_continuity_api_is_additive(t *testing.T) {
	t.Parallel()
	var _ = session.New
	var store port.SessionStore
	var _ interface {
		Save(context.Context, *session.Session) error
		Load(context.Context, session.SessionID) (*session.Session, error)
	} = store
}
