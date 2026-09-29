package grpcdriver

//revive:disable:exported // methods implement the documented ProposalRepository contract

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

// ProposalRepository adapts the bounded staged-learning proposal lifecycle to
// a remote driver. Versions are relayed as opaque CAS tokens.
type ProposalRepository struct {
	client driverv1.ProposalRepositoryServiceClient
}

var _ learning.ProposalRepository = (*ProposalRepository)(nil)

// NewProposalRepository wraps an established driver connection.
func NewProposalRepository(conn grpc.ClientConnInterface) *ProposalRepository {
	return &ProposalRepository{client: driverv1.NewProposalRepositoryServiceClient(conn)}
}

func (r *ProposalRepository) StageBatch(ctx context.Context, partition learning.ProposalPartition, digest string, candidates []learning.Candidate, signals []learning.Signal) ([]learning.ProposalRecord, error) {
	if len(candidates) == 0 || len(candidates) > learning.MaxCandidates || len(signals) > learning.MaxInputSignals {
		return nil, learning.ErrInvalidProposal
	}
	for _, candidate := range candidates {
		if err := learning.ValidateProposalMaterial(partition, digest, candidate, signals); err != nil {
			return nil, err
		}
		if !proposalEvidenceOrdinalsBounded(candidate, signals) {
			return nil, learning.ErrInvalidProposal
		}
	}
	resp, err := r.client.StageProposals(ctx, &driverv1.StageProposalsRequest{
		Partition: proposalPartitionToProto(partition), InputDigest: digest,
		Candidates: proposalCandidatesToProto(candidates), Signals: proposalSignalsToProto(signals),
	})
	if err != nil {
		return nil, proposalStatusToErr(ctx, err)
	}
	if len(resp.GetRecords()) != len(candidates) {
		return nil, errors.New("grpcdriver: proposal stage response count mismatch")
	}
	return proposalRecordsFromProto(resp.GetRecords())
}

func (r *ProposalRepository) List(ctx context.Context, partition learning.ProposalPartition, query learning.ProposalList) (learning.ProposalPage, error) {
	resp, err := r.client.ListProposals(ctx, &driverv1.ListProposalsRequest{
		Partition: proposalPartitionToProto(partition), After: string(query.After), Limit: int32(query.Limit), Status: string(query.Status), // #nosec G115 -- domain page limit is <= 200
	})
	if err != nil {
		return learning.ProposalPage{}, proposalStatusToErr(ctx, err)
	}
	records, err := proposalRecordsFromProto(resp.GetRecords())
	if err != nil {
		return learning.ProposalPage{}, err
	}
	if len(resp.GetNext()) > 128 || !utf8.ValidString(resp.GetNext()) {
		return learning.ProposalPage{}, errors.New("grpcdriver: invalid proposal page cursor")
	}
	return learning.ProposalPage{Records: records, Next: learning.ProposalID(resp.GetNext())}, nil
}

func (r *ProposalRepository) Get(ctx context.Context, partition learning.ProposalPartition, id learning.ProposalID) (learning.ProposalRecord, bool, error) {
	resp, err := r.client.GetProposal(ctx, &driverv1.GetProposalRequest{Partition: proposalPartitionToProto(partition), Id: string(id)})
	if err != nil {
		return learning.ProposalRecord{}, false, proposalStatusToErr(ctx, err)
	}
	if !resp.GetFound() {
		if resp.GetRecord() != nil {
			return learning.ProposalRecord{}, false, errors.New("grpcdriver: proposal miss included a record")
		}
		return learning.ProposalRecord{}, false, nil
	}
	record, err := proposalRecordFromProto(resp.GetRecord())
	return record, err == nil, err
}

func (r *ProposalRepository) ClaimDecision(ctx context.Context, partition learning.ProposalPartition, id learning.ProposalID, expected learning.ProposalVersion, decision learning.Decision) (learning.ProposalRecord, error) {
	resp, err := r.client.ClaimProposalDecision(ctx, &driverv1.ProposalDecisionRequest{Mutation: proposalMutationToProto(partition, id, expected), Decision: proposalDecisionToProto(decision)})
	return proposalRecordResponse(ctx, resp, err)
}

