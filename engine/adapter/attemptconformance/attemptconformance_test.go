package attemptconformance

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

func TestCloudNativeLearning_Scenario2_RetentionAndDeletionRespectClaimsAndPartition(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	RunRetentionAndDeletion(t, func(t *testing.T) Harness {
		t.Helper()
		repo := newRetentionRepository(now)
		return Harness{Repository: repo, SetNow: repo.setNow}
	})
}

type retentionRepository struct {
	now                time.Time
	nextVersion        uint64
	records            map[learning.AttemptPartition]map[learning.AttemptID]learning.AttemptRecord
	allowClaimedDelete bool
}

func newRetentionRepository(now time.Time) *retentionRepository {
	return &retentionRepository{now: now, records: make(map[learning.AttemptPartition]map[learning.AttemptID]learning.AttemptRecord)}
}

func (r *retentionRepository) setNow(now time.Time) { r.now = now }

func (r *retentionRepository) version() learning.AttemptVersion {
	r.nextVersion++
	return learning.AttemptVersion(fmt.Sprintf("v-%d", r.nextVersion))
}

func (r *retentionRepository) Create(_ context.Context, partition learning.AttemptPartition, create learning.AttemptCreate) (learning.AttemptRecord, error) {
	if records := r.records[partition]; records != nil {
		if record, ok := records[create.ID]; ok {
			if record.Provenance == create.Provenance {
				return record, nil
			}
			return learning.AttemptRecord{}, learning.ErrAttemptCreateConflict
		}
	}
	if r.records[partition] == nil {
		r.records[partition] = make(map[learning.AttemptID]learning.AttemptRecord)
	}
	if len(r.records[partition]) >= learning.MaxAttemptsPerPartition {
		var oldest learning.AttemptRecord
		found := false
		for _, candidate := range r.records[partition] {
			if candidate.State.Terminal() && (!found || candidate.UpdatedAt.Before(oldest.UpdatedAt) || (candidate.UpdatedAt.Equal(oldest.UpdatedAt) && candidate.ID < oldest.ID)) {
				oldest, found = candidate, true
			}
		}
		if !found {
			return learning.AttemptRecord{}, learning.ErrAttemptQuotaExceeded
		}
		delete(r.records[partition], oldest.ID)
	}
	record := learning.AttemptRecord{ID: create.ID, Version: r.version(), State: learning.AttemptQueued, Provenance: create.Provenance, AttemptGeneration: 1, CreatedAt: r.now, UpdatedAt: r.now}
	r.records[partition][create.ID] = record
	return record, nil
}

func (r *retentionRepository) Get(_ context.Context, partition learning.AttemptPartition, id learning.AttemptID) (learning.AttemptRecord, bool, error) {
	record, ok := r.records[partition][id]
	return record, ok, nil
}

func (r *retentionRepository) List(_ context.Context, partition learning.AttemptPartition, query learning.AttemptList) (learning.AttemptPage, error) {
	var records []learning.AttemptRecord
	for _, record := range r.records[partition] {
		if query.State == "" || record.State == query.State {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return learning.AttemptPage{Records: records}, nil
}

func (*retentionRepository) DiscoverWork(context.Context, learning.AttemptWorkList) (learning.AttemptWorkPage, error) {
	panic("not used by retention conformance")
}

func (r *retentionRepository) AcquireClaim(_ context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, now, expires time.Time) (learning.AttemptRecord, learning.AttemptClaim, error) {
	record, ok := r.records[partition][id]
	if !ok {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrAttemptNotFound
	}
	if record.Version != expected {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrAttemptVersionConflict
	}
	if record.State == learning.AttemptRunning && now.Before(record.ClaimExpiresAt) {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrAttemptClaimConflict
	}
	record.State = learning.AttemptRunning
	record.ClaimGeneration++
	record.ClaimExpiresAt = expires
	record.Version = r.version()
	record.UpdatedAt = now
	r.records[partition][id] = record
	return record, learning.AttemptClaim{Generation: record.ClaimGeneration, ExpiresAt: expires}, nil
}

func (*retentionRepository) RenewClaim(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, learning.AttemptClaim, time.Time, time.Time) (learning.AttemptRecord, learning.AttemptClaim, error) {
	panic("not used by retention conformance")
}

func (*retentionRepository) Checkpoint(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, learning.AttemptClaim, time.Time, learning.AttemptCheckpoint) (learning.AttemptRecord, error) {
	panic("not used by retention conformance")
}

func (*retentionRepository) ReleaseClaim(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, learning.AttemptClaim, time.Time) (learning.AttemptRecord, error) {
	panic("not used by retention conformance")
}

func (r *retentionRepository) Finalize(_ context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, now time.Time, final learning.AttemptFinalization) (learning.AttemptRecord, error) {
	record, ok := r.records[partition][id]
	if !ok {
		return learning.AttemptRecord{}, learning.ErrAttemptNotFound
	}
	if record.Version != expected {
		return learning.AttemptRecord{}, learning.ErrAttemptVersionConflict
	}
	if record.ClaimGeneration != claim.Generation || record.ClaimExpiresAt != claim.ExpiresAt || !claim.ValidAt(now) {
		return learning.AttemptRecord{}, learning.ErrAttemptClaimLost
	}
	record.State, record.Outcome, record.FailureCode = final.State, final.Outcome, final.FailureCode
	record.ClaimGeneration, record.ClaimExpiresAt = 0, time.Time{}
	record.Version, record.UpdatedAt = r.version(), now
	r.records[partition][id] = record
	return record, nil
}

func (*retentionRepository) Retry(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, time.Time) (learning.AttemptRecord, error) {
	panic("not used by retention conformance")
}

func (*retentionRepository) Abandon(context.Context, learning.AttemptPartition, learning.AttemptID, learning.AttemptVersion, time.Time) (learning.AttemptRecord, error) {
	panic("not used by retention conformance")
}

func (r *retentionRepository) Delete(_ context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, _ time.Time) error {
	record, ok := r.records[partition][id]
	if !ok {
		return learning.ErrAttemptNotFound
	}
	if record.Version != expected {
		return learning.ErrAttemptVersionConflict
	}
	if record.State == learning.AttemptRunning && !r.allowClaimedDelete {
		return learning.ErrAttemptClaimConflict
	}
	if !record.State.Terminal() && record.State != learning.AttemptQueued && !r.allowClaimedDelete {
		return learning.ErrAttemptTransition
	}
	delete(r.records[partition], id)
	return nil
}

func (r *retentionRepository) DeleteTerminalBefore(_ context.Context, partition learning.AttemptPartition, before time.Time, limit int) (int, error) {
	if limit < 1 || limit > learning.MaxAttemptDeleteBatch {
		return 0, learning.ErrInvalidAttempt
	}
	var ids []learning.AttemptID
	for id, record := range r.records[partition] {
		if record.State.Terminal() && record.UpdatedAt.Before(before) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	for _, id := range ids {
		delete(r.records[partition], id)
	}
	return len(ids), nil
}

var _ learning.AttemptRepository = (*retentionRepository)(nil)
