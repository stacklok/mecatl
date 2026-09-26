package scrollback

import "testing"

func TestMecatuiTypedScrollbackModel_Scenario2_EmptySlicesRemainNonNil(t *testing.T) {
	var c Conversation
	c.Messages().AddUser(UserInput{Media: []string{}})
	user := c.SnapshotAt(0).Payload.(UserCardSnapshot)
	if user.Media == nil {
		t.Fatal("non-nil empty media lost during snapshot")
	}
	c.Tools().Add(ToolCall{ID: "call", Artifacts: []Artifact{}})
	tool := c.SnapshotAt(1).Payload.(ToolCardSnapshot)
	if tool.Call.Artifacts == nil {
		t.Fatal("non-nil empty artifacts lost during snapshot")
	}
}

func TestClonePreservesNilAndEmptySlices(t *testing.T) {
	tests := []struct {
		name  string
		in    PayloadSnapshot
		check func(*testing.T, PayloadSnapshot)
	}{
		{
			name: "user media",
			in:   UserCardSnapshot{Media: []string{}},
			check: func(t *testing.T, got PayloadSnapshot) {
				t.Helper()
				if got.(UserCardSnapshot).Media == nil {
					t.Fatal("empty media became nil")
				}
			},
		},
		{
			name: "tool artifacts",
			in:   ToolCardSnapshot{Call: ToolCall{Artifacts: []Artifact{}}, Result: ToolResult{Artifacts: []Artifact{}}},
			check: func(t *testing.T, got PayloadSnapshot) {
				t.Helper()
				card := got.(ToolCardSnapshot)
				if card.Call.Artifacts == nil || card.Result.Artifacts == nil {
					t.Fatal("empty artifacts became nil")
				}
			},
		},
		{
			name: "subagent trace and artifacts",
			in:   SubagentCardSnapshot{Update: SubagentUpdate{Trace: []TraceEntry{}, Artifacts: []Artifact{}}},
			check: func(t *testing.T, got PayloadSnapshot) {
				t.Helper()
				update := got.(SubagentCardSnapshot).Update
				if update.Trace == nil || update.Artifacts == nil {
					t.Fatal("empty subagent data became nil")
				}
			},
		},
		{
			name: "team nested slices",
			in:   TeamCardSnapshot{Update: TeamUpdate{Lanes: []TeamLane{}, Tasks: []Task{{Dependencies: []string{}}}, Findings: []Finding{}}},
			check: func(t *testing.T, got PayloadSnapshot) {
				t.Helper()
				update := got.(TeamCardSnapshot).Update
				if update.Lanes == nil || update.Tasks == nil || update.Tasks[0].Dependencies == nil || update.Findings == nil {
					t.Fatal("empty team data became nil")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, clonePayload(tt.in))
		})
	}

	if got := clonePayload(UserCardSnapshot{}).(UserCardSnapshot).Media; got != nil {
		t.Fatalf("nil media = %#v, want nil", got)
	}
	if got := clonePayload(TeamCardSnapshot{}).(TeamCardSnapshot).Update.Tasks; got != nil {
		t.Fatalf("nil tasks = %#v, want nil", got)
	}
}
