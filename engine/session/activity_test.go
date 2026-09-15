package session

import "testing"

func TestDraftAwareSessionInventory_Scenario1_ClassifiesEmptyTextAndMultimodalHistory(t *testing.T) {
	image := Content{Kind: MediaImage, MIMEType: "image/png", Data: []byte{1}}
	tests := []struct {
		name     string
		messages []Message
		want     ActivityState
	}{
		{name: "empty", want: ActivityDraft},
		{name: "empty user message is genuine", messages: []Message{NewUserMessage("")}, want: ActivityActive},
		{name: "multimodal user message", messages: []Message{NewUserMessageWithParts("", []Content{image})}, want: ActivityActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ActivityOf(tt.messages); got != tt.want {
				t.Fatalf("ActivityOf(%v) = %q, want %q", tt.messages, got, tt.want)
			}
		})
	}
}

func TestDraftAwareSessionInventory_Scenario1_CompactionSummaryDoesNotActivateDraft(t *testing.T) {
	messages := []Message{{Role: RoleUser, Text: CompactionSummaryMarker + " prior turns"}}
	if got := ActivityOf(messages); got != ActivityDraft {
		t.Fatalf("ActivityOf(compaction summary) = %q, want %q", got, ActivityDraft)
	}
}

func TestDraftAwareSessionInventory_Scenario1_ValidHistoryIsNeverUnknown(t *testing.T) {
	for _, messages := range [][]Message{
		nil,
		{{Role: RoleAssistant, Text: "answer"}},
		{NewUserMessage("hello")},
	} {
		if got := ActivityOf(messages); got != ActivityDraft && got != ActivityActive {
			t.Fatalf("ActivityOf(%v) = %q, want draft or active", messages, got)
		}
	}
}
