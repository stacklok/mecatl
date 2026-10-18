package learning_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func migratedEvidenceSource(t *testing.T, user bool) *session.Session {
	t.Helper()
	prefix := ""
	if user {
		prefix = `{"role":"user","text":"remember legacy coordinates"},`
	}
	raw := `{"id":"legacy-evidence-session","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[` + prefix + `{"role":"assistant","tool_calls":[{"ID":"provider-call-legacy","Name":"Read"}],"reasoning":"private-replay"},{"role":"tool","tool_result":{"CallID":"provider-call-legacy","Content":"legacy result"}}]}`
	s, err := sessnap.Unmarshal([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func frozenV1(t *testing.T) learning.Input {
	t.Helper()
	raw, err := os.ReadFile("testdata/legacy-evidence-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var in learning.Input
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestFrozenEvidenceVerifiesActualMigratedSources(t *testing.T) {
	var v0 struct {
		Input learning.Input
		Ref   learning.EvidenceRef
	}
	raw, err := os.ReadFile("testdata/legacy-evidence-v0.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &v0); err != nil {
		t.Fatal(err)
	}
	s := migratedEvidenceSource(t, false)
	for range 3 {
		citation, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: s.ID, Messages: s.Conversation.Messages}, nil, s.Incarnation(), v0.Ref)
		if err != nil {
			t.Fatal(err)
		}
		if citation.ToolCallID != s.Conversation.Messages[0].ToolCalls[0].ID || strings.Contains(citation.Preview, "provider-call-legacy") || strings.Contains(citation.Preview, "private-replay") {
			t.Fatalf("unsafe citation: %+v", citation)
		}
		encoded, err := sessnap.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		s, err = sessnap.Unmarshal(encoded)
		if err != nil {
			t.Fatal(err)
		}
	}
	old := frozenV1(t)
	manifestBytes, _ := json.Marshal(old.Manifest)
	ref, err := learning.ResolveEvidenceHandle(old, "m:1")
	if err != nil {
		t.Fatal(err)
	}
	s = migratedEvidenceSource(t, true)
	for range 2 {
		in, err := learning.RematerializeEvidence(learning.Trajectory{SessionID: s.ID, Messages: s.Conversation.Messages}, nil, s.Incarnation(), *old.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := learning.ResolveEvidence(in, ref); err != nil {
			t.Fatal(err)
		}
		citation, err := learning.CanonicalEvidenceCitation(in, ref)
		if err != nil {
			t.Fatal(err)
		}
		if citation.ToolCallID != s.Conversation.Messages[1].ToolCalls[0].ID || strings.Contains(citation.Preview, "provider-call-legacy") {
			t.Fatalf("citation %+v", citation)
		}
		after, _ := json.Marshal(old.Manifest)
		if string(after) != string(manifestBytes) {
			t.Fatal("rewrote stored manifest")
		}
		encoded, err := sessnap.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		s, err = sessnap.Unmarshal(encoded)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Removing unrelated retained history and active replay must not reindex old
	// verification coordinates or erase surviving source provenance.
	retained := session.CloneMessages(s.Conversation.Messages[1:])
	for i := range retained {
		retained[i].Replay = nil
	}
	ref.Protocol = ""
	ref.AggregateDigest = ""
	ref.ManifestIndex = 0
	ref.OriginalMessage = nil
	ref.Ordinal = 1
	if _, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: s.ID, Messages: retained}, nil, s.Incarnation(), ref); err != nil {
		t.Fatalf("retained source after prune: %v", err)
	}
	for name, mutate := range map[string]func([]session.Message){
		"content":     func(m []session.Message) { m[0].ToolCalls[0].Name = "tampered" },
		"coordinate":  func(m []session.Message) { m[0].Legacy.Ordinal = 88 },
		"incarnation": func(m []session.Message) { m[0].Legacy.Incarnation = session.NewIncarnationID() },
		"namespace":   func(m []session.Message) { m[0].Legacy.Namespace = "eventlog/legacy-v1" },
	} {
		t.Run(name, func(t *testing.T) {
			messages := session.CloneMessages(retained)
			mutate(messages)
			if _, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: s.ID, Messages: messages}, nil, s.Incarnation(), ref); err == nil {
				t.Fatal("changed source verified")
			}
		})
	}
	if _, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: s.ID, Messages: retained[1:]}, nil, s.Incarnation(), ref); err == nil {
		t.Fatal("pruned source recovered")
	}
}

