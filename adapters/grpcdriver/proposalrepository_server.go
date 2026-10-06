package grpcdriver

import (
	"context"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/learning"
)

type proposalRepositoryServer struct {
	driverv1.UnimplementedProposalRepositoryServiceServer
	repository learning.ProposalRepository
}

// NewProposalRepositoryServer wraps a domain proposal repository as a driver service.
func NewProposalRepositoryServer(repository learning.ProposalRepository) driverv1.ProposalRepositoryServiceServer {
	return &proposalRepositoryServer{repository: repository}
}

func (s *proposalRepositoryServer) StageProposals(ctx context.Context, req *driverv1.StageProposalsRequest) (*driverv1.StageProposalsResponse, error) {
	partition, err := proposalPartitionFromProto(req.GetPartition())
	if err != nil || len(req.GetCandidates()) == 0 || len(req.GetCandidates()) > learning.MaxCandidates || len(req.GetSignals()) > learning.MaxInputSignals {
		return nil, proposalRepositoryStatus(learning.ErrInvalidProposal)
	}
	candidates := make([]learning.Candidate, len(req.GetCandidates()))
	for i, wire := range req.GetCandidates() {
		candidates[i] = proposalCandidateFromProto(wire)
	}
	signals := proposalSignalsFromProto(req.GetSignals())
	for _, candidate := range candidates {
		if err := learning.ValidateProposalMaterial(partition, req.GetInputDigest(), candidate, signals); err != nil {
			return nil, proposalRepositoryStatus(err)
		}
	}
	records, err := s.repository.StageBatch(ctx, partition, req.GetInputDigest(), candidates, signals)
	if err != nil {
		return nil, proposalRepositoryStatus(err)
	}
	if len(records) != len(candidates) {
		return nil, status.Error(codes.Internal, "proposal repository returned an invalid record count")
	}
	response := &driverv1.StageProposalsResponse{Records: make([]*driverv1.ProposalRecord, len(records))}
	for i, record := range records {
		if err := validateProposalRecord(record); err != nil {
			return nil, status.Error(codes.Internal, "proposal repository returned an invalid record")
		}
		response.Records[i] = proposalRecordToProto(record)
	}
	return response, nil
}

func (s *proposalRepositoryServer) ListProposals(ctx context.Context, req *driverv1.ListProposalsRequest) (*driverv1.ListProposalsResponse, error) {
	partition, err := proposalPartitionFromProto(req.GetPartition())
	query := learning.ProposalList{After: learning.ProposalID(req.GetAfter()), Limit: int(req.GetLimit()), Status: learning.ProposalStatus(req.GetStatus())}
	if err != nil || len(query.After) > 128 || !utf8.ValidString(string(query.After)) || query.Limit > learning.MaxProposalPageSize || query.Status != "" && !query.Status.Valid() {
		return nil, proposalRepositoryStatus(learning.ErrInvalidProposal)
	}
	page, err := s.repository.List(ctx, partition, query)
	if err != nil {
		return nil, proposalRepositoryStatus(err)
	}
	if len(page.Records) > learning.MaxProposalPageSize || len(page.Next) > 128 || !utf8.ValidString(string(page.Next)) {
		return nil, status.Error(codes.Internal, "proposal repository returned an invalid page")
	}
	response := &driverv1.ListProposalsResponse{Records: make([]*driverv1.ProposalRecord, len(page.Records)), Next: string(page.Next)}
	for i, record := range page.Records {
		if err := validateProposalRecord(record); err != nil {
			return nil, status.Error(codes.Internal, "proposal repository returned an invalid record")
		}
		response.Records[i] = proposalRecordToProto(record)
	}
	return response, nil
}

func (s *proposalRepositoryServer) GetProposal(ctx context.Context, req *driverv1.GetProposalRequest) (*driverv1.GetProposalResponse, error) {
	partition, err := proposalPartitionFromProto(req.GetPartition())
	if err != nil || !validProposalMutationToken(req.GetId()) {
		return nil, proposalRepositoryStatus(learning.ErrInvalidProposal)
	}
	record, found, err := s.repository.Get(ctx, partition, learning.ProposalID(req.GetId()))
	if err != nil {
		return nil, proposalRepositoryStatus(err)
	}
	if !found {
		return &driverv1.GetProposalResponse{}, nil
	}
	if err := validateProposalRecord(record); err != nil {
		return nil, status.Error(codes.Internal, "proposal repository returned an invalid record")
	}
	return &driverv1.GetProposalResponse{Found: true, Record: proposalRecordToProto(record)}, nil
}