func (r *ProposalRepository) ClaimPromotion(ctx context.Context, partition learning.ProposalPartition, id learning.ProposalID, expected learning.ProposalVersion) (learning.ProposalRecord, error) {
	resp, err := r.client.ClaimProposalPromotion(ctx, proposalMutationToProto(partition, id, expected))
	return proposalRecordResponse(ctx, resp, err)
}

func (r *ProposalRepository) Finalize(ctx context.Context, partition learning.ProposalPartition, id learning.ProposalID, expected learning.ProposalVersion, proposalStatus learning.ProposalStatus, receipt *learning.PromotionReceipt, decision learning.Decision) (learning.ProposalRecord, error) {
	request := &driverv1.FinalizeProposalRequest{Mutation: proposalMutationToProto(partition, id, expected), Status: string(proposalStatus), Receipt: proposalReceiptToProto(receipt)}
	if decision.Kind != "" {
		request.Decision = proposalDecisionToProto(decision)
	}
	resp, err := r.client.FinalizeProposal(ctx, request)
	return proposalRecordResponse(ctx, resp, err)
}

func (r *ProposalRepository) LinkSkillDraft(ctx context.Context, partition learning.ProposalPartition, id learning.ProposalID, expected learning.ProposalVersion, skillID learning.SkillID, decision learning.Decision) (learning.ProposalRecord, error) {
	resp, err := r.client.LinkProposalSkillDraft(ctx, &driverv1.LinkProposalSkillDraftRequest{Mutation: proposalMutationToProto(partition, id, expected), SkillId: string(skillID), Decision: proposalDecisionToProto(decision)})
	return proposalRecordResponse(ctx, resp, err)
}

func proposalMutationToProto(partition learning.ProposalPartition, id learning.ProposalID, expected learning.ProposalVersion) *driverv1.ProposalMutationRequest {
	return &driverv1.ProposalMutationRequest{Partition: proposalPartitionToProto(partition), Id: string(id), ExpectedVersion: string(expected)}
}

func proposalRecordResponse(ctx context.Context, resp *driverv1.ProposalRecordResponse, err error) (learning.ProposalRecord, error) {
	if err != nil {
		return learning.ProposalRecord{}, proposalStatusToErr(ctx, err)
	}
	return proposalRecordFromProto(resp.GetRecord())
}

func proposalPartitionToProto(partition learning.ProposalPartition) *driverv1.ProposalPartition {
	return &driverv1.ProposalPartition{Principal: partition.Principal, Project: partition.Project}
}

func proposalPartitionFromProto(wire *driverv1.ProposalPartition) (learning.ProposalPartition, error) {
	if wire == nil {
		return learning.ProposalPartition{}, learning.ErrInvalidProposal
	}
	partition := learning.ProposalPartition{Principal: wire.GetPrincipal(), Project: wire.GetProject()}
	if !proposalPartitionValid(partition) {
		return learning.ProposalPartition{}, learning.ErrInvalidProposal
	}
	return partition, nil
}

func proposalPartitionValid(partition learning.ProposalPartition) bool {
	return strings.TrimSpace(partition.Principal) != "" && len(partition.Principal) <= 1024 && len(partition.Project) <= 4096 && utf8.ValidString(partition.Principal) && utf8.ValidString(partition.Project)
}

func proposalEvidenceToProto(ref learning.EvidenceRef) *driverv1.ProposalEvidence {
	wire := &driverv1.ProposalEvidence{SessionId: string(ref.SessionID), Locator: string(ref.Locator), Ordinal: int32(ref.Ordinal), ToolCallId: string(ref.ToolCallID), Digest: ref.Digest} // #nosec G115 -- evidence ordinal is protocol-bounded
	if ref.EventSeq != nil {
		wire.EventSeq = ref.EventSeq
	}
	return wire
}

func proposalEvidenceFromProto(wire *driverv1.ProposalEvidence) learning.EvidenceRef {
	if wire == nil {
		return learning.EvidenceRef{}
	}
	var eventSeq *int64
	if wire.EventSeq != nil {
		value := wire.GetEventSeq()
		eventSeq = &value
	}
	return learning.EvidenceRef{SessionID: session.SessionID(wire.GetSessionId()), Locator: learning.EvidenceLocator(wire.GetLocator()), Ordinal: int(wire.GetOrdinal()), EventSeq: eventSeq, ToolCallID: session.ToolCallID(wire.GetToolCallId()), Digest: wire.GetDigest()}
}

