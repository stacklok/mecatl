package server

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestAuxiliaryTokenUsage_Scenario4_RoundTripAndProjection(t *testing.T) {
	env := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "rev-1"}
	s := session.New("auxiliary-round-trip", session.ModeDefault, env, session.Limits{}, time.Unix(1, 0))
	kinds := []session.UsageKind{
		session.UsageKindCompaction,
		session.UsageKindReflection,
		session.UsageKindRouter,
		session.UsageKindAskReviewer,
		session.UsageKindGuardrail,
		session.UsageKindParallelJudge,
		session.UsageKind("future_helper"),
	}
	for i, kind := range kinds {
		s.RecordTokenUsage(kind, "provider", "model", session.Usage{InputTokens: i + 1, OutputTokens: i + 2})
	}
	want := s.TokenUsageSnapshot()

	store := memstore.New()
	if err := store.Save(context.Background(), s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.TokenUsageSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("store ledger = %#v, want %#v", got, want)
	}

	folded, err := eventsource.Fold(eventsource.SessionMeta{
		ID: s.ID, Mode: s.Mode, Limits: s.Limits, EnvironmentRef: env,
		TokenUsage: want, CreatedAt: s.CreatedAt,
	}, func(func(session.Event, error) bool) {})
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if got := folded.TokenUsageSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("event-source ledger = %#v, want %#v", got, want)
	}

	projected := toProtoSession(loaded, ResolvedModel{}, nil, port.ProviderCapabilities{})
	summary := toProtoSessionSummary(SessionSummary{SessionID: string(s.ID), TokenUsage: loaded.TokenUsageSnapshot()})
	for _, kind := range kinds {
		key := string(kind)
		if projected.TokenUsage[key] == nil {
			t.Errorf("session projection missing %q", key)
		}
		if summary.TokenUsage[key] == nil {
			t.Errorf("session-summary projection missing %q", key)
		}
	}
}
