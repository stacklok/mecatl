package app

import (
	"context"
	"sync"

	"github.com/stacklok/mecatl/engine/learning"
)

// lazyProposalRepository defers filesystem creation and flock construction until
// an explicit reflection or proposal operation actually needs persistence.
type lazyProposalRepository struct {
	once sync.Once
	open func() (learning.ProposalRepository, error)
	repo learning.ProposalRepository
	err  error
}

func (r *lazyProposalRepository) get() (learning.ProposalRepository, error) {
	r.once.Do(func() { r.repo, r.err = r.open() })
	return r.repo, r.err
}

func (r *lazyProposalRepository) StageBatch(ctx context.Context, p learning.ProposalPartition, digest string, candidates []learning.Candidate, signals []learning.Signal) ([]learning.ProposalRecord, error) {
	repo, err := r.get()
	if err != nil {
		return nil, err
	}
	return repo.StageBatch(ctx, p, digest, candidates, signals)
}
func (r *lazyProposalRepository) List(ctx context.Context, p learning.ProposalPartition, options learning.ProposalList) (learning.ProposalPage, error) {
	repo, err := r.get()
	if err != nil {
		return learning.ProposalPage{}, err
	}
	return repo.List(ctx, p, options)
}
func (r *lazyProposalRepository) Get(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID) (learning.ProposalRecord, bool, error) {
	repo, err := r.get()
	if err != nil {
		return learning.ProposalRecord{}, false, err
	}
	return repo.Get(ctx, p, id)
}
func (r *lazyProposalRepository) ClaimDecision(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion, decision learning.Decision) (learning.ProposalRecord, error) {
	repo, err := r.get()
	if err != nil {
		return learning.ProposalRecord{}, err
	}
	return repo.ClaimDecision(ctx, p, id, version, decision)
}
func (r *lazyProposalRepository) ClaimPromotion(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion) (learning.ProposalRecord, error) {
	repo, err := r.get()
	if err != nil {
		return learning.ProposalRecord{}, err
	}
	return repo.ClaimPromotion(ctx, p, id, version)
}
func (r *lazyProposalRepository) Finalize(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion, status learning.ProposalStatus, receipt *learning.PromotionReceipt, decision learning.Decision) (learning.ProposalRecord, error) {
	repo, err := r.get()
	if err != nil {
		return learning.ProposalRecord{}, err
	}
	return repo.Finalize(ctx, p, id, version, status, receipt, decision)
}

func (r *lazyProposalRepository) LinkSkillDraft(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion, skillID learning.SkillID, decision learning.Decision) (learning.ProposalRecord, error) {
	repo, err := r.get()
	if err != nil {
		return learning.ProposalRecord{}, err
	}
	return repo.LinkSkillDraft(ctx, p, id, version, skillID, decision)
}

var _ learning.ProposalRepository = (*lazyProposalRepository)(nil)