func proposalEvidenceListToProto(refs []learning.EvidenceRef) []*driverv1.ProposalEvidence {
	out := make([]*driverv1.ProposalEvidence, len(refs))
	for i, ref := range refs {
		out[i] = proposalEvidenceToProto(ref)
	}
	return out
}

func proposalEvidenceListFromProto(refs []*driverv1.ProposalEvidence) []learning.EvidenceRef {
	out := make([]learning.EvidenceRef, len(refs))
	for i, ref := range refs {
		out[i] = proposalEvidenceFromProto(ref)
	}
	return out
}

func proposalCandidateToProto(candidate learning.Candidate) *driverv1.ProposalCandidate {
	return &driverv1.ProposalCandidate{Kind: string(candidate.Kind), Key: candidate.Key, Value: candidate.Value, Description: candidate.Description, Name: candidate.Name, Title: candidate.Title, Body: candidate.Body, Evidence: proposalEvidenceListToProto(candidate.Evidence)}
}

func proposalCandidateFromProto(wire *driverv1.ProposalCandidate) learning.Candidate {
	if wire == nil {
		return learning.Candidate{}
	}
	return learning.Candidate{Kind: learning.CandidateKind(wire.GetKind()), Key: wire.GetKey(), Value: wire.GetValue(), Description: wire.GetDescription(), Name: wire.GetName(), Title: wire.GetTitle(), Body: wire.GetBody(), Evidence: proposalEvidenceListFromProto(wire.GetEvidence())}
}

func proposalCandidatesToProto(candidates []learning.Candidate) []*driverv1.ProposalCandidate {
	out := make([]*driverv1.ProposalCandidate, len(candidates))
	for i, candidate := range candidates {
		out[i] = proposalCandidateToProto(candidate)
	}
	return out
}

func proposalSignalsToProto(signals []learning.Signal) []*driverv1.ProposalSignal {
	out := make([]*driverv1.ProposalSignal, len(signals))
	for i, signal := range signals {
		out[i] = &driverv1.ProposalSignal{Kind: string(signal.Kind), Evidence: proposalEvidenceListToProto(signal.Evidence)}
	}
	return out
}

func proposalSignalsFromProto(signals []*driverv1.ProposalSignal) []learning.Signal {
	out := make([]learning.Signal, len(signals))
	for i, signal := range signals {
		if signal != nil {
			out[i] = learning.Signal{Kind: learning.SignalKind(signal.GetKind()), Evidence: proposalEvidenceListFromProto(signal.GetEvidence())}
		}
	}
	return out
}

func proposalDecisionToProto(decision learning.Decision) *driverv1.ProposalDecision {
	return &driverv1.ProposalDecision{Kind: string(decision.Kind), Actor: decision.Actor, Reason: decision.Reason, At: proposalTimeToProto(decision.At)}
}

func proposalDecisionFromProto(wire *driverv1.ProposalDecision) (learning.Decision, error) {
	if wire == nil {
		return learning.Decision{}, learning.ErrInvalidProposal
	}
	at, err := proposalTimeFromProto(wire.GetAt(), false)
	if err != nil {
		return learning.Decision{}, learning.ErrInvalidProposal
	}
	decision := learning.Decision{Kind: learning.DecisionKind(wire.GetKind()), Actor: wire.GetActor(), Reason: wire.GetReason(), At: at}
	if err := learning.ValidateDecision(decision); err != nil {
		return learning.Decision{}, err
	}
	return decision, nil
}

func proposalReceiptToProto(receipt *learning.PromotionReceipt) *driverv1.ProposalPromotionReceipt {
	if receipt == nil {
		return nil
	}
	return &driverv1.ProposalPromotionReceipt{MemoryKey: receipt.MemoryKey, PreviousExists: receipt.PreviousExists, PreviousVersion: receipt.PreviousVersion, ResultVersion: receipt.ResultVersion}
}

func proposalReceiptFromProto(wire *driverv1.ProposalPromotionReceipt) *learning.PromotionReceipt {
	if wire == nil {
		return nil
	}
	return &learning.PromotionReceipt{MemoryKey: wire.GetMemoryKey(), PreviousExists: wire.GetPreviousExists(), PreviousVersion: wire.GetPreviousVersion(), ResultVersion: wire.GetResultVersion()}
}

