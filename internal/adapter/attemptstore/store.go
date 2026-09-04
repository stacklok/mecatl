// Package attemptstore persists bounded learning attempts in a crash-safe,
// owner-partitioned document.
package attemptstore

//revive:disable:exported // methods implement the documented AttemptRepository contract

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/learning"
)

const (
	documentName = "attempts.json"
	lockName     = "attempts.lock"
	documentType = "mecatl-attemptstore/1"
	maxDocument  = 16 << 20
	lockRetry    = 5 * time.Millisecond
	lockTimeout  = 5 * time.Second
)

type partitionDocument struct {
	Records          map[learning.AttemptID]learning.AttemptRecord   `json:"records"`
	ClaimGenerations map[learning.AttemptID]learning.ClaimGeneration `json:"claim_generations,omitempty"`
}

type document struct {
	Format     string                       `json:"format"`
	Partitions map[string]partitionDocument `json:"partitions"`
}

// Store is a flocked, crash-safe learning.AttemptRepository. New is lazy: an
// empty repository does not create its directory until the first mutation.
type Store struct {
	mu     sync.Mutex
	dir    string
	path   string
	lock   *flock.Flock
	now    func() time.Time
	rename func(string, string) error
}

var _ learning.AttemptRepository = (*Store)(nil)

// New prepares a repository rooted at dir without creating it.
func New(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("attemptstore: directory required")
	}
	abs, err := canonicalStoreDir(dir)
	if err != nil {
		return nil, err
	}
	return &Store{
		dir: abs, path: filepath.Join(abs, documentName),
		lock: flock.New(filepath.Join(abs, lockName)), now: time.Now, rename: os.Rename,
	}, nil
}

func canonicalStoreDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("attemptstore: symlink repository root rejected: %s", abs)
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var missing []string
	ancestor := abs
	for {
		if _, err = os.Lstat(ancestor); err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("attemptstore: no existing ancestor for %s", abs)
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	physical, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		physical = filepath.Join(physical, missing[i])
	}
	return physical, nil
}

func emptyDocument() document {
	return document{Format: documentType, Partitions: make(map[string]partitionDocument)}
}

func partitionKey(partition learning.AttemptPartition) (string, error) {
	key := string(partition)
	if len(key) != 64 {
		return "", learning.ErrInvalidAttempt
	}
	if _, err := hex.DecodeString(key); err != nil {
		return "", learning.ErrInvalidAttempt
	}
	return key, nil
}

func (s *Store) ensureDir() error {
	_, statErr := os.Lstat(s.dir)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	if os.IsNotExist(statErr) {
		if err := syncDir(filepath.Dir(s.dir)); err != nil {
			return err
		}
	}
	return rejectKnownSymlinks(s.dir)
}

func rejectKnownSymlinks(dir string) error {
	for _, path := range []string{dir, filepath.Join(dir, documentName), filepath.Join(dir, lockName)} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("attemptstore: symlink path rejected: %s", path)
		}
	}
	return nil
}

