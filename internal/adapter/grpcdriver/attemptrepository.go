package grpcdriver

//revive:disable:exported // methods implement the documented AttemptRepository contract

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

// AttemptRepository adapts the bounded learning attempt lifecycle to a remote
// driver. All records are revalidated after transport; versions remain opaque.
type AttemptRepository struct {
	client driverv1.AttemptRepositoryServiceClient
}

var _ learning.AttemptRepository = (*AttemptRepository)(nil)

// NewAttemptRepository wraps an established driver connection.
func NewAttemptRepository(conn grpc.ClientConnInterface) *AttemptRepository {
	return &AttemptRepository{client: driverv1.NewAttemptRepositoryServiceClient(conn)}
}

func (r *AttemptRepository) Create(ctx context.Context, partition learning.AttemptPartition, create learning.AttemptCreate) (learning.AttemptRecord, error) {
	resp, err := r.client.CreateAttempt(ctx, &driverv1.CreateAttemptRequest{
		Partition: string(partition), Id: string(create.ID), Provenance: attemptProvenanceToProto(create.Provenance),
	})
	return recordResponse(ctx, resp, err)
}

func (r *AttemptRepository) Get(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID) (learning.AttemptRecord, bool, error) {
	resp, err := r.client.GetAttempt(ctx, &driverv1.GetAttemptRequest{Partition: string(partition), Id: string(id)})
	if err != nil {
		return learning.AttemptRecord{}, false, attemptStatusToErr(ctx, err)
	}
	if !resp.GetFound() {
		if resp.GetRecord() != nil {
			return learning.AttemptRecord{}, false, errors.New("grpcdriver: attempt miss included a record")
		}
		return learning.AttemptRecord{}, false, nil
	}
	record, err := attemptRecordFromProto(resp.GetRecord())
	return record, err == nil, err
}

func (r *AttemptRepository) List(ctx context.Context, partition learning.AttemptPartition, query learning.AttemptList) (learning.AttemptPage, error) {
	resp, err := r.client.ListAttempts(ctx, &driverv1.ListAttemptsRequest{
		Partition: string(partition), After: string(query.After), Limit: int32(query.Limit), State: string(query.State), // #nosec G115 -- domain limit is <= 200
	})
	if err != nil {
		return learning.AttemptPage{}, attemptStatusToErr(ctx, err)
	}
	page := learning.AttemptPage{Records: make([]learning.AttemptRecord, 0, len(resp.GetRecords())), Next: learning.AttemptID(resp.GetNext())}
	for _, wire := range resp.GetRecords() {
		record, decodeErr := attemptRecordFromProto(wire)
		if decodeErr != nil {
			return learning.AttemptPage{}, decodeErr
		}
		page.Records = append(page.Records, record)
	}
	return page, nil
}

func (r *AttemptRepository) DiscoverWork(ctx context.Context, query learning.AttemptWorkList) (learning.AttemptWorkPage, error) {
	resp, err := r.client.DiscoverAttemptWork(ctx, &driverv1.DiscoverAttemptWorkRequest{
		Now: attemptTimeToProto(query.Now), Limit: int32(query.Limit), AfterPartition: string(query.After.Partition), AfterId: string(query.After.ID), // #nosec G115 -- domain limit is <= 200
	})
	if err != nil {
		return learning.AttemptWorkPage{}, attemptStatusToErr(ctx, err)
	}
	page := learning.AttemptWorkPage{Work: make([]learning.AttemptWork, 0, len(resp.GetWork()))}
	for _, wire := range resp.GetWork() {
		if wire.GetPartition() == "" {
			return learning.AttemptWorkPage{}, errors.New("grpcdriver: attempt work omitted partition")
		}
		record, decodeErr := attemptRecordFromProto(wire.GetRecord())
		if decodeErr != nil {
			return learning.AttemptWorkPage{}, decodeErr
		}
		page.Work = append(page.Work, learning.AttemptWork{Partition: learning.AttemptPartition(wire.GetPartition()), Record: record})
	}
	page.Next = learning.AttemptWorkCursor{Partition: learning.AttemptPartition(resp.GetNextPartition()), ID: learning.AttemptID(resp.GetNextId())}
	if (page.Next.Partition == "") != (page.Next.ID == "") {
		return learning.AttemptWorkPage{}, errors.New("grpcdriver: attempt work returned an incomplete cursor")
	}
	return page, nil
}

