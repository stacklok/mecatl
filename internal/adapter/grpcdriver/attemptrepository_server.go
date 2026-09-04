package grpcdriver

import (
	"context"
	"math"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/learning"
)

type attemptRepositoryServer struct {
	driverv1.UnimplementedAttemptRepositoryServiceServer
	repository learning.AttemptRepository
}

// NewAttemptRepositoryServer wraps a domain repository as a driver service.
func NewAttemptRepositoryServer(repository learning.AttemptRepository) driverv1.AttemptRepositoryServiceServer {
	return &attemptRepositoryServer{repository: repository}
}

func (s *attemptRepositoryServer) CreateAttempt(ctx context.Context, req *driverv1.CreateAttemptRequest) (*driverv1.AttemptRecordResponse, error) {
	provenance, err := attemptProvenanceFromProto(req.GetProvenance())
	if err != nil {
		return nil, attemptRepositoryStatus(learning.ErrInvalidAttempt)
	}
	create := learning.AttemptCreate{ID: learning.AttemptID(req.GetId()), Provenance: provenance}
	if req.GetPartition() == "" || create.Validate() != nil {
		return nil, attemptRepositoryStatus(learning.ErrInvalidAttempt)
	}
	record, err := s.repository.Create(ctx, learning.AttemptPartition(req.GetPartition()), create)
	return serverRecordResponse(record, err)
}

func (s *attemptRepositoryServer) GetAttempt(ctx context.Context, req *driverv1.GetAttemptRequest) (*driverv1.GetAttemptResponse, error) {
	if req.GetPartition() == "" || req.GetId() == "" {
		return nil, attemptRepositoryStatus(learning.ErrInvalidAttempt)
	}
	record, found, err := s.repository.Get(ctx, learning.AttemptPartition(req.GetPartition()), learning.AttemptID(req.GetId()))
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	if !found {
		return &driverv1.GetAttemptResponse{}, nil
	}
	if err := learning.ValidateAttemptRecord(record); err != nil {
		return nil, status.Error(codes.Internal, "attempt repository returned an invalid record")
	}
	return &driverv1.GetAttemptResponse{Found: true, Record: attemptRecordToProto(record)}, nil
}

func (s *attemptRepositoryServer) ListAttempts(ctx context.Context, req *driverv1.ListAttemptsRequest) (*driverv1.ListAttemptsResponse, error) {
	query := learning.AttemptList{After: learning.AttemptID(req.GetAfter()), Limit: int(req.GetLimit()), State: learning.AttemptState(req.GetState())}
	if req.GetPartition() == "" || query.Validate() != nil {
		return nil, attemptRepositoryStatus(learning.ErrInvalidAttempt)
	}
	page, err := s.repository.List(ctx, learning.AttemptPartition(req.GetPartition()), query)
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	response := &driverv1.ListAttemptsResponse{Records: make([]*driverv1.AttemptRecord, 0, len(page.Records)), Next: string(page.Next)}
	for _, record := range page.Records {
		if err := learning.ValidateAttemptRecord(record); err != nil {
			return nil, status.Error(codes.Internal, "attempt repository returned an invalid record")
		}
		response.Records = append(response.Records, attemptRecordToProto(record))
	}
	return response, nil
}

func (s *attemptRepositoryServer) DiscoverAttemptWork(ctx context.Context, req *driverv1.DiscoverAttemptWorkRequest) (*driverv1.DiscoverAttemptWorkResponse, error) {
	query := learning.AttemptWorkList{
		Limit: int(req.GetLimit()),
		After: learning.AttemptWorkCursor{Partition: learning.AttemptPartition(req.GetAfterPartition()), ID: learning.AttemptID(req.GetAfterId())},
	}
	if query.Validate() != nil {
		return nil, attemptRepositoryStatus(learning.ErrInvalidAttempt)
	}
	page, err := s.repository.DiscoverWork(ctx, query)
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	if len(page.Work) > query.Limit {
		return nil, status.Error(codes.Internal, "attempt repository returned too much work")
	}
	response := &driverv1.DiscoverAttemptWorkResponse{
		Work: make([]*driverv1.AttemptWork, 0, len(page.Work)), NextPartition: string(page.Next.Partition), NextId: string(page.Next.ID),
	}
	if (page.Next.Partition == "") != (page.Next.ID == "") {
		return nil, status.Error(codes.Internal, "attempt repository returned an invalid work cursor")
	}
	for _, item := range page.Work {
		if item.Partition == "" || learning.ValidateAttemptRecord(item.Record) != nil {
			return nil, status.Error(codes.Internal, "attempt repository returned invalid work")
		}
		response.Work = append(response.Work, &driverv1.AttemptWork{Partition: string(item.Partition), Record: attemptRecordToProto(item.Record)})
	}
	return response, nil
}

