package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

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
	s.RecordTitleAttempt(session.TitleAttempt{ID: "attempt-1", Outcome: session.TitleAttemptDeferred})
	if got, want := s.TitleRevision, uint64(3); got != want {
		t.Fatalf("TitleRevision = %d, want %d", got, want)
	}
	s.RecordTokenUsage(session.UsageKindSessionTitle, "provider", "model", session.Usage{InputTokens: 3, OutputTokens: 2})

	got := toProtoSession(s, ResolvedModel{}, nil)
	meta := got.GetTitleMetadata()
	if meta.GetTitle() != "First prompt" || meta.GetProvenance() != "first-prompt" || meta.GetGenerationState() != "pending" {
		t.Fatalf("title metadata = %#v", meta)
	}
	if meta.GetRevision() != 3 {
		t.Fatalf("title revision = %d, want 3", meta.GetRevision())
	}
	if attempt := meta.GetLatestAttempt(); attempt.GetId() != "attempt-1" || attempt.GetOutcome() != "deferred" {
		t.Fatalf("latest attempt = %#v", attempt)
	}
	if got := got.GetTokenUsage()["session_title"]; got.GetTotal().GetInputTokens() != 3 || got.GetModels()["provider/model"].GetOutputTokens() != 2 {
		t.Fatalf("canonical token usage = %#v", got)
	}
}

func TestSessionTitleGeneration_Scenario4_TitleEventIsAuthoritativeAndSanitized(t *testing.T) {
	t.Parallel()

	ev := toProto(session.Event{Type: session.EvSessionTitle, Title: &session.TitlePayload{
		Title: "valid\xff title", Provenance: session.TitleProvenanceGenerated, GenerationState: session.TitleGenerationGenerated, Revision: 9,
		LatestAttempt: &session.TitleAttempt{ID: "attempt-1", Outcome: session.TitleAttemptSucceeded},
	}})

	if ev.GetType() != "session.title" || ev.GetTitle() == nil {
		t.Fatalf("title event = %#v", ev)
	}
	if got := ev.GetTitle(); got.GetTitle() != "valid� title" || got.GetProvenance() != "generated" || got.GetGenerationState() != "generated" || got.GetRevision() != 9 {
		t.Fatalf("title payload = %#v", got)
	}
	fields := ev.GetTitle().ProtoReflect().Descriptor().Fields()
	if fields.ByName("latest_usage") != nil || fields.ByName("created_at_unix") != nil {
		t.Fatal("title lifecycle projection must contain only source-free title metadata")
	}
	body, err := json.Marshal(sessionTitleToJSON(session.TitlePayload{Title: "valid\xff title", Revision: 8}))
	if err != nil || strings.Contains(string(body), "source") || strings.Contains(string(body), "error") || !strings.Contains(string(body), `"revision":8`) {
		t.Fatalf("source-free HTTP title metadata = %s, err = %v", body, err)
	}
}

func TestSessionTitleRevisionWireCompatibility(t *testing.T) {
	fields := (&mecatlv1.SessionTitle{}).ProtoReflect().Descriptor().Fields()
	if got := fields.ByName("revision").Number(); got != 5 {
		t.Fatalf("revision field number = %d, want 5", got)
	}

	var legacy mecatlv1.SessionTitle
	if err := proto.Unmarshal([]byte{0x0a, 0x06, 'l', 'e', 'g', 'a', 'c', 'y'}, &legacy); err != nil {
		t.Fatalf("unmarshal legacy SessionTitle: %v", err)
	}
	if got := legacy.GetRevision(); got != 0 {
		t.Fatalf("legacy revision = %d, want 0", got)
	}

	var current mecatlv1.SessionTitle
	if err := proto.Unmarshal([]byte{0x28, 0x09}, &current); err != nil {
		t.Fatalf("unmarshal SessionTitle revision: %v", err)
	}
	if got := current.GetRevision(); got != 9 {
		t.Fatalf("revision = %d, want 9", got)
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
