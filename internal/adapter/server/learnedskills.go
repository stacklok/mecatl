package server

//revive:disable:exported // learnedskills.go declares the complete RPC lifecycle surface as one unit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/learning"
)

const (
	maxLearnedSkillDiffBytes = 32 << 10
	skillPublicationTimeout  = 5 * time.Second
)

func (s *Service) liveSkillGeneration(ctx context.Context, project string) uint64 {
	if s.cfg.LiveSkillGeneration == nil {
		return 0
	}
	partition, err := s.skillPartition(ctx, project)
	if err != nil {
		return 0
	}
	return s.cfg.LiveSkillGeneration(partition)
}

func (s *Service) publishLearnedSkills(ctx context.Context, partition learning.SkillPartition) (string, string) {
	if s.cfg.PublishLearnedSkills == nil {
		return "unavailable", "no publication target"
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), skillPublicationTimeout)
	err := s.cfg.PublishLearnedSkills(publishCtx, partition)
	cancel()
	if err != nil {
		return "pending_reconciliation", safeSkillText(err.Error(), 1024)
	}
	return "published", ""
}

func (s *Service) skillPartition(ctx context.Context, project string) (learning.SkillPartition, error) {
	proposal, err := s.proposalPartition(ctx, project)
	if err != nil {
		return learning.SkillPartition{}, err
	}
	return learning.SkillPartition(proposal), nil
}

func (s *Service) ListLearnedSkills(ctx context.Context, request *mecatlv1.ListLearnedSkillsRequest) (*mecatlv1.ListLearnedSkillsResponse, error) {
	if s.cfg.LearnedSkills == nil {
		return nil, ErrLearningUnavailable
	}
	if request.GetLimit() < 0 || request.GetLimit() > learning.MaxSkillPageSize {
		return nil, fmt.Errorf("%w: skill limit must be between 0 and %d", ErrInvalidArgument, learning.MaxSkillPageSize)
	}
	state := learning.SkillState(request.GetState())
	if state != "" && !state.Valid() {
		return nil, fmt.Errorf("%w: invalid skill state", ErrInvalidArgument)
	}
	partition, err := s.skillPartition(ctx, request.GetProject())
	if err != nil {
		return nil, err
	}
	if s.cfg.BeginSkillPublication != nil {
		unlock := s.cfg.BeginSkillPublication()
		defer unlock()
	}
	if s.cfg.PublishLearnedSkills != nil {
		publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), skillPublicationTimeout)
		publishErr := s.cfg.PublishLearnedSkills(publishCtx, partition)
		cancel()
		if publishErr != nil {
			return nil, skillServiceError(publishErr)
		}
	}
	limit := int(request.GetLimit())
	if limit == 0 {
		limit = learning.DefaultSkillPageSize
	}
	page, err := s.cfg.LearnedSkills.List(ctx, partition, learning.SkillList{After: learning.SkillID(request.GetCursor()), Limit: limit, State: state, OwnerAgent: request.GetOwnerAgent()})
	if err != nil {
		return nil, skillServiceError(err)
	}
	out := make([]*mecatlv1.LearnedSkillVersion, len(page.Versions))
	for i := range page.Versions {
		out[i] = toProtoLearnedSkill(page.Versions[i])
	}
	return &mecatlv1.ListLearnedSkillsResponse{Skills: out, NextCursor: validLearningText(string(page.Next)), Generation: s.liveSkillGeneration(ctx, request.GetProject()), Project: validLearningText(request.GetProject())}, nil
}

func (s *Service) GetLearnedSkill(ctx context.Context, request *mecatlv1.GetLearnedSkillRequest) (*mecatlv1.GetLearnedSkillResponse, error) {
	version, err := s.getLearnedSkill(ctx, request.GetProject(), request.GetOwnerAgent(), request.GetId(), request.GetVersion())
	if err != nil {
		return nil, err
	}
	return &mecatlv1.GetLearnedSkillResponse{Skill: toProtoLearnedSkill(version), Generation: s.liveSkillGeneration(ctx, request.GetProject()), Project: validLearningText(request.GetProject())}, nil
}