func (s *attemptRepositoryServer) AcquireAttemptClaim(ctx context.Context, req *driverv1.AttemptClaimMutationRequest) (*driverv1.AttemptClaimResponse, error) {
	mutation, err := claimMutationFromProto(req, false, true)
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	record, claim, err := s.repository.AcquireClaim(ctx, mutation.partition, mutation.id, mutation.expected, mutation.duration)
	return serverClaimResponse(record, claim, err)
}

func (s *attemptRepositoryServer) RenewAttemptClaim(ctx context.Context, req *driverv1.AttemptClaimMutationRequest) (*driverv1.AttemptClaimResponse, error) {
	mutation, err := claimMutationFromProto(req, true, true)
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	record, claim, err := s.repository.RenewClaim(ctx, mutation.partition, mutation.id, mutation.expected, mutation.claim, mutation.duration)
	return serverClaimResponse(record, claim, err)
}

func (s *attemptRepositoryServer) CheckpointAttempt(ctx context.Context, req *driverv1.CheckpointAttemptRequest) (*driverv1.AttemptRecordResponse, error) {
	mutation, err := claimMutationFromProto(req.GetMutation(), true, false)
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	checkpoint := learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointStage(req.GetStage()), ProposalID: learning.ProposalID(req.GetProposalId()), SkillID: learning.SkillID(req.GetSkillId())}
	if checkpoint.Validate() != nil {
		return nil, attemptRepositoryStatus(learning.ErrInvalidAttempt)
	}
	record, err := s.repository.Checkpoint(ctx, mutation.partition, mutation.id, mutation.expected, mutation.claim, checkpoint)
	return serverRecordResponse(record, err)
}

func (s *attemptRepositoryServer) ReleaseAttemptClaim(ctx context.Context, req *driverv1.AttemptClaimMutationRequest) (*driverv1.AttemptRecordResponse, error) {
	mutation, err := claimMutationFromProto(req, true, false)
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	record, err := s.repository.ReleaseClaim(ctx, mutation.partition, mutation.id, mutation.expected, mutation.claim)
	return serverRecordResponse(record, err)
}

func (s *attemptRepositoryServer) FinalizeAttempt(ctx context.Context, req *driverv1.FinalizeAttemptRequest) (*driverv1.AttemptRecordResponse, error) {
	mutation, err := claimMutationFromProto(req.GetMutation(), true, false)
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	final := learning.AttemptFinalization{State: learning.AttemptState(req.GetState()), Outcome: learning.AttemptOutcome(req.GetOutcome()), FailureCode: learning.AttemptFailureCode(req.GetFailureCode())}
	if err := final.Validate(); err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	record, err := s.repository.Finalize(ctx, mutation.partition, mutation.id, mutation.expected, mutation.claim, final)
	return serverRecordResponse(record, err)
}

func (s *attemptRepositoryServer) RetryAttempt(ctx context.Context, req *driverv1.AttemptMutationRequest) (*driverv1.AttemptRecordResponse, error) {
	mutation, err := mutationFromProto(req)
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	record, err := s.repository.Retry(ctx, mutation.partition, mutation.id, mutation.expected)
	return serverRecordResponse(record, err)
}

func (s *attemptRepositoryServer) AbandonAttempt(ctx context.Context, req *driverv1.AttemptMutationRequest) (*driverv1.AttemptRecordResponse, error) {
	mutation, err := mutationFromProto(req)
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	record, err := s.repository.Abandon(ctx, mutation.partition, mutation.id, mutation.expected)
	return serverRecordResponse(record, err)
}

func (s *attemptRepositoryServer) DeleteAttempt(ctx context.Context, req *driverv1.AttemptMutationRequest) (*driverv1.DeleteAttemptResponse, error) {
	mutation, err := mutationFromProto(req)
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	if err := s.repository.Delete(ctx, mutation.partition, mutation.id, mutation.expected); err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	return &driverv1.DeleteAttemptResponse{}, nil
}

