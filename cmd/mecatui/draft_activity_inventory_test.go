package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

type activityStartupResumeSource struct{ *fakeStartupResumeSource }

func (*activityStartupResumeSource) SupportsSessionActivityInventory(context.Context) bool {
	return true
}

func TestDraftAwareSessionInventory_Scenario3_LatestResumeUsesActivityAndTranscriptGuard(t *testing.T) {
	source := &activityStartupResumeSource{&fakeStartupResumeSource{
		rows: []client.SessionListItem{
			{ID: "draft", ModifiedAt: 40, Kind: client.SessionKindMain, State: "completed", UsageState: client.SessionActivityDraft, Capabilities: client.SessionInventoryCapabilities{PublicChat: true}},
			{ID: "empty-active", ModifiedAt: 30, Kind: client.SessionKindMain, State: "completed", UsageState: client.SessionActivityActive, Capabilities: client.SessionInventoryCapabilities{PublicChat: true}},
			{ID: "older-active", ModifiedAt: 20, Kind: client.SessionKindMain, State: "completed", UsageState: client.SessionActivityActive, Capabilities: client.SessionInventoryCapabilities{PublicChat: true}},
		},
		transcripts: map[string]client.SessionTranscript{
			"draft":        {SessionID: "draft", Complete: true, Kind: client.SessionKindMain},
			"empty-active": {SessionID: "empty-active", Complete: true},
			"older-active": {SessionID: "older-active", Complete: true, Messages: []client.ConversationMessage{{Role: "user", Text: "keep"}}},
		},
		transcriptErrs: map[string]error{"failed-draft": errors.New("stale transcript")},
	}}
	selection, err := resolveStartupResume(t.Context(), source, "", true)
	if err != nil || selection == nil || selection.Row.ID != "older-active" {
		t.Fatalf("selection = %+v, %v", selection, err)
	}
	if want := []string{"empty-active", "older-active"}; !reflect.DeepEqual(source.transcriptCalls, want) {
		t.Fatalf("transcript checks = %v, want %v", source.transcriptCalls, want)
	}

	if _, err := resolveStartupResume(t.Context(), source, "failed-draft", false); err == nil {
		t.Fatal("failed exact resume unexpectedly succeeded")
	}
	if len(source.rows) != 3 || source.rows[0].ID != "draft" || source.rows[0].UsageState != client.SessionActivityDraft {
		t.Fatalf("failed/stale resume mutated draft inventory: %+v", source.rows)
	}
}