func (r *AttemptRepository) AcquireClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, now, expires time.Time) (learning.AttemptRecord, learning.AttemptClaim, error) {
	resp, err := r.client.AcquireAttemptClaim(ctx, claimMutationToProto(partition, id, expected, learning.AttemptClaim{}, now, expires))
	return claimResponse(ctx, resp, err)
}

func (r *AttemptRepository) RenewClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, now, expires time.Time) (learning.AttemptRecord, learning.AttemptClaim, error) {
	resp, err := r.client.RenewAttemptClaim(ctx, claimMutationToProto(partition, id, expected, claim, now, expires))
	return claimResponse(ctx, resp, err)
}

func (r *AttemptRepository) Checkpoint(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, now time.Time, checkpoint learning.AttemptCheckpoint) (learning.AttemptRecord, error) {
	resp, err := r.client.CheckpointAttempt(ctx, &driverv1.CheckpointAttemptRequest{
		Mutation: claimMutationToProto(partition, id, expected, claim, now, time.Time{}), Stage: string(checkpoint.Stage),
		ProposalId: string(checkpoint.ProposalID), SkillId: string(checkpoint.SkillID),
	})
	return recordResponse(ctx, resp, err)
}

func (r *AttemptRepository) ReleaseClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, now time.Time) (learning.AttemptRecord, error) {
	resp, err := r.client.ReleaseAttemptClaim(ctx, claimMutationToProto(partition, id, expected, claim, now, time.Time{}))
	return recordResponse(ctx, resp, err)
}

func (r *AttemptRepository) Finalize(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, now time.Time, final learning.AttemptFinalization) (learning.AttemptRecord, error) {
	resp, err := r.client.FinalizeAttempt(ctx, &driverv1.FinalizeAttemptRequest{
		Mutation: claimMutationToProto(partition, id, expected, claim, now, time.Time{}), State: string(final.State),
		Outcome: string(final.Outcome), FailureCode: string(final.FailureCode),
	})
	return recordResponse(ctx, resp, err)
}

func (r *AttemptRepository) Retry(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, now time.Time) (learning.AttemptRecord, error) {
	resp, err := r.client.RetryAttempt(ctx, mutationToProto(partition, id, expected, now))
	return recordResponse(ctx, resp, err)
}

func (r *AttemptRepository) Abandon(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, now time.Time) (learning.AttemptRecord, error) {
	resp, err := r.client.AbandonAttempt(ctx, mutationToProto(partition, id, expected, now))
	return recordResponse(ctx, resp, err)
}

func (r *AttemptRepository) Delete(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, now time.Time) error {
	_, err := r.client.DeleteAttempt(ctx, mutationToProto(partition, id, expected, now))
	return attemptStatusToErr(ctx, err)
}

func (r *AttemptRepository) DeleteTerminalBefore(ctx context.Context, partition learning.AttemptPartition, before time.Time, limit int) (int, error) {
	resp, err := r.client.DeleteTerminalAttemptsBefore(ctx, &driverv1.DeleteTerminalAttemptsBeforeRequest{
		Partition: string(partition), Before: attemptTimeToProto(before), Limit: int32(limit), // #nosec G115 -- domain limit is <= 200
	})
	if err != nil {
		return 0, attemptStatusToErr(ctx, err)
	}
	return int(resp.GetDeleted()), nil
}

func mutationToProto(partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, now time.Time) *driverv1.AttemptMutationRequest {
	return &driverv1.AttemptMutationRequest{Partition: string(partition), Id: string(id), ExpectedVersion: string(expected), Now: attemptTimeToProto(now)}
}

func claimMutationToProto(partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, now, expires time.Time) *driverv1.AttemptClaimMutationRequest {
	request := &driverv1.AttemptClaimMutationRequest{
		Partition: string(partition), Id: string(id), ExpectedVersion: string(expected), Now: attemptTimeToProto(now), ExpiresAt: attemptTimeToProto(expires),
	}
	if claim.Valid() {
		request.Claim = &driverv1.AttemptClaim{Generation: uint64(claim.Generation), ExpiresAt: attemptTimeToProto(claim.ExpiresAt)}
	}
	return request
}

func recordResponse(ctx context.Context, resp *driverv1.AttemptRecordResponse, err error) (learning.AttemptRecord, error) {
	if err != nil {
		return learning.AttemptRecord{}, attemptStatusToErr(ctx, err)
	}
	return attemptRecordFromProto(resp.GetRecord())
}