func proposalRecordToProto(record learning.ProposalRecord) *driverv1.ProposalRecord {
	wire := &driverv1.ProposalRecord{Id: string(record.ID), Version: string(record.Version), Status: string(record.Status), Partition: proposalPartitionToProto(record.Partition), InputDigest: record.InputDigest, Candidate: proposalCandidateToProto(record.Candidate), Signals: proposalSignalsToProto(record.Signals), Receipt: proposalReceiptToProto(record.Receipt), SkillId: string(record.SkillID), CreatedAt: proposalTimeToProto(record.CreatedAt), UpdatedAt: proposalTimeToProto(record.UpdatedAt)}
	wire.Decisions = make([]*driverv1.ProposalDecision, len(record.Decisions))
	for i, decision := range record.Decisions {
		wire.Decisions[i] = proposalDecisionToProto(decision)
	}
	return wire
}

func proposalRecordFromProto(wire *driverv1.ProposalRecord) (learning.ProposalRecord, error) {
	if wire == nil {
		return learning.ProposalRecord{}, errors.New("grpcdriver: proposal response omitted record")
	}
	partition, err := proposalPartitionFromProto(wire.GetPartition())
	if err != nil {
		return learning.ProposalRecord{}, errors.New("grpcdriver: invalid proposal record partition")
	}
	created, err := proposalTimeFromProto(wire.GetCreatedAt(), true)
	if err != nil {
		return learning.ProposalRecord{}, err
	}
	updated, err := proposalTimeFromProto(wire.GetUpdatedAt(), true)
	if err != nil {
		return learning.ProposalRecord{}, err
	}
	record := learning.ProposalRecord{ID: learning.ProposalID(wire.GetId()), Version: learning.ProposalVersion(wire.GetVersion()), Status: learning.ProposalStatus(wire.GetStatus()), Partition: partition, InputDigest: wire.GetInputDigest(), Candidate: proposalCandidateFromProto(wire.GetCandidate()), Signals: proposalSignalsFromProto(wire.GetSignals()), Receipt: proposalReceiptFromProto(wire.GetReceipt()), SkillID: learning.SkillID(wire.GetSkillId()), CreatedAt: created, UpdatedAt: updated}
	if len(wire.GetDecisions()) > learning.MaxProposalDecisions {
		return learning.ProposalRecord{}, errors.New("grpcdriver: invalid proposal decision history")
	}
	record.Decisions = make([]learning.Decision, len(wire.GetDecisions()))
	for i, wireDecision := range wire.GetDecisions() {
		decision, decodeErr := proposalDecisionFromProto(wireDecision)
		if decodeErr != nil {
			return learning.ProposalRecord{}, errors.New("grpcdriver: invalid proposal decision history")
		}
		record.Decisions[i] = decision
	}
	if err := validateProposalRecord(record); err != nil {
		return learning.ProposalRecord{}, fmt.Errorf("grpcdriver: invalid proposal record: %w", err)
	}
	return record, nil
}

