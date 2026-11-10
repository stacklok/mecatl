package client

import (
	"reflect"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestTranscriptMappingOmitsPrivateReplay(t *testing.T) {
	for _, field := range []string{"Reasoning", "ReasoningItemID", "Replay", "Historical"} {
		if _, ok := reflect.TypeFor[ConversationMessage]().FieldByName(field); ok {
			t.Fatalf("withdrawn private field %s remains", field)
		}
	}
}

func TestPendingV2ControlsUseAskAuthorityNotCallIdentity(t *testing.T) {
	const askID = "child-session:2:ask-token"
	const args = `{"command":"printf 'full decoded command'\nnext", "timeout_ms":7500, "background":true, "temp_scope":"system"}`
	ask := ordinaryAsk("current-run", askID)
	ask.Ask.Args = args
	service := &pendingApprovalWireServer{frames: []*mecatlv1.WatchSessionEventsResponse{
		watchFrame("c1", "replay", &mecatlv1.Event{Type: "tool.call", RunId: "current-run", ToolCall: &mecatlv1.ToolCall{Id: "canonical-call", Name: "Shell", Args: args}}),
		watchFrame("c2", "replay", ask),
		watchFrame("c3", "live", nil),
	}}
	cl := newPendingApprovalWireClient(t, service)
	pending, err := cl.DiscoverPendingApproval(t.Context(), "parent-session")
	if err != nil {
		t.Fatal(err)
	}
	if pending.AskID != askID || pending.Args != args || pending.RunID != "current-run" {
		t.Fatalf("exact ask/projection lost: %#v", pending)
	}
	if err := cl.ResolvePendingApproval(t.Context(), pending, VerdictAllowOnce); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if req := service.resolveReq; req.GetAskId() != askID || req.GetExpectedRunId() != "current-run" || req.GetSessionId() != "parent-session" {
		t.Fatalf("control inferred tool authority: %#v", req)
	}
}

func TestLearningEvidenceV2UnavailableAttachmentRetainsCitation(t *testing.T) {
	got := mapLearningProposal(&mecatlv1.LearningProposal{Evidence: []*mecatlv1.LearningEvidenceRef{{
		SessionId: "source", Locator: "event", EventSeq: 7, Digest: "digest", Protocol: "reflection-evidence/v2",
		Available: true, Preview: "verified readable citation", AttachmentUnavailable: true,
	}}}).Evidence[0]
	if got.ToolCallID != "" || !got.Available || !got.AttachmentUnavailable || got.Protocol != "reflection-evidence/v2" || got.Preview != "verified readable citation" || got.EventSeq != 7 {
		t.Fatalf("citation mapping = %#v", got)
	}
}
