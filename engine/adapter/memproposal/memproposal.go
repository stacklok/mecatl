// Package memproposal provides the in-memory proposal repository reference adapter.
package memproposal

//revive:disable:exported // interface methods mirror the documented learning repository contract

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

type Store struct {
	mu      sync.RWMutex
	records map[string]map[learning.ProposalID]learning.ProposalRecord
	now     func() time.Time
}

func New() *Store {
	return &Store{records: map[string]map[learning.ProposalID]learning.ProposalRecord{}, now: time.Now}
}

var _ learning.ProposalRepository = (*Store)(nil)

func key(p learning.ProposalPartition) string {
	return strconv.Itoa(len(p.Principal)) + ":" + p.Principal + p.Project
}
func ver(n int) learning.ProposalVersion   { return learning.ProposalVersion("mem-" + strconv.Itoa(n)) }
func parse(v learning.ProposalVersion) int { n, _ := strconv.Atoi(string(v)[4:]); return n }
func clone(r learning.ProposalRecord) learning.ProposalRecord {
	r.Candidate.Evidence = append([]learning.EvidenceRef(nil), r.Candidate.Evidence...)
	r.Signals = append([]learning.Signal(nil), r.Signals...)
	for i := range r.Signals {
		r.Signals[i].Evidence = append([]learning.EvidenceRef(nil), r.Signals[i].Evidence...)
	}
	r.Decisions = append([]learning.Decision(nil), r.Decisions...)
	if r.Receipt != nil {
		x := *r.Receipt
		r.Receipt = &x
	}
	return r
}
func (s *Store) StageBatch(ctx context.Context, p learning.ProposalPartition, input string, cs []learning.Candidate, signals []learning.Signal) ([]learning.ProposalRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(cs) == 0 || len(cs) > learning.MaxCandidates {
		return nil, learning.ErrInvalidProposal
	}
	ids := make([]learning.ProposalID, len(cs))
	for i, c := range cs {
		if err := learning.ValidateProposalMaterial(p, input, c, signals); err != nil {
			return nil, err
		}
		id, err := learning.DeterministicProposalID(p, input, c)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.records[key(p)]
	if b == nil {
		b = map[learning.ProposalID]learning.ProposalRecord{}
	}
	missing := 0
	for _, id := range ids {
		if _, ok := b[id]; !ok {
			missing++
		}
	}
	if len(b)+missing > learning.MaxProposalsPerPartition {
		return nil, learning.ErrProposalLimit
	}
	now := s.now().UTC()
	out := make([]learning.ProposalRecord, len(cs))
	for i, c := range cs {
		r, ok := b[ids[i]]
		if !ok {
			r = learning.ProposalRecord{ID: ids[i], Version: ver(1), Status: learning.ProposalStaged, Partition: p, InputDigest: input, Candidate: c, Signals: signals, CreatedAt: now, UpdatedAt: now}
			b[ids[i]] = clone(r)
		}
		out[i] = clone(r)
	}
	s.records[key(p)] = b
	return out, nil
}
func (s *Store) List(ctx context.Context, p learning.ProposalPartition, o learning.ProposalList) (learning.ProposalPage, error) {
	if err := ctx.Err(); err != nil {
		return learning.ProposalPage{}, err
	}
	n := o.Limit
	if n <= 0 {
		n = learning.DefaultProposalPageSize
	}
	if n > learning.MaxProposalPageSize {
		return learning.ProposalPage{}, learning.ErrProposalLimit
	}
	if o.Status != "" && !o.Status.Valid() {
		return learning.ProposalPage{}, learning.ErrInvalidProposal
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	b := s.records[key(p)]
	ids := []learning.ProposalID{}
	for id, r := range b {
		if id > o.After && (o.Status == "" || r.Status == o.Status) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var page learning.ProposalPage
	for i, id := range ids {
		if i == n {
			page.Next = page.Records[len(page.Records)-1].ID
			break
		}
		page.Records = append(page.Records, clone(b[id]))
	}
	return page, nil
}
func (s *Store) Get(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID) (learning.ProposalRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return learning.ProposalRecord{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[key(p)][id]
	return clone(r), ok, nil
}
func decisions(ds []learning.Decision, d learning.Decision) []learning.Decision {
	ds = append(ds, d)
	if len(ds) > learning.MaxProposalDecisions {
		ds = append([]learning.Decision(nil), ds[len(ds)-learning.MaxProposalDecisions:]...)
	}
	return ds
}
func (s *Store) update(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, v learning.ProposalVersion, fn func(*learning.ProposalRecord) error) (learning.ProposalRecord, error) {
	if err := ctx.Err(); err != nil {
		return learning.ProposalRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.records[key(p)]
	r, ok := b[id]
	if !ok {
		return r, learning.ErrProposalNotFound
	}
	if r.Version != v {
		return r, learning.ErrProposalVersionConflict
	}
	if err := fn(&r); err != nil {
		return learning.ProposalRecord{}, err
	}
	r.Version = ver(parse(r.Version) + 1)
	r.UpdatedAt = s.now().UTC()
	b[id] = clone(r)
	return clone(r), nil
}
func (s *Store) ClaimDecision(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, v learning.ProposalVersion, d learning.Decision) (learning.ProposalRecord, error) {
	if err := learning.ValidateDecision(d); err != nil {
		return learning.ProposalRecord{}, err
	}
	return s.update(ctx, p, id, v, func(r *learning.ProposalRecord) error {
		if r.Status != learning.ProposalStaged {
			return learning.ErrProposalTransition
		}
		if d.At.IsZero() {
			d.At = s.now().UTC()
		}
		r.Decisions = decisions(r.Decisions, d)
		if d.Kind == learning.DecisionReject {
			r.Status = learning.ProposalRejected
		}
		return nil
	})
}
func (s *Store) ClaimPromotion(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, v learning.ProposalVersion) (learning.ProposalRecord, error) {
	return s.update(ctx, p, id, v, func(r *learning.ProposalRecord) error {
		if r.Status != learning.ProposalStaged {
			return learning.ErrProposalTransition
		}
		r.Status = learning.ProposalPromoting
		return nil
	})
}

func finalizeRetryMatches(r learning.ProposalRecord, status learning.ProposalStatus, receipt *learning.PromotionReceipt, decision learning.Decision) bool {
	if r.Status != status || (r.Receipt == nil) != (receipt == nil) {
		return false
	}
	if receipt != nil && *r.Receipt != *receipt {
		return false
	}
	if decision.Kind == "" {
		return len(r.Decisions) == 0
	}
	if len(r.Decisions) == 0 {
		return false
	}
	last := r.Decisions[len(r.Decisions)-1]
	return last.Kind == decision.Kind && last.Actor == decision.Actor && last.Reason == decision.Reason
}

func (s *Store) finalizeUpdate(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, v learning.ProposalVersion, status learning.ProposalStatus, receipt *learning.PromotionReceipt, d learning.Decision, fn func(*learning.ProposalRecord) error) (learning.ProposalRecord, error) {
	if err := ctx.Err(); err != nil {
		return learning.ProposalRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.records[key(p)]
	r, ok := b[id]
	if !ok {
		return r, learning.ErrProposalNotFound
	}
	if finalizeRetryMatches(r, status, receipt, d) {
		return clone(r), nil
	}
	if r.Version != v {
		return r, learning.ErrProposalVersionConflict
	}
	if err := fn(&r); err != nil {
		return learning.ProposalRecord{}, err
	}
	r.Version = ver(parse(r.Version) + 1)
	r.UpdatedAt = s.now().UTC()
	b[id] = clone(r)
	return clone(r), nil
}

func (s *Store) LinkSkillDraft(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, v learning.ProposalVersion, skillID learning.SkillID, d learning.Decision) (learning.ProposalRecord, error) {
	if skillID == "" {
		return learning.ProposalRecord{}, fmt.Errorf("%w: skill id", learning.ErrInvalidProposal)
	}
	if err := learning.ValidateDecision(d); err != nil {
		return learning.ProposalRecord{}, err
	}
	return s.update(ctx, p, id, v, func(r *learning.ProposalRecord) error {
		if r.Status != learning.ProposalDeferredUnsupported || r.Candidate.Kind != learning.CandidateProcedure || r.SkillID != "" {
			return learning.ErrProposalTransition
		}
		if d.At.IsZero() {
			d.At = s.now().UTC()
		}
		r.SkillID = skillID
		r.Status = learning.ProposalSkillMaterialized
		r.Decisions = decisions(r.Decisions, d)
		return nil
	})
}

//nolint:gocyclo
func (s *Store) Finalize(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, v learning.ProposalVersion, status learning.ProposalStatus, receipt *learning.PromotionReceipt, d learning.Decision) (learning.ProposalRecord, error) {
	if status != learning.ProposalPromoted && status != learning.ProposalRejected && status != learning.ProposalDeferredUnsupported && status != learning.ProposalConflicted && status != learning.ProposalUndone {
		return learning.ProposalRecord{}, learning.ErrProposalTransition
	}
	if d.Kind != "" {
		if err := learning.ValidateDecision(d); err != nil {
			return learning.ProposalRecord{}, err
		}
	}
	return s.finalizeUpdate(ctx, p, id, v, status, receipt, d, func(r *learning.ProposalRecord) error {
		validPromotedTransition := r.Status == learning.ProposalPromoted && (status == learning.ProposalUndone || status == learning.ProposalConflicted)
		if r.Status != learning.ProposalPromoting && !validPromotedTransition {
			return learning.ErrProposalTransition
		}
		if status == learning.ProposalPromoted && (receipt == nil || receipt.MemoryKey == "" || receipt.ResultVersion == "") || status == learning.ProposalUndone && receipt == nil {
			return fmt.Errorf("%w: receipt", learning.ErrInvalidProposal)
		}
		r.Status = status
		if receipt != nil {
			x := *receipt
			r.Receipt = &x
		}
		if d.Kind != "" {
			if d.At.IsZero() {
				d.At = s.now().UTC()
			}
			r.Decisions = decisions(r.Decisions, d)
		}
		return nil
	})
}
