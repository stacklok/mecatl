package learning

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/session"
)

// Sensitivity controls the score required for automatic weighted admission.
// Its order is tighten-only: Conservative < Balanced < Eager.
type Sensitivity uint8

// Sensitivity values are ordered from unset through increasingly eager policy.
const (
	SensitivityUnset Sensitivity = iota
	Conservative
	Balanced
	Eager
)

// ParseSensitivity parses the strict lower-case settings vocabulary.
func ParseSensitivity(value string) (Sensitivity, error) {
	switch value {
	case "conservative":
		return Conservative, nil
	case "balanced":
		return Balanced, nil
	case "eager":
		return Eager, nil
	default:
		return Balanced, fmt.Errorf("learning: invalid sensitivity %q (want conservative, balanced, or eager)", value)
	}
}

func (s Sensitivity) String() string {
	switch s {
	case Conservative:
		return "conservative"
	case Balanced:
		return "balanced"
	case Eager:
		return "eager"
	default:
		return fmt.Sprintf("Sensitivity(%d)", s)
	}
}

// Next returns the operator-selection cycle Conservative → Balanced → Eager → Conservative.
func (s Sensitivity) Next() Sensitivity {
	switch s {
	case Conservative:
		return Balanced
	case Balanced:
		return Eager
	default:
		return Conservative
	}
}

// Threshold returns the standard weighted-admission threshold.
func (s Sensitivity) Threshold() int {
	switch s {
	case Conservative:
		return 6
	case Eager:
		return 3
	default:
		return 4
	}
}

// MessageSpan is a half-open range in Trajectory.Messages. A zero span is invalid.
type MessageSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Valid reports whether the span is non-empty and within messageCount.
func (s MessageSpan) Valid(messageCount int) bool {
	return s.Start >= 0 && s.Start < s.End && s.End <= messageCount
}

// Contains reports whether ordinal belongs to the half-open span.
func (s MessageSpan) Contains(ordinal int) bool { return ordinal >= s.Start && ordinal < s.End }

// DetectionScope limits signal evidence to the genuine current run.
type DetectionScope struct {
	Current MessageSpan
}

// AdmissionClass is the closed kind of an admission decision.
type AdmissionClass string

// Admission class values distinguish skip, weighted, hard, and host-requested work.
const (
	AdmissionSkipped       AdmissionClass = "skipped"
	AdmissionWeighted      AdmissionClass = "weighted"
	AdmissionHard          AdmissionClass = "hard"
	AdmissionHostRequested AdmissionClass = "host_requested"
)

// Valid reports whether c belongs to the closed admission-class vocabulary.
func (c AdmissionClass) Valid() bool {
	return c == AdmissionSkipped || c == AdmissionWeighted || c == AdmissionHard || c == AdmissionHostRequested
}

// AdmissionReason is a closed, content-free explanation suitable for metrics.
type AdmissionReason string

// Admission reasons are closed, content-free policy and modifier labels.
const (
	ReasonBelowThreshold           AdmissionReason = "below_threshold"
	ReasonInvalidCurrentSpan       AdmissionReason = "invalid_current_span"
	ReasonNonMainSession           AdmissionReason = "non_main_session"
	ReasonIneligibleStop           AdmissionReason = "ineligible_stop"
	ReasonTrivialRun               AdmissionReason = "trivial_run"
	ReasonExplicitRemember         AdmissionReason = "explicit_remember"
	ReasonExplicitLearnProcedure   AdmissionReason = "explicit_learn_procedure"
	ReasonRepeatedCorrection       AdmissionReason = "repeated_correction"
	ReasonTrustedHostContradiction AdmissionReason = "trusted_host_contradiction"
	ReasonFailureRecovery          AdmissionReason = "failure_recovery"
	ReasonRepeatedToolSequence     AdmissionReason = "repeated_tool_sequence"
	ReasonSubstantialSuccess       AdmissionReason = "substantial_success"
	ReasonModelTurnsModifier       AdmissionReason = "model_turns_modifier"
	ReasonSuccessfulToolsModifier  AdmissionReason = "successful_tools_modifier"
	ReasonRunTokensModifier        AdmissionReason = "run_tokens_modifier" //nolint:gosec // closed token-count label, not a credential
	ReasonPolicyAlways             AdmissionReason = "policy_always"
	ReasonPolicyNever              AdmissionReason = "policy_never"
	ReasonHostRequested            AdmissionReason = "host_requested"
	ReasonHardTrigger              AdmissionReason = "hard"
	ReasonWeightedThreshold        AdmissionReason = "weighted"
	ReasonDuplicate                AdmissionReason = "duplicate"
	ReasonRateLimit                AdmissionReason = "rate_limit"
	ReasonQueueFull                AdmissionReason = "queue_full"
	ReasonCoordinatorClosed        AdmissionReason = "closed"
	ReasonTimeout                  AdmissionReason = "timeout"
	ReasonReflectionFailed         AdmissionReason = "reflection_failed"
	ReasonAbstained                AdmissionReason = "abstained"
	ReasonStaged                   AdmissionReason = "staged"
	ReasonPromoted                 AdmissionReason = "promoted"
	ReasonConflicted               AdmissionReason = "conflicted"
)

