// Package reflectionstore persists bounded learning proposals in a crash-safe,
// principal/project-partitioned document.
package reflectionstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/learning"
)

const (
	lockRetry   = 5 * time.Millisecond
	lockTimeout = 5 * time.Second
	maxDocument = 16 << 20
)

type document struct {
	Partitions map[string]map[learning.ProposalID]learning.ProposalRecord `json:"partitions"`
}

// Store is a flocked, crash-safe proposal repository.
type Store struct {
	mu     sync.Mutex
	path   string
	lock   *flock.Flock
	now    func() time.Time
	rename func(string, string) error
}

// New opens a proposal repository rooted at dir.
func New(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("reflectionstore: directory required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, "proposals.json"), lock: flock.New(filepath.Join(dir, "proposals.lock")), now: time.Now, rename: os.Rename}, nil
}

var _ learning.ProposalRepository = (*Store)(nil)

func pkey(p learning.ProposalPartition) string {
	raw, _ := json.Marshal(p)
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}
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
func version() (learning.ProposalVersion, error) {
	var x [16]byte
	if _, err := rand.Read(x[:]); err != nil {
		return "", err
	}
	return learning.ProposalVersion("local-" + hex.EncodeToString(x[:])), nil
}
func (s *Store) locked(ctx context.Context, write bool, fn func(*document) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	var ok bool
	var err error
	if write {
		ok, err = s.lock.TryLockContext(c, lockRetry)
	} else {
		ok, err = s.lock.TryRLockContext(c, lockRetry)
	}
	if err != nil || !ok {
		if err == nil {
			err = context.DeadlineExceeded
		}
		return err
	}
	defer func() { _ = s.lock.Unlock() }()
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
func (s *Store) load() (document, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return document{Partitions: map[string]map[learning.ProposalID]learning.ProposalRecord{}}, nil
	}
	if err != nil {
		return document{}, err
	}
	if len(raw) > maxDocument {
		return document{}, learning.ErrProposalLimit
	}
	var d document
	if err = json.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	if d.Partitions == nil {
		d.Partitions = map[string]map[learning.ProposalID]learning.ProposalRecord{}
	}
	if err := validateDocument(d); err != nil {
		return document{}, err
	}
	return d, nil
}

func validateDocument(d document) error {
	for partitionKey, records := range d.Partitions {
		if len(records) > learning.MaxProposalsPerPartition {
			return learning.ErrProposalLimit
		}
		for id, record := range records {
			if id != record.ID || pkey(record.Partition) != partitionKey || record.Version == "" || len(record.Version) > 256 || !record.Status.Valid() || record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
				return fmt.Errorf("%w: corrupt record identity or lifecycle", learning.ErrInvalidProposal)
			}
			if err := learning.ValidateProposalMaterial(record.Partition, record.InputDigest, record.Candidate, record.Signals); err != nil {
				return err
			}
			expectedID, err := learning.DeterministicProposalID(record.Partition, record.InputDigest, record.Candidate)
			if err != nil || expectedID != record.ID {
				return fmt.Errorf("%w: corrupt deterministic id", learning.ErrInvalidProposal)
			}
			if len(record.Decisions) > learning.MaxProposalDecisions {
				return learning.ErrProposalLimit
			}
			for _, decision := range record.Decisions {
				if err := learning.ValidateDecision(decision); err != nil || decision.At.IsZero() {
					return fmt.Errorf("%w: corrupt decision", learning.ErrInvalidProposal)
				}
			}
			if err := validateReceipt(record); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateReceipt(record learning.ProposalRecord) error {
	if (record.Status == learning.ProposalPromoted || record.Status == learning.ProposalUndone) && record.Receipt == nil {
		return fmt.Errorf("%w: missing promotion receipt", learning.ErrInvalidProposal)
	}
	if record.Receipt == nil {
		return nil
	}
	receipt := record.Receipt
	if receipt.MemoryKey == "" || len(receipt.MemoryKey) > learning.MaxCandidateKeyBytes || receipt.ResultVersion == "" || len(receipt.ResultVersion) > 256 || len(receipt.PreviousVersion) > 256 || receipt.PreviousExists != (receipt.PreviousVersion != "") {
		return fmt.Errorf("%w: corrupt promotion receipt", learning.ErrInvalidProposal)
	}
	return nil
}

func (s *Store) save(d document) error {
	if err := validateDocument(d); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > maxDocument {
		return learning.ErrProposalLimit
	}
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".proposals-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = s.rename(name, s.path); err != nil {
		return fmt.Errorf("reflectionstore: rename: %w", err)
	}
	df, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = df.Sync()
	_ = df.Close()
	return err
}

// StageBatch atomically and idempotently stages bounded candidates.
func (s *Store) StageBatch(ctx context.Context, p learning.ProposalPartition, input string, cs []learning.Candidate, signals []learning.Signal) (out []learning.ProposalRecord, err error) {
	if len(cs) == 0 || len(cs) > learning.MaxCandidates {
		return nil, learning.ErrInvalidProposal
	}
	ids := make([]learning.ProposalID, len(cs))
	for i, c := range cs {
		if err = learning.ValidateProposalMaterial(p, input, c, signals); err != nil {
			return
		}
		ids[i], err = learning.DeterministicProposalID(p, input, c)
		if err != nil {
			return
		}
	}
	err = s.locked(ctx, true, func(d *document) error {
		b := d.Partitions[pkey(p)]
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
			return learning.ErrProposalLimit
		}
		now := s.now().UTC()
		out = make([]learning.ProposalRecord, len(cs))
		for i, c := range cs {
			r, ok := b[ids[i]]
			if !ok {
				v, e := version()
				if e != nil {
					return e
				}
				r = learning.ProposalRecord{ID: ids[i], Version: v, Status: learning.ProposalStaged, Partition: p, InputDigest: input, Candidate: c, Signals: signals, CreatedAt: now, UpdatedAt: now}
				b[ids[i]] = clone(r)
			}
			out[i] = clone(r)
		}
		d.Partitions[pkey(p)] = b
		return nil
	})
	return
}

// Promoting returns durable in-flight records for startup-only crash reconciliation.
func (s *Store) Promoting(ctx context.Context) (records []learning.ProposalRecord, err error) {
	err = s.locked(ctx, false, func(d *document) error {
		for _, partition := range d.Partitions {
			for _, record := range partition {
				if record.Status == learning.ProposalPromoting {
					records = append(records, clone(record))
				}
			}
		}
		sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
		return nil
	})
	return records, err
}

// List returns one bounded proposal page within a partition.
func (s *Store) List(ctx context.Context, p learning.ProposalPartition, o learning.ProposalList) (page learning.ProposalPage, err error) {
	n := o.Limit
	if n <= 0 {
		n = learning.DefaultProposalPageSize
	}
	if n > learning.MaxProposalPageSize {
		return page, learning.ErrProposalLimit
	}
	if o.Status != "" && !o.Status.Valid() {
		return page, learning.ErrInvalidProposal
	}
	err = s.locked(ctx, false, func(d *document) error {
		b := d.Partitions[pkey(p)]
		ids := []learning.ProposalID{}
		for id, r := range b {
			if id > o.After && (o.Status == "" || r.Status == o.Status) {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for i, id := range ids {
			if i == n {
				page.Next = page.Records[len(page.Records)-1].ID
				break
			}
			page.Records = append(page.Records, clone(b[id]))
		}
		return nil
	})
	return
}

// Get returns one proposal from its exact partition.
func (s *Store) Get(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID) (r learning.ProposalRecord, found bool, err error) {
	err = s.locked(ctx, false, func(d *document) error { r, found = d.Partitions[pkey(p)][id]; r = clone(r); return nil })
	return
}
func bounded(ds []learning.Decision, x learning.Decision) []learning.Decision {
	ds = append(ds, x)
	if len(ds) > learning.MaxProposalDecisions {
		ds = append([]learning.Decision(nil), ds[len(ds)-learning.MaxProposalDecisions:]...)
	}
	return ds
}
func (s *Store) update(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, expected learning.ProposalVersion, fn func(*learning.ProposalRecord) error) (out learning.ProposalRecord, err error) {
	err = s.locked(ctx, true, func(d *document) error {
		b := d.Partitions[pkey(p)]
		r, ok := b[id]
		if !ok {
			return learning.ErrProposalNotFound
		}
		if r.Version != expected {
			return learning.ErrProposalVersionConflict
		}
		if e := fn(&r); e != nil {
			return e
		}
		v, e := version()
		if e != nil {
			return e
		}
		r.Version = v
		r.UpdatedAt = s.now().UTC()
		b[id] = clone(r)
		out = clone(r)
		return nil
	})
	return
}

// ClaimDecision records a bounded decision under proposal CAS.
func (s *Store) ClaimDecision(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, v learning.ProposalVersion, x learning.Decision) (learning.ProposalRecord, error) {
	if err := learning.ValidateDecision(x); err != nil {
		return learning.ProposalRecord{}, err
	}
	return s.update(ctx, p, id, v, func(r *learning.ProposalRecord) error {
		if r.Status != learning.ProposalStaged {
			return learning.ErrProposalTransition
		}
		if x.At.IsZero() {
			x.At = s.now().UTC()
		}
		r.Decisions = bounded(r.Decisions, x)
		if x.Kind == learning.DecisionReject {
			r.Status = learning.ProposalRejected
		}
		return nil
	})
}

// ClaimPromotion atomically moves a staged proposal to promoting.
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

func (s *Store) finalizeUpdate(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, expected learning.ProposalVersion, status learning.ProposalStatus, receipt *learning.PromotionReceipt, decision learning.Decision, fn func(*learning.ProposalRecord) error) (out learning.ProposalRecord, err error) {
	err = s.locked(ctx, true, func(d *document) error {
		b := d.Partitions[pkey(p)]
		r, ok := b[id]
		if !ok {
			return learning.ErrProposalNotFound
		}
		if finalizeRetryMatches(r, status, receipt, decision) {
			out = clone(r)
			return nil
		}
		if r.Version != expected {
			return learning.ErrProposalVersionConflict
		}
		if e := fn(&r); e != nil {
			return e
		}
		v, e := version()
		if e != nil {
			return e
		}
		r.Version = v
		r.UpdatedAt = s.now().UTC()
		b[id] = clone(r)
		out = clone(r)
		return nil
	})
	return
}

// Finalize records a terminal promotion, reconciliation, or undo outcome.
//
//nolint:gocyclo // closed transition validation is clearer as one repository transaction
func (s *Store) Finalize(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, v learning.ProposalVersion, status learning.ProposalStatus, receipt *learning.PromotionReceipt, x learning.Decision) (learning.ProposalRecord, error) {
	if status != learning.ProposalPromoted && status != learning.ProposalRejected && status != learning.ProposalDeferredUnsupported && status != learning.ProposalConflicted && status != learning.ProposalUndone {
		return learning.ProposalRecord{}, learning.ErrProposalTransition
	}
	if x.Kind != "" {
		if err := learning.ValidateDecision(x); err != nil {
			return learning.ProposalRecord{}, err
		}
	}
	return s.finalizeUpdate(ctx, p, id, v, status, receipt, x, func(r *learning.ProposalRecord) error {
		validPromotedTransition := r.Status == learning.ProposalPromoted && (status == learning.ProposalUndone || status == learning.ProposalConflicted)
		if r.Status != learning.ProposalPromoting && !validPromotedTransition {
			return learning.ErrProposalTransition
		}
		if status == learning.ProposalPromoted && (receipt == nil || receipt.MemoryKey == "" || receipt.ResultVersion == "") || status == learning.ProposalUndone && receipt == nil {
			return learning.ErrInvalidProposal
		}
		r.Status = status
		if receipt != nil {
			z := *receipt
			r.Receipt = &z
		}
		if x.Kind != "" {
			if x.At.IsZero() {
				x.At = s.now().UTC()
			}
			r.Decisions = bounded(r.Decisions, x)
		}
		return nil
	})
}
