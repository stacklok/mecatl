package learning_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacySessionsPermanentlyExcludedFromLearning(t *testing.T) {
	for _, messages := range []string{
		`[{"role":"user","text":"remember that I prefer concise output"}]`,
		`[{"role":"assistant","tool_calls":[{"ID":"provider-call","Name":"Read"}]},{"role":"tool","tool_result":{"CallID":"provider-call","Content":"done"}}]`,
	} {
		s, err := sessnap.Unmarshal([]byte(`{"id":"legacy","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":` + messages + `}`))
		if err != nil {
			t.Fatal(err)
		}
		store := memstore.New()
		for range 3 {
			tr := learning.NewSessionTrajectory(s, "/fixture", session.StopEndTurn, session.Usage{})
			assertIneligible(t, tr)
			// Canonical history replacement and snapshot/store reload cannot restore eligibility.
			if err := s.ReplaceHistoryAtBoundary([]session.Message{session.NewUserMessage("remember that I prefer concise output")}); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(t.Context(), s); err != nil {
				t.Fatal(err)
			}
			s, err = store.Load(t.Context(), s.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func assertIneligible(t *testing.T, tr learning.Trajectory) {
	t.Helper()
	if tr.LearningEligible() {
		t.Fatal("ineligible trajectory gained authority")
	}
	in := learning.NewInput(tr, nil, []learning.Signal{{Kind: learning.SignalHostRequested}}, nil)
	for _, policy := range []learning.AdmissionPolicy{learning.ThresholdPolicy{}, learning.AlwaysPolicy{}} {
		decision, err := policy.Decide(t.Context(), learning.AdmissionRequest{Trajectory: tr, Signals: in.Signals})
		if err != nil || decision.Admitted {
			t.Fatalf("ineligible admission: %+v %v", decision, err)
		}
	}
	if err := learning.ValidateInput(in); err == nil {
		t.Fatal("ineligible input accepted")
	}
	if _, err := learning.ProjectInput(in); err == nil {
		t.Fatal("ineligible projection accepted")
	}
	if _, err := learning.MessageEvidenceRef(in, 0, ""); err == nil {
		t.Fatal("ineligible citation minted")
	}
	for _, explicit := range []bool{false, true} {
		read := false
		got, err := learning.MaterializeEvidence(t.Context(), learning.MaterializationRequest{
			Trajectory: tr, Explicit: explicit,
			EventSource: func(_ func(session.Event, error) bool) { read = true },
		})
		if err != nil || read || got.Disposition == learning.MaterializationSelected || len(got.Canonical) != 0 {
			t.Fatalf("ineligible materialization: %+v %v read=%v", got, err, read)
		}
	}
}

func TestCanonicalSparseEventRecoveryKeepsOriginalCoordinates(t *testing.T) {
	tr := sourceTrajectory()
	events := []session.Event{
		{Type: session.EvReasoningDelta, Text: "omitted"},
		{Type: session.EvResult, Seq: 77, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	got := materialize(t, learning.MaterializationRequest{Trajectory: tr, Events: events, Explicit: true})
	if len(got.Manifest.Entries) != 1 || *got.Manifest.Entries[0].EventSequence != 1 {
		t.Fatal("event coordinates renumbered")
	}
	in, err := learning.RematerializeEvidence(tr, events[1:], got.Manifest, []int{1})
	if err != nil || in.Manifest.Digest != got.Manifest.Digest {
		t.Fatalf("sparse recovery: %v", err)
	}
	if _, err := learning.RematerializeEvidence(tr, events[1:], got.Manifest); err == nil {
		t.Fatal("retained index substituted for original coordinate")
	}
}

func TestCanonicalEvidenceSourceRejectsInvalidCoordinates(t *testing.T) {
	tr := sourceTrajectory(session.NewUserMessage("remember concise output"))
	events := []session.Event{{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}}}
	got := materialize(t, learning.MaterializationRequest{Trajectory: tr, Events: events, Explicit: true})
	for _, handle := range []string{"m:0", "e:0"} {
		ref, err := learning.ResolveEvidenceHandle(got.Input, handle)
		if err != nil {
			t.Fatal(err)
		}
		for _, coordinate := range []int{-1, 1} {
			invalid := ref
			manifest := got.Manifest
			manifest.Entries = append([]learning.ManifestEntry(nil), got.Manifest.Entries...)
			entry := &manifest.Entries[ref.ManifestIndex]
			if ref.Locator == learning.EvidenceMessage {
				invalid.OriginalMessage, entry.OriginalMessage = &coordinate, &coordinate
			} else {
				sequence := int64(coordinate)
				invalid.EventSeq, entry.EventSequence = &sequence, &sequence
			}
			if _, err := learning.VerifyEvidenceSource(tr, events, invalid); err == nil {
				t.Fatalf("%s verified out-of-bounds coordinate %d", handle, coordinate)
			}
			if _, err := learning.RematerializeEvidence(tr, events, manifest); err == nil {
				t.Fatalf("%s recovered out-of-bounds coordinate %d", handle, coordinate)
			}
		}
	}
}

func TestCanonicalEvidenceRecoveryAndEligibility(t *testing.T) {
	s := session.New("current", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/fixture", Revision: "test"}, session.Limits{}, time.Unix(0, 0))
	if err := s.SeedHistory([]session.Message{
		session.NewUserMessage("remember that I prefer concise output"),
		session.NewAssistantMessage("reading", []session.ToolCall{session.NewToolCall("call", "Read", nil)}),
		session.NewToolMessage(session.NewToolResult("call", "done")),
	}); err != nil {
		t.Fatal(err)
	}
	events := []session.Event{{Type: session.EvToolCall, Seq: 42, ToolCall: &session.ToolCall{ID: "call", Name: "Read"}}, {Type: session.EvResult, Seq: 43, Result: &session.ResultPayload{Stop: session.StopEndTurn}}}
	tr := learning.NewSessionTrajectory(s, "/fixture", session.StopEndTurn, session.Usage{})
	got, err := learning.MaterializeEvidence(context.Background(), learning.MaterializationRequest{Trajectory: tr, Events: events, Explicit: true})
	if err != nil || got.Disposition != learning.MaterializationSelected {
		t.Fatalf("current materialization: %+v %v", got, err)
	}
	for _, handle := range []string{"m:0", "m:1", "m:2", "e:0", "e:1"} {
		ref, err := learning.ResolveEvidenceHandle(got.Input, handle)
		if err != nil {
			t.Fatal(err)
		}
		citation, err := learning.VerifyEvidenceSource(tr, events, ref)
		if err != nil || citation.ToolCallID != ref.ToolCallID {
			t.Fatalf("canonical citation: %+v %v", citation, err)
		}
		for _, protocol := range []learning.EvidenceProtocol{"", "reflection-evidence/legacy-v0", "reflection-evidence/v1"} {
			old := ref
			old.Protocol = protocol
			if err := learning.ResolveEvidence(got.Input, old); err == nil {
				t.Fatal("old protocol resolved")
			}
			if _, err := learning.VerifyEvidenceSource(tr, events, old); err == nil {
				t.Fatal("old protocol source verified")
			}
		}
	}
	recovered, err := learning.RematerializeEvidence(tr, events, got.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Manifest.Digest != got.Manifest.Digest {
		t.Fatal("current digest changed")
	}
	changed := tr
	changed.Messages = session.CloneMessages(tr.Messages)
	changed.Messages[1].ToolCalls[0].Name = "tampered"
	if _, err := learning.RematerializeEvidence(changed, events, got.Manifest); err == nil {
		t.Fatal("changed current source recovered")
	}
	ref, err := learning.ResolveEvidenceHandle(got.Input, "m:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := learning.VerifyEvidenceSource(changed, events, ref); err == nil {
		t.Fatal("changed current citation verified")
	}
	ref.ToolCallID = "forged"
	if _, err := learning.CanonicalEvidenceCitation(got.Input, ref); err == nil {
		t.Fatal("forged canonical attachment verified")
	}
	// Sparse event source positions, rather than retained slice indexes, bind evidence.
	if _, err := learning.RematerializeEvidence(tr, events[1:], got.Manifest, []int{1}); err == nil {
		t.Fatal("missing event recovered")
	}
	for _, protocol := range []learning.EvidenceProtocol{"", "reflection-evidence/legacy-v0", "reflection-evidence/v1"} {
		old := got.Manifest
		old.Protocol = protocol
		input := got.Input
		input.Manifest = &old
		for _, handle := range []string{"m:0", "e:0"} {
			if _, err := learning.ResolveEvidenceHandle(input, handle); err == nil {
				t.Fatal("old manifest reinterpreted as current evidence")
			}
		}
		if _, err := learning.RematerializeEvidence(tr, events, old); err == nil {
			t.Fatal("old manifest recovered")
		}
	}
	for _, value := range []any{tr, got.Input} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var input learning.Input
		if _, ok := value.(learning.Trajectory); ok {
			var restored learning.Trajectory
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			input = learning.NewInput(restored, nil, nil, nil)
		} else if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		assertIneligible(t, input.Trajectory)
	}
	s.ExcludeFromLearning()
	excluded := learning.NewSessionTrajectory(s, "/fixture", session.StopEndTurn, session.Usage{})
	if _, err := learning.RematerializeEvidence(excluded, events, got.Manifest); err == nil {
		t.Fatal("excluded checkpoint recovered")
	}
	ref, err = learning.ResolveEvidenceHandle(got.Input, "m:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := learning.VerifyEvidenceSource(excluded, events, ref); err == nil {
		t.Fatal("excluded citation verified")
	}
}