func claimResponse(ctx context.Context, resp *driverv1.AttemptClaimResponse, err error) (learning.AttemptRecord, learning.AttemptClaim, error) {
	if err != nil {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, attemptStatusToErr(ctx, err)
	}
	record, err := attemptRecordFromProto(resp.GetRecord())
	if err != nil {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, err
	}
	claim, err := attemptClaimFromProto(resp.GetClaim())
	if err != nil {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, err
	}
	return record, claim, nil
}

func attemptRecordToProto(record learning.AttemptRecord) *driverv1.AttemptRecord {
	return &driverv1.AttemptRecord{
		Id: string(record.ID), Version: string(record.Version), State: string(record.State), Outcome: string(record.Outcome), FailureCode: string(record.FailureCode),
		Provenance: attemptProvenanceToProto(record.Provenance), AttemptGeneration: uint64(record.AttemptGeneration), ClaimGeneration: uint64(record.ClaimGeneration),
		ClaimExpiresAt: attemptTimeToProto(record.ClaimExpiresAt), CheckpointStage: string(record.CheckpointStage), ProposalId: string(record.ProposalID), SkillId: string(record.SkillID),
		CreatedAt: attemptTimeToProto(record.CreatedAt), UpdatedAt: attemptTimeToProto(record.UpdatedAt),
	}
}

func attemptRecordFromProto(wire *driverv1.AttemptRecord) (learning.AttemptRecord, error) {
	if wire == nil {
		return learning.AttemptRecord{}, errors.New("grpcdriver: attempt response omitted record")
	}
	provenance, err := attemptProvenanceFromProto(wire.GetProvenance())
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	created, err := attemptTimeFromProto(wire.GetCreatedAt(), "created_at", true)
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	updated, err := attemptTimeFromProto(wire.GetUpdatedAt(), "updated_at", true)
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	claimExpiry, err := attemptTimeFromProto(wire.GetClaimExpiresAt(), "claim_expires_at", false)
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	record := learning.AttemptRecord{
		ID: learning.AttemptID(wire.GetId()), Version: learning.AttemptVersion(wire.GetVersion()), State: learning.AttemptState(wire.GetState()),
		Outcome: learning.AttemptOutcome(wire.GetOutcome()), FailureCode: learning.AttemptFailureCode(wire.GetFailureCode()), Provenance: provenance,
		AttemptGeneration: learning.AttemptGeneration(wire.GetAttemptGeneration()), ClaimGeneration: learning.ClaimGeneration(wire.GetClaimGeneration()),
		ClaimExpiresAt: claimExpiry, CheckpointStage: learning.AttemptCheckpointStage(wire.GetCheckpointStage()), ProposalID: learning.ProposalID(wire.GetProposalId()),
		SkillID: learning.SkillID(wire.GetSkillId()), CreatedAt: created, UpdatedAt: updated,
	}
	if err := learning.ValidateAttemptRecord(record); err != nil {
		return learning.AttemptRecord{}, fmt.Errorf("grpcdriver: invalid attempt record: %w", err)
	}
	return record, nil
}

func attemptProvenanceToProto(provenance learning.AdmissionProvenance) *driverv1.AttemptProvenance {
	return &driverv1.AttemptProvenance{
		AdmissionClass: string(provenance.Class), SessionId: string(provenance.Source.SessionID), RunId: string(provenance.Source.RunID),
		CanonicalDigest: string(provenance.Source.CanonicalDigest), PromptOrdinal: int32(provenance.CurrentPrompt.Ordinal), // #nosec G115 -- validated admission ordinal is protocol-bounded by callers
		PromptDigest: string(provenance.CurrentPrompt.Digest), PromptOrigin: string(provenance.CurrentPrompt.Origin), Binding: string(provenance.Binding),
	}
}

func attemptProvenanceFromProto(wire *driverv1.AttemptProvenance) (learning.AdmissionProvenance, error) {
	if wire == nil {
		return learning.AdmissionProvenance{}, errors.New("grpcdriver: attempt response omitted provenance")
	}
	provenance := learning.AdmissionProvenance{
		Class:         learning.AdmissionClass(wire.GetAdmissionClass()),
		Source:        learning.AttemptSource{SessionID: session.SessionID(wire.GetSessionId()), RunID: learning.DurableRunID(wire.GetRunId()), CanonicalDigest: learning.CanonicalDigest(wire.GetCanonicalDigest())},
		CurrentPrompt: learning.CurrentPromptBinding{Ordinal: int(wire.GetPromptOrdinal()), Digest: learning.CanonicalDigest(wire.GetPromptDigest()), Origin: learning.PromptOrigin(wire.GetPromptOrigin())},
		Binding:       learning.ProvenanceBinding(wire.GetBinding()),
	}
	if err := provenance.Validate(provenance.Source, provenance.CurrentPrompt); err != nil {
		return learning.AdmissionProvenance{}, fmt.Errorf("grpcdriver: invalid attempt provenance: %w", err)
	}
	return provenance, nil
}

