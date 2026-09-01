package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
)

func resolveLearningRepositories(ctx context.Context, cfg Config) (learning.AttemptRepository, learning.ProposalRepository, learning.SkillRepository, func(), error) {
	if cfg.LearningStoreURL == "" {
		return nil, nil, nil, func() {}, nil
	}
	conn, closeConn, err := cfg.drivers().dial(cfg, cfg.LearningStoreURL)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("dial learning-store driver %q: %w", cfg.LearningStoreURL, err)
	}
	caps, err := grpcdriver.ProbeLearningRepositoryCapabilities(ctx, conn)
	if err != nil {
		closeConn()
		return nil, nil, nil, nil, fmt.Errorf("probe learning-store driver capabilities %q: %w", cfg.LearningStoreURL, err)
	}
	if !caps.AttemptRepository || !caps.ProposalRepository || !caps.SkillRepository {
		closeConn()
		return nil, nil, nil, nil, fmt.Errorf("learning-store driver %q does not advertise the complete attempt/proposal/skill repository set", cfg.LearningStoreURL)
	}
	if caps.OwnershipMode != grpcdriver.LearningRepositoryOwnershipEnforced || !caps.CallerInfrastructureRPCsSeparated {
		closeConn()
		return nil, nil, nil, nil, fmt.Errorf("learning-store driver %q does not advertise enforced ownership with separated caller and infrastructure RPCs", cfg.LearningStoreURL)
	}

	attempts := grpcdriver.NewAttemptRepository(conn)
	proposals := &opaqueProposalRepository{inner: grpcdriver.NewProposalRepository(conn)}
	var skills learning.SkillRepository
	if caps.ValidatedSkillActivation {
		skills = &opaqueValidatedSkillRepository{opaqueSkillRepository: opaqueSkillRepository{inner: grpcdriver.NewValidatedSkillRepository(conn)}}
	} else {
		skills = &opaqueSkillRepository{inner: grpcdriver.NewSkillRepository(conn)}
	}
	return attempts, proposals, skills, closeConn, nil
}

