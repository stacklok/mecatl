// Package memorypromotion provides conservative proposal-to-memory convergence.
package memorypromotion

//revive:disable:exported // exported policy vocabulary is documented at the package and ADR boundary

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/tool"
)

type PolicyDisposition string

const (
	DispositionPromote     PolicyDisposition = "promote"
	DispositionDuplicate   PolicyDisposition = "duplicate"
	DispositionReview      PolicyDisposition = "review"
	DispositionReject      PolicyDisposition = "reject"
	DispositionConflict    PolicyDisposition = "conflict"
	DispositionUnsupported PolicyDisposition = "unsupported"
)

type PolicyInput struct {
	Mode           learning.Mode
	TrustedProject bool
	Approved       bool
	Candidate      learning.Candidate
	Current        *tool.MemoryRevision
	Existing       []tool.MemoryEntry
}
type PolicyResult struct {
	Disposition PolicyDisposition
	Reason      string
}
type Policy interface {
	Evaluate(PolicyInput) PolicyResult
}
type StandardPolicy struct{}

func canonicalEqual(a, b string) bool {
	return strings.TrimSpace(tool.CanonicalMemoryText(a)) == strings.TrimSpace(tool.CanonicalMemoryText(b))
}

//nolint:gocyclo // conservative policy keeps every disposition visible in one closed decision table
func (StandardPolicy) Evaluate(in PolicyInput) PolicyResult {
	c := in.Candidate
	if c.Kind == learning.CandidateProcedure {
		return PolicyResult{DispositionUnsupported, "procedure promotion is deferred"}
	}
	if c.Kind != learning.CandidateOperatorFact && c.Kind != learning.CandidateProjectFact {
		return PolicyResult{DispositionReject, "unsupported candidate kind"}
	}
	e := tool.MemoryEntry{Key: c.Key, Value: c.Value, Description: c.Description}
	attr := tool.MemoryAttribution{Writer: tool.MemoryWriterModel, Origin: tool.MemoryOriginLearning}
	if tool.CanonicalMemoryText(c.Key) != c.Key || tool.CanonicalMemoryText(c.Value) != c.Value || tool.CanonicalMemoryText(c.Description) != c.Description || tool.ValidateMemoryEntryWrite(e, attr) != nil {
		return PolicyResult{DispositionReject, "candidate failed durable memory validation"}
	}
	if in.Current != nil && in.Current.Status == tool.MemoryStatusActive {
		if canonicalEqual(in.Current.Value, c.Value) && canonicalEqual(in.Current.Description, c.Description) {
			return PolicyResult{DispositionDuplicate, "exact current duplicate"}
		}
		if in.Current.Writer == tool.MemoryWriterUser || in.Current.Origin == tool.MemoryOriginExplicit {
			return PolicyResult{DispositionConflict, "current fact is user-explicit"}
		}
		return PolicyResult{DispositionConflict, "current key has different content"}
	}
	for _, x := range in.Existing {
		if x.Key != c.Key && canonicalEqual(x.Value, c.Value) {
			return PolicyResult{DispositionReview, "possible semantic duplicate under another key"}
		}
	}
	if in.Approved {
		return PolicyResult{DispositionPromote, "approved for promotion"}
	}
	if in.Mode != learning.Auto || c.Kind == learning.CandidateProjectFact && !in.TrustedProject {
		return PolicyResult{DispositionReview, "host posture requires review"}
	}
	return PolicyResult{DispositionPromote, "eligible under automatic host posture"}
}

type Promoter struct {
	Proposals learning.ProposalRepository
	Memory    tool.MemoryStore
	Policy    Policy
}

func receipt(key string, prev tool.MemoryRecord, result tool.MemoryVersion) learning.PromotionReceipt {
	r := learning.PromotionReceipt{MemoryKey: key, ResultVersion: string(result)}
	if prev.Current.Version != "" {
		r.PreviousExists = true
		r.PreviousVersion = string(prev.Current.Version)
	}
	return r
}

