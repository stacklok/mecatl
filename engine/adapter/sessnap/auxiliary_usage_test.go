package sessnap

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestAuxiliaryTokenUsage_Scenario4_OpaqueKindAndForwardOnlyCompatibility(t *testing.T) {
	const opaque session.UsageKind = "future_accounting_purpose"
	env := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "rev-1"}
	s := session.New("forward-compatible-usage", session.ModeDefault, env, session.Limits{}, time.Unix(1, 0))
	s.RestoreTokenUsage(map[session.UsageKind]session.TokenUsage{
		session.UsageKindMain: {
			Total:  session.Usage{InputTokens: 999},
			Models: map[string]session.Usage{"unknown": {InputTokens: 3}},
		},
		session.UsageKindSessionTitle: {
			Models: map[string]session.Usage{"unknown": {OutputTokens: 2}},
		},
		opaque: {
			Models: map[string]session.Usage{"provider/future-model": {InputTokens: 7, OutputTokens: 4}},
		},
	})
	s.RecordTitleAttempt(session.TitleAttempt{ID: "attempt-1", Outcome: session.TitleAttemptSucceeded})

	snap, err := Of(s)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var persisted Snapshot
	if err := json.Unmarshal(body, &persisted); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	restored, err := persisted.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	want := s.TokenUsageSnapshot()
	if got := restored.TokenUsageSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored ledger = %#v, want %#v", got, want)
	}
	if got := restored.UsageFor(session.UsageKindMain); got != (session.Usage{InputTokens: 3}) {
		t.Fatalf("legacy main attribution = %#v, want unknown/main input 3", got)
	}
	if got := restored.TokenUsageSnapshot()[opaque].Models["provider/future-model"]; got != (session.Usage{InputTokens: 7, OutputTokens: 4}) {
		t.Fatalf("opaque model attribution = %#v", got)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var attempts []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["title_attempts"], &attempts); err != nil {
		t.Fatalf("decode attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("title attempts = %d, want 1", len(attempts))
	}
	hasAttemptUsage := func(records []map[string]json.RawMessage) bool {
		for _, record := range records {
			if _, exists := record["usage"]; exists {
				return true
			}
		}
		return false
	}
	planted := []map[string]json.RawMessage{{"usage": json.RawMessage(`{"InputTokens":1}`)}}
	if !hasAttemptUsage(planted) {
		t.Fatal("test oracle did not detect planted per-attempt usage")
	}
	if hasAttemptUsage(attempts) {
		t.Fatal("per-attempt usage record was persisted")
	}
}
