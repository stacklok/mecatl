package session

import "testing"

func TestUsageCacheHitRate(t *testing.T) {
	tests := []struct {
		name string
		u    Usage
		want float64
	}{
		{"zero input avoids div by zero", Usage{InputTokens: 0, CacheReadTokens: 0}, 0},
		{"zero input with cache reads still zero", Usage{InputTokens: 0, CacheReadTokens: 100}, 0},
		{"half cached", Usage{InputTokens: 100, CacheReadTokens: 50}, 0.5},
		{"fully cached", Usage{InputTokens: 200, CacheReadTokens: 200}, 1.0},
		{"none cached", Usage{InputTokens: 100, CacheReadTokens: 0}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.u.CacheHitRate(); got != tc.want {
				t.Fatalf("CacheHitRate() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUsageTotalTokens(t *testing.T) {
	tests := []struct {
		name string
		u    Usage
		want int
	}{
		{"zero", Usage{}, 0},
		{"input+output only", Usage{InputTokens: 30, OutputTokens: 12}, 42},
		// Cache tokens are EXCLUDED: CacheReadTokens is a subset of InputTokens (so
		// counting it would double-count) and CacheWriteTokens is a side cost.
		{"cache tokens excluded", Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 40, CacheWriteTokens: 20}, 150},
		// ReasoningTokens is likewise a SUBSET of OutputTokens (providers bill
		// reasoning as part of the inclusive output total), so it is NOT added to
		// the total — adding it would double-count.
		{"reasoning subset of output", Usage{InputTokens: 100, OutputTokens: 50, ReasoningTokens: 40}, 150},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.u.TotalTokens(); got != tc.want {
				t.Fatalf("TotalTokens() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestUsageAdd(t *testing.T) {
	a := Usage{InputTokens: 10, OutputTokens: 1, CacheReadTokens: 2, CacheWriteTokens: 3, ReasoningTokens: 5}
	b := Usage{InputTokens: 5, OutputTokens: 4, CacheReadTokens: 1, CacheWriteTokens: 1, ReasoningTokens: 2}
	got := a.Add(b)
	want := Usage{InputTokens: 15, OutputTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 4, ReasoningTokens: 7}
	if got != want {
		t.Fatalf("Add = %+v, want %+v", got, want)
	}
}

func TestRecordUsageCachesCanonicalAttribution(t *testing.T) {
	s := newTestSession(Limits{})
	mustOK(t, s.BeginTurn())
	s.SetUsageAttribution(" openrouter\n", " model \t")
	if got, want := s.usageAttribution, "openrouter/model"; got != want {
		t.Fatalf("cached attribution = %q, want %q", got, want)
	}
	mustOK(t, s.RecordUsage(Usage{InputTokens: 1}))
	if got := s.TokenUsageSnapshot()[UsageKindMain].Models["openrouter/model"]; got != (Usage{InputTokens: 1}) {
		t.Fatalf("usage = %#v, want one attributed token", got)
	}
	if got := testing.AllocsPerRun(1_000, func() {
		mustOK(t, s.RecordUsage(Usage{InputTokens: 1}))
	}); got != 0 {
		t.Fatalf("cached RecordUsage allocations = %v, want 0", got)
	}
}

func TestRecordUsageLazilyCachesDurableAttributionFallback(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		model    string
		want     string
	}{
		{"durable labels", " openrouter\n", " model \t", "openrouter/model"},
		{"missing label", "openrouter", "", unknownModelAttribution},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			s.ProviderID, s.ModelID = tc.provider, tc.model
			mustOK(t, s.BeginTurn())
			mustOK(t, s.RecordUsage(Usage{InputTokens: 1}))
			if got := s.usageAttribution; got != tc.want {
				t.Fatalf("cached attribution = %q, want %q", got, tc.want)
			}
			if got := s.TokenUsageSnapshot()[UsageKindMain].Models[tc.want]; got != (Usage{InputTokens: 1}) {
				t.Fatalf("usage for %q = %#v, want one token", tc.want, got)
			}
		})
	}
}

// TestReasoningSubsetOfOutput mirrors the CacheReadTokens ⊂ InputTokens
// invariant: a Usage where ReasoningTokens EXCEEDS OutputTokens indicates an
// adapter put reasoning OUTSIDE the inclusive output total (the providers bill
// reasoning as part of OutputTokens), which would double-count if TotalTokens()
// were ever widened. This guard catches a future adapter regression early.
func TestReasoningSubsetOfOutput(t *testing.T) {
	tests := []struct {
		name string
		u    Usage
	}{
		{"zero", Usage{}},
		{"output only", Usage{OutputTokens: 50}},
		{"reasoning equals output", Usage{OutputTokens: 50, ReasoningTokens: 50}},
		{"reasoning below output", Usage{InputTokens: 100, OutputTokens: 50, ReasoningTokens: 40}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.u.ReasoningTokens > tc.u.OutputTokens {
				t.Fatalf("ReasoningTokens %d > OutputTokens %d — reasoning must be a subset of output (double-count guard)",
					tc.u.ReasoningTokens, tc.u.OutputTokens)
			}
		})
	}
}