func opaqueLearningPartition(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func opaqueProposalPartition(p learning.ProposalPartition) learning.ProposalPartition {
	return learning.ProposalPartition{Principal: opaqueLearningPartition(p.Principal), Project: opaqueLearningPartition(p.Project)}
}

func restoreProposalRecord(record learning.ProposalRecord, partition learning.ProposalPartition) learning.ProposalRecord {
	if record.ID != "" {
		record.Partition = partition
	}
	return record
}

type opaqueProposalRepository struct{ inner learning.ProposalRepository }

func (r *opaqueProposalRepository) StageBatch(ctx context.Context, p learning.ProposalPartition, digest string, candidates []learning.Candidate, signals []learning.Signal) ([]learning.ProposalRecord, error) {
	records, err := r.inner.StageBatch(ctx, opaqueProposalPartition(p), digest, candidates, signals)
	for i := range records {
		records[i] = restoreProposalRecord(records[i], p)
	}
	return records, err
}
func (r *opaqueProposalRepository) List(ctx context.Context, p learning.ProposalPartition, query learning.ProposalList) (learning.ProposalPage, error) {
	page, err := r.inner.List(ctx, opaqueProposalPartition(p), query)
	for i := range page.Records {
		page.Records[i] = restoreProposalRecord(page.Records[i], p)
	}
	return page, err
}
func (r *opaqueProposalRepository) Get(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID) (learning.ProposalRecord, bool, error) {
	record, found, err := r.inner.Get(ctx, opaqueProposalPartition(p), id)
	return restoreProposalRecord(record, p), found, err
}
func (r *opaqueProposalRepository) ClaimDecision(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion, decision learning.Decision) (learning.ProposalRecord, error) {
	record, err := r.inner.ClaimDecision(ctx, opaqueProposalPartition(p), id, version, decision)
	return restoreProposalRecord(record, p), err
}
func (r *opaqueProposalRepository) ClaimPromotion(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion) (learning.ProposalRecord, error) {
	record, err := r.inner.ClaimPromotion(ctx, opaqueProposalPartition(p), id, version)
	return restoreProposalRecord(record, p), err
}
func (r *opaqueProposalRepository) Finalize(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion, status learning.ProposalStatus, receipt *learning.PromotionReceipt, decision learning.Decision) (learning.ProposalRecord, error) {
	record, err := r.inner.Finalize(ctx, opaqueProposalPartition(p), id, version, status, receipt, decision)
	return restoreProposalRecord(record, p), err
}
func (r *opaqueProposalRepository) LinkSkillDraft(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion, skillID learning.SkillID, decision learning.Decision) (learning.ProposalRecord, error) {
	record, err := r.inner.LinkSkillDraft(ctx, opaqueProposalPartition(p), id, version, skillID, decision)
	return restoreProposalRecord(record, p), err
}

func opaqueSkillPartition(p learning.SkillPartition) learning.SkillPartition {
	return learning.SkillPartition{Principal: opaqueLearningPartition(p.Principal), Project: opaqueLearningPartition(p.Project)}
}

func restoreSkillVersion(value learning.SkillVersion, partition learning.SkillPartition) learning.SkillVersion {
	if value.ID != "" {
		value.Partition = partition
	}
	return value
}

type opaqueSkillRepository struct{ inner learning.SkillRepository }

func (r *opaqueSkillRepository) Generation(ctx context.Context, p learning.SkillPartition) (learning.SkillGeneration, error) {
	return r.inner.Generation(ctx, opaqueSkillPartition(p))
}

func (r *opaqueSkillRepository) CreateDraft(ctx context.Context, p learning.SkillPartition, owner string, bundle learning.SkillBundle, provenance learning.SkillProvenance) (learning.SkillVersion, error) {
	value, err := r.inner.CreateDraft(ctx, opaqueSkillPartition(p), owner, bundle, provenance)
	return restoreSkillVersion(value, p), err
}
func (r *opaqueSkillRepository) Get(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID) (learning.SkillVersion, bool, error) {
	value, found, err := r.inner.Get(ctx, opaqueSkillPartition(p), owner, id, version)
	return restoreSkillVersion(value, p), found, err
}
func (r *opaqueSkillRepository) List(ctx context.Context, p learning.SkillPartition, query learning.SkillList) (learning.SkillPage, error) {
	page, err := r.inner.List(ctx, opaqueSkillPartition(p), query)
	for i := range page.Versions {
		page.Versions[i] = restoreSkillVersion(page.Versions[i], p)
	}
	return page, err
}
func (r *opaqueSkillRepository) RecordEvaluation(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, revision learning.Revision, evaluation learning.SkillEvaluation) (learning.SkillVersion, error) {
	value, err := r.inner.RecordEvaluation(ctx, opaqueSkillPartition(p), owner, id, version, revision, evaluation)
	return restoreSkillVersion(value, p), err
}
func (r *opaqueSkillRepository) Stage(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, revision learning.Revision) (learning.SkillVersion, error) {
	value, err := r.inner.Stage(ctx, opaqueSkillPartition(p), owner, id, version, revision)
	return restoreSkillVersion(value, p), err
}
func (r *opaqueSkillRepository) Activate(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, revision learning.Revision) (learning.SkillVersion, error) {
	value, err := r.inner.Activate(ctx, opaqueSkillPartition(p), owner, id, version, revision)
	return restoreSkillVersion(value, p), err
}
func (r *opaqueSkillRepository) Reject(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, revision learning.Revision) (learning.SkillVersion, error) {
	value, err := r.inner.Reject(ctx, opaqueSkillPartition(p), owner, id, version, revision)
	return restoreSkillVersion(value, p), err
}
func (r *opaqueSkillRepository) Archive(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, revision learning.Revision) (learning.SkillVersion, error) {
	value, err := r.inner.Archive(ctx, opaqueSkillPartition(p), owner, id, version, revision)
	return restoreSkillVersion(value, p), err
}
func (r *opaqueSkillRepository) Rollback(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, revision learning.Revision, target learning.VersionID) (learning.SkillVersion, error) {
	value, err := r.inner.Rollback(ctx, opaqueSkillPartition(p), owner, id, revision, target)
	return restoreSkillVersion(value, p), err
}

type opaqueValidatedSkillRepository struct{ opaqueSkillRepository }

func (r *opaqueValidatedSkillRepository) ActivateValidated(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, revision learning.Revision) (learning.SkillVersion, error) {
	activator := r.inner.(learning.ValidatedSkillActivator)
	value, err := activator.ActivateValidated(ctx, opaqueSkillPartition(p), owner, id, version, revision)
	return restoreSkillVersion(value, p), err
}