func (s *Service) getLearnedSkill(ctx context.Context, project, owner, id, version string) (learning.SkillVersion, error) {
	if s.cfg.LearnedSkills == nil {
		return learning.SkillVersion{}, ErrLearningUnavailable
	}
	if owner == "" || id == "" {
		return learning.SkillVersion{}, fmt.Errorf("%w: owner_agent and id are required", ErrInvalidArgument)
	}
	partition, err := s.skillPartition(ctx, project)
	if err != nil {
		return learning.SkillVersion{}, err
	}
	value, found, err := s.cfg.LearnedSkills.Get(ctx, partition, owner, learning.SkillID(id), learning.VersionID(version))
	if err != nil {
		return learning.SkillVersion{}, skillServiceError(err)
	}
	if !found {
		return learning.SkillVersion{}, fmt.Errorf("%w: learned skill", ErrNotFound)
	}
	return value, nil
}

func (s *Service) DiffLearnedSkillVersions(ctx context.Context, request *mecatlv1.DiffLearnedSkillVersionsRequest) (*mecatlv1.DiffLearnedSkillVersionsResponse, error) {
	from, err := s.getLearnedSkill(ctx, request.GetProject(), request.GetOwnerAgent(), request.GetId(), request.GetFromVersion())
	if err != nil {
		return nil, err
	}
	to, err := s.getLearnedSkill(ctx, request.GetProject(), request.GetOwnerAgent(), request.GetId(), request.GetToVersion())
	if err != nil {
		return nil, err
	}
	diff := fmt.Sprintf("--- %s\n+++ %s\n@@ description @@\n-%s\n+%s\n@@ body @@\n-%s\n+%s\n", from.Version, to.Version, safeSkillText(from.Bundle.Description, 4096), safeSkillText(to.Bundle.Description, 4096), safeSkillText(from.Bundle.Body, 12<<10), safeSkillText(to.Bundle.Body, 12<<10))
	return &mecatlv1.DiffLearnedSkillVersionsResponse{Diff: safeSkillText(diff, maxLearnedSkillDiffBytes), Generation: s.liveSkillGeneration(ctx, request.GetProject()), Project: validLearningText(request.GetProject()), SkillId: validLearningText(request.GetId()), FromVersion: validLearningText(request.GetFromVersion()), ToVersion: validLearningText(request.GetToVersion())}, nil
}

type skillMutation func(context.Context, learning.SkillPartition, string, learning.SkillID, learning.VersionID, learning.Revision) (learning.SkillVersion, error)

func (s *Service) mutateLearnedSkill(ctx context.Context, request *mecatlv1.MutateLearnedSkillRequest, operation string, mutate skillMutation) (*mecatlv1.MutateLearnedSkillResponse, error) {
	if s.cfg.LearnedSkills == nil {
		return nil, ErrLearningUnavailable
	}
	if request.GetOwnerAgent() == "" || request.GetId() == "" || request.GetVersion() == "" || request.GetExpectedRevision() == "" {
		return nil, fmt.Errorf("%w: owner_agent, id, version, and expected_revision are required", ErrInvalidArgument)
	}
	partition, err := s.skillPartition(ctx, request.GetProject())
	if err != nil {
		return nil, err
	}
	if s.cfg.BeginSkillPublication != nil {
		unlock := s.cfg.BeginSkillPublication()
		defer unlock()
	}
	if s.cfg.SkillActionAvailable == nil {
		return nil, fmt.Errorf("%w: learned-skill publication target is unavailable", ErrFailedPrecondition)
	}
	if available, reason := s.cfg.SkillActionAvailable(partition, request.GetOwnerAgent()); !available {
		return nil, fmt.Errorf("%w: %s", ErrFailedPrecondition, reason)
	}
	if operation == "activate" {
		current, getErr := s.getLearnedSkill(ctx, request.GetProject(), request.GetOwnerAgent(), request.GetId(), request.GetVersion())
		if getErr != nil {
			return nil, getErr
		}
		if s.cfg.LearnedSkillNameAvailable != nil && !s.cfg.LearnedSkillNameAvailable(current.Bundle.Name) {
			return nil, fmt.Errorf("%w: external skill %q has precedence", ErrFailedPrecondition, current.Bundle.Name)
		}
	}
	value, err := mutate(ctx, partition, request.GetOwnerAgent(), learning.SkillID(request.GetId()), learning.VersionID(request.GetVersion()), learning.Revision(request.GetExpectedRevision()))
	if err != nil {
		return nil, skillServiceError(err)
	}
	status, publishErr := s.publishLearnedSkills(ctx, partition)
	return &mecatlv1.MutateLearnedSkillResponse{Skill: toProtoLearnedSkill(value), Generation: s.liveSkillGeneration(ctx, request.GetProject()), Project: validLearningText(request.GetProject()), PublicationStatus: status, PublicationError: publishErr}, nil
}

