package ui

import (
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestADR_0301_UIBlockIdentityResetsWithConversation pins ADR 0301's client-local
// document identity: blocks and the changed-files appendix receive monotonic
// identities from their conversation, while a reconstructed conversation begins a
// new document.
func TestADR_0301_UIBlockIdentityResetsWithConversation(t *testing.T) {
	var c conversation
	c.addUser("first")
	c.startAssistant()
	c.appendAssistant("answer")

	if got, want := c.testBlocks()[0].id, uint64(1); got != want {
		t.Fatalf("first block ID = %d, want %d", got, want)
	}
	if got, want := c.testBlocks()[1].id, uint64(2); got != want {
		t.Fatalf("second block ID = %d, want %d", got, want)
	}
	if got, want := c.testBlocks()[1].id, uint64(2); got != want {
		t.Fatalf("mutated block ID = %d, want %d", got, want)
	}

	c.recordFileChange("first.go")
	c.recordFileChange("second.go")
	c.recordFileChange("first.go")
	if got, want := c.testChangedFiles(), []string{"first.go", "second.go"}; !equalStrings(got, want) {
		t.Fatalf("changed files = %v, want %v", got, want)
	}
	if got, want := c.testAppendixID(), uint64(3); got != want {
		t.Fatalf("changed-files appendix ID = %d, want %d", got, want)
	}

	rebuilt := conversationFromTranscript([]client.ConversationMessage{{Role: "user", Text: "fresh document"}})
	rebuilt.recordFileChange("fresh.go")
	if got, want := rebuilt.testBlocks()[0].id, uint64(1); got != want {
		t.Fatalf("rebuilt first block ID = %d, want %d", got, want)
	}
	if got, want := rebuilt.testAppendixID(), uint64(2); got != want {
		t.Fatalf("rebuilt changed-files appendix ID = %d, want %d", got, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
