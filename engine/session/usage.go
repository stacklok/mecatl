package session

import "strings"

// UsageKind identifies a canonical token-usage bucket. "main" and its
// descendants are reserved for normal agent-run accounting; "session_title" is
// the server-owned title-generator bucket.
type UsageKind string

const (
	// UsageKindMain is the reserved normal agent-run accounting bucket.
	UsageKindMain UsageKind = "main"
	// UsageKindSessionTitle is the server-owned title-generator accounting bucket.
	UsageKindSessionTitle UsageKind = "session_title"
)

// TokenUsage is one canonical usage bucket. Total is always the element-wise
// sum of Models. Model keys are opaque server-produced provider/model attributions;
// legacy data without exact attribution uses "unknown".
type TokenUsage struct {
	Total  Usage
	Models map[string]Usage
}

const unknownModelAttribution = "unknown"

func modelAttribution(providerID, modelID string) string {
	providerID = strings.Join(strings.Fields(providerID), " ")
	modelID = strings.Join(strings.Fields(modelID), " ")
	if providerID == "" || modelID == "" {
		return unknownModelAttribution
	}
	return providerID + "/" + modelID
}

// RecordTokenUsage adds usage to a canonical bucket under the opaque provider/model
// attribution. It does not affect the main-run budget projection in Session.Usage.
func (s *Session) RecordTokenUsage(kind UsageKind, providerID, modelID string, usage Usage) {
	if kind != UsageKindMain && kind != UsageKindSessionTitle {
		return
	}
	s.recordTokenUsage(kind, modelAttribution(providerID, modelID), usage)
}

func (s *Session) recordTokenUsage(kind UsageKind, attribution string, usage Usage) {
	if s.tokenUsage == nil {
		s.tokenUsage = make(map[UsageKind]TokenUsage)
	}
	bucket := s.tokenUsage[kind]
	if bucket.Models == nil {
		bucket.Models = make(map[string]Usage)
	}
	if attribution == "" {
		attribution = unknownModelAttribution
	}
	bucket.Models[attribution] = bucket.Models[attribution].Add(usage)
	bucket.Total = bucket.Total.Add(usage)
	s.tokenUsage[kind] = bucket
	s.TokenUsage = cloneTokenUsage(s.tokenUsage)
}

// TokenUsageSnapshot returns an owned copy of the canonical accounting ledger.
func (s *Session) TokenUsageSnapshot() map[UsageKind]TokenUsage {
	return cloneTokenUsage(s.tokenUsage)
}

// RestoreTokenUsage restores the canonical accounting ledger from trusted
// persistence. A nil ledger is legacy data; compatibility projections are retained.
func (s *Session) RestoreTokenUsage(usage map[UsageKind]TokenUsage) {
	s.tokenUsage = make(map[UsageKind]TokenUsage, len(usage))
	for kind, bucket := range usage {
		if kind != UsageKindMain && kind != UsageKindSessionTitle {
			continue
		}
		models := make(map[string]Usage, len(bucket.Models))
		var total Usage
		for model, value := range bucket.Models {
			if model == "" {
				model = unknownModelAttribution
			}
			models[model] = models[model].Add(value)
			total = total.Add(value)
		}
		s.tokenUsage[kind] = TokenUsage{Total: total, Models: models}
	}
	s.TokenUsage = cloneTokenUsage(s.tokenUsage)
	s.Usage = s.tokenUsage[UsageKindMain].Total
}

func cloneTokenUsage(in map[UsageKind]TokenUsage) map[UsageKind]TokenUsage {
	out := make(map[UsageKind]TokenUsage, len(in))
	for kind, bucket := range in {
		models := make(map[string]Usage, len(bucket.Models))
		for model, usage := range bucket.Models {
			models[model] = usage
		}
		out[kind] = TokenUsage{Total: bucket.Total, Models: models}
	}
	return out
}

// Usage is an immutable value object accounting for the token cost of a single
// model call (or an aggregate thereof). Construct it as a literal; it carries no
// mutating methods.
type Usage struct {
	// InputTokens is the number of prompt tokens billed for this call,
	// including any tokens served from cache.
	InputTokens int
	// OutputTokens is the number of completion tokens generated.
	OutputTokens int
	// CacheReadTokens is the number of input tokens served from the prompt
	// cache (a subset of InputTokens).
	CacheReadTokens int
	// CacheWriteTokens is the number of input tokens written into the prompt
	// cache on this call.
	CacheWriteTokens int
	// ReasoningTokens is the number of output tokens spent on internal
	// reasoning (a subset of OutputTokens — providers bill reasoning as part of
	// the inclusive output total; this is the breakdown, not an addend).
	ReasoningTokens int
}

// CacheHitRate returns the fraction of input tokens that were served from the
// prompt cache: CacheReadTokens / InputTokens. It returns 0 when InputTokens is
// zero (guarding against division by zero).
func (u Usage) CacheHitRate() float64 {
	if u.InputTokens == 0 {
		return 0
	}
	return float64(u.CacheReadTokens) / float64(u.InputTokens)
}

// TotalTokens returns the spend proxy used by the loop-level token budget
// (Deps.MaxRunTokens / StopBudget): InputTokens + OutputTokens. Cache tokens are
// DELIBERATELY excluded — CacheReadTokens is a subset of InputTokens (double
// counting it would inflate the total) and CacheWriteTokens is a write-through
// side cost, not the model-call spend the budget bounds. ReasoningTokens is
// likewise DELIBERATELY excluded — it is a subset of OutputTokens (providers
// bill reasoning as part of the inclusive output total; adding it here would
// double-count). The budget is a coarse runaway brake, so input+output is the
// right, simple proxy.
func (u Usage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens
}

// Add returns a new Usage that is the element-wise sum of u and other. Usage is
// immutable, so accumulation is expressed by replacement, not mutation.
func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:      u.InputTokens + other.InputTokens,
		OutputTokens:     u.OutputTokens + other.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + other.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + other.CacheWriteTokens,
		ReasoningTokens:  u.ReasoningTokens + other.ReasoningTokens,
	}
}