//nolint:gocyclo // claim, policy, mutation, and finalization remain visibly ordered
func (p Promoter) Process(ctx context.Context, part learning.ProposalPartition, id learning.ProposalID, expected learning.ProposalVersion, input PolicyInput) (learning.ProposalRecord, error) {
	if p.Proposals == nil || p.Memory == nil {
		return learning.ProposalRecord{}, errors.New("memorypromotion: proposals and memory are required")
	}
	r, found, err := p.Proposals.Get(ctx, part, id)
	if err != nil {
		return r, err
	}
	if !found {
		return r, learning.ErrProposalNotFound
	}
	if r.Version != expected {
		return r, learning.ErrProposalVersionConflict
	}
	if r.Status == learning.ProposalPromoting {
		return p.reconcile(ctx, r)
	}
	if r.Status != learning.ProposalStaged {
		return r, learning.ErrProposalTransition
	}
	life, ok := p.Memory.(tool.MemoryConvergenceStore)
	if !ok {
		return r, errors.New("memorypromotion: target lacks atomic convergence lifecycle")
	}
	mr, mfound, err := life.Inspect(ctx, r.Candidate.Key)
	if err != nil {
		return r, err
	}
	input.Candidate = r.Candidate
	if mfound {
		x := mr.Current
		input.Current = &x
	}
	input.Existing, err = p.Memory.List(ctx, "")
	if err != nil {
		return r, err
	}
	policy := p.Policy
	if policy == nil {
		policy = StandardPolicy{}
	}
	result := policy.Evaluate(input)
	decision := learning.Decision{Kind: learning.DecisionDefer, Actor: "standard-policy", Reason: result.Reason}
	if result.Disposition == DispositionReview {
		return p.Proposals.ClaimDecision(ctx, part, id, expected, decision)
	}
	if result.Disposition == DispositionReject {
		decision.Kind = learning.DecisionReject
		return p.Proposals.ClaimDecision(ctx, part, id, expected, decision)
	}
	claimed, err := p.Proposals.ClaimPromotion(ctx, part, id, expected)
	if err != nil {
		return r, err
	}
	switch result.Disposition {
	case DispositionUnsupported:
		return p.Proposals.Finalize(ctx, part, id, claimed.Version, learning.ProposalDeferredUnsupported, nil, decision)
	case DispositionConflict:
		return p.Proposals.Finalize(ctx, part, id, claimed.Version, learning.ProposalConflicted, nil, decision)
	case DispositionDuplicate:
		rec := receipt(r.Candidate.Key, mr, mr.Current.Version)
		return p.Proposals.Finalize(ctx, part, id, claimed.Version, learning.ProposalPromoted, &rec, decision)
	case DispositionPromote:
	default:
		return r, errors.New("memorypromotion: unknown policy result")
	}
	cur := tool.MemoryCurrent{Exists: mfound}
	if mfound {
		cur.Version = mr.Current.Version
	}
	wctx := tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Writer: tool.MemoryWriterModel, Origin: tool.MemoryOriginLearning, Source: tool.MemorySource{SessionID: string(r.Candidate.Evidence[0].SessionID), ProposalID: string(id)}})
	written, err := life.RememberIfCurrent(wctx, tool.MemoryEntry{Key: r.Candidate.Key, Value: r.Candidate.Value, Description: r.Candidate.Description}, cur)
	if err != nil {
		var conflict *tool.MemoryVersionConflictError
		if errors.As(err, &conflict) {
			return p.Proposals.Finalize(ctx, part, id, claimed.Version, learning.ProposalConflicted, nil, learning.Decision{Kind: learning.DecisionDefer, Actor: "convergence-cas", Reason: "memory changed during promotion"})
		}
		return r, err
	}
	rec := receipt(r.Candidate.Key, mr, written.Current.Version)
	return p.Proposals.Finalize(ctx, part, id, claimed.Version, learning.ProposalPromoted, &rec, learning.Decision{Kind: learning.DecisionApprove, Actor: "standard-policy", Reason: result.Reason})
}
func (p Promoter) reconcile(ctx context.Context, r learning.ProposalRecord) (learning.ProposalRecord, error) {
	life, ok := p.Memory.(tool.MemoryLifecycleStore)
	if !ok {
		return r, errors.New("memorypromotion: lifecycle required")
	}
	mr, found, err := life.Inspect(ctx, r.Candidate.Key)
	if err != nil {
		return r, err
	}
	if found && mr.Current.Source.ProposalID == string(r.ID) {
		rec := receipt(r.Candidate.Key, tool.MemoryRecord{}, mr.Current.Version)
		return p.Proposals.Finalize(ctx, r.Partition, r.ID, r.Version, learning.ProposalPromoted, &rec, learning.Decision{Kind: learning.DecisionApprove, Actor: "reconciler", Reason: "linked memory revision already committed"})
	}
	return p.Proposals.Finalize(ctx, r.Partition, r.ID, r.Version, learning.ProposalConflicted, nil, learning.Decision{Kind: learning.DecisionDefer, Actor: "reconciler", Reason: "promotion claim has no current linked revision"})
}
func (p Promoter) Undo(ctx context.Context, part learning.ProposalPartition, id learning.ProposalID, expected learning.ProposalVersion) (learning.ProposalRecord, error) {
	r, found, err := p.Proposals.Get(ctx, part, id)
	if err != nil {
		return r, err
	}
	if !found {
		return r, learning.ErrProposalNotFound
	}
	if r.Version != expected || r.Status != learning.ProposalPromoted || r.Receipt == nil {
		return r, learning.ErrProposalTransition
	}
	life, ok := p.Memory.(tool.MemoryLifecycleStore)
	if !ok {
		return r, errors.New("memorypromotion: lifecycle required")
	}
	mr, found, err := life.Inspect(ctx, r.Receipt.MemoryKey)
	if err != nil {
		return r, err
	}
	if !found || string(mr.Current.Version) != r.Receipt.ResultVersion || mr.Current.Source.ProposalID != string(id) {
		return p.Proposals.Finalize(ctx, part, id, expected, learning.ProposalConflicted, nil, learning.Decision{Kind: learning.DecisionDefer, Actor: "undo", Reason: "promoted revision is no longer current"})
	}
	uctx := tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Writer: tool.MemoryWriterSystem, Origin: tool.MemoryOriginUndo, Source: tool.MemorySource{ProposalID: string(id)}})
	done, err := life.UndoLatest(uctx, r.Receipt.MemoryKey, mr.Current.Version)
	if err != nil {
		return r, fmt.Errorf("memorypromotion: undo: %w", err)
	}
	rec := *r.Receipt
	rec.ResultVersion = string(done.Current.Version)
	return p.Proposals.Finalize(ctx, part, id, expected, learning.ProposalUndone, &rec, learning.Decision{Kind: learning.DecisionApprove, Actor: "undo", Reason: "compensating revision committed"})
}