func (s *proposalRepositoryServer) ClaimProposalDecision(ctx context.Context, req *driverv1.ProposalDecisionRequest) (*driverv1.ProposalRecordResponse, error) {
	mutation, err := proposalMutationFromProto(req.GetMutation())
	if err != nil {
		return nil, proposalRepositoryStatus(err)
	}
	decision, err := proposalDecisionFromProto(req.GetDecision())
	if err != nil {
		return nil, proposalRepositoryStatus(learning.ErrInvalidProposal)
	}
	record, err := s.repository.ClaimDecision(ctx, mutation.partition, mutation.id, mutation.expected, decision)
	return serverProposalRecordResponse(record, err)
}

func (s *proposalRepositoryServer) ClaimProposalPromotion(ctx context.Context, req *driverv1.ProposalMutationRequest) (*driverv1.ProposalRecordResponse, error) {
	mutation, err := proposalMutationFromProto(req)
	if err != nil {
		return nil, proposalRepositoryStatus(err)
	}
	record, err := s.repository.ClaimPromotion(ctx, mutation.partition, mutation.id, mutation.expected)
	return serverProposalRecordResponse(record, err)
}

func (s *proposalRepositoryServer) FinalizeProposal(ctx context.Context, req *driverv1.FinalizeProposalRequest) (*driverv1.ProposalRecordResponse, error) {
	mutation, err := proposalMutationFromProto(req.GetMutation())
	if err != nil {
		return nil, proposalRepositoryStatus(err)
	}
	var decision learning.Decision
	if req.GetDecision() != nil {
		decision, err = proposalDecisionFromProto(req.GetDecision())
		if err != nil {
			return nil, proposalRepositoryStatus(learning.ErrInvalidProposal)
		}
	}
	receipt := proposalReceiptFromProto(req.GetReceipt())
	if receipt != nil && (len(receipt.MemoryKey) > 256 || len(receipt.PreviousVersion) > 128 || len(receipt.ResultVersion) > 128 || !utf8.ValidString(receipt.MemoryKey) || !utf8.ValidString(receipt.PreviousVersion) || !utf8.ValidString(receipt.ResultVersion)) {
		return nil, proposalRepositoryStatus(learning.ErrInvalidProposal)
	}
	record, err := s.repository.Finalize(ctx, mutation.partition, mutation.id, mutation.expected, learning.ProposalStatus(req.GetStatus()), receipt, decision)
	return serverProposalRecordResponse(record, err)
}

func (s *proposalRepositoryServer) LinkProposalSkillDraft(ctx context.Context, req *driverv1.LinkProposalSkillDraftRequest) (*driverv1.ProposalRecordResponse, error) {
	mutation, err := proposalMutationFromProto(req.GetMutation())
	if err != nil || !validProposalMutationToken(req.GetSkillId()) {
		return nil, proposalRepositoryStatus(learning.ErrInvalidProposal)
	}
	decision, err := proposalDecisionFromProto(req.GetDecision())
	if err != nil {
		return nil, proposalRepositoryStatus(learning.ErrInvalidProposal)
	}
	record, err := s.repository.LinkSkillDraft(ctx, mutation.partition, mutation.id, mutation.expected, learning.SkillID(req.GetSkillId()), decision)
	return serverProposalRecordResponse(record, err)
}

type proposalMutation struct {
	partition learning.ProposalPartition
	id        learning.ProposalID
	expected  learning.ProposalVersion
}

func proposalMutationFromProto(req *driverv1.ProposalMutationRequest) (proposalMutation, error) {
	if req == nil || !validProposalMutationToken(req.GetId()) || !validProposalMutationToken(req.GetExpectedVersion()) {
		return proposalMutation{}, learning.ErrInvalidProposal
	}
	partition, err := proposalPartitionFromProto(req.GetPartition())
	if err != nil {
		return proposalMutation{}, learning.ErrInvalidProposal
	}
	return proposalMutation{partition: partition, id: learning.ProposalID(req.GetId()), expected: learning.ProposalVersion(req.GetExpectedVersion())}, nil
}

func validProposalMutationToken(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value)
}

func serverProposalRecordResponse(record learning.ProposalRecord, err error) (*driverv1.ProposalRecordResponse, error) {
	if err != nil {
		return nil, proposalRepositoryStatus(err)
	}
	if err := validateProposalRecord(record); err != nil {
		return nil, status.Error(codes.Internal, "proposal repository returned an invalid record")
	}
	return &driverv1.ProposalRecordResponse{Record: proposalRecordToProto(record)}, nil
}