func attemptClaimFromProto(wire *driverv1.AttemptClaim) (learning.AttemptClaim, error) {
	if wire == nil {
		return learning.AttemptClaim{}, errors.New("grpcdriver: attempt response omitted claim")
	}
	expires, err := attemptTimeFromProto(wire.GetExpiresAt(), "claim.expires_at", true)
	if err != nil {
		return learning.AttemptClaim{}, err
	}
	claim := learning.AttemptClaim{Generation: learning.ClaimGeneration(wire.GetGeneration()), ExpiresAt: expires}
	if !claim.Valid() {
		return learning.AttemptClaim{}, errors.New("grpcdriver: invalid attempt claim")
	}
	return claim, nil
}

func attemptTimeToProto(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}

func attemptTimeFromProto(value *timestamppb.Timestamp, field string, required bool) (time.Time, error) {
	if value == nil {
		if required {
			return time.Time{}, fmt.Errorf("grpcdriver: attempt response omitted %s", field)
		}
		return time.Time{}, nil
	}
	if err := value.CheckValid(); err != nil {
		return time.Time{}, fmt.Errorf("grpcdriver: invalid attempt %s: %w", field, err)
	}
	return value.AsTime(), nil
}

func attemptStatusToErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	st, ok := status.FromError(err)
	if !ok {
		return errors.New("grpcdriver: attempt repository driver request failed")
	}
	for _, detail := range st.Details() {
		if typed, ok := detail.(*driverv1.AttemptErrorDetail); ok {
			if sentinel := attemptErrorSentinel(typed.GetCode()); sentinel != nil {
				return sentinel
			}
		}
	}
	return status.Error(st.Code(), "attempt repository driver request failed")
}

func attemptErrorSentinel(code driverv1.AttemptErrorCode) error {
	switch code {
	case driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_INVALID:
		return learning.ErrInvalidAttempt
	case driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_NOT_FOUND:
		return learning.ErrAttemptNotFound
	case driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_CREATE_CONFLICT:
		return learning.ErrAttemptCreateConflict
	case driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_VERSION_CONFLICT:
		return learning.ErrAttemptVersionConflict
	case driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_TRANSITION:
		return learning.ErrAttemptTransition
	case driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_CLAIM_CONFLICT:
		return learning.ErrAttemptClaimConflict
	case driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_CLAIM_LOST:
		return learning.ErrAttemptClaimLost
	default:
		return nil
	}
}

func attemptErrorCode(err error) (driverv1.AttemptErrorCode, codes.Code) {
	switch {
	case errors.Is(err, learning.ErrInvalidAttempt):
		return driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_INVALID, codes.InvalidArgument
	case errors.Is(err, learning.ErrAttemptNotFound):
		return driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_NOT_FOUND, codes.NotFound
	case errors.Is(err, learning.ErrAttemptCreateConflict):
		return driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_CREATE_CONFLICT, codes.AlreadyExists
	case errors.Is(err, learning.ErrAttemptVersionConflict):
		return driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_VERSION_CONFLICT, codes.Aborted
	case errors.Is(err, learning.ErrAttemptTransition):
		return driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_TRANSITION, codes.FailedPrecondition
	case errors.Is(err, learning.ErrAttemptClaimConflict):
		return driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_CLAIM_CONFLICT, codes.ResourceExhausted
	case errors.Is(err, learning.ErrAttemptClaimLost):
		return driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_CLAIM_LOST, codes.OutOfRange
	default:
		return driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_UNSPECIFIED, codes.Internal
	}
}

func attemptRepositoryStatus(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "attempt repository operation cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "attempt repository operation timed out")
	}
	code, grpcCode := attemptErrorCode(err)
	if code == driverv1.AttemptErrorCode_ATTEMPT_ERROR_CODE_UNSPECIFIED {
		return status.Error(grpcCode, "attempt repository operation failed")
	}
	sentinel := attemptErrorSentinel(code)
	st := status.New(grpcCode, sentinel.Error())
	withDetails, detailErr := st.WithDetails(&driverv1.AttemptErrorDetail{Code: code})
	if detailErr != nil {
		return status.Error(codes.Internal, "attempt repository operation failed")
	}
	return withDetails.Err()
}
