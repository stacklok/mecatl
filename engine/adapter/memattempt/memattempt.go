// Package memattempt provides the concurrency-safe in-memory reference
// implementation of learning.AttemptRepository.
package memattempt

//revive:disable:exported // methods implement the documented AttemptRepository contract

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
)

// Store is an in-memory AttemptRepository. All lifecycle mutations are
// serialized so version and claim checks are atomic.
type Store struct {
	mu              sync.RWMutex
	records         map[learning.AttemptPartition]map[learning.AttemptID]learning.AttemptRecord
	claimGeneration map[learning.AttemptPartition]map[learning.AttemptID]learning.ClaimGeneration
	clock           port.Clock
	nextVersion     uint64
}

var _ learning.AttemptRepository = (*Store)(nil)

// New constructs an isolated repository using clock for creation timestamps.
func New(clock port.Clock) *Store {
	if clock == nil {
		panic("memattempt: nil clock")
	}
	return &Store{
		records:         make(map[learning.AttemptPartition]map[learning.AttemptID]learning.AttemptRecord),
		claimGeneration: make(map[learning.AttemptPartition]map[learning.AttemptID]learning.ClaimGeneration),
		clock:           clock,
	}
}

func (s *Store) Create(ctx context.Context, partition learning.AttemptPartition, create learning.AttemptCreate) (learning.AttemptRecord, error) {
	if err := ctx.Err(); err != nil {
		return learning.AttemptRecord{}, err
	}
	if partition == "" || create.Validate() != nil {
		return learning.AttemptRecord{}, learning.ErrInvalidAttempt
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return learning.AttemptRecord{}, learning.ErrInvalidAttempt
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	bucket := s.records[partition]
	if existing, ok := bucket[create.ID]; ok {
		if existing.Provenance == create.Provenance {
			return existing, nil
		}
		return learning.AttemptRecord{}, learning.ErrAttemptCreateConflict
	}
	if err := s.makeRoomLocked(partition); err != nil {
		return learning.AttemptRecord{}, err
	}
	bucket = s.records[partition]
	if bucket == nil {
		bucket = make(map[learning.AttemptID]learning.AttemptRecord)
		s.records[partition] = bucket
	}
	record := learning.AttemptRecord{
		ID:                create.ID,
		Version:           s.versionLocked(),
		State:             learning.AttemptQueued,
		Provenance:        create.Provenance,
		AttemptGeneration: 1,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	bucket[record.ID] = record
	return record, nil
}

func (s *Store) Get(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID) (learning.AttemptRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return learning.AttemptRecord{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, found := s.records[partition][id]
	return record, found, nil
}

func (s *Store) List(ctx context.Context, partition learning.AttemptPartition, query learning.AttemptList) (learning.AttemptPage, error) {
	if err := ctx.Err(); err != nil {
		return learning.AttemptPage{}, err
	}
	if partition == "" || query.Validate() != nil {
		return learning.AttemptPage{}, learning.ErrInvalidAttempt
	}
	limit := query.Limit
	if limit == 0 {
		limit = learning.DefaultAttemptPageSize
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]learning.AttemptID, 0, len(s.records[partition]))
	for id, record := range s.records[partition] {
		if id > query.After && (query.State == "" || record.State == query.State) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	page := learning.AttemptPage{Records: make([]learning.AttemptRecord, 0, min(limit, len(ids)))}
	for _, id := range ids[:min(limit, len(ids))] {
		page.Records = append(page.Records, s.records[partition][id])
	}
	if len(ids) > limit {
		page.Next = page.Records[len(page.Records)-1].ID
	}
	return page, nil
}

func (s *Store) DiscoverWork(ctx context.Context, query learning.AttemptWorkList) (learning.AttemptWorkPage, error) {
	if err := ctx.Err(); err != nil {
		return learning.AttemptWorkPage{}, err
	}
	if query.Validate() != nil {
		return learning.AttemptWorkPage{}, learning.ErrInvalidAttempt
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	work := make([]learning.AttemptWork, 0, query.Limit+1)
	for partition, records := range s.records {
		for _, record := range records {
			after := partition > query.After.Partition || (partition == query.After.Partition && record.ID > query.After.ID)
			if after && (record.State == learning.AttemptQueued || (record.State == learning.AttemptRunning && !record.ClaimExpiresAt.After(query.Now))) {
				work = append(work, learning.AttemptWork{Partition: partition, Record: record})
			}
		}
	}
	sort.Slice(work, func(i, j int) bool {
		if work[i].Partition == work[j].Partition {
			return work[i].Record.ID < work[j].Record.ID
		}
		return work[i].Partition < work[j].Partition
	})
	page := learning.AttemptWorkPage{Work: work[:min(query.Limit, len(work))]}
	if len(work) > query.Limit {
		last := page.Work[len(page.Work)-1]
		page.Next = learning.AttemptWorkCursor{Partition: last.Partition, ID: last.Record.ID}
	}
	return page, nil
}

func (s *Store) AcquireClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, now, expiresAt time.Time) (learning.AttemptRecord, learning.AttemptClaim, error) {
	if err := ctx.Err(); err != nil {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, err
	}
	if !validClaimWindow(now, expiresAt) {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrInvalidAttempt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.currentLocked(partition, id, expected)
	if err != nil {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, err
	}
	if record.State == learning.AttemptRunning && now.Before(record.ClaimExpiresAt) {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrAttemptClaimConflict
	}
	if record.State != learning.AttemptQueued && record.State != learning.AttemptRunning {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrAttemptTransition
	}
	if now.Before(record.CreatedAt) {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrInvalidAttempt
	}
	generation := s.nextClaimGenerationLocked(partition, id)
	record.State = learning.AttemptRunning
	record.ClaimGeneration = generation
	record.ClaimExpiresAt = expiresAt.UTC()
	s.commitLocked(partition, &record, now)
	claim := learning.AttemptClaim{Generation: generation, ExpiresAt: record.ClaimExpiresAt}
	return record, claim, nil
}

func (s *Store) RenewClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, now, expiresAt time.Time) (learning.AttemptRecord, learning.AttemptClaim, error) {
	if err := ctx.Err(); err != nil {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, err
	}
	if !validClaimWindow(now, expiresAt) {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrInvalidAttempt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.currentLocked(partition, id, expected)
	if err != nil {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, err
	}
	if !matchesClaim(record, claim, now) {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrAttemptClaimLost
	}
	record.ClaimExpiresAt = expiresAt.UTC()
	s.commitLocked(partition, &record, now)
	return record, learning.AttemptClaim{Generation: claim.Generation, ExpiresAt: record.ClaimExpiresAt}, nil
}

func (s *Store) Checkpoint(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, now time.Time, checkpoint learning.AttemptCheckpoint) (learning.AttemptRecord, error) {
	if err := ctx.Err(); err != nil {
		return learning.AttemptRecord{}, err
	}
	if now.IsZero() || checkpoint.Validate() != nil {
		return learning.AttemptRecord{}, learning.ErrInvalidAttempt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.currentLocked(partition, id, expected)
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	if !matchesClaim(record, claim, now) {
		return learning.AttemptRecord{}, learning.ErrAttemptClaimLost
	}
	if !record.CheckpointStage.CanAdvanceTo(checkpoint.Stage) {
		return learning.AttemptRecord{}, learning.ErrAttemptTransition
	}
	if record.CheckpointStage == checkpoint.Stage {
		if record.ProposalID != checkpoint.ProposalID || record.SkillID != checkpoint.SkillID {
			return learning.AttemptRecord{}, learning.ErrAttemptTransition
		}
		return record, nil
	}
	record.CheckpointStage = checkpoint.Stage
	record.ProposalID = checkpoint.ProposalID
	record.SkillID = checkpoint.SkillID
	s.commitLocked(partition, &record, now)
	return record, nil
}

func (s *Store) ReleaseClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, now time.Time) (learning.AttemptRecord, error) {
	if err := ctx.Err(); err != nil {
		return learning.AttemptRecord{}, err
	}
	if now.IsZero() {
		return learning.AttemptRecord{}, learning.ErrInvalidAttempt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.currentLocked(partition, id, expected)
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	if !matchesClaim(record, claim, now) {
		return learning.AttemptRecord{}, learning.ErrAttemptClaimLost
	}
	record.State = learning.AttemptQueued
	clearClaim(&record)
	s.commitLocked(partition, &record, now)
	return record, nil
}

func (s *Store) Finalize(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, now time.Time, final learning.AttemptFinalization) (learning.AttemptRecord, error) {
	if err := ctx.Err(); err != nil {
		return learning.AttemptRecord{}, err
	}
	if now.IsZero() {
		return learning.AttemptRecord{}, learning.ErrInvalidAttempt
	}
	if err := final.Validate(); err != nil {
		return learning.AttemptRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.currentLocked(partition, id, expected)
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	if !matchesClaim(record, claim, now) {
		return learning.AttemptRecord{}, learning.ErrAttemptClaimLost
	}
	if !learning.ValidAttemptTransition(record.State, final.State) {
		return learning.AttemptRecord{}, learning.ErrAttemptTransition
	}
	record.State = final.State
	record.Outcome = final.Outcome
	record.FailureCode = final.FailureCode
	clearClaim(&record)
	s.commitLocked(partition, &record, now)
	return record, nil
}

func (s *Store) Retry(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, now time.Time) (learning.AttemptRecord, error) {
	if err := ctx.Err(); err != nil {
		return learning.AttemptRecord{}, err
	}
	if now.IsZero() {
		return learning.AttemptRecord{}, learning.ErrInvalidAttempt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.currentLocked(partition, id, expected)
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	if !learning.ValidAttemptTransition(record.State, learning.AttemptQueued) {
		return learning.AttemptRecord{}, learning.ErrAttemptTransition
	}
	record.State = learning.AttemptQueued
	record.Outcome = learning.AttemptOutcomeNone
	record.FailureCode = learning.FailureNone
	record.AttemptGeneration++
	clearClaim(&record)
	s.commitLocked(partition, &record, now)
	return record, nil
}

func (s *Store) Abandon(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, now time.Time) (learning.AttemptRecord, error) {
	if err := ctx.Err(); err != nil {
		return learning.AttemptRecord{}, err
	}
	if now.IsZero() {
		return learning.AttemptRecord{}, learning.ErrInvalidAttempt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.currentLocked(partition, id, expected)
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	if record.State == learning.AttemptRunning && now.Before(record.ClaimExpiresAt) {
		return learning.AttemptRecord{}, learning.ErrAttemptClaimConflict
	}
	if !learning.ValidAttemptTransition(record.State, learning.AttemptAbandoned) {
		return learning.AttemptRecord{}, learning.ErrAttemptTransition
	}
	record.State = learning.AttemptAbandoned
	record.Outcome = learning.AttemptOutcomeAbandoned
	record.FailureCode = learning.FailureNone
	clearClaim(&record)
	s.commitLocked(partition, &record, now)
	return record, nil
}

func (s *Store) Delete(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if now.IsZero() {
		return learning.ErrInvalidAttempt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.currentLocked(partition, id, expected)
	if err != nil {
		return err
	}
	if record.State == learning.AttemptRunning {
		return learning.ErrAttemptClaimConflict
	}
	if record.State != learning.AttemptQueued && !record.State.Terminal() {
		return learning.ErrAttemptTransition
	}
	delete(s.records[partition], id)
	delete(s.claimGeneration[partition], id)
	return nil
}

func (s *Store) DeleteTerminalBefore(ctx context.Context, partition learning.AttemptPartition, before time.Time, limit int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if partition == "" || before.IsZero() || limit < 1 || limit > learning.MaxAttemptDeleteBatch {
		return 0, learning.ErrInvalidAttempt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	type candidate struct {
		id learning.AttemptID
		at time.Time
	}
	candidates := make([]candidate, 0)
	for id, record := range s.records[partition] {
		if record.State.Terminal() && record.UpdatedAt.Before(before) {
			candidates = append(candidates, candidate{id: id, at: record.UpdatedAt})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].at.Before(candidates[j].at)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for _, candidate := range candidates {
		delete(s.records[partition], candidate.id)
		delete(s.claimGeneration[partition], candidate.id)
	}
	return len(candidates), nil
}

func (s *Store) makeRoomLocked(partition learning.AttemptPartition) error {
	records := s.records[partition]
	if len(records) < learning.MaxAttemptsPerPartition {
		return nil
	}
	var oldest learning.AttemptRecord
	found := false
	for _, record := range records {
		if record.State.Terminal() && (!found || record.UpdatedAt.Before(oldest.UpdatedAt) || (record.UpdatedAt.Equal(oldest.UpdatedAt) && record.ID < oldest.ID)) {
			oldest, found = record, true
		}
	}
	if !found {
		return learning.ErrAttemptQuotaExceeded
	}
	delete(records, oldest.ID)
	delete(s.claimGeneration[partition], oldest.ID)
	return nil
}

func (s *Store) currentLocked(partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion) (learning.AttemptRecord, error) {
	record, found := s.records[partition][id]
	if !found {
		return learning.AttemptRecord{}, learning.ErrAttemptNotFound
	}
	if record.Version != expected {
		return learning.AttemptRecord{}, learning.ErrAttemptVersionConflict
	}
	return record, nil
}

func (s *Store) commitLocked(partition learning.AttemptPartition, record *learning.AttemptRecord, now time.Time) {
	record.Version = s.versionLocked()
	record.UpdatedAt = now.UTC()
	s.records[partition][record.ID] = *record
}

func (s *Store) versionLocked() learning.AttemptVersion {
	s.nextVersion++
	return learning.AttemptVersion("mem-" + strconv.FormatUint(s.nextVersion, 10))
}

func (s *Store) nextClaimGenerationLocked(partition learning.AttemptPartition, id learning.AttemptID) learning.ClaimGeneration {
	generations := s.claimGeneration[partition]
	if generations == nil {
		generations = make(map[learning.AttemptID]learning.ClaimGeneration)
		s.claimGeneration[partition] = generations
	}
	generations[id]++
	return generations[id]
}

func validClaimWindow(now, expiresAt time.Time) bool {
	return !now.IsZero() && expiresAt.After(now)
}

func matchesClaim(record learning.AttemptRecord, claim learning.AttemptClaim, now time.Time) bool {
	return record.State == learning.AttemptRunning && claim.ValidAt(now) &&
		record.ClaimGeneration == claim.Generation && record.ClaimExpiresAt.Equal(claim.ExpiresAt)
}

func clearClaim(record *learning.AttemptRecord) {
	record.ClaimGeneration = 0
	record.ClaimExpiresAt = time.Time{}
}