// Valid reports whether r belongs to the closed admission-reason vocabulary.
func (r AdmissionReason) Valid() bool {
	switch r {
	case ReasonBelowThreshold, ReasonInvalidCurrentSpan, ReasonNonMainSession,
		ReasonIneligibleStop, ReasonTrivialRun, ReasonExplicitRemember,
		ReasonExplicitLearnProcedure, ReasonRepeatedCorrection,
		ReasonTrustedHostContradiction, ReasonFailureRecovery,
		ReasonRepeatedToolSequence, ReasonSubstantialSuccess,
		ReasonModelTurnsModifier, ReasonSuccessfulToolsModifier,
		ReasonRunTokensModifier, ReasonPolicyAlways, ReasonPolicyNever,
		ReasonHostRequested, ReasonHardTrigger, ReasonWeightedThreshold,
		ReasonDuplicate, ReasonRateLimit, ReasonQueueFull, ReasonCoordinatorClosed,
		ReasonTimeout, ReasonReflectionFailed, ReasonAbstained, ReasonStaged,
		ReasonPromoted, ReasonConflicted:
		return true
	default:
		return false
	}
}

// AdmissionRequest contains the borrowed completed trajectory to classify.
// Policies must not retain or copy the full source.
type AdmissionRequest struct {
	Trajectory Trajectory
	Signals    []Signal
}

// AdmissionDecision is deterministic and content-free apart from its validated signals.
type AdmissionDecision struct {
	Admitted  bool
	Class     AdmissionClass
	Score     int
	Threshold int
	Reasons   []AdmissionReason
	Signals   []Signal
}

// AdmissionPolicy decides whether an automatic completed trajectory warrants reflection.
// Decide must observe ctx while scanning the borrowed source.
type AdmissionPolicy interface {
	Decide(context.Context, AdmissionRequest) (AdmissionDecision, error)
}

// ThresholdPolicy implements the standard weighted and hard-trigger policy.
type ThresholdPolicy struct{ Sensitivity Sensitivity }

