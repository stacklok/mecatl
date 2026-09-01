package server

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/types/known/timestamppb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

// attemptPartition resolves the caller's private immutable owner binding before
// any repository operation. The resulting one-way partition contains no
// principal value and gives system callers no ownership bypass.
func (s *Service) attemptPartition(ctx context.Context) (learning.AttemptPartition, error) {
	principal := session.PrincipalFromContext(ctx)
	if s.cfg.OwnershipEnforced && principal == nil {
		return "", fmt.Errorf("%w: verified attempt principal unavailable", ErrFailedPrecondition)
	}
	principalFn := s.cfg.AttemptPrincipal
	if principalFn == nil {
		principalFn = reflectionPrincipal
	}
	ownerBinding := principalFn(principal)
	if ownerBinding == "" {
		return "", fmt.Errorf("%w: attempt owner binding unavailable", ErrFailedPrecondition)
	}
	partition, err := learning.DeriveAttemptPartition(ownerBinding)
	if err != nil {
		return "", fmt.Errorf("%w: attempt owner binding unavailable", ErrFailedPrecondition)
	}
	return partition, nil
}

// GetLearningAttempt returns one content-free projection from the verified
// caller's private attempt partition.
func (s *Service) GetLearningAttempt(ctx context.Context, id string) (*mecatlv1.LearningAttempt, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: attempt id is required", ErrInvalidArgument)
	}
	if s.cfg.Attempts == nil {
		return nil, ErrLearningUnavailable
	}
	partition, err := s.attemptPartition(ctx)
	if err != nil {
		return nil, err
	}
	record, found, err := s.cfg.Attempts.Get(ctx, partition, learning.AttemptID(id))
	if err != nil {
		return nil, attemptServiceError(err)
	}
	if !found {
		return nil, fmt.Errorf("%w: learning attempt", ErrNotFound)
	}
	return toProtoLearningAttempt(record)
}

// ListLearningAttempts returns one bounded page from the verified caller's
// private attempt partition. The repository's opaque next ID is the cursor.
func (s *Service) ListLearningAttempts(ctx context.Context, stateValue, cursor string, limit int) (*mecatlv1.ListLearningAttemptsResponse, error) {
	if s.cfg.Attempts == nil {
		return nil, ErrLearningUnavailable
	}
	partition, err := s.attemptPartition(ctx)
	if err != nil {
		return nil, err
	}
	state := learning.AttemptState(stateValue)
	query := learning.AttemptList{After: learning.AttemptID(cursor), Limit: limit, State: state}
	if err := query.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid attempt page", ErrInvalidArgument)
	}
	if query.Limit == 0 {
		query.Limit = learning.DefaultAttemptPageSize
	}
	page, err := s.cfg.Attempts.List(ctx, partition, query)
	if err != nil {
		return nil, attemptServiceError(err)
	}
	out := make([]*mecatlv1.LearningAttempt, len(page.Records))
	for i := range page.Records {
		out[i], err = toProtoLearningAttempt(page.Records[i])
		if err != nil {
			return nil, err
		}
	}
	return &mecatlv1.ListLearningAttemptsResponse{Attempts: out, NextCursor: validLearningText(string(page.Next))}, nil
}

func toProtoLearningAttempt(record learning.AttemptRecord) (*mecatlv1.LearningAttempt, error) {
	projection, err := learning.ProjectAttempt(record)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid attempt projection", ErrInternal)
	}
	claimExpires := timestampOrNil(projection.ClaimExpiresAt)
	created := timestamppb.New(projection.CreatedAt)
	updated := timestamppb.New(projection.UpdatedAt)
	for _, timestamp := range []*timestamppb.Timestamp{claimExpires, created, updated} {
		if timestamp != nil && timestamp.CheckValid() != nil {
			return nil, fmt.Errorf("%w: invalid attempt projection", ErrInternal)
		}
	}
	return &mecatlv1.LearningAttempt{
		Id: validLearningText(string(projection.ID)), Version: validLearningText(string(projection.Version)),
		State: validLearningText(string(projection.State)), Outcome: validLearningText(string(projection.Outcome)),
		FailureCode: validLearningText(string(projection.FailureCode)), AttemptGeneration: uint64(projection.AttemptGeneration),
		ClaimGeneration: uint64(projection.ClaimGeneration), ClaimExpiresAt: claimExpires,
		CheckpointStage: validLearningText(string(projection.CheckpointStage)), ProposalId: validLearningText(string(projection.ProposalID)),
		SkillId: validLearningText(string(projection.SkillID)), CreatedAt: created, UpdatedAt: updated,
	}, nil
}

func attemptServiceError(err error) error {
	switch {
	case errors.Is(err, learning.ErrAttemptNotFound):
		return fmt.Errorf("%w: learning attempt", ErrNotFound)
	case errors.Is(err, learning.ErrInvalidAttempt):
		return fmt.Errorf("%w: invalid attempt request", ErrInvalidArgument)
	default:
		return fmt.Errorf("%w: attempt operation failed", ErrInternal)
	}
}