func proposalRecordsFromProto(wires []*driverv1.ProposalRecord) ([]learning.ProposalRecord, error) {
	out := make([]learning.ProposalRecord, 0, len(wires))
	for _, wire := range wires {
		record, err := proposalRecordFromProto(wire)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

//nolint:gocyclo // validates the complete bounded proposal record crossing the trust boundary
func validateProposalRecord(record learning.ProposalRecord) error {
	if len(record.ID) == 0 || len(record.ID) > 128 || len(record.Version) == 0 || len(record.Version) > 128 || !record.Status.Valid() || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) || len(record.Decisions) > learning.MaxProposalDecisions || len(record.SkillID) > 128 {
		return learning.ErrInvalidProposal
	}
	if err := learning.ValidateProposalMaterial(record.Partition, record.InputDigest, record.Candidate, record.Signals); err != nil {
		return err
	}
	if !proposalEvidenceOrdinalsBounded(record.Candidate, record.Signals) {
		return learning.ErrInvalidProposal
	}
	id, err := learning.DeterministicProposalID(record.Partition, record.InputDigest, record.Candidate)
	if err != nil || id != record.ID {
		return learning.ErrInvalidProposal
	}
	for _, decision := range record.Decisions {
		if decision.At.IsZero() {
			return learning.ErrInvalidProposal
		}
		if err := learning.ValidateDecision(decision); err != nil {
			return err
		}
	}
	if receipt := record.Receipt; receipt != nil {
		if len(receipt.MemoryKey) > 256 || len(receipt.PreviousVersion) > 128 || len(receipt.ResultVersion) > 128 || !utf8.ValidString(receipt.MemoryKey) || !utf8.ValidString(receipt.PreviousVersion) || !utf8.ValidString(receipt.ResultVersion) {
			return learning.ErrInvalidProposal
		}
	}
	if record.Status == learning.ProposalPromoted && (record.Receipt == nil || record.Receipt.MemoryKey == "" || record.Receipt.ResultVersion == "") || record.Status == learning.ProposalUndone && record.Receipt == nil || record.Status == learning.ProposalSkillMaterialized && record.SkillID == "" {
		return learning.ErrInvalidProposal
	}
	return nil
}

func proposalEvidenceOrdinalsBounded(candidate learning.Candidate, signals []learning.Signal) bool {
	for _, ref := range candidate.Evidence {
		if ref.Ordinal < 0 || ref.Ordinal > math.MaxInt32 {
			return false
		}
	}
	for _, signal := range signals {
		for _, ref := range signal.Evidence {
			if ref.Ordinal < 0 || ref.Ordinal > math.MaxInt32 {
				return false
			}
		}
	}
	return true
}

func proposalTimeToProto(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}

func proposalTimeFromProto(value *timestamppb.Timestamp, required bool) (time.Time, error) {
	if value == nil {
		if required {
			return time.Time{}, errors.New("grpcdriver: proposal response omitted timestamp")
		}
		return time.Time{}, nil
	}
	if err := value.CheckValid(); err != nil {
		return time.Time{}, errors.New("grpcdriver: invalid proposal timestamp")
	}
	return value.AsTime(), nil
}

func proposalStatusToErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	st, ok := status.FromError(err)
	if !ok {
		return errors.New("grpcdriver: proposal repository driver request failed")
	}
	for _, detail := range st.Details() {
		if typed, ok := detail.(*driverv1.ProposalErrorDetail); ok {
			if sentinel := proposalErrorSentinel(typed.GetCode()); sentinel != nil {
				return sentinel
			}
		}
	}
	return status.Error(st.Code(), "proposal repository driver request failed")
}

func proposalErrorSentinel(code driverv1.ProposalErrorCode) error {
	switch code {
	case driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_INVALID:
		return learning.ErrInvalidProposal
	case driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_NOT_FOUND:
		return learning.ErrProposalNotFound
	case driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_VERSION_CONFLICT:
		return learning.ErrProposalVersionConflict
	case driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_TRANSITION:
		return learning.ErrProposalTransition
	case driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_LIMIT:
		return learning.ErrProposalLimit
	default:
		return nil
	}
}

func proposalErrorCode(err error) (driverv1.ProposalErrorCode, codes.Code) {
	switch {
	case errors.Is(err, learning.ErrInvalidProposal):
		return driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_INVALID, codes.InvalidArgument
	case errors.Is(err, learning.ErrProposalNotFound):
		return driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_NOT_FOUND, codes.NotFound
	case errors.Is(err, learning.ErrProposalVersionConflict):
		return driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_VERSION_CONFLICT, codes.Aborted
	case errors.Is(err, learning.ErrProposalTransition):
		return driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_TRANSITION, codes.FailedPrecondition
	case errors.Is(err, learning.ErrProposalLimit):
		return driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_LIMIT, codes.ResourceExhausted
	default:
		return driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_UNSPECIFIED, codes.Internal
	}
}

func proposalRepositoryStatus(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "proposal repository operation cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "proposal repository operation timed out")
	}
	code, grpcCode := proposalErrorCode(err)
	if code == driverv1.ProposalErrorCode_PROPOSAL_ERROR_CODE_UNSPECIFIED {
		return status.Error(grpcCode, "proposal repository operation failed")
	}
	sentinel := proposalErrorSentinel(code)
	st := status.New(grpcCode, sentinel.Error())
	withDetails, detailErr := st.WithDetails(&driverv1.ProposalErrorDetail{Code: code})
	if detailErr != nil {
		return status.Error(codes.Internal, "proposal repository operation failed")
	}
	return withDetails.Err()
}
