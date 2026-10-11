package sessnap_test

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyMalformedOutstandingLoadsButCannotBeAdmitted(t *testing.T) {
	s := session.New("legacy-malformed", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("duplicate", "Read", []byte(`{}`))
	if err := s.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call, call})); err != nil {
		t.Fatal(err)
	}
	restored, err := sessnap.Unmarshal(mustMarshal(t, s))
	if err != nil {
		t.Fatalf("old malformed snapshot must load: %v", err)
	}
	if err := restored.ValidateIncomingCalls([]session.ToolCall{call}); err == nil {
		t.Fatal("admitted ambiguous restored turn")
	}
}

func TestInvocationTranslationDoesNotMutateSharedSnapshot(t *testing.T) {
	original := authorizingSnapshotSession(t)
	snapshot, err := sessnap.Of(original)
	if err != nil {
		t.Fatal(err)
	}
	before := mustMarshal(t, original)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			restored, err := snapshot.Restore()
			if err != nil {
				t.Error(err)
				return
			}
			if restored.Conversation.Messages[0].ToolCalls[0].InvocationID == "" || restored.Conversation.Messages[1].ToolResult.InvocationID == "" {
				t.Error("missing restored occurrence identity")
			}
		})
	}
	wg.Wait()
	if !bytes.Equal(before, mustMarshal(t, original)) {
		t.Fatal("restoration mutated the source session through shared snapshot values")
	}
}

func TestLegacyAuthorizationOccurrenceTranslation(t *testing.T) {
	original := authorizingSnapshotSession(t)
	wire := mustMarshal(t, original)
	var first session.InvocationID
	for i := 0; i < 2; i++ {
		restored, err := sessnap.Unmarshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		calls := restored.Conversation.Messages[0].ToolCalls
		pending, ok := restored.PendingAuthorization()
		if !ok {
			t.Fatal("missing authorization")
		}
		if calls[0].InvocationID == "" || calls[1].InvocationID == "" || calls[1].InvocationID == calls[2].InvocationID || pending.Call.InvocationID != calls[1].InvocationID || pending.Deferred[0].InvocationID != calls[2].InvocationID || restored.Conversation.Messages[1].ToolResult.InvocationID != calls[0].InvocationID {
			t.Fatalf("unpaired legacy occurrences: calls=%+v pending=%+v result=%+v", calls, pending, restored.Conversation.Messages[1].ToolResult)
		}
		if i > 0 && first != calls[1].InvocationID {
			t.Fatalf("translation changed: %q -> %q", first, calls[1].InvocationID)
		}
		first = calls[1].InvocationID
		if i == 0 {
			saved, err := sessnap.Marshal(restored)
			if err != nil {
				t.Fatal(err)
			}
			again, err := sessnap.Unmarshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			next, _ := again.PendingAuthorization()
			if next.Call.InvocationID != first || !bytes.Equal(next.Call.Args, effectiveCallArgs) || next.Call.ItemID != "provider-item-parked" {
				t.Fatalf("normal save lost effective occurrence: %+v", next.Call)
			}
		}
	}
}