func (s *Service) ActivateLearnedSkill(ctx context.Context, r *mecatlv1.MutateLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	if s.cfg.LearnedSkills == nil {
		return nil, ErrLearningUnavailable
	}
	return s.mutateLearnedSkill(ctx, r, "activate", s.cfg.LearnedSkills.Activate)
}
func (s *Service) RejectLearnedSkill(ctx context.Context, r *mecatlv1.MutateLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	if s.cfg.LearnedSkills == nil {
		return nil, ErrLearningUnavailable
	}
	return s.mutateLearnedSkill(ctx, r, "reject", s.cfg.LearnedSkills.Reject)
}
func (s *Service) ArchiveLearnedSkill(ctx context.Context, r *mecatlv1.MutateLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	if s.cfg.LearnedSkills == nil {
		return nil, ErrLearningUnavailable
	}
	return s.mutateLearnedSkill(ctx, r, "archive", s.cfg.LearnedSkills.Archive)
}

func (s *Service) RollbackLearnedSkill(ctx context.Context, r *mecatlv1.RollbackLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	if s.cfg.LearnedSkills == nil {
		return nil, ErrLearningUnavailable
	}
	if r.GetOwnerAgent() == "" || r.GetId() == "" || r.GetTargetVersion() == "" || r.GetExpectedRevision() == "" {
		return nil, fmt.Errorf("%w: owner_agent, id, target_version, and expected_revision are required", ErrInvalidArgument)
	}
	partition, err := s.skillPartition(ctx, r.GetProject())
	if err != nil {
		return nil, err
	}
	if s.cfg.BeginSkillPublication != nil {
		unlock := s.cfg.BeginSkillPublication()
		defer unlock()
	}
	if s.cfg.SkillActionAvailable == nil {
		return nil, fmt.Errorf("%w: learned-skill publication target is unavailable", ErrFailedPrecondition)
	}
	if available, reason := s.cfg.SkillActionAvailable(partition, r.GetOwnerAgent()); !available {
		return nil, fmt.Errorf("%w: %s", ErrFailedPrecondition, reason)
	}
	if s.cfg.LearnedSkillNameAvailable != nil {
		target, found, getErr := s.cfg.LearnedSkills.Get(ctx, partition, r.GetOwnerAgent(), learning.SkillID(r.GetId()), learning.VersionID(r.GetTargetVersion()))
		if getErr != nil {
			return nil, skillServiceError(getErr)
		}
		if !found {
			return nil, fmt.Errorf("%w: learned skill", ErrNotFound)
		}
		if !s.cfg.LearnedSkillNameAvailable(target.Bundle.Name) {
			return nil, fmt.Errorf("%w: external skill %q has precedence", ErrFailedPrecondition, target.Bundle.Name)
		}
	}
	value, err := s.cfg.LearnedSkills.Rollback(ctx, partition, r.GetOwnerAgent(), learning.SkillID(r.GetId()), learning.Revision(r.GetExpectedRevision()), learning.VersionID(r.GetTargetVersion()))
	if err != nil {
		return nil, skillServiceError(err)
	}
	status, publishErr := s.publishLearnedSkills(ctx, partition)
	return &mecatlv1.MutateLearnedSkillResponse{Skill: toProtoLearnedSkill(value), Generation: s.liveSkillGeneration(ctx, r.GetProject()), Project: validLearningText(r.GetProject()), PublicationStatus: status, PublicationError: publishErr}, nil
}