func TestLegacyUnsupportedCitationAvailableWithoutAttachment(t *testing.T) {
	s, err := sessnap.Unmarshal([]byte(`{"id":"legacy-evidence-session","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[{"role":"assistant","text":"provider-call-legacy embedded private-item","tool_calls":[{"ID":"provider-call-legacy","Name":"provider-call-legacy","item_id":"private-item"}],"reasoning":"private-replay","reasoning_item_id":"private-item"},{"role":"assistant","tool_calls":[{"ID":"provider-call-legacy","Name":"Read"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	m := s.Conversation.Messages[0]
	// Freeze the original verification representation, not the sanitized preview.
	old := learning.Input{Trajectory: learning.Trajectory{SessionID: s.ID, Messages: []session.Message{{Role: session.RoleAssistant, Text: "provider-call-legacy embedded private-item", ToolCalls: []session.ToolCall{{ID: "provider-call-legacy", Name: "provider-call-legacy"}}}}}}
	ref, err := learning.MessageEvidenceRef(old, 0, "provider-call-legacy")
	if err != nil {
		t.Fatal(err)
	}
	citation, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: s.ID, Messages: []session.Message{m}}, nil, s.Incarnation(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if citation.ToolCallID != "" {
		t.Fatal("unsupported occurrence attached")
	}
	for _, bad := range []string{"provider-call-legacy", "private-replay", "private-item", "reasoning_item_id"} {
		if strings.Contains(citation.Preview, bad) {
			t.Fatalf("preview leaked %q: %s", bad, citation.Preview)
		}
	}
	if !strings.Contains(citation.Preview, "embedded") {
		t.Fatal("citation content lost")
	}
}

func TestLegacyEventCoordinatesAndSafePreview(t *testing.T) {
	inc := session.NewIncarnationID()
	raw := []byte(`{"Type":"tool.call","Seq":77,"Turn":1,"ToolCall":{"ID":"provider-event-id","Name":"Read"}}`)
	event, err := eventcodec.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := eventcodec.Unmarshal([]byte(`{"Type":"tool.result","Seq":78,"Turn":1,"ToolResult":{"CallID":"provider-event-id","Content":"safe result"}}`))
	if err != nil {
		t.Fatal(err)
	}
	// The fixture supplies original source positions; Seq is run-local metadata.
	callIdentity := event.LegacyIdentity()
	callIdentity.Ordinal = 0
	event = event.WithLegacyIdentity(*callIdentity)
	resultIdentity := result.LegacyIdentity()
	resultIdentity.Ordinal = 1
	result = result.WithLegacyIdentity(*resultIdentity)
	projected, err := eventcodec.Project([]session.Event{event, result}, inc)
	if err != nil {
		t.Fatal(err)
	}
	old := learning.Input{Trajectory: learning.Trajectory{SessionID: "s"}, Events: []learning.EvidenceEventData{{Type: session.EvToolCall, Seq: 77, Turn: 1, ToolCall: &learning.ToolProjection{ID: "provider-event-id", Name: "Read"}}}}
	ref, err := learning.EventEvidenceRef(old, 0, "provider-event-id")
	if err != nil {
		t.Fatal(err)
	}
	// A partial record is valid citation evidence, but not an attached tool card.
	citation, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: "s"}, []session.Event{event}, inc, ref)
	if err != nil {
		t.Fatal(err)
	}
	if citation.ToolCallID != "" || strings.Contains(citation.Preview, "provider-event-id") {
		t.Fatalf("partial citation %+v", citation)
	}
	citation, err = learning.VerifyEvidenceSource(learning.Trajectory{SessionID: "s"}, projected.Events, inc, ref)
	if err != nil {
		t.Fatal(err)
	}
	if citation.ToolCallID != projected.Events[0].ToolCall.ID {
		t.Fatalf("proven citation %+v", citation)
	}
	bad := projected.Events[0]
	p := bad.LegacyIdentity()
	p.Ordinal = 77
	bad = bad.WithLegacyIdentity(*p)
	if _, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: "s"}, []session.Event{bad}, inc, ref); err == nil {
		t.Fatal("Seq used as ordinal")
	}
	if _, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: "s"}, projected.Events, session.NewIncarnationID(), ref); err == nil {
		t.Fatal("wrong incarnation verified")
	}
}

func TestLegacyTokenShapedIdentifiersStayPrivate(t *testing.T) {
	provider := "ghp_" + strings.Repeat("x", 24)
	snapshot := `{"id":"s","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[{"role":"assistant","text":"embedded PROVIDER","tool_calls":[{"ID":"PROVIDER","Name":"Read"}]},{"role":"tool","tool_result":{"CallID":"PROVIDER","Content":"safe"}}]}`
	source, err := sessnap.Unmarshal([]byte(strings.ReplaceAll(snapshot, "PROVIDER", provider)))
	if err != nil {
		t.Fatal(err)
	}
	message := session.Message{Role: session.RoleAssistant, Text: "embedded " + provider, ToolCalls: []session.ToolCall{{ID: session.ToolCallID(provider), Name: "Read"}}}
	original := learning.Input{Trajectory: learning.Trajectory{SessionID: "s", Messages: []session.Message{message}}}
	for _, input := range []learning.Input{original, learning.NewInput(original.Trajectory, nil, nil, nil)} {
		input.Protocol = ""
		id := input.Trajectory.Messages[0].ToolCalls[0].ID
		ref, err := learning.MessageEvidenceRef(input, 0, id)
		if err != nil {
			t.Fatal(err)
		}
		citation, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: source.ID, Messages: source.Conversation.Messages}, nil, source.Incarnation(), ref)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(citation)
		if strings.Contains(string(raw), provider) || strings.Contains(citation.Preview, string(ref.ToolCallID)) && string(ref.ToolCallID) != "[redacted-secret]" {
			t.Fatalf("legacy token leaked: %s", raw)
		}
	}
}

func TestCanonicalMaterializationOfPrunedMigratedHistory(t *testing.T) {
	source := migratedEvidenceSource(t, true)
	messages := session.CloneMessages(source.Conversation.Messages[1:])
	messages = append(messages, session.NewUserMessage("remember another preference"), session.NewAssistantMessage("noted", nil))
	materialized, err := learning.MaterializeEvidence(context.Background(), learning.MaterializationRequest{Trajectory: learning.Trajectory{SessionID: source.ID, Messages: messages}, Explicit: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := learning.ValidateInput(materialized.Input); err != nil {
		t.Fatal(err)
	}
	if _, err := learning.RematerializeEvidence(learning.Trajectory{SessionID: source.ID, Messages: messages}, nil, source.Incarnation(), materialized.Manifest); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyResultCitationRequiresCoordinateBoundAttachmentWitness(t *testing.T) {
	source := migratedEvidenceSource(t, false)
	original := learning.Input{Trajectory: learning.Trajectory{SessionID: source.ID, Messages: []session.Message{{}, session.NewToolMessage(session.NewToolResult("provider-call-legacy", "legacy result"))}}}
	ref, err := learning.MessageEvidenceRef(original, 1, "provider-call-legacy")
	if err != nil {
		t.Fatal(err)
	}
	citation, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: source.ID, Messages: source.Conversation.Messages}, nil, source.Incarnation(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if citation.ToolCallID != source.Conversation.Messages[0].ToolCalls[0].ID {
		t.Fatal("exact result witness was not attached")
	}
	retained := session.CloneMessages(source.Conversation.Messages[1:])
	citation, err = learning.VerifyEvidenceSource(learning.Trajectory{SessionID: source.ID, Messages: retained}, nil, source.Incarnation(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if citation.ToolCallID != "" || !strings.Contains(citation.Preview, "legacy result") {
		t.Fatal("valid retained result lost content or guessed a missing call")
	}
	messages := session.CloneMessages(source.Conversation.Messages)
	messages[1].ToolResult.CallID = "unrelated-canonical-id"
	citation, err = learning.VerifyEvidenceSource(learning.Trajectory{SessionID: source.ID, Messages: messages}, nil, source.Incarnation(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if citation.ToolCallID != "" {
		t.Fatal("tampered canonical attachment published")
	}
	for name, mutate := range map[string]func([]session.Message){
		"incarnation": func(m []session.Message) { m[0].Legacy.Incarnation = session.NewIncarnationID() },
		"namespace":   func(m []session.Message) { m[0].Legacy.Namespace = "eventlog/legacy-v1" },
		"slot":        func(m []session.Message) { m[0].Legacy.Calls[0].Slot++ },
		"provider":    func(m []session.Message) { m[0].Legacy.Calls[0].ProviderID = "other-provider" },
		"call":        func(m []session.Message) { m[0].ToolCalls[0].ID = "other-canonical" },
		"unsupported": func(m []session.Message) { m[0].Legacy.Unsupported = []byte(`{}`) },
	} {
		t.Run(name, func(t *testing.T) {
			messages := session.CloneMessages(source.Conversation.Messages)
			mutate(messages)
			citation, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: source.ID, Messages: messages}, nil, source.Incarnation(), ref)
			if err != nil || citation.ToolCallID != "" || !strings.Contains(citation.Preview, "legacy result") {
				t.Fatalf("unproven witness changed content availability or attached: %+v, %v", citation, err)
			}
		})
	}
}

func TestEvidenceSourceSparseEventSelection(t *testing.T) {
	trajectory := learning.Trajectory{SessionID: "s"}
	event := session.Event{Type: session.EvResult, Seq: 77, Result: &session.ResultPayload{Stop: session.StopEndTurn}}
	materialized, err := learning.MaterializeEvidence(t.Context(), learning.MaterializationRequest{
		Trajectory: trajectory,
		Events:     []session.Event{{Type: session.EvReasoningDelta}, {Type: session.EvReasoningDelta}, {Type: session.EvReasoningDelta}, event},
		Explicit:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := learning.ResolveEvidenceHandle(materialized.Input, "e:0")
	if err != nil {
		t.Fatal(err)
	}
	if ref.EventSeq == nil || *ref.EventSeq != 3 {
		t.Fatalf("source coordinate = %+v", ref)
	}
	for _, tc := range []struct {
		name     string
		events   []session.Event
		ordinals []int
		valid    bool
	}{
		{"sparse source", []session.Event{event}, []int{3}, true},
		{"missing source", nil, nil, false},
		{"local index is not source", []session.Event{event}, []int{0}, false},
		{"semantic sequence is not source", []session.Event{event}, []int{77}, false},
		{"ordinal count mismatch", []session.Event{event}, []int{3, 4}, false},
		{"ambiguous source", []session.Event{event, event}, []int{3, 3}, false},
		{"tampered content", []session.Event{{Type: session.EvResult, Seq: 78, Result: event.Result}}, []int{3}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inc := session.NewIncarnationID()
			_, citationErr := learning.VerifyEvidenceSource(trajectory, tc.events, inc, ref, tc.ordinals)
			_, manifestErr := learning.RematerializeEvidence(trajectory, tc.events, inc, materialized.Manifest, tc.ordinals)
			if (citationErr == nil) != tc.valid || (manifestErr == nil) != tc.valid {
				t.Fatalf("valid=%v citation=%v manifest=%v", tc.valid, citationErr, manifestErr)
			}
		})
	}
}

func TestLegacyVerificationInputBindingsAndPreview(t *testing.T) {
	inc := session.NewIncarnationID()
	call := session.ToolCall{ID: "provider-recovered", Name: "Read"}
	result := session.NewToolResult(call.ID, "safe recovered result")
	originalEvents := []session.Event{{Type: session.EvToolCall, Seq: 77, ToolCall: &call}, {Type: session.EvToolResult, Seq: 78, ToolResult: &result}}
	var decoded []session.Event
	for i, original := range originalEvents {
		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		event, err := eventcodec.Unmarshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		identity := event.LegacyIdentity()
		identity.Ordinal = i // Original coordinate in this complete fixture.
		decoded = append(decoded, event.WithLegacyIdentity(*identity))
	}
	projected, err := eventcodec.Project(decoded, inc)
	if err != nil {
		t.Fatal(err)
	}
	trajectory := learning.Trajectory{SessionID: "s", Messages: []session.Message{
		session.NewAssistantMessage("", []session.ToolCall{*projected.Events[0].ToolCall}),
		session.NewToolMessage(*projected.Events[1].ToolResult),
	}}
	in, err := learning.LegacyVerificationInput(trajectory, projected.Events)
	if err != nil {
		t.Fatal(err)
	}
	expected := learning.NewInput(learning.Trajectory{SessionID: "s", Messages: []session.Message{session.NewAssistantMessage("", []session.ToolCall{call}), session.NewToolMessage(result)}}, originalEvents, nil, nil)
	expected.Protocol = learning.ReflectionEvidenceLegacyV0
	for i := range trajectory.Messages {
		ref := mustMessageEvidenceRef(t, expected, i, call.ID)
		if err := learning.ResolveEvidence(in, ref); err != nil {
			t.Fatalf("frozen message identity changed: %v", err)
		}
		ref = mustEventEvidenceRef(t, expected, i, call.ID)
		if err := learning.ResolveEvidence(in, ref); err != nil {
			t.Fatalf("frozen event identity changed: %v", err)
		}
	}
	preview, err := learning.ProjectVerifiedInput(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), string(call.ID)) || !strings.Contains(string(raw), result.Content) {
		t.Fatalf("unsafe or missing preview content: %s", raw)
	}
	for name, mutate := range map[string]func([]session.Message){
		"unbound call":   func(m []session.Message) { m[0].ToolCalls[0].ID = "unproven" },
		"unbound result": func(m []session.Message) { m[1].ToolResult.CallID = "unproven" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := trajectory
			bad.Messages = session.CloneMessages(trajectory.Messages)
			mutate(bad.Messages)
			if _, err := learning.LegacyVerificationInput(bad, projected.Events); err == nil {
				t.Fatal("unproven binding accepted")
			}
		})
	}
	badEvent := session.CloneEventIdentity(projected.Events[0])
	badEvent.ToolCall.ID = "unproven"
	if _, err := learning.LegacyVerificationInput(trajectory, []session.Event{badEvent, projected.Events[1]}); err == nil {
		t.Fatal("unproven event occurrence accepted")
	}
	if _, err := learning.LegacyVerificationInput(trajectory, originalEvents); err == nil {
		t.Fatal("canonical source treated as legacy")
	}
}

func TestNewEvidenceExplicitCanonicalProtocol(t *testing.T) {
	s := migratedEvidenceSource(t, false)
	materialized, err := learning.MaterializeEvidence(context.Background(), learning.MaterializationRequest{Trajectory: learning.Trajectory{SessionID: s.ID, Messages: s.Conversation.Messages}, Explicit: true})
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Manifest.Protocol != learning.ReflectionEvidenceV2 {
		t.Fatal("new evidence reinterpreted v1")
	}
	ref, err := learning.ResolveEvidenceHandle(materialized.Input, "m:0")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Protocol != learning.ReflectionEvidenceV2 || ref.ToolCallID != s.Conversation.Messages[0].ToolCalls[0].ID {
		t.Fatalf("ref %+v", ref)
	}
	in, err := learning.RematerializeEvidence(learning.Trajectory{SessionID: s.ID, Messages: s.Conversation.Messages}, nil, s.Incarnation(), materialized.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := learning.CanonicalEvidenceCitation(in, ref); err != nil {
		t.Fatal(err)
	}
}
