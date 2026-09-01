package grpcdriver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/learning"
)

type skillRepositoryServer struct {
	driverv1.UnimplementedSkillRepositoryServiceServer
	repository learning.SkillRepository
}

// NewSkillRepositoryServer wraps a domain repository as a driver service.
func NewSkillRepositoryServer(repository learning.SkillRepository) driverv1.SkillRepositoryServiceServer {
	return &skillRepositoryServer{repository: repository}
}

func (s *skillRepositoryServer) CreateSkillDraft(ctx context.Context, req *driverv1.CreateSkillDraftRequest) (*driverv1.SkillVersionResponse, error) {
	partition, bundle, provenance := skillPartitionFromProto(req.GetPartition()), skillBundleFromProto(req.GetBundle()), skillProvenanceFromProto(req.GetProvenance())
	if learning.ValidateSkillPartition(partition, req.GetOwnerAgent()) != nil || learning.ValidateSkillBundle(bundle) != nil || learning.ValidateSkillProvenance(provenance) != nil {
		return nil, skillRepositoryStatus(learning.ErrInvalidSkill)
	}
	value, err := s.repository.CreateDraft(ctx, partition, req.GetOwnerAgent(), bundle, provenance)
	return serverSkillResponse(value, err)
}

func (s *skillRepositoryServer) GetSkillVersion(ctx context.Context, req *driverv1.GetSkillVersionRequest) (*driverv1.GetSkillVersionResponse, error) {
	partition := skillPartitionFromProto(req.GetPartition())
	if learning.ValidateSkillPartition(partition, req.GetOwnerAgent()) != nil || req.GetId() == "" || req.GetVersion() == "" {
		return nil, skillRepositoryStatus(learning.ErrInvalidSkill)
	}
	value, found, err := s.repository.Get(ctx, partition, req.GetOwnerAgent(), learning.SkillID(req.GetId()), learning.VersionID(req.GetVersion()))
	if err != nil {
		return nil, skillRepositoryStatus(err)
	}
	if !found {
		return &driverv1.GetSkillVersionResponse{}, nil
	}
	if err := validateWireSkillVersion(value); err != nil {
		return nil, status.Error(codes.Internal, "skill repository returned an invalid version")
	}
	return &driverv1.GetSkillVersionResponse{Found: true, Version: skillVersionToProto(value)}, nil
}

func (s *skillRepositoryServer) ListSkillVersions(ctx context.Context, req *driverv1.ListSkillVersionsRequest) (*driverv1.ListSkillVersionsResponse, error) {
	partition := skillPartitionFromProto(req.GetPartition())
	query := learning.SkillList{After: learning.SkillID(req.GetAfter()), Limit: int(req.GetLimit()), Name: req.GetName(), State: learning.SkillState(req.GetState()), OwnerAgent: req.GetOwnerAgent()}
	if partition.Principal == "" || query.Limit < 0 || query.Limit > learning.MaxSkillPageSize || (query.State != "" && !query.State.Valid()) {
		return nil, skillRepositoryStatus(learning.ErrInvalidSkill)
	}
	page, err := s.repository.List(ctx, partition, query)
	if err != nil {
		return nil, skillRepositoryStatus(err)
	}
	response := &driverv1.ListSkillVersionsResponse{Versions: make([]*driverv1.LearnedSkillVersion, 0, len(page.Versions)), Next: string(page.Next), Generation: uint64(page.Generation)}
	for _, value := range page.Versions {
		if err := validateWireSkillVersion(value); err != nil {
			return nil, status.Error(codes.Internal, "skill repository returned an invalid version")
		}
		response.Versions = append(response.Versions, skillVersionToProto(value))
	}
	return response, nil
}

func (s *skillRepositoryServer) RecordSkillEvaluation(ctx context.Context, req *driverv1.RecordSkillEvaluationRequest) (*driverv1.SkillVersionResponse, error) {
	mutation, err := skillMutationFromProto(req.GetMutation())
	evaluation := skillEvaluationFromProto(req.GetEvaluation())
	if err != nil || learning.ValidateSkillEvaluation(evaluation) != nil {
		return nil, skillRepositoryStatus(learning.ErrInvalidSkill)
	}
	value, err := s.repository.RecordEvaluation(ctx, mutation.partition, mutation.owner, mutation.id, mutation.version, mutation.revision, evaluation)
	return serverSkillResponse(value, err)
}