func (s *Service) ListSkillChanges(ctx context.Context, r *mecatlv1.ListSkillChangesRequest) (*mecatlv1.ListSkillChangesResponse, error) {
	if s.cfg.LearnedSkills == nil {
		return nil, ErrLearningUnavailable
	}
	limit := int(r.GetLimit())
	if limit < 0 || limit > learning.MaxSkillPageSize {
		return nil, fmt.Errorf("%w: change limit must be between 0 and %d", ErrInvalidArgument, learning.MaxSkillPageSize)
	}
	if limit == 0 {
		limit = learning.DefaultSkillPageSize
	}
	partition, err := s.skillPartition(ctx, r.GetProject())
	if err != nil {
		return nil, err
	}
	receipts, ok := s.cfg.LearnedSkills.(learning.SkillReceiptRepository)
	if !ok {
		return nil, fmt.Errorf("%w: durable skill receipt index unavailable", ErrFailedPrecondition)
	}
	page, err := receipts.ListSkillReceipts(ctx, partition, learning.SkillReceiptList{After: r.GetCursor(), Limit: limit})
	if err != nil {
		return nil, skillServiceError(err)
	}
	changes := make([]*mecatlv1.SkillChangeReceipt, len(page.Records))
	for i, record := range page.Records {
		changes[i] = protoSkillReceiptRecord(record)
	}
	return &mecatlv1.ListSkillChangesResponse{Changes: changes, NextCursor: validLearningText(page.Next), Generation: s.liveSkillGeneration(ctx, r.GetProject()), Project: validLearningText(r.GetProject())}, nil
}

func toProtoLearnedSkill(v learning.SkillVersion) *mecatlv1.LearnedSkillVersion {
	out := &mecatlv1.LearnedSkillVersion{Id: validLearningText(string(v.ID)), Name: validLearningText(v.Bundle.Name), Version: validLearningText(string(v.Version)), Revision: validLearningText(string(v.Revision)), State: validLearningText(string(v.State)), OwnerAgent: validLearningText(v.OwnerAgent), Description: safeSkillText(v.Bundle.Description, learning.MaxSkillDescriptionBytes), Body: safeSkillText(v.Bundle.Body, learning.MaxSkillBodyBytes), Supersedes: validLearningText(string(v.Supersedes)), EvidenceCount: int32(len(v.Provenance.EvidenceRefs)), CreatedAt: timestampOrNil(v.CreatedAt), UpdatedAt: timestampOrNil(v.UpdatedAt), InspectAvailable: true, UndoAvailable: v.State == learning.SkillActive} //nolint:gosec // provenance is capped by MaxSkillEvidence
	for _, ref := range v.Provenance.EvidenceRefs {
		out.Evidence = append(out.Evidence, &mecatlv1.LearningEvidenceRef{SessionId: validLearningText(string(ref.SessionID)), Locator: validLearningText(string(ref.Locator)), Ordinal: int32(ref.Ordinal), ToolCallId: validLearningText(string(ref.ToolCallID)), Digest: validLearningText(ref.Digest)}) //nolint:gosec // validated proposal ordinals are bounded
	}
	for _, evaluation := range v.Evaluations {
		out.Evaluations = append(out.Evaluations, &mecatlv1.SkillEvaluation{Verdict: validLearningText(string(evaluation.Verdict)), FixtureIds: validStrings(evaluation.FixtureIDs), Baseline: safeSkillText(evaluation.Baseline, learning.MaxSkillEvaluationTextBytes), Treatment: safeSkillText(evaluation.Treatment, learning.MaxSkillEvaluationTextBytes), Reason: safeSkillText(evaluation.Reason, learning.MaxSkillEvaluationTextBytes), At: timestampOrNil(evaluation.At)})
	}
	for i, receipt := range v.Receipts {
		out.Receipts = append(out.Receipts, protoSkillReceipt(v, i, receipt))
	}
	return out
}