// Decide applies hard provenance, stop/kind exclusions, standard weights, and modifiers.
//
//nolint:gocyclo // the closed policy table is clearest as one visibly ordered decision
func (p ThresholdPolicy) Decide(ctx context.Context, req AdmissionRequest) (AdmissionDecision, error) {
	if err := ctx.Err(); err != nil {
		return AdmissionDecision{}, err
	}
	trajectory := req.Trajectory
	threshold := p.Sensitivity.Threshold()
	skipped := func(reason AdmissionReason) AdmissionDecision {
		return AdmissionDecision{Class: AdmissionSkipped, Threshold: threshold, Reasons: []AdmissionReason{reason}}
	}
	if trajectory.Kind != session.SessionKindMain {
		return skipped(ReasonNonMainSession), nil
	}
	span := trajectory.Current
	if !span.Valid(len(trajectory.Messages)) || !session.IsGenuineUserPrompt(trajectory.Messages[span.Start]) {
		return skipped(ReasonInvalidCurrentSpan), nil
	}
	for range trajectory.Messages {
		if err := ctx.Err(); err != nil {
			return AdmissionDecision{}, err
		}
	}
	signals := detectSignalsScoped(trajectory, nil, req.Signals, nil, DetectionScope{Current: span})
	if err := ctx.Err(); err != nil {
		return AdmissionDecision{}, err
	}
	for _, signal := range signals {
		if signal.Kind == SignalExplicitRemember || signal.Kind == SignalExplicitLearnProcedure {
			if hardStop(trajectory.Stop) && signalHasGenuineCurrentPrompt(trajectory, signal, span) {
				reason := ReasonExplicitRemember
				if signal.Kind == SignalExplicitLearnProcedure {
					reason = ReasonExplicitLearnProcedure
				}
				return AdmissionDecision{Admitted: true, Class: AdmissionHard, Threshold: threshold, Reasons: []AdmissionReason{reason}, Signals: signals}, nil
			}
		}
	}
	if trajectory.Stop != session.StopEndTurn {
		return skipped(ReasonIneligibleStop), nil
	}
	if trajectory.Counters.Turns <= 1 && trajectory.Counters.ToolCalls == 0 {
		return skipped(ReasonTrivialRun), nil
	}

	score := 0
	reasons := make([]AdmissionReason, 0, 6)
	for _, signal := range signals {
		switch signal.Kind {
		case SignalRepeatedCorrection:
			score += 5
			reasons = append(reasons, ReasonRepeatedCorrection)
		case SignalContradiction:
			score += 5
			reasons = append(reasons, ReasonTrustedHostContradiction)
		case SignalFailureRecovery:
			score += 4
			reasons = append(reasons, ReasonFailureRecovery)
		case SignalRepeatedToolSequence:
			score += 3
			reasons = append(reasons, ReasonRepeatedToolSequence)
		case SignalSubstantialSuccess:
			score += 2
			reasons = append(reasons, ReasonSubstantialSuccess)
		}
	}
	if score == 0 {
		return skipped(ReasonBelowThreshold), nil
	}
	if trajectory.Counters.Turns >= 4 {
		score++
		reasons = append(reasons, ReasonModelTurnsModifier)
	}
	if successfulToolResults(trajectory, span) >= 5 {
		score++
		reasons = append(reasons, ReasonSuccessfulToolsModifier)
	}
	if trajectory.Usage.TotalTokens() >= 12000 {
		score++
		reasons = append(reasons, ReasonRunTokensModifier)
	}
	admitted := score >= threshold
	class := AdmissionSkipped
	if admitted {
		class = AdmissionWeighted
	} else {
		reasons = append(reasons, ReasonBelowThreshold)
	}
	return AdmissionDecision{Admitted: admitted, Class: class, Score: score, Threshold: threshold, Reasons: reasons, Signals: signals}, nil
}

func hardStop(stop session.StopReason) bool {
	return stop == session.StopEndTurn || stop == session.StopMaxTurns || stop == session.StopMaxToolCalls || stop == session.StopBudget
}

func signalHasGenuineCurrentPrompt(trajectory Trajectory, signal Signal, span MessageSpan) bool {
	for _, ref := range signal.Evidence {
		if ref.Locator == EvidenceMessage && span.Contains(ref.Ordinal) && ref.Ordinal < len(trajectory.Messages) && session.IsGenuineUserPrompt(trajectory.Messages[ref.Ordinal]) {
			return true
		}
	}
	return false
}

func successfulToolResults(trajectory Trajectory, span MessageSpan) int {
	count := 0
	for i := span.Start; i < span.End; i++ {
		message := trajectory.Messages[i]
		if message.Role == session.RoleTool && message.ToolResult != nil && !message.ToolResult.IsError {
			count++
		}
	}
	return count
}

// AlwaysPolicy admits valid main/current trajectories, primarily for hosts and tests.
type AlwaysPolicy struct{}

// Decide admits any valid main/current trajectory.
func (AlwaysPolicy) Decide(ctx context.Context, req AdmissionRequest) (AdmissionDecision, error) {
	if err := ctx.Err(); err != nil {
		return AdmissionDecision{}, err
	}
	tr := req.Trajectory
	if tr.Kind != session.SessionKindMain || !tr.Current.Valid(len(tr.Messages)) {
		return AdmissionDecision{Class: AdmissionSkipped, Reasons: []AdmissionReason{ReasonPolicyNever}}, nil
	}
	for range tr.Messages {
		if err := ctx.Err(); err != nil {
			return AdmissionDecision{}, err
		}
	}
	signals := detectSignalsScoped(tr, nil, req.Signals, nil, DetectionScope{Current: tr.Current})
	if err := ctx.Err(); err != nil {
		return AdmissionDecision{}, err
	}
	return AdmissionDecision{Admitted: true, Class: AdmissionWeighted, Reasons: []AdmissionReason{ReasonPolicyAlways}, Signals: signals}, nil
}

// NeverPolicy disables automatic admission.
type NeverPolicy struct{}

// Decide always returns the closed policy-never skip.
func (NeverPolicy) Decide(ctx context.Context, _ AdmissionRequest) (AdmissionDecision, error) {
	if err := ctx.Err(); err != nil {
		return AdmissionDecision{}, err
	}
	return AdmissionDecision{Class: AdmissionSkipped, Reasons: []AdmissionReason{ReasonPolicyNever}}, nil
}
