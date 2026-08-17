// Package memskill provides the in-memory learned-skill repository reference adapter.
package memskill

//revive:disable:exported // methods implement the learning repository contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

type skillRecord struct {
	owner    string
	name     string
	versions []learning.SkillVersion
}

type Store struct {
	mu      sync.RWMutex
	records map[string]map[learning.SkillID]*skillRecord
	next    uint64
	now     func() time.Time
}

func New() *Store {
	return &Store{records: map[string]map[learning.SkillID]*skillRecord{}, now: time.Now}
}

var _ learning.SkillRepository = (*Store)(nil)

func partitionKey(p learning.SkillPartition) string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func skillID(p learning.SkillPartition, owner, name string) learning.SkillID {
	raw, _ := json.Marshal([]string{p.Principal, p.Project, owner, name})
	sum := sha256.Sum256(raw)
	return learning.SkillID("skill-" + hex.EncodeToString(sum[:16]))
}
func (s *Store) revision() learning.Revision {
	s.next++
	return learning.Revision("mem-" + strconv.FormatUint(s.next, 10))
}
func cloneSignal(x learning.Signal) learning.Signal {
	x.Evidence = append([]learning.EvidenceRef(nil), x.Evidence...)
	return x
}
func clone(v learning.SkillVersion) learning.SkillVersion {
	v.Provenance.ProposalIDs = append([]learning.ProposalID(nil), v.Provenance.ProposalIDs...)
	v.Provenance.EvidenceRefs = append([]learning.EvidenceRef(nil), v.Provenance.EvidenceRefs...)
	v.Provenance.Signals = append([]learning.Signal(nil), v.Provenance.Signals...)
	for i := range v.Provenance.Signals {
		v.Provenance.Signals[i] = cloneSignal(v.Provenance.Signals[i])
	}
	v.Evaluations = append([]learning.SkillEvaluation(nil), v.Evaluations...)
	for i := range v.Evaluations {
		v.Evaluations[i].FixtureIDs = append([]string(nil), v.Evaluations[i].FixtureIDs...)
	}
	v.Receipts = append([]learning.SkillReceipt(nil), v.Receipts...)
	return v
}
func mergeStrings[T ~string](dst, src []T, limit int) ([]T, error) {
	seen := make(map[T]bool, len(dst)+len(src))
	out := append([]T(nil), dst...)
	for _, x := range out {
		seen[x] = true
	}
	for _, x := range src {
		if !seen[x] {
			out = append(out, x)
			seen[x] = true
		}
	}
	if len(out) > limit {
		return nil, learning.ErrSkillLimit
	}
	return out, nil
}
func mergeProvenance(dst, src learning.SkillProvenance) (learning.SkillProvenance, error) {
	if dst.Origin != src.Origin && dst.Origin != "" && src.Origin != "" {
		return dst, learning.ErrInvalidSkill
	}
	if dst.Origin == "" {
		dst.Origin = src.Origin
	}
	if dst.ValidationDisposition == learning.ValidationSimilarStageHint || src.ValidationDisposition == learning.ValidationSimilarStageHint {
		dst.ValidationDisposition = learning.ValidationSimilarStageHint
	} else if dst.ValidationDisposition == "" {
		dst.ValidationDisposition = src.ValidationDisposition
	}
	var err error
	dst.ProposalIDs, err = mergeStrings(dst.ProposalIDs, src.ProposalIDs, learning.MaxSkillProposals)
	if err != nil {
		return dst, err
	}
	seenEvidence := map[string]bool{}
	for _, x := range dst.EvidenceRefs {
		raw, _ := json.Marshal(x)
		seenEvidence[string(raw)] = true
	}
	for _, x := range src.EvidenceRefs {
		raw, _ := json.Marshal(x)
		if !seenEvidence[string(raw)] {
			dst.EvidenceRefs = append(dst.EvidenceRefs, x)
			seenEvidence[string(raw)] = true
		}
	}
	if len(dst.EvidenceRefs) > learning.MaxSkillEvidence {
		return dst, learning.ErrSkillLimit
	}
	seenSignals := map[string]bool{}
	for _, x := range dst.Signals {
		raw, _ := json.Marshal(x)
		seenSignals[string(raw)] = true
	}
	for _, x := range src.Signals {
		raw, _ := json.Marshal(x)
		if !seenSignals[string(raw)] {
			dst.Signals = append(dst.Signals, cloneSignal(x))
			seenSignals[string(raw)] = true
		}
	}
	if len(dst.Signals) > learning.MaxSkillSignals {
		return dst, learning.ErrSkillLimit
	}
	return dst, nil
}