func protoSkillReceiptRecord(record learning.SkillReceiptRecord) *mecatlv1.SkillChangeReceipt {
	receipt := record.Receipt
	return &mecatlv1.SkillChangeReceipt{Id: validLearningText(record.ID), SkillId: validLearningText(string(record.SkillID)), Name: validLearningText(record.Name), Version: validLearningText(string(receipt.Version)), Operation: validLearningText(receipt.Operation), FromState: validLearningText(string(receipt.From)), ToState: validLearningText(string(receipt.To)), At: timestampOrNil(receipt.At), InspectAvailable: true, UndoAvailable: receipt.To == learning.SkillActive}
}

func protoSkillReceipt(v learning.SkillVersion, index int, receipt learning.SkillReceipt) *mecatlv1.SkillChangeReceipt {
	raw := fmt.Sprintf("%s\x00%s\x00%d\x00%s", v.ID, v.Version, index, receipt.Operation)
	sum := sha256.Sum256([]byte(raw))
	out := &mecatlv1.SkillChangeReceipt{Id: hex.EncodeToString(sum[:16]), SkillId: validLearningText(string(v.ID)), Name: validLearningText(v.Bundle.Name), Version: validLearningText(string(receipt.Version)), Operation: validLearningText(receipt.Operation), FromState: validLearningText(string(receipt.From)), ToState: validLearningText(string(receipt.To)), EvidenceCount: int32(len(v.Provenance.EvidenceRefs)), At: timestampOrNil(receipt.At), InspectAvailable: true, UndoAvailable: receipt.To == learning.SkillActive} //nolint:gosec // provenance is capped by MaxSkillEvidence
	for _, ref := range v.Provenance.EvidenceRefs {
		out.SourceSessionIds = append(out.SourceSessionIds, validLearningText(string(ref.SessionID)))
		if ref.ToolCallID != "" {
			out.SourceToolCallIds = append(out.SourceToolCallIds, validLearningText(string(ref.ToolCallID)))
		}
	}
	if len(v.Evaluations) > 0 {
		e := v.Evaluations[len(v.Evaluations)-1]
		out.Verdict = validLearningText(string(e.Verdict))
		out.FixtureIds = validStrings(e.FixtureIDs)
		out.Baseline = safeSkillText(e.Baseline, 1024)
		out.Treatment = safeSkillText(e.Treatment, 1024)
	}
	return out
}

func safeSkillText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) > limit {
		value = value[:limit]
		value = strings.ToValidUTF8(value, "�")
	}
	return value
}
func validStrings(values []string) []string {
	out := make([]string, len(values))
	for i := range values {
		out[i] = validLearningText(values[i])
	}
	return out
}
func skillServiceError(err error) error {
	switch {
	case errors.Is(err, learning.ErrSkillNotFound):
		return fmt.Errorf("%w: learned skill", ErrNotFound)
	case errors.Is(err, learning.ErrSkillConflict), errors.Is(err, learning.ErrSkillTransition), errors.Is(err, learning.ErrSkillNameCollision):
		return fmt.Errorf("%w: %v", ErrProposalConflict, err)
	case errors.Is(err, learning.ErrInvalidSkill), errors.Is(err, learning.ErrSkillOwnerMismatch), errors.Is(err, learning.ErrSkillLimit), errors.Is(err, learning.ErrSkillCursor):
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	default:
		return fmt.Errorf("%w: learned skill operation failed", ErrInternal)
	}
}
