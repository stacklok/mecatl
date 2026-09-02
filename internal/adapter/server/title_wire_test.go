package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionTitleGeneration_Scenario1_TitleCommandIsClientOnly(t *testing.T) {
	t.Parallel()

	methods := mecatlv1.File_mecatl_v1_harness_proto.Services().ByName("HarnessService").Methods()
	for i := range methods.Len() {
		name := string(methods.Get(i).Name())
		if strings.Contains(strings.ToLower(name), "title") || strings.Contains(strings.ToLower(name), "auxiliary") {
			t.Fatalf("unexpected title/auxiliary RPC %q", name)
		}
	}
}

func TestSessionTitleGeneration_Scenario2_TitleMetadataRoundTrip(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	s := session.New("session-1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "test", Revision: "test"}, session.Limits{}, at)
	s.SetTitle("First prompt")
	s.SetTitleGeneration(session.TitleGenerationPending)
	s.RecordTitleAttempt(session.TitleAttempt{ID: "attempt-1", Outcome: session.TitleAttemptDeferred, CreatedAt: at})
	s.RecordTokenUsage(session.UsageKindSessionTitle, "provider", "model", session.Usage{InputTokens: 3, OutputTokens: 2})

	got := toProtoSession(s, ResolvedModel{}, nil)
	meta := got.GetTitleMetadata()
	if meta.GetTitle() != "First prompt" || meta.GetProvenance() != "first-prompt" || meta.GetGenerationState() != "pending" {
		t.Fatalf("title metadata = %#v", meta)
	}
	if attempt := meta.GetLatestAttempt(); attempt.GetId() != "attempt-1" || attempt.GetOutcome() != "deferred" || attempt.GetCreatedAtUnix() != at.Unix() {
		t.Fatalf("latest attempt = %#v", attempt)
	}
	if got := got.GetTokenUsage()["session_title"]; got.GetTotal().GetInputTokens() != 3 || got.GetModels()["provider/model"].GetOutputTokens() != 2 {
		t.Fatalf("canonical token usage = %#v", got)
	}
}

func TestSessionTitleGeneration_Scenario4_TitleEventIsAuthoritativeAndSanitized(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	ev := toProto(session.Event{Type: session.EvSessionTitle, Title: &session.TitlePayload{
		Title: "valid\xff title", Provenance: session.TitleProvenanceGenerated, GenerationState: session.TitleGenerationGenerated,
		LatestAttempt: &session.TitleAttempt{ID: "attempt-1", Outcome: session.TitleAttemptSucceeded, CreatedAt: at},
	}})

	if ev.GetType() != "session.title" || ev.GetTitle() == nil {
		t.Fatalf("title event = %#v", ev)
	}
	if got := ev.GetTitle(); got.GetTitle() != "valid� title" || got.GetProvenance() != "generated" || got.GetGenerationState() != "generated" {
		t.Fatalf("title payload = %#v", got)
	}
	if ev.GetTitle().ProtoReflect().Descriptor().Fields().ByName("latest_usage") != nil {
		t.Fatal("title lifecycle projection must not contain nested usage")
	}
	body, err := json.Marshal(sessionTitleToJSON(session.TitlePayload{Title: "valid\xff title"}))
	if err != nil || strings.Contains(string(body), "source") || strings.Contains(string(body), "error") {
		t.Fatalf("source-free HTTP title metadata = %s, err = %v", body, err)
	}
}

func TestSessionTitleGeneration_Scenario5_TokenUsageRoundTripAndProjection(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	s := session.New("session-1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "test", Revision: "test"}, session.Limits{}, at)
	for i := range 17 {
		s.RecordTokenUsage(session.UsageKindSessionTitle, "provider", "model", session.Usage{InputTokens: i})
	}

	usage := toProtoSession(s, ResolvedModel{}, nil).GetTokenUsage()["session_title"]
	if got := usage.GetTotal(); got.GetInputTokens() != 136 || usage.GetModels()["provider/model"].GetInputTokens() != 136 {
		t.Fatalf("canonical aggregated usage = %#v", usage)
	}
	summary := toProtoSessionSummary(SessionSummary{TitleMetadata: titlePayload(s), TokenUsage: s.TokenUsage})
	if got := summary.GetTokenUsage()["session_title"]; got.GetTotal().GetInputTokens() != 136 {
		t.Fatalf("summary canonical usage = %#v", got)
	}
}