func (s *attemptRepositoryServer) DeleteTerminalAttemptsOlderThan(ctx context.Context, req *driverv1.DeleteTerminalAttemptsOlderThanRequest) (*driverv1.DeleteTerminalAttemptsOlderThanResponse, error) {
	olderThanMillis := req.GetOlderThanMillis()
	limit := int(req.GetLimit())
	if req.GetPartition() == "" || olderThanMillis < 1 || olderThanMillis > learning.MaxAttemptRetentionAge.Milliseconds() || limit < 1 || limit > learning.MaxAttemptDeleteBatch {
		return nil, attemptRepositoryStatus(learning.ErrInvalidAttempt)
	}
	deleted, err := s.repository.DeleteTerminalOlderThan(ctx, learning.AttemptPartition(req.GetPartition()), time.Duration(olderThanMillis)*time.Millisecond, limit)
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	if deleted < 0 || deleted > math.MaxInt32 {
		return nil, status.Error(codes.Internal, "attempt repository returned an invalid delete count")
	}
	return &driverv1.DeleteTerminalAttemptsOlderThanResponse{Deleted: int32(deleted)}, nil // #nosec G115 -- checked above
}

type attemptMutation struct {
	partition learning.AttemptPartition
	id        learning.AttemptID
	expected  learning.AttemptVersion
	claim     learning.AttemptClaim
	duration  time.Duration
}

func mutationFromProto(req *driverv1.AttemptMutationRequest) (attemptMutation, error) {
	if req == nil || req.GetPartition() == "" || req.GetId() == "" || req.GetExpectedVersion() == "" {
		return attemptMutation{}, learning.ErrInvalidAttempt
	}
	return attemptMutation{partition: learning.AttemptPartition(req.GetPartition()), id: learning.AttemptID(req.GetId()), expected: learning.AttemptVersion(req.GetExpectedVersion())}, nil
}

func claimMutationFromProto(req *driverv1.AttemptClaimMutationRequest, requireClaim, requireDuration bool) (attemptMutation, error) {
	if req == nil || req.GetPartition() == "" || req.GetId() == "" || req.GetExpectedVersion() == "" {
		return attemptMutation{}, learning.ErrInvalidAttempt
	}
	durationMillis := req.GetDurationMillis()
	if (requireDuration && (durationMillis < 1 || durationMillis > learning.MaxAttemptClaimDuration.Milliseconds())) || (!requireDuration && durationMillis != 0) {
		return attemptMutation{}, learning.ErrInvalidAttempt
	}
	mutation := attemptMutation{
		partition: learning.AttemptPartition(req.GetPartition()), id: learning.AttemptID(req.GetId()), expected: learning.AttemptVersion(req.GetExpectedVersion()),
		duration: time.Duration(durationMillis) * time.Millisecond,
	}
	if requireClaim {
		claim, claimErr := attemptClaimFromProto(req.GetClaim())
		if claimErr != nil {
			return attemptMutation{}, learning.ErrInvalidAttempt
		}
		mutation.claim = claim
	} else if req.GetClaim() != nil {
		return attemptMutation{}, learning.ErrInvalidAttempt
	}
	return mutation, nil
}

func serverRecordResponse(record learning.AttemptRecord, err error) (*driverv1.AttemptRecordResponse, error) {
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	if err := learning.ValidateAttemptRecord(record); err != nil {
		return nil, status.Error(codes.Internal, "attempt repository returned an invalid record")
	}
	return &driverv1.AttemptRecordResponse{Record: attemptRecordToProto(record)}, nil
}

func serverClaimResponse(record learning.AttemptRecord, claim learning.AttemptClaim, err error) (*driverv1.AttemptClaimResponse, error) {
	if err != nil {
		return nil, attemptRepositoryStatus(err)
	}
	if err := learning.ValidateAttemptRecord(record); err != nil || !claim.Valid() || record.ClaimGeneration != claim.Generation || !record.ClaimExpiresAt.Equal(claim.ExpiresAt) {
		return nil, status.Error(codes.Internal, "attempt repository returned an invalid claim")
	}
	return &driverv1.AttemptClaimResponse{Record: attemptRecordToProto(record), Claim: &driverv1.AttemptClaim{Generation: uint64(claim.Generation), ExpiresAt: attemptTimeToProto(claim.ExpiresAt)}}, nil
}
