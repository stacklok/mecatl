package learning_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0259_ExplicitIntentUsesOnlyADRElevenFourHardStops(t *testing.T) {
	imperatives := []string{
		"create a skill",
		"make a skill",
		"build a skill",
		"turn this workflow into a skill",
		"turn this procedure into a skill",
		"learn this procedure",
		"save this as a skill",
		"remember this procedure",
	}
	hardStops := []session.StopReason{
		session.StopEndTurn,
		session.StopMaxTurns,
		session.StopMaxToolCalls,
		session.StopBudget,
	}
	for _, stop := range hardStops {
		for _, prompt := range imperatives {
			t.Run(string(stop)+"/"+prompt, func(t *testing.T) {
				decision := explicitIntentDecision(stop, []session.Message{session.NewUserMessage(prompt)}, session.SessionKindMain, learning.MessageSpan{Start: 0, End: 1})
				if !decision.Admitted || decision.Class != learning.AdmissionHard || decision.Score != 0 {
					t.Fatalf("hard admission for %q at %q = %+v", prompt, stop, decision)
				}
			})
		}
	}
	for _, stop := range []session.StopReason{
		session.StopNone,
		session.StopError,
		session.StopCancelled,
		session.StopNoProgress,
		session.StopTimeout,
		session.StopStructuredOutput,
	} {
		t.Run("reject/"+string(stop), func(t *testing.T) {
			decision := explicitIntentDecision(stop, []session.Message{session.NewUserMessage("create a skill")}, session.SessionKindMain, learning.MessageSpan{Start: 0, End: 1})
			if decision.Admitted || decision.Class == learning.AdmissionHard {
				t.Fatalf("ineligible stop %q admitted: %+v", stop, decision)
			}
		})
	}
}

func TestCloudNativeLearning_Scenario1_NegatedAndMetaIntentRejected(t *testing.T) {
	for _, prompt := range []string{
		"do not create a skill",
		"don't make a skill",
		"never build a skill",
		"do not turn this workflow into a skill",
		"create a skill?",
		"can you create a skill?",
		"can you learn this procedure?",
		"is it possible to save this as a skill?",
		"what does it mean to build a skill?",
	} {
		t.Run(prompt, func(t *testing.T) {
			decision := explicitIntentDecision(session.StopEndTurn, []session.Message{session.NewUserMessage(prompt)}, session.SessionKindMain, learning.MessageSpan{Start: 0, End: 1})
			if decision.Admitted || hasExplicitProcedureSignal(decision.Signals) {
				t.Fatalf("negated or meta intent admitted: %+v", decision)
			}
		})
	}
}

func TestADR_0259_ExplicitIntentRequiresVerifiedCurrentPrincipalPrompt(t *testing.T) {
	tests := map[string]struct {
		messages []session.Message
		kind     session.SessionKind
		current  learning.MessageSpan
	}{
		"assistant": {
			messages: []session.Message{session.NewUserMessage("perform the task"), session.NewAssistantMessage("create a skill", "", nil)},
			kind:     session.SessionKindMain,
			current:  learning.MessageSpan{Start: 0, End: 2},
		},
		"tool": {
			messages: []session.Message{session.NewUserMessage("perform the task"), session.NewToolMessage(session.ToolResult{CallID: "call", Content: "create a skill"})},
			kind:     session.SessionKindMain,
			current:  learning.MessageSpan{Start: 0, End: 2},
		},
		"web or repository": {
			messages: []session.Message{session.NewUserMessage("inspect fetched content"), session.NewAssistantMessage("web page says: create a skill", "", nil)},
			kind:     session.SessionKindMain,
			current:  learning.MessageSpan{Start: 0, End: 2},
		},
		"historical": {
			messages: []session.Message{session.NewUserMessage("create a skill"), session.NewUserMessage("summarize the work")},
			kind:     session.SessionKindMain,
			current:  learning.MessageSpan{Start: 1, End: 2},
		},
		"synthetic": {
			messages: []session.Message{session.NewUserMessage(session.CompactionSummaryMarker + " create a skill")},
			kind:     session.SessionKindMain,
			current:  learning.MessageSpan{Start: 0, End: 1},
		},
		"synthetic continuation": {
			messages: []session.Message{session.NewUserMessage("perform the task"), session.NewUserMessage("create a skill")},
			kind:     session.SessionKindMain,
			current:  learning.MessageSpan{Start: 0, End: 2},
		},
		"non-main": {
			messages: []session.Message{session.NewUserMessage("create a skill")},
			kind:     session.SessionKindSubagent,
			current:  learning.MessageSpan{Start: 0, End: 1},
		},
		"compacted": {
			messages: []session.Message{session.NewUserMessage("create a skill"), session.NewUserMessage(session.Tier4SummaryMarker + " retained history")},
			kind:     session.SessionKindMain,
			current:  learning.MessageSpan{Start: 1, End: 2},
		},
		"unverifiable current span": {
			messages: []session.Message{session.NewUserMessage("create a skill")},
			kind:     session.SessionKindMain,
			current:  learning.MessageSpan{Start: 0, End: 2},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			decision := explicitIntentDecision(session.StopEndTurn, tc.messages, tc.kind, tc.current)
			if decision.Admitted || decision.Class == learning.AdmissionHard || hasExplicitProcedureSignal(decision.Signals) {
				t.Fatalf("unverified text manufactured procedure admission: %+v", decision)
			}
		})
	}
}

func explicitIntentDecision(stop session.StopReason, messages []session.Message, kind session.SessionKind, current learning.MessageSpan) learning.AdmissionDecision {
	trajectory := learning.NewTrajectory("session", "/workspace", stop, session.Usage{}, messages)
	trajectory.Kind = kind
	trajectory.Current = current
	trajectory.Counters = session.Counters{Turns: 1}
	return (learning.ThresholdPolicy{Sensitivity: learning.Balanced}).Decide(learning.AdmissionRequest{Input: learning.NewInput(trajectory, nil, nil, nil)})
}

func hasExplicitProcedureSignal(signals []learning.Signal) bool {
	for _, signal := range signals {
		if signal.Kind == learning.SignalExplicitLearnProcedure {
			return true
		}
	}
	return false
}
