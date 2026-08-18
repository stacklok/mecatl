package server

import (
	"context"
	"errors"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

const maxDreamUnavailableReasonRunes = 256

func toProtoDreamReview(review DreamReview) *mecatlv1.DreamReviewPlan {
	operations := make([]*mecatlv1.DreamOperation, len(review.Operations))
	for i, operation := range review.Operations {
		sources := make([]*mecatlv1.DreamParticipant, len(operation.Sources))
		for j, source := range operation.Sources {
			sources[j] = toProtoDreamParticipant(source)
		}
		operations[i] = &mecatlv1.DreamOperation{
			Kind: valid(operation.Kind), Survivor: toProtoDreamParticipant(operation.Survivor), Sources: sources,
			Replacement: &mecatlv1.DreamReplacement{Value: valid(operation.Replacement.Value), Description: valid(operation.Replacement.Description)},
			Reason:      valid(operation.Reason), ExactDuplicateEligible: operation.ExactDuplicateEligible,
		}
	}
	return &mecatlv1.DreamReviewPlan{
		Id: valid(review.ID), Target: valid(string(review.Target)), ExpiresAt: timestamppb.New(review.ExpiresAt),
		PlannedOperationCount: ClampInt32(review.PlannedOperations), PlannedSourceCount: ClampInt32(review.PlannedSourceCount),
		Operations: operations,
	}
}

func toProtoDreamParticipant(participant DreamParticipant) *mecatlv1.DreamParticipant {
	return &mecatlv1.DreamParticipant{
		Key: valid(participant.Key), Value: valid(participant.Value), Description: valid(participant.Description),
	}
}

func toProtoDreamReceipt(receipt DreamReceipt) *mecatlv1.DreamReceipt {
	return &mecatlv1.DreamReceipt{
		Id: valid(receipt.ID), Target: valid(string(receipt.Target)), Disposition: valid(string(receipt.Disposition)),
		PlannedSourceCount: ClampInt32(receipt.Planned), AppliedSourceCount: ClampInt32(receipt.Applied),
		ConflictedSourceCount: ClampInt32(receipt.Conflicted), SkippedSourceCount: ClampInt32(receipt.Skipped),
		FailedSourceCount: ClampInt32(receipt.Failed),
	}
}

func toProtoDreamCapabilities(caps DreamCapabilities) *mecatlv1.ManualDreamCapabilities {
	return &mecatlv1.ManualDreamCapabilities{
		ProjectMemory: toProtoDreamTargetCapability(caps.Targets[DreamTargetProjectMemory], caps.UnavailableReason),
		UserModel:     toProtoDreamTargetCapability(caps.Targets[DreamTargetUserModel], caps.UnavailableReason),
	}
}

func toProtoDreamTargetCapability(capability DreamTargetCapability, fallback string) *mecatlv1.DreamTargetCapability {
	reason := capability.UnavailableReason
	if reason == "" && (!capability.Generate || !capability.Decide) {
		reason = fallback
	}
	return &mecatlv1.DreamTargetCapability{
		Generate: capability.Generate, Decide: capability.Decide, UnavailableReason: boundedDreamReason(reason),
	}
}

func boundedDreamReason(reason string) string {
	reason = valid(reason)
	if utf8.RuneCountInString(reason) <= maxDreamUnavailableReasonRunes {
		return reason
	}
	runes := []rune(reason)
	return string(runes[:maxDreamUnavailableReasonRunes])
}

func normalizeDreamError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ErrDreamDeadline
	case errors.Is(err, ErrInvalidArgument):
		return err
	case errors.Is(err, ErrDreamUnavailable):
		return ErrDreamUnavailable
	case errors.Is(err, ErrDreamNotFound):
		return ErrDreamNotFound
	case errors.Is(err, ErrDreamInProgress):
		return ErrDreamInProgress
	case errors.Is(err, ErrDreamConflict):
		return ErrDreamConflict
	case errors.Is(err, ErrDreamTerminalConflict):
		return ErrDreamTerminalConflict
	case errors.Is(err, ErrDreamCapacity):
		return ErrDreamCapacity
	case errors.Is(err, ErrDreamGenerateFailed):
		return ErrDreamGenerateFailed
	case errors.Is(err, ErrDreamApplyFailed):
		return ErrDreamApplyFailed
	default:
		return ErrDreamRequestFailed
	}
}