func (s *Store) locked(ctx context.Context, write bool, fn func(*document) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !write {
		if _, err := os.Lstat(s.dir); os.IsNotExist(err) {
			doc := emptyDocument()
			return fn(&doc)
		} else if err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	var ok bool
	var err error
	if write {
		ok, err = s.lock.TryLockContext(bounded, lockRetry)
	} else {
		ok, err = s.lock.TryRLockContext(bounded, lockRetry)
	}
	if err != nil || !ok {
		if err == nil {
			err = context.DeadlineExceeded
		}
		return err
	}
	defer func() { _ = s.lock.Unlock() }()
	if err = rejectKnownSymlinks(s.dir); err != nil {
		return err
	}
	doc, err := s.load()
	if err != nil {
		return err
	}
	if err = fn(&doc); err != nil {
		return err
	}
	if write {
		return s.save(doc)
	}
	return nil
}

func boundedReadRegular(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("attemptstore: non-regular document rejected: %s", path)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxDocument+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxDocument {
		return nil, fmt.Errorf("%w: attempt document too large", learning.ErrInvalidAttempt)
	}
	return raw, nil
}

func (s *Store) load() (document, error) {
	raw, err := boundedReadRegular(s.path)
	if os.IsNotExist(err) {
		return emptyDocument(), nil
	}
	if err != nil {
		return document{}, err
	}
	var doc document
	if err = json.Unmarshal(raw, &doc); err != nil {
		return document{}, err
	}
	if err = validateDocument(doc); err != nil {
		return document{}, err
	}
	return doc, nil
}

func validateDocument(doc document) error {
	if doc.Format != documentType || doc.Partitions == nil {
		return fmt.Errorf("%w: invalid attempt document", learning.ErrInvalidAttempt)
	}
	for key, partition := range doc.Partitions {
		if _, err := partitionKey(learning.AttemptPartition(key)); err != nil || partition.Records == nil {
			return fmt.Errorf("%w: invalid attempt partition", learning.ErrInvalidAttempt)
		}
		for id, record := range partition.Records {
			if id != record.ID || learning.ValidateAttemptRecord(record) != nil {
				return fmt.Errorf("%w: invalid attempt record", learning.ErrInvalidAttempt)
			}
			generation := partition.ClaimGenerations[id]
			if generation < record.ClaimGeneration {
				return fmt.Errorf("%w: invalid claim generation", learning.ErrInvalidAttempt)
			}
		}
		for id, generation := range partition.ClaimGenerations {
			if generation == 0 {
				return fmt.Errorf("%w: invalid claim generation for %s", learning.ErrInvalidAttempt, id)
			}
			if _, ok := partition.Records[id]; !ok {
				return fmt.Errorf("%w: orphaned claim generation", learning.ErrInvalidAttempt)
			}
		}
	}
	return nil
}

func (s *Store) save(doc document) error {
	if err := validateDocument(doc); err != nil {
		return err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if len(raw) > maxDocument {
		return fmt.Errorf("%w: attempt document too large", learning.ErrInvalidAttempt)
	}
	file, err := os.CreateTemp(s.dir, ".attemptstore-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = s.rename(name, s.path); err != nil {
		return fmt.Errorf("attemptstore: rename: %w", err)
	}
	return syncDir(s.dir)
}

func syncDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func version() (learning.AttemptVersion, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return learning.AttemptVersion("local-" + hex.EncodeToString(value[:])), nil
}

func bucket(doc *document, key string) partitionDocument {
	partition, ok := doc.Partitions[key]
	if !ok {
		partition = partitionDocument{
			Records:          make(map[learning.AttemptID]learning.AttemptRecord),
			ClaimGenerations: make(map[learning.AttemptID]learning.ClaimGeneration),
		}
	}
	if partition.ClaimGenerations == nil {
		partition.ClaimGenerations = make(map[learning.AttemptID]learning.ClaimGeneration)
	}
	return partition
}

func makeRoom(part *partitionDocument) error {
	if len(part.Records) < learning.MaxAttemptsPerPartition {
		return nil
	}
	var oldest learning.AttemptRecord
	found := false
	for _, record := range part.Records {
		if record.State.Terminal() && (!found || record.UpdatedAt.Before(oldest.UpdatedAt) || (record.UpdatedAt.Equal(oldest.UpdatedAt) && record.ID < oldest.ID)) {
			oldest, found = record, true
		}
	}
	if !found {
		return learning.ErrAttemptQuotaExceeded
	}
	delete(part.Records, oldest.ID)
	delete(part.ClaimGenerations, oldest.ID)
	if len(part.Records) >= learning.MaxAttemptsPerPartition {
		return learning.ErrAttemptQuotaExceeded
	}
	return nil
}

func (s *Store) Create(ctx context.Context, partition learning.AttemptPartition, create learning.AttemptCreate) (out learning.AttemptRecord, err error) {
	key, err := partitionKey(partition)
	if err != nil || create.Validate() != nil {
		return out, learning.ErrInvalidAttempt
	}
	now := s.now().UTC()
	if now.IsZero() {
		return out, learning.ErrInvalidAttempt
	}
	err = s.locked(ctx, true, func(doc *document) error {
		part := bucket(doc, key)
		if existing, ok := part.Records[create.ID]; ok {
			if existing.Provenance != create.Provenance {
				return learning.ErrAttemptCreateConflict
			}
			out = existing
			return nil
		}
		if roomErr := makeRoom(&part); roomErr != nil {
			return roomErr
		}
		v, versionErr := version()
		if versionErr != nil {
			return versionErr
		}
		out = learning.AttemptRecord{ID: create.ID, Version: v, State: learning.AttemptQueued, Provenance: create.Provenance, AttemptGeneration: 1, CreatedAt: now, UpdatedAt: now}
		part.Records[out.ID] = out
		doc.Partitions[key] = part
		return nil
	})
	return out, err
}

func (s *Store) Get(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID) (out learning.AttemptRecord, found bool, err error) {
	key, err := partitionKey(partition)
	if err != nil {
		return out, false, err
	}
	err = s.locked(ctx, false, func(doc *document) error {
		out, found = doc.Partitions[key].Records[id]
		return nil
	})
	return out, found, err
}

func (s *Store) List(ctx context.Context, partition learning.AttemptPartition, query learning.AttemptList) (page learning.AttemptPage, err error) {
	key, err := partitionKey(partition)
	if err != nil || query.Validate() != nil {
		return page, learning.ErrInvalidAttempt
	}
	limit := query.Limit
	if limit == 0 {
		limit = learning.DefaultAttemptPageSize
	}
	err = s.locked(ctx, false, func(doc *document) error {
		records := doc.Partitions[key].Records
		ids := make([]learning.AttemptID, 0, len(records))
		for id, record := range records {
			if id > query.After && (query.State == "" || record.State == query.State) {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids[:min(limit, len(ids))] {
			page.Records = append(page.Records, records[id])
		}
		if len(ids) > limit {
			page.Next = page.Records[len(page.Records)-1].ID
		}
		return nil
	})
	return page, err
}

// DiscoverWork returns bounded repository-authoritative work across partitions.
func (s *Store) DiscoverWork(ctx context.Context, query learning.AttemptWorkList) (page learning.AttemptWorkPage, err error) {
	if query.Validate() != nil {
		return page, learning.ErrInvalidAttempt
	}
	err = s.locked(ctx, false, func(doc *document) error {
		now := s.now().UTC()
		if now.IsZero() {
			return learning.ErrInvalidAttempt
		}
		for partition, values := range doc.Partitions {
			for _, record := range values.Records {
				after := learning.AttemptPartition(partition) > query.After.Partition || (learning.AttemptPartition(partition) == query.After.Partition && record.ID > query.After.ID)
				if after && (record.State == learning.AttemptQueued || (record.State == learning.AttemptRunning && !record.ClaimExpiresAt.After(now))) {
					page.Work = append(page.Work, learning.AttemptWork{Partition: learning.AttemptPartition(partition), Record: record})
				}
			}
		}
		sort.Slice(page.Work, func(i, j int) bool {
			if page.Work[i].Partition == page.Work[j].Partition {
				return page.Work[i].Record.ID < page.Work[j].Record.ID
			}
			return page.Work[i].Partition < page.Work[j].Partition
		})
		if len(page.Work) > query.Limit {
			last := page.Work[query.Limit-1]
			page.Next = learning.AttemptWorkCursor{Partition: last.Partition, ID: last.Record.ID}
			page.Work = page.Work[:query.Limit]
		}
		return nil
	})
	return page, err
}

func current(part partitionDocument, id learning.AttemptID, expected learning.AttemptVersion) (learning.AttemptRecord, error) {
	record, found := part.Records[id]
	if !found {
		return learning.AttemptRecord{}, learning.ErrAttemptNotFound
	}
	if record.Version != expected {
		return learning.AttemptRecord{}, learning.ErrAttemptVersionConflict
	}
	return record, nil
}

func commit(part *partitionDocument, record *learning.AttemptRecord, now time.Time) error {
	v, err := version()
	if err != nil {
		return err
	}
	record.Version = v
	record.UpdatedAt = now.UTC()
	part.Records[record.ID] = *record
	return nil
}

func (s *Store) AcquireClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, duration time.Duration) (out learning.AttemptRecord, claim learning.AttemptClaim, err error) {
	key, err := partitionKey(partition)
	if err != nil || !validClaimDuration(duration) {
		return out, claim, learning.ErrInvalidAttempt
	}
	err = s.locked(ctx, true, func(doc *document) error {
		now := s.now().UTC()
		if now.IsZero() {
			return learning.ErrInvalidAttempt
		}
		expiresAt := now.Add(duration)
		part := bucket(doc, key)
		record, currentErr := current(part, id, expected)
		if currentErr != nil {
			return currentErr
		}
		if record.State == learning.AttemptRunning && now.Before(record.ClaimExpiresAt) {
			return learning.ErrAttemptClaimConflict
		}
		if record.State != learning.AttemptQueued && record.State != learning.AttemptRunning {
			return learning.ErrAttemptTransition
		}
		if now.Before(record.CreatedAt) {
			return learning.ErrInvalidAttempt
		}
		generation := part.ClaimGenerations[id] + 1
		part.ClaimGenerations[id] = generation
		record.State = learning.AttemptRunning
		record.ClaimGeneration = generation
		record.ClaimExpiresAt = expiresAt.UTC()
		if err := commit(&part, &record, now); err != nil {
			return err
		}
		doc.Partitions[key] = part
		out = record
		claim = learning.AttemptClaim{Generation: generation, ExpiresAt: record.ClaimExpiresAt}
		return nil
	})
	return out, claim, err
}

func (s *Store) updateClaimed(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, fn func(*learning.AttemptRecord, time.Time) error) (out learning.AttemptRecord, err error) {
	key, err := partitionKey(partition)
	if err != nil {
		return out, learning.ErrInvalidAttempt
	}
	err = s.locked(ctx, true, func(doc *document) error {
		now := s.now().UTC()
		if now.IsZero() {
			return learning.ErrInvalidAttempt
		}
		part := bucket(doc, key)
		record, currentErr := current(part, id, expected)
		if currentErr != nil {
			return currentErr
		}
		if !matchesClaim(record, claim, now) {
			return learning.ErrAttemptClaimLost
		}
		if err := fn(&record, now); err != nil {
			return err
		}
		if err := commit(&part, &record, now); err != nil {
			return err
		}
		doc.Partitions[key] = part
		out = record
		return nil
	})
	return out, err
}

func (s *Store) RenewClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, duration time.Duration) (learning.AttemptRecord, learning.AttemptClaim, error) {
	if !validClaimDuration(duration) {
		return learning.AttemptRecord{}, learning.AttemptClaim{}, learning.ErrInvalidAttempt
	}
	out, err := s.updateClaimed(ctx, partition, id, expected, claim, func(record *learning.AttemptRecord, now time.Time) error {
		record.ClaimExpiresAt = now.Add(duration)
		return nil
	})
	return out, learning.AttemptClaim{Generation: claim.Generation, ExpiresAt: out.ClaimExpiresAt}, err
}

func (s *Store) Checkpoint(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, checkpoint learning.AttemptCheckpoint) (learning.AttemptRecord, error) {
	if checkpoint.Validate() != nil {
		return learning.AttemptRecord{}, learning.ErrInvalidAttempt
	}
	return s.updateClaimed(ctx, partition, id, expected, claim, func(record *learning.AttemptRecord, _ time.Time) error {
		if !record.CheckpointStage.CanAdvanceTo(checkpoint.Stage) {
			return learning.ErrAttemptTransition
		}
		if record.CheckpointStage == checkpoint.Stage {
			if record.ProposalID != checkpoint.ProposalID || record.SkillID != checkpoint.SkillID {
				return learning.ErrAttemptTransition
			}
			return nil
		}
		record.CheckpointStage = checkpoint.Stage
		record.ProposalID = checkpoint.ProposalID
		record.SkillID = checkpoint.SkillID
		return nil
	})
}

func (s *Store) ReleaseClaim(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim) (learning.AttemptRecord, error) {
	return s.updateClaimed(ctx, partition, id, expected, claim, func(record *learning.AttemptRecord, _ time.Time) error {
		record.State = learning.AttemptQueued
		clearClaim(record)
		return nil
	})
}

func (s *Store) Finalize(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, claim learning.AttemptClaim, final learning.AttemptFinalization) (learning.AttemptRecord, error) {
	if err := final.Validate(); err != nil {
		return learning.AttemptRecord{}, err
	}
	return s.updateClaimed(ctx, partition, id, expected, claim, func(record *learning.AttemptRecord, _ time.Time) error {
		if !learning.ValidAttemptTransition(record.State, final.State) {
			return learning.ErrAttemptTransition
		}
		record.State, record.Outcome, record.FailureCode = final.State, final.Outcome, final.FailureCode
		clearClaim(record)
		return nil
	})
}

func (s *Store) update(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion, fn func(*learning.AttemptRecord, time.Time) error) (out learning.AttemptRecord, err error) {
	key, err := partitionKey(partition)
	if err != nil {
		return out, learning.ErrInvalidAttempt
	}
	err = s.locked(ctx, true, func(doc *document) error {
		now := s.now().UTC()
		if now.IsZero() {
			return learning.ErrInvalidAttempt
		}
		part := bucket(doc, key)
		record, currentErr := current(part, id, expected)
		if currentErr != nil {
			return currentErr
		}
		if err := fn(&record, now); err != nil {
			return err
		}
		if err := commit(&part, &record, now); err != nil {
			return err
		}
		doc.Partitions[key] = part
		out = record
		return nil
	})
	return out, err
}

func (s *Store) Retry(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion) (learning.AttemptRecord, error) {
	return s.update(ctx, partition, id, expected, func(record *learning.AttemptRecord, _ time.Time) error {
		if !learning.ValidAttemptTransition(record.State, learning.AttemptQueued) {
			return learning.ErrAttemptTransition
		}
		record.State = learning.AttemptQueued
		record.Outcome = learning.AttemptOutcomeNone
		record.FailureCode = learning.FailureNone
		record.AttemptGeneration++
		clearClaim(record)
		return nil
	})
}

func (s *Store) Abandon(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion) (learning.AttemptRecord, error) {
	return s.update(ctx, partition, id, expected, func(record *learning.AttemptRecord, now time.Time) error {
		if record.State == learning.AttemptRunning && now.Before(record.ClaimExpiresAt) {
			return learning.ErrAttemptClaimConflict
		}
		if !learning.ValidAttemptTransition(record.State, learning.AttemptAbandoned) {
			return learning.ErrAttemptTransition
		}
		record.State = learning.AttemptAbandoned
		record.Outcome = learning.AttemptOutcomeAbandoned
		record.FailureCode = learning.FailureNone
		clearClaim(record)
		return nil
	})
}

func (s *Store) Delete(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion) (err error) {
	key, err := partitionKey(partition)
	if err != nil {
		return learning.ErrInvalidAttempt
	}
	return s.locked(ctx, true, func(doc *document) error {
		part := bucket(doc, key)
		record, currentErr := current(part, id, expected)
		if currentErr != nil {
			return currentErr
		}
		if record.State == learning.AttemptRunning {
			return learning.ErrAttemptClaimConflict
		}
		if record.State != learning.AttemptQueued && !record.State.Terminal() {
			return learning.ErrAttemptTransition
		}
		delete(part.Records, id)
		delete(part.ClaimGenerations, id)
		doc.Partitions[key] = part
		return nil
	})
}

func (s *Store) DeleteTerminalOlderThan(ctx context.Context, partition learning.AttemptPartition, olderThan time.Duration, limit int) (deleted int, err error) {
	key, err := partitionKey(partition)
	if err != nil || olderThan <= 0 || olderThan > learning.MaxAttemptRetentionAge || limit < 1 || limit > learning.MaxAttemptDeleteBatch {
		return 0, learning.ErrInvalidAttempt
	}
	err = s.locked(ctx, true, func(doc *document) error {
		now := s.now().UTC()
		if now.IsZero() {
			return learning.ErrInvalidAttempt
		}
		cutoff := now.Add(-olderThan)
		part := bucket(doc, key)
		type candidate struct {
			id learning.AttemptID
			at time.Time
		}
		candidates := make([]candidate, 0)
		for id, record := range part.Records {
			if record.State.Terminal() && record.UpdatedAt.Before(cutoff) {
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
			delete(part.Records, candidate.id)
			delete(part.ClaimGenerations, candidate.id)
		}
		doc.Partitions[key] = part
		deleted = len(candidates)
		return nil
	})
	return deleted, err
}

func validClaimDuration(duration time.Duration) bool {
	return duration > 0 && duration <= learning.MaxAttemptClaimDuration
}

func matchesClaim(record learning.AttemptRecord, claim learning.AttemptClaim, now time.Time) bool {
	return record.State == learning.AttemptRunning && claim.ValidAt(now) &&
		record.ClaimGeneration == claim.Generation && record.ClaimExpiresAt.Equal(claim.ExpiresAt)
}

func clearClaim(record *learning.AttemptRecord) {
	record.ClaimGeneration = 0
	record.ClaimExpiresAt = time.Time{}
}
