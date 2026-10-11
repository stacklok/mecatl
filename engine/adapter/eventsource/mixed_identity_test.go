package eventsource_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestFoldRejectsTwoOpenLegacyProviderOccurrences(t *testing.T) {
	first := toolCall("reused", "Read", `{}`)
	second := toolCall("reused", "Write", `{}`)
	result := session.NewToolResult(first.ID, "done") // old ID-only event
	ambiguous := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &first},
		{Type: session.EvToolCall, ToolCall: &second},
		{Type: session.EvToolResult, ToolResult: &result},
	}
	if _, err := eventsource.Fold(meta(), seq(ambiguous)); !errors.Is(err, eventsource.ErrReconstruct) {
		t.Fatalf("ambiguous open provider pair folded: %v", err)
	}
	completed := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &first},
		{Type: session.EvToolResult, ToolResult: &result},
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &second},
		{Type: session.EvToolResult, ToolResult: &result},
	}
	folded, err := eventsource.Fold(meta(), seq(completed))
	if err != nil {
		t.Fatalf("completed provider ID reuse: %v", err)
	}
	if len(folded.Conversation.Messages) != 4 || folded.Conversation.Messages[0].ToolCalls[0].InvocationID == folded.Conversation.Messages[2].ToolCalls[0].InvocationID {
		t.Fatalf("completed occurrences not distinct: %+v", folded.Conversation.Messages)
	}
}

func TestFoldMixedLegacyLogAndPendingSnapshotContinuation(t *testing.T) {
	call := toolCall("provider-reused", "external", `{}`)
	result := session.NewToolResult(call.ID, "done")
	result.InvocationID = "legacy_tc_snapshot_occurrence"
	events := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &call},
		authorizationRequired("auth-1", call.ID),
		{Type: session.EvToolResult, ToolResult: &result},
		authorizationResolved("auth-1", call.ID, session.AuthorizationDenied),
	}
	folded, err := eventsource.Fold(meta(), seq(events))
	if err != nil {
		t.Fatal(err)
	}
	messages := folded.Conversation.Messages
	if len(messages) != 2 || len(messages[0].ToolCalls) != 1 || messages[1].ToolResult == nil {
		t.Fatalf("unexpected history: %+v", messages)
	}
	logID := messages[0].ToolCalls[0].InvocationID
	if !strings.HasPrefix(string(logID), "legacy_event_tc_") || messages[1].ToolResult.InvocationID != result.InvocationID || logID == result.InvocationID {
		t.Fatalf("distinct proven anchors collapsed: log=%q continuation=%q", logID, messages[1].ToolResult.InvocationID)
	}
	if call.InvocationID != "" || result.InvocationID != "legacy_tc_snapshot_occurrence" {
		t.Fatal("fold mutated source events")
	}
	if err := folded.RestoreInvocationIDs(nil, nil); err != nil || folded.Conversation.Messages[0].ToolCalls[0].InvocationID != logID || folded.Conversation.Messages[1].ToolResult.InvocationID != result.InvocationID {
		t.Fatalf("normal snapshot backfill collapsed mixed domains: %v", err)
	}
	wire, err := sessnap.Marshal(folded)
	if err != nil {
		t.Fatalf("save mixed fold: %v", err)
	}
	reloaded, err := sessnap.Unmarshal(wire)
	if err != nil || reloaded.Conversation.Messages[0].ToolCalls[0].InvocationID != logID || reloaded.Conversation.Messages[1].ToolResult.InvocationID != result.InvocationID {
		t.Fatalf("reload mixed fold lost identity: %v", err)
	}
	// A truly conflicting explicit ID is not a mixed legacy-domain continuation.
	bad := result
	bad.InvocationID = "tc_unproven"
	events[3].ToolResult = &bad
	if _, err := eventsource.Fold(meta(), seq(events)); err == nil {
		t.Fatal("unproven conflicting identity accepted")
	}
}
