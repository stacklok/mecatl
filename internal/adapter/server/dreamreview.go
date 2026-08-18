package server

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DreamTarget is a closed, transport-neutral manual-consolidation target.
type DreamTarget string

const (
	// DreamTargetProjectMemory selects the Build-owned project-memory store.
	DreamTargetProjectMemory DreamTarget = "project_memory"
	// DreamTargetUserModel selects the Build-owned cross-project user-model store.
	DreamTargetUserModel DreamTarget = "user_model"
)

// DreamDecision is the complete manual-plan decision vocabulary.
type DreamDecision string

const (
	// DreamDecisionApply applies the exact retained authoritative plan.
	DreamDecisionApply DreamDecision = "apply"
	// DreamDecisionDismiss retires the plan without mutating memory.
	DreamDecisionDismiss DreamDecision = "dismiss"
)

var (
	// ErrDreamUnavailable means manual consolidation was not composed for the target.
	// Wire adapters map it to Unimplemented / HTTP 501.
	ErrDreamUnavailable = errors.New("server: manual dream unavailable")
	// ErrDreamNotFound deliberately covers unknown, expired, and pre-restart plan IDs.
	ErrDreamNotFound = errors.New("server: dream plan not found; generate a new plan")
	// ErrDreamInProgress means the same authoritative decision is still applying.
	ErrDreamInProgress = errors.New("server: dream plan decision is still in progress")
	// ErrDreamConflict means an opposite decision cannot replace the applying decision.
	ErrDreamConflict = errors.New("server: dream plan has a conflicting decision in progress")
	// ErrDreamTerminalConflict means the plan already reached the opposite terminal decision.
	ErrDreamTerminalConflict = errors.New("server: dream plan was already decided differently")
	// ErrDreamCapacity means the bounded process-local review registry is full.
	ErrDreamCapacity = errors.New("server: dream review capacity exhausted")
	// ErrDreamGenerateFailed is the content-free generation failure category.
	ErrDreamGenerateFailed = errors.New("server: dream plan generation failed")
	// ErrDreamApplyFailed is the content-free partial/full application failure category.
	ErrDreamApplyFailed = errors.New("server: dream plan application failed")
	// ErrDreamDeadline is the content-free deadline category used by both transports.
	ErrDreamDeadline = errors.New("dream request deadline exceeded")
	// ErrDreamRequestFailed is the content-free fallback for unclassified backend errors.
	ErrDreamRequestFailed = errors.New("server: dream request failed")
)

// DreamCapabilities is the honest process-wide manual-review capability snapshot.
// Reasons are fixed operator-facing categories and never contain memory or provider content.
type DreamCapabilities struct {
	Generate          bool
	Decide            bool
	UnavailableReason string
	Targets           map[DreamTarget]DreamTargetCapability
}

// DreamTargetCapability describes one exact Build-owned target.
type DreamTargetCapability struct {
	Generate          bool
	Decide            bool
	UnavailableReason string
}

// DreamReview is the detached review projection returned by generation.
type DreamReview struct {
	ID                 string
	Target             DreamTarget
	ExpiresAt          time.Time
	Operations         []DreamOperation
	PlannedOperations  int
	PlannedSourceCount int
}

// DreamOperation is one ordered operation. It deliberately contains no versions,
// paths, provider/model identity, or caller-supplied target information.
type DreamOperation struct {
	Kind                   string
	Survivor               DreamParticipant
	Sources                []DreamParticipant
	Replacement            DreamReplacement
	Reason                 string
	ExactDuplicateEligible bool
}

// DreamParticipant is reviewable memory content for one survivor or source.
type DreamParticipant struct {
	Key         string
	Value       string
	Description string
}

// DreamReplacement is the proposed survivor content after synthesis.
type DreamReplacement struct {
	Value       string
	Description string
}

// DreamReceipt reports source counts supplied by the core dream.Report.
type DreamReceipt struct {
	ID          string
	Target      DreamTarget
	Disposition DreamDecision
	Planned     int
	Applied     int
	Conflicted  int
	Skipped     int
	Failed      int
}

// DreamReviewer owns process-local plan retention and whole-plan decisions.
type DreamReviewer interface {
	Generate(context.Context, DreamTarget) (DreamReview, error)
	Decide(context.Context, string, DreamDecision) (DreamReceipt, error)
}

// ManualDreamCapabilities returns a defensive copy of the composition snapshot.
func (s *Service) ManualDreamCapabilities() DreamCapabilities {
	caps := s.cfg.DreamCapabilities
	caps.Targets = cloneDreamTargetCapabilities(caps.Targets)
	if s.cfg.OwnershipEnforced {
		caps.Generate, caps.Decide = false, false
		caps.UnavailableReason = "manual dreaming is unavailable while ownership enforcement is enabled"
		for target, capability := range caps.Targets {
			capability.Generate, capability.Decide = false, false
			capability.UnavailableReason = caps.UnavailableReason
			caps.Targets[target] = capability
		}
		return caps
	}
	if s.cfg.DreamReviewer == nil {
		caps.Generate, caps.Decide = false, false
		if caps.UnavailableReason == "" {
			caps.UnavailableReason = "manual dreaming is unavailable"
		}
		for target, capability := range caps.Targets {
			capability.Generate, capability.Decide = false, false
			if capability.UnavailableReason == "" {
				capability.UnavailableReason = caps.UnavailableReason
			}
			caps.Targets[target] = capability
		}
	}
	return caps
}

func cloneDreamTargetCapabilities(in map[DreamTarget]DreamTargetCapability) map[DreamTarget]DreamTargetCapability {
	if in == nil {
		return nil
	}
	out := make(map[DreamTarget]DreamTargetCapability, len(in))
	for target, capability := range in {
		out[target] = capability
	}
	return out
}

// GenerateDream creates an opaque, bounded-lifetime review plan for a closed target.
func (s *Service) GenerateDream(ctx context.Context, target DreamTarget) (DreamReview, error) {
	if !target.valid() {
		return DreamReview{}, fmt.Errorf("%w: target must be project_memory or user_model", ErrInvalidArgument)
	}
	caps := s.ManualDreamCapabilities()
	capability, ok := caps.Targets[target]
	if s.cfg.DreamReviewer == nil || !ok || !capability.Generate {
		return DreamReview{}, ErrDreamUnavailable
	}
	return s.cfg.DreamReviewer.Generate(ctx, target)
}

// DecideDream applies or dismisses one retained opaque plan. No operation material
// is accepted from the caller, so the authoritative retained plan is the only mutation input.
func (s *Service) DecideDream(ctx context.Context, id string, decision DreamDecision) (DreamReceipt, error) {
	if id == "" {
		return DreamReceipt{}, fmt.Errorf("%w: plan id is required", ErrInvalidArgument)
	}
	if !decision.valid() {
		return DreamReceipt{}, fmt.Errorf("%w: decision must be apply or dismiss", ErrInvalidArgument)
	}
	caps := s.ManualDreamCapabilities()
	if s.cfg.DreamReviewer == nil || !caps.Decide {
		return DreamReceipt{}, ErrDreamUnavailable
	}
	return s.cfg.DreamReviewer.Decide(ctx, id, decision)
}

func (t DreamTarget) valid() bool {
	return t == DreamTargetProjectMemory || t == DreamTargetUserModel
}

func (d DreamDecision) valid() bool {
	return d == DreamDecisionApply || d == DreamDecisionDismiss
}