func (s *Store) CreateDraft(ctx context.Context, p learning.SkillPartition, owner string, bundle learning.SkillBundle, provenance learning.SkillProvenance) (learning.SkillVersion, error) {
	if err := ctx.Err(); err != nil {
		return learning.SkillVersion{}, err
	}
	if err := learning.ValidateSkillPartition(p, owner); err != nil {
		return learning.SkillVersion{}, err
	}
	if err := learning.ValidateSkillBundle(bundle); err != nil {
		return learning.SkillVersion{}, err
	}
	if err := learning.ValidateSkillProvenance(provenance); err != nil {
		return learning.SkillVersion{}, err
	}
	versionID, _ := learning.SkillVersionID(bundle)
	s.mu.Lock()
	defer s.mu.Unlock()
	pk := partitionKey(p)
	bucket := s.records[pk]
	if bucket == nil {
		bucket = map[learning.SkillID]*skillRecord{}
		s.records[pk] = bucket
	}
	for _, record := range bucket {
		if record.name != bundle.Name {
			continue
		}
		if record.owner != owner {
			return learning.SkillVersion{}, learning.ErrSkillOwnerMismatch
		}
		for i := range record.versions {
			if record.versions[i].Version != versionID {
				continue
			}
			merged, err := mergeProvenance(record.versions[i].Provenance, provenance)
			if err != nil {
				return learning.SkillVersion{}, err
			}
			if rawA, _ := json.Marshal(merged); string(rawA) != mustJSON(record.versions[i].Provenance) {
				record.versions[i].Provenance = merged
				record.versions[i].Disposition = merged.ValidationDisposition
				record.versions[i].Revision = s.revision()
				record.versions[i].UpdatedAt = s.now().UTC()
			}
			return clone(record.versions[i]), nil
		}
		if len(record.versions) >= learning.MaxSkillVersionsPerSkill {
			return learning.SkillVersion{}, learning.ErrSkillLimit
		}
		now := s.now().UTC()
		previous := record.versions[len(record.versions)-1]
		v := learning.SkillVersion{ID: previous.ID, Version: versionID, Revision: s.revision(), State: learning.SkillDraft, OwnerAgent: owner, Partition: p, Bundle: bundle, Provenance: provenance, Disposition: provenance.ValidationDisposition, Supersedes: previous.Version, CreatedAt: now, UpdatedAt: now}
		record.versions = append(record.versions, clone(v))
		return clone(v), nil
	}
	if len(bucket) >= learning.MaxSkillsPerPartition {
		return learning.SkillVersion{}, learning.ErrSkillLimit
	}
	now := s.now().UTC()
	id := skillID(p, owner, bundle.Name)
	v := learning.SkillVersion{ID: id, Version: versionID, Revision: s.revision(), State: learning.SkillDraft, OwnerAgent: owner, Partition: p, Bundle: bundle, Provenance: provenance, Disposition: provenance.ValidationDisposition, CreatedAt: now, UpdatedAt: now}
	bucket[id] = &skillRecord{owner: owner, name: bundle.Name, versions: []learning.SkillVersion{clone(v)}}
	return clone(v), nil
}
func mustJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }

