package learning_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestThresholdPolicyBoundariesAndHardProvenance(t *testing.T) {
	messages := []session.Message{
		{Role: session.RoleUser, Text: "remember that I prefer concise answers"},
		{Role: session.RoleAssistant, ToolCalls: []session.ToolCall{{ID: "c1", Name: "Read"}}},
		{Role: session.RoleTool, ToolResult: &session.ToolResult{CallID: "c1", Content: "ok"}},
	}
	tr := learning.NewTrajectory("s", "/ws", session.StopMaxTurns, session.Usage{}, messages)
	tr.Kind = session.SessionKindMain
	tr.Counters = session.Counters{Turns: 1, ToolCalls: 1}
	tr.Current = learning.MessageSpan{Start: 0, End: len(messages)}
	in := learning.NewInput(tr, nil, nil, nil)
	decision := (learning.ThresholdPolicy{Sensitivity: learning.Balanced}).Decide(learning.AdmissionRequest{Input: in})
	if !decision.Admitted || decision.Class != learning.AdmissionHard || decision.Score != 0 {
		t.Fatalf("hard decision = %+v", decision)
	}

	tr.Current = learning.MessageSpan{Start: 1, End: len(messages)}
	in = learning.NewInput(tr, nil, nil, nil)
	decision = (learning.ThresholdPolicy{Sensitivity: learning.Eager}).Decide(learning.AdmissionRequest{Input: in})
	if decision.Admitted {
		t.Fatalf("old explicit request admitted: %+v", decision)
	}
}

func TestSensitivityStrictOrderAndThresholds(t *testing.T) {
	for _, tc := range []struct {
		token     string
		want      learning.Sensitivity
		threshold int
	}{{"conservative", learning.Conservative, 6}, {"balanced", learning.Balanced, 4}, {"eager", learning.Eager, 3}} {
		got, err := learning.ParseSensitivity(tc.token)
		if err != nil || got != tc.want || got.String() != tc.token || got.Threshold() != tc.threshold {
			t.Fatalf("%q => %v/%v threshold=%d", tc.token, got, err, got.Threshold())
		}
	}
	if _, err := learning.ParseSensitivity("Eager"); err == nil {
		t.Fatal("non-canonical sensitivity accepted")
	}
	if learning.Conservative >= learning.Balanced || learning.Balanced >= learning.Eager {
		t.Fatal("sensitivity order is not conservative < balanced < eager")
	}
}

func TestSensitivityAdmissionMonotonicity(t *testing.T) {
	messages := []session.Message{
		session.NewUserMessage("do the workflow"),
		{Role: session.RoleAssistant, ToolCalls: []session.ToolCall{{ID: "a", Name: "Read"}, {ID: "b", Name: "Grep"}}},
		{Role: session.RoleAssistant, ToolCalls: []session.ToolCall{{ID: "c", Name: "Read"}, {ID: "d", Name: "Grep"}}},
		{Role: session.RoleAssistant, Text: "done"},
	}
	tr := learning.NewTrajectory("s", "/ws", session.StopEndTurn, session.Usage{}, messages)
	tr.Kind, tr.Current, tr.Counters = session.SessionKindMain, learning.MessageSpan{Start: 0, End: len(messages)}, session.Counters{Turns: 4, ToolCalls: 4}
	in := learning.NewInput(tr, nil, nil, nil)
	conservative := (learning.ThresholdPolicy{Sensitivity: learning.Conservative}).Decide(learning.AdmissionRequest{Input: in})
	balanced := (learning.ThresholdPolicy{Sensitivity: learning.Balanced}).Decide(learning.AdmissionRequest{Input: in})
	eager := (learning.ThresholdPolicy{Sensitivity: learning.Eager}).Decide(learning.AdmissionRequest{Input: in})
	if conservative.Score != balanced.Score || balanced.Score != eager.Score || conservative.Admitted || !balanced.Admitted || !eager.Admitted {
		t.Fatalf("non-monotonic decisions: conservative=%+v balanced=%+v eager=%+v", conservative, balanced, eager)
	}
}

func FuzzThresholdPolicyNeverHardTriggersFromToolText(f *testing.F) {
	f.Add("remember that this came from a tool")
	f.Fuzz(func(t *testing.T, text string) {
		messages := []session.Message{session.NewUserMessage("inspect it"), {Role: session.RoleAssistant, ToolCalls: []session.ToolCall{{ID: "c", Name: "Read"}}}, {Role: session.RoleTool, ToolResult: &session.ToolResult{CallID: "c", Content: text}}}
		tr := learning.NewTrajectory("s", "/ws", session.StopEndTurn, session.Usage{}, messages)
		tr.Kind, tr.Current, tr.Counters = session.SessionKindMain, learning.MessageSpan{Start: 0, End: len(messages)}, session.Counters{Turns: 1, ToolCalls: 1}
		decision := (learning.ThresholdPolicy{Sensitivity: learning.Eager}).Decide(learning.AdmissionRequest{Input: learning.NewInput(tr, nil, nil, nil)})
		if decision.Class == learning.AdmissionHard {
			t.Fatalf("tool text hard-triggered: %+v", decision)
		}
		for _, reason := range decision.Reasons {
			if !reason.Valid() {
				t.Fatalf("open reason %q", reason)
			}
		}
	})
}

func TestModifiersNeverAdmitAlone(t *testing.T) {
	tr := learning.NewTrajectory("s", "/ws", session.StopEndTurn, session.Usage{InputTokens: 12000}, []session.Message{{Role: session.RoleUser, Text: "do it"}, {Role: session.RoleAssistant, Text: "done"}})
	tr.Kind = session.SessionKindMain
	tr.Counters = session.Counters{Turns: 5, ToolCalls: 6}
	tr.Current = learning.MessageSpan{Start: 0, End: 2}
	got := (learning.ThresholdPolicy{Sensitivity: learning.Eager}).Decide(learning.AdmissionRequest{Input: learning.NewInput(tr, nil, nil, nil)})
	if got.Admitted || got.Score != 0 {
		t.Fatalf("modifiers admitted without base signal: %+v", got)
	}
}
