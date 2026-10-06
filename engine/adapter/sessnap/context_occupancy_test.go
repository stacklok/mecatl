package sessnap_test

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestResumableSessionStatusMetrics_Scenario1_AllSessionKindsRoundTrip(t *testing.T) {
	cases := []struct {
		kind session.SessionKind
		rel  session.SessionRelationship
	}{
		{kind: session.SessionKindMain},
		{kind: session.SessionKindScheduled, rel: session.SessionRelationship{ScheduleName: "nightly", OriginSessionID: "origin"}},
		{kind: session.SessionKindSubagent, rel: session.SessionRelationship{ParentSessionID: "parent", CallID: "subagent"}},
		{kind: session.SessionKindParallelBranch, rel: session.SessionRelationship{ParentSessionID: "parent", CallID: "parallel", BranchIndex: intPointer(1)}},
		{kind: session.SessionKindTeamMember, rel: session.SessionRelationship{TeamID: "team", MemberName: "member", ParentSessionID: "parent"}},
		{kind: session.SessionKindDebug, rel: session.SessionRelationship{DebugTargetID: "target"}},
	}
	for i, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			s := session.New(session.SessionID(string(tc.kind)), session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(int64(i), 0))
			if err := s.RestoreSessionMetadata(tc.kind, tc.rel); err != nil {
				t.Fatalf("RestoreSessionMetadata: %v", err)
			}
			want := session.ContextOccupancy{InputTokens: i + 1, Estimated: tc.kind == session.SessionKindDebug}
			s.RecordLatestContextOccupancy(want)

			restored, err := sessnap.Unmarshal(mustMarshal(t, s))
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			got, ok := restored.LatestContextOccupancy()
			if !ok || got != want {
				t.Fatalf("LatestContextOccupancy = (%+v, %v), want (%+v, true)", got, ok, want)
			}
		})
	}
}

func intPointer(v int) *int { return &v }