func locate(bucket map[learning.SkillID]*skillRecord, owner string, id learning.SkillID, version learning.VersionID) (*skillRecord, int, error) {
	r := bucket[id]
	if r == nil {
		return nil, 0, learning.ErrSkillNotFound
	}
	if r.owner != owner {
		return nil, 0, learning.ErrSkillOwnerMismatch
	}
	if version == "" {
		return r, len(r.versions) - 1, nil
	}
	for i := range r.versions {
		if r.versions[i].Version == version {
			return r, i, nil
		}
	}
	return nil, 0, learning.ErrSkillNotFound
}
func (s *Store) Get(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID) (learning.SkillVersion, bool, error) {
	if err := ctx.Err(); err != nil {
		return learning.SkillVersion{}, false, err
	}
	if err := learning.ValidateSkillPartition(p, owner); err != nil {
		return learning.SkillVersion{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, i, err := locate(s.records[partitionKey(p)], owner, id, version)
	if err == learning.ErrSkillNotFound {
		return learning.SkillVersion{}, false, nil
	}
	if err != nil {
		return learning.SkillVersion{}, false, err
	}
	return clone(r.versions[i]), true, nil
}
func (s *Store) List(ctx context.Context, p learning.SkillPartition, o learning.SkillList) (learning.SkillPage, error) {
	if err := ctx.Err(); err != nil {
		return learning.SkillPage{}, err
	}
	if o.Limit <= 0 {
		o.Limit = learning.DefaultSkillPageSize
	}
	if o.Limit > learning.MaxSkillPageSize {
		return learning.SkillPage{}, learning.ErrSkillLimit
	}
	if o.State != "" && !o.State.Valid() {
		return learning.SkillPage{}, learning.ErrInvalidSkill
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket := s.records[partitionKey(p)]
	ids := make([]learning.SkillID, 0, len(bucket))
	selected := make(map[learning.SkillID]learning.SkillVersion, len(bucket))
	for id, r := range bucket {
		value := r.versions[len(r.versions)-1]
		if o.State != "" {
			found := false
			for i := len(r.versions) - 1; i >= 0; i-- {
				if r.versions[i].State == o.State {
					value, found = r.versions[i], true
					break
				}
			}
			if !found {
				continue
			}
		}
		if id > o.After && (o.Name == "" || r.name == o.Name) && (o.OwnerAgent == "" || r.owner == o.OwnerAgent) {
			ids = append(ids, id)
			selected[id] = value
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var page learning.SkillPage
	for i, id := range ids {
		if i == o.Limit {
			page.Next = page.Versions[len(page.Versions)-1].ID
			break
		}
		page.Versions = append(page.Versions, clone(selected[id]))
	}
	return page, nil
}
func appendReceipt(v *learning.SkillVersion, operation string, from, to learning.SkillState, now time.Time) {
	v.Receipts = append(v.Receipts, learning.SkillReceipt{Operation: operation, From: from, To: to, Version: v.Version, At: now})
	if len(v.Receipts) > learning.MaxSkillReceipts {
		v.Receipts = append([]learning.SkillReceipt(nil), v.Receipts[len(v.Receipts)-learning.MaxSkillReceipts:]...)
	}
}
func (s *Store) update(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision, fn func(*skillRecord, int, time.Time) error) (learning.SkillVersion, error) {
	if err := ctx.Err(); err != nil {
		return learning.SkillVersion{}, err
	}
	if err := learning.ValidateSkillPartition(p, owner); err != nil {
		return learning.SkillVersion{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, i, err := locate(s.records[partitionKey(p)], owner, id, version)
	if err != nil {
		return learning.SkillVersion{}, err
	}
	if r.versions[i].Revision != expected {
		return learning.SkillVersion{}, learning.ErrSkillConflict
	}
	now := s.now().UTC()
	if err = fn(r, i, now); err != nil {
		return learning.SkillVersion{}, err
	}
	r.versions[i].Revision = s.revision()
	r.versions[i].UpdatedAt = now
	return clone(r.versions[i]), nil
}
func (s *Store) RecordEvaluation(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision, evaluation learning.SkillEvaluation) (learning.SkillVersion, error) {
	if err := learning.ValidateSkillEvaluation(evaluation); err != nil {
		return learning.SkillVersion{}, err
	}
	return s.update(ctx, p, owner, id, version, expected, func(r *skillRecord, i int, now time.Time) error {
		v := &r.versions[i]
		if v.State != learning.SkillDraft && v.State != learning.SkillEvaluated {
			return learning.ErrSkillTransition
		}
		if len(v.Evaluations) >= learning.MaxSkillEvaluations {
			return learning.ErrSkillLimit
		}
		if evaluation.At.IsZero() {
			evaluation.At = now
		}
		from := v.State
		v.Evaluations = append(v.Evaluations, evaluation)
		if evaluation.Verdict == learning.EvaluationFail || evaluation.Verdict == learning.EvaluationError {
			v.State = learning.SkillRejected
		} else {
			v.State = learning.SkillEvaluated
		}
		appendReceipt(v, "record_evaluation", from, v.State, now)
		return nil
	})
}
func (s *Store) Stage(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision) (learning.SkillVersion, error) {
	return s.update(ctx, p, owner, id, version, expected, func(r *skillRecord, i int, now time.Time) error {
		v := &r.versions[i]
		if v.State != learning.SkillEvaluated || len(v.Evaluations) == 0 || v.Evaluations[len(v.Evaluations)-1].Verdict == learning.EvaluationFail {
			return learning.ErrSkillTransition
		}
		v.State = learning.SkillStaged
		appendReceipt(v, "stage", learning.SkillEvaluated, v.State, now)
		return nil
	})
}
func (s *Store) Activate(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision) (learning.SkillVersion, error) {
	return s.activate(ctx, p, owner, id, version, expected, false)
}

// ActivateValidated activates only an evidence-backed, accepted/exact, staged
// ABSTAIN version and archives the prior active version in the same update.
func (s *Store) ActivateValidated(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision) (learning.SkillVersion, error) {
	return s.activate(ctx, p, owner, id, version, expected, true)
}

func (s *Store) activate(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision, validated bool) (learning.SkillVersion, error) {
	return s.update(ctx, p, owner, id, version, expected, func(r *skillRecord, i int, now time.Time) error {
		v := &r.versions[i]
		if v.State != learning.SkillStaged || len(v.Evaluations) == 0 {
			return learning.ErrSkillTransition
		}
		verdict := v.Evaluations[len(v.Evaluations)-1].Verdict
		if (!validated && verdict != learning.EvaluationPass) || (validated && (verdict != learning.EvaluationAbstain || len(v.Provenance.EvidenceRefs) == 0 || (v.Disposition != learning.ValidationAccept && v.Disposition != learning.ValidationExactDuplicate) || v.Provenance.Origin == learning.SkillProvenanceLegacyModel)) {
			return learning.ErrSkillTransition
		}
		for j := range r.versions {
			if j != i && r.versions[j].State == learning.SkillActive {
				from := r.versions[j].State
				r.versions[j].State = learning.SkillArchived
				r.versions[j].Revision = s.revision()
				r.versions[j].UpdatedAt = now
				appendReceipt(&r.versions[j], "superseded", from, r.versions[j].State, now)
			}
		}
		v.State = learning.SkillActive
		operation := "activate"
		if validated {
			operation = "activate_validated"
		}
		appendReceipt(v, operation, learning.SkillStaged, v.State, now)
		return nil
	})
}
func (s *Store) Reject(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision) (learning.SkillVersion, error) {
	return s.update(ctx, p, owner, id, version, expected, func(r *skillRecord, i int, now time.Time) error {
		v := &r.versions[i]
		if v.State == learning.SkillActive || v.State == learning.SkillArchived || v.State == learning.SkillRejected {
			return learning.ErrSkillTransition
		}
		from := v.State
		v.State = learning.SkillRejected
		appendReceipt(v, "reject", from, v.State, now)
		return nil
	})
}
func (s *Store) Archive(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision) (learning.SkillVersion, error) {
	return s.update(ctx, p, owner, id, version, expected, func(r *skillRecord, i int, now time.Time) error {
		v := &r.versions[i]
		if v.State != learning.SkillActive {
			return learning.ErrSkillTransition
		}
		from := v.State
		v.State = learning.SkillArchived
		appendReceipt(v, "archive", from, v.State, now)
		return nil
	})
}
func (s *Store) Rollback(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, expectedActive learning.Revision, target learning.VersionID) (learning.SkillVersion, error) {
	if err := ctx.Err(); err != nil {
		return learning.SkillVersion{}, err
	}
	if err := learning.ValidateSkillPartition(p, owner); err != nil {
		return learning.SkillVersion{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[partitionKey(p)][id]
	if r == nil {
		return learning.SkillVersion{}, learning.ErrSkillNotFound
	}
	if r.owner != owner {
		return learning.SkillVersion{}, learning.ErrSkillOwnerMismatch
	}
	active, targetIndex := -1, -1
	for i := range r.versions {
		if r.versions[i].State == learning.SkillActive {
			active = i
		}
		if r.versions[i].Version == target {
			targetIndex = i
		}
	}
	if active < 0 || r.versions[active].Revision != expectedActive {
		return learning.SkillVersion{}, learning.ErrSkillConflict
	}
	if targetIndex < 0 {
		return learning.SkillVersion{}, learning.ErrSkillNotFound
	}
	if !rollbackEligible(r.versions[targetIndex]) {
		return learning.SkillVersion{}, learning.ErrSkillTransition
	}
	now := s.now().UTC()
	a := &r.versions[active]
	appendReceipt(a, "rollback_from", a.State, learning.SkillArchived, now)
	a.State = learning.SkillArchived
	a.Revision = s.revision()
	a.UpdatedAt = now
	t := &r.versions[targetIndex]
	appendReceipt(t, "rollback_to", t.State, learning.SkillActive, now)
	t.State = learning.SkillActive
	t.Revision = s.revision()
	t.UpdatedAt = now
	return clone(*t), nil
}

func rollbackEligible(v learning.SkillVersion) bool {
	if v.State != learning.SkillArchived {
		return false
	}
	for _, receipt := range v.Receipts {
		if receipt.To == learning.SkillActive && (receipt.Operation == "activate" || receipt.Operation == "activate_validated" || receipt.Operation == "rollback_to") {
			return true
		}
	}
	return false
}