func (s *skillRepositoryServer) StageSkill(ctx context.Context, req *driverv1.SkillMutationRequest) (*driverv1.SkillVersionResponse, error) {
	mutation, err := skillMutationFromProto(req)
	if err != nil {
		return nil, skillRepositoryStatus(err)
	}
	value, err := s.repository.Stage(ctx, mutation.partition, mutation.owner, mutation.id, mutation.version, mutation.revision)
	return serverSkillResponse(value, err)
}
func (s *skillRepositoryServer) ActivateSkill(ctx context.Context, req *driverv1.SkillMutationRequest) (*driverv1.SkillVersionResponse, error) {
	mutation, err := skillMutationFromProto(req)
	if err != nil {
		return nil, skillRepositoryStatus(err)
	}
	value, err := s.repository.Activate(ctx, mutation.partition, mutation.owner, mutation.id, mutation.version, mutation.revision)
	return serverSkillResponse(value, err)
}
func (s *skillRepositoryServer) ActivateValidatedSkill(ctx context.Context, req *driverv1.SkillMutationRequest) (*driverv1.SkillVersionResponse, error) {
	activator, ok := s.repository.(learning.ValidatedSkillActivator)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "validated skill activation is not supported")
	}
	mutation, err := skillMutationFromProto(req)
	if err != nil {
		return nil, skillRepositoryStatus(err)
	}
	value, err := activator.ActivateValidated(ctx, mutation.partition, mutation.owner, mutation.id, mutation.version, mutation.revision)
	return serverSkillResponse(value, err)
}
func (s *skillRepositoryServer) RejectSkill(ctx context.Context, req *driverv1.SkillMutationRequest) (*driverv1.SkillVersionResponse, error) {
	mutation, err := skillMutationFromProto(req)
	if err != nil {
		return nil, skillRepositoryStatus(err)
	}
	value, err := s.repository.Reject(ctx, mutation.partition, mutation.owner, mutation.id, mutation.version, mutation.revision)
	return serverSkillResponse(value, err)
}
func (s *skillRepositoryServer) ArchiveSkill(ctx context.Context, req *driverv1.SkillMutationRequest) (*driverv1.SkillVersionResponse, error) {
	mutation, err := skillMutationFromProto(req)
	if err != nil {
		return nil, skillRepositoryStatus(err)
	}
	value, err := s.repository.Archive(ctx, mutation.partition, mutation.owner, mutation.id, mutation.version, mutation.revision)
	return serverSkillResponse(value, err)
}
func (s *skillRepositoryServer) RollbackSkill(ctx context.Context, req *driverv1.RollbackSkillRequest) (*driverv1.SkillVersionResponse, error) {
	partition := skillPartitionFromProto(req.GetPartition())
	if learning.ValidateSkillPartition(partition, req.GetOwnerAgent()) != nil || req.GetId() == "" || req.GetExpectedRevision() == "" || req.GetTargetVersion() == "" {
		return nil, skillRepositoryStatus(learning.ErrInvalidSkill)
	}
	value, err := s.repository.Rollback(ctx, partition, req.GetOwnerAgent(), learning.SkillID(req.GetId()), learning.Revision(req.GetExpectedRevision()), learning.VersionID(req.GetTargetVersion()))
	return serverSkillResponse(value, err)
}

type skillMutation struct {
	partition learning.SkillPartition
	owner     string
	id        learning.SkillID
	version   learning.VersionID
	revision  learning.Revision
}

func skillMutationFromProto(req *driverv1.SkillMutationRequest) (skillMutation, error) {
	if req == nil {
		return skillMutation{}, learning.ErrInvalidSkill
	}
	mutation := skillMutation{partition: skillPartitionFromProto(req.GetPartition()), owner: req.GetOwnerAgent(), id: learning.SkillID(req.GetId()), version: learning.VersionID(req.GetVersion()), revision: learning.Revision(req.GetExpectedRevision())}
	if learning.ValidateSkillPartition(mutation.partition, mutation.owner) != nil || mutation.id == "" || mutation.version == "" || mutation.revision == "" {
		return skillMutation{}, learning.ErrInvalidSkill
	}
	return mutation, nil
}
func serverSkillResponse(value learning.SkillVersion, err error) (*driverv1.SkillVersionResponse, error) {
	if err != nil {
		return nil, skillRepositoryStatus(err)
	}
	if err := validateWireSkillVersion(value); err != nil {
		return nil, status.Error(codes.Internal, "skill repository returned an invalid version")
	}
	return &driverv1.SkillVersionResponse{Version: skillVersionToProto(value)}, nil
}
