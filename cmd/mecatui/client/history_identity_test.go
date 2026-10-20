package client

import (
	"reflect"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestHistoricalV2Mapping(t *testing.T) {
	h := &mecatlv1.HistoricalRecord{RecordId: "log:7", Kind: "tool.result", Text: "old output", AttachmentUnavailable: true,
		Parts: []*mecatlv1.Content{{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte{1, 2}}}}
	want := HistoricalRecord{RecordID: "log:7", Kind: "tool.result", Text: "old output", AttachmentUnavailable: true,
		Parts: []ContentBlock{{Kind: ContentBlockImage, MimeType: "image/png", Data: []byte{1, 2}}}}
	if got := EventToMsg(&mecatlv1.Event{Type: "historical", Historical: h}); !reflect.DeepEqual(got, want) {
		t.Fatalf("historical event = %#v, want %#v", got, want)
	}
	// A standalone record never gains ordinary tool/control plumbing, even if
	// another payload is present on the wire.
	message := &mecatlv1.ConversationMessage{Role: "tool", Historical: h, ProviderPhase: "commentary",
		ToolCalls: []*mecatlv1.ToolCall{{Id: "not-a-target"}}, ToolResult: &mecatlv1.ToolResult{CallId: "not-a-target"}}
	archive := EventToMsg(&mecatlv1.Event{Type: "compaction.archive", CompactionArchive: &mecatlv1.CompactionArchive{Replaced: []*mecatlv1.ConversationMessage{message}}}).(CompactionArchiveMsg)
	got := archive.Replaced[0]
	if !reflect.DeepEqual(got.Historical, &want) || len(got.ToolCalls) != 0 || got.ToolResult != nil || got.ProviderPhase != "commentary" {
		t.Fatalf("archive historical mapping = %#v", got)
	}
	if EventToMsg(&mecatlv1.Event{Type: "historical"}) != nil || historicalFromProto(nil) != nil {
		t.Fatal("missing historical payload manufactured content")
	}
	for _, field := range []string{"Reasoning", "ReasoningItemID", "Replay"} {
		if _, ok := reflect.TypeFor[ConversationMessage]().FieldByName(field); ok {
			t.Fatalf("withdrawn private field %s remains", field)
		}
	}
}

func TestPendingV2ControlsUseAskAuthorityNotHistoricalOrCallIdentity(t *testing.T) {
	const askID = "child-session:2:ask-token"
	const args = `{"command":"printf 'full decoded command'\nnext", "timeout_ms":7500, "background":true, "temp_scope":"system"}`
	ask := ordinaryAsk("current-run", askID)
	ask.Ask.Args = args
	service := &pendingApprovalWireServer{frames: []*mecatlv1.WatchSessionEventsResponse{
		watchFrame("c1", "replay", &mecatlv1.Event{Type: "tool.call", RunId: "current-run", ToolCall: &mecatlv1.ToolCall{Id: "canonical-call", Name: "Shell", Args: args}}),
		watchFrame("c2", "replay", ask),
		watchFrame("c3", "replay", &mecatlv1.Event{Type: "historical", RunId: "current-run", Historical: &mecatlv1.HistoricalRecord{RecordId: "canonical-call", Kind: "approval", Text: "old approval", AttachmentUnavailable: true}}),
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
		t.Fatalf("control inferred tool/historical authority: %#v", req)
	}
}

func TestLearningEvidenceV2UnavailableAttachmentRetainsCitation(t *testing.T) {
	got := mapLearningProposal(&mecatlv1.LearningProposal{Evidence: []*mecatlv1.LearningEvidenceRef{{
		SessionId: "source", Locator: "event", EventSeq: 7, Digest: "digest", Protocol: "v1",
		Available: true, Preview: "verified readable citation", AttachmentUnavailable: true,
	}}}).Evidence[0]
	if got.ToolCallID != "" || !got.Available || !got.AttachmentUnavailable || got.Protocol != "v1" || got.Preview != "verified readable citation" || got.EventSeq != 7 {
		t.Fatalf("citation mapping = %#v", got)
	}
}
