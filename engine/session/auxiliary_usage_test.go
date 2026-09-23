package session

import (
	"reflect"
	"testing"
)

func TestAuxiliaryTokenUsage_Scenario1_RecognizedAndOpaqueKindTotals(t *testing.T) {
	kinds := []UsageKind{
		UsageKindCompaction,
		UsageKindReflection,
		UsageKindRouter,
		UsageKindAskReviewer,
		UsageKindGuardrail,
		UsageKindParallelJudge,
		UsageKind("future_helper"),
	}
	left := AuxiliaryUsage{Buckets: make(map[UsageKind]TokenUsage, len(kinds))}
	for i, kind := range kinds {
		usage := Usage{InputTokens: i + 1, OutputTokens: i + 2}
		left.Buckets[kind] = TokenUsage{
			Total:  Usage{InputTokens: 999}, // Merge must derive totals from model entries.
			Models: map[string]Usage{"provider/model": usage},
		}
	}

	got := (AuxiliaryUsage{}).Merge(left)
	for i, kind := range kinds {
		want := Usage{InputTokens: i + 1, OutputTokens: i + 2}
		if bucket := got.Buckets[kind]; bucket.Total != want || bucket.Models["provider/model"] != want {
			t.Errorf("bucket %q = %#v, want total and model usage %#v", kind, bucket, want)
		}
	}
	left.Buckets[UsageKindCompaction].Models["provider/model"] = Usage{InputTokens: 1000}
	if got.Buckets[UsageKindCompaction].Models["provider/model"].InputTokens == 1000 {
		t.Fatal("Merge retained caller-owned model map")
	}
}

func TestAuxiliaryTokenUsage_AuxiliaryKindsDoNotChangeMain(t *testing.T) {
	s := newTestSession(Limits{})
	main := Usage{InputTokens: 8, OutputTokens: 5}
	mustOK(t, s.BeginTurn())
	mustOK(t, s.RecordUsage(main))

	for _, kind := range []UsageKind{
		UsageKindSessionTitle,
		UsageKindCompaction,
		UsageKindReflection,
		UsageKindRouter,
		UsageKindAskReviewer,
		UsageKindGuardrail,
		UsageKindParallelJudge,
	} {
		s.RecordTokenUsage(kind, "provider", "model", Usage{InputTokens: 100, OutputTokens: 50})
	}

	if got := s.UsageFor(UsageKindMain); got != main {
		t.Fatalf("Session.Usage/main = %#v, want %#v", got, main)
	}
	if got := s.TokenUsageSnapshot()[UsageKindMain].Total; got != main {
		t.Fatalf("token_usage[main] = %#v, want %#v", got, main)
	}
}

func TestAuxiliaryTokenUsage_PreservesOpaqueKindRoundTrip(t *testing.T) {
	const opaque UsageKind = "future_accounting_purpose"
	s := newTestSession(Limits{})
	s.RecordTokenUsage(opaque, "provider", "model", Usage{InputTokens: 7, OutputTokens: 4})
	s.RecordTokenUsage("", "provider", "model", Usage{InputTokens: 100})

	snapshot := s.TokenUsageSnapshot()
	restored := newTestSession(Limits{})
	restored.RestoreTokenUsage(snapshot)

	want := TokenUsage{
		Total:  Usage{InputTokens: 7, OutputTokens: 4},
		Models: map[string]Usage{"provider/model": {InputTokens: 7, OutputTokens: 4}},
	}
	if got := restored.TokenUsageSnapshot()[opaque]; !reflect.DeepEqual(got, want) {
		t.Fatalf("opaque bucket = %#v, want %#v", got, want)
	}
	if _, ok := restored.TokenUsageSnapshot()[""]; ok {
		t.Fatal("empty usage kind was preserved")
	}
	if got := restored.UsageFor(UsageKindMain); got != (Usage{}) {
		t.Fatalf("main usage = %#v, want zero", got)
	}
}
