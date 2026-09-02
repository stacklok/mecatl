// Package automaticstore persists automatic-admission accounting in one
// flock-serialized, crash-safe document shared by cooperating processes.
package automaticstore

//revive:disable:exported // methods implement the documented AutomaticAdmissionLedger contract

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
	"github.com/stacklok/mecatl/engine/port"
)

const (
	documentName = "automatic-admission.json"
	lockName     = "automatic-admission.lock"
	documentType = "mecatl-automatic-admission/1"
	maxDocument  = 16 << 20
	lockRetry    = 5 * time.Millisecond
	lockTimeout  = 5 * time.Second
)

var errNoExpiredReservations = errors.New("automaticstore: no expired reservations")

type entry struct {
	Reservation learning.AutomaticReservation `json:"reservation"`
}

type document struct {
	Format         string                                    `json:"format"`
	Policy         learning.AutomaticAdmissionPolicy         `json:"policy"`
	PolicyRevision learning.AutomaticAdmissionPolicyRevision `json:"policy_revision"`
	Records        map[learning.AutomaticReservationID]entry `json:"records"`
}

// Store is a durable learning.AutomaticAdmissionLedger for cooperating
// processes on one host filesystem. Each operation reloads under flock; no
// process-local cache participates in admission decisions.
type Store struct {
	mu             sync.Mutex
	dir            string
	path           string
	lock           *flock.Flock
	policy         learning.AutomaticAdmissionPolicy
	policyRevision learning.AutomaticAdmissionPolicyRevision
	clock          port.Clock
}

var _ learning.AutomaticAdmissionLedger = (*Store)(nil)

// New prepares a ledger rooted at dir with backend-owned immutable policy and
// time authority.
func New(dir string, policy learning.AutomaticAdmissionPolicy, clock port.Clock) (*Store, error) {
	if strings.TrimSpace(dir) == "" || clock == nil {
		return nil, errors.New("automaticstore: directory, policy, and clock required")
	}
	revision, err := learning.AutomaticAdmissionPolicyRevisionFor(policy)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Lstat(abs); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("automaticstore: symlink repository root rejected")
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	return &Store{
		dir: abs, path: filepath.Join(abs, documentName), lock: flock.New(filepath.Join(abs, lockName)),
		policy: policy, policyRevision: revision, clock: clock,
	}, nil
}

func (s *Store) emptyDocument() document {
	return document{Format: documentType, Policy: s.policy, PolicyRevision: s.policyRevision, Records: make(map[learning.AutomaticReservationID]entry)}
}

func (s *Store) transaction(ctx context.Context, write bool, fn func(*document) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	if err := rejectSymlinks(s.dir, s.path, filepath.Join(s.dir, lockName)); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	var (
		ok  bool
		err error
	)
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

func rejectSymlinks(paths ...string) error {
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("automaticstore: symlink path rejected")
		}
	}
	return nil
}

func (s *Store) load() (document, error) {
	file, err := os.OpenFile(s.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return s.emptyDocument(), nil
	}
	if err != nil {
		return document{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return document{}, errors.New("automaticstore: invalid ledger document")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxDocument+1))
	if err != nil {
		return document{}, err
	}
	if len(raw) > maxDocument {
		return document{}, errors.New("automaticstore: ledger document too large")
	}
	var doc document
	if err = json.Unmarshal(raw, &doc); err != nil {
		return document{}, err
	}
	if err = validateDocument(doc); err != nil {
		return document{}, err
	}
	if doc.Policy != s.policy || doc.PolicyRevision != s.policyRevision {
		return document{}, errors.New("automaticstore: configured policy revision does not match durable ledger")
	}
	return doc, nil
}

func validateDocument(doc document) error {
	revision, err := learning.AutomaticAdmissionPolicyRevisionFor(doc.Policy)
	if doc.Format != documentType || doc.Records == nil || err != nil || revision != doc.PolicyRevision {
		return errors.New("automaticstore: invalid ledger document")
	}
	for id, stored := range doc.Records {
		if id != stored.Reservation.ID || stored.Reservation.Validate() != nil || stored.Reservation.PolicyRevision != doc.PolicyRevision {
			return errors.New("automaticstore: invalid ledger record")
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
		return errors.New("automaticstore: ledger document too large")
	}
	tmp, err := os.CreateTemp(s.dir, ".automatic-admission-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmpName, s.path); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func newVersion() (learning.AutomaticReservationVersion, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return learning.AutomaticReservationVersion(hex.EncodeToString(raw[:])), nil
}

// Reserve atomically applies all configured accounting controls.
func (s *Store) Reserve(ctx context.Context, req learning.AutomaticReservationRequest) (learning.AutomaticReservation, error) {
	if err := req.Validate(); err != nil || len(req.Principal) > 128 || req.ExpectedPolicyRevision != s.policyRevision ||
		req.Tokens > s.policy.MaxTokens || req.Tokens > s.policy.MaxTokensPerPrincipal {
		return learning.AutomaticReservation{}, learning.ErrInvalidAutomaticReservation
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return learning.AutomaticReservation{}, learning.ErrInvalidAutomaticReservation
	}
	var result learning.AutomaticReservation
	err := s.transaction(ctx, true, func(doc *document) error {
		pruneResolved(doc, now)
		if stored, ok := doc.Records[req.ID]; ok {
			if sameImmutable(stored, req) {
				result = stored.Reservation
				return nil
			}
			return learning.ErrAutomaticReservationConflict
		}
		if err := s.admissionError(*doc, req, now); err != nil {
			return err
		}
		if err := reservationQuotaError(*doc, req.Principal); err != nil {
			return err
		}
		version, err := newVersion()
		if err != nil {
			return err
		}
		result = learning.AutomaticReservation{
			ID: req.ID, AttemptID: req.AttemptID, Version: version, Principal: req.Principal,
			Digest: req.Digest, Class: req.Class, PolicyRevision: s.policyRevision, Tokens: req.Tokens, Charge: learning.AutomaticChargeHeld,
			ReservedAt: now, ChargeExpiresAt: now.Add(s.policy.Window), DedupeExpiresAt: now.Add(s.policy.DedupeWindow),
			ClaimDuration: s.policy.ReservationClaimDuration,
			Fence:         learning.AutomaticReservationFence{Generation: 1, ExpiresAt: now.Add(s.policy.ReservationClaimDuration)},
		}
		doc.Records[req.ID] = entry{Reservation: result}
		return nil
	})
	return result, err
}

func pruneResolved(doc *document, now time.Time) {
	for id, stored := range doc.Records {
		if stored.Reservation.Charge != learning.AutomaticChargeHeld && !now.Before(stored.Reservation.DedupeExpiresAt) {
			delete(doc.Records, id)
		}
	}
}

func reservationQuotaError(doc document, principal learning.AttemptPartition) error {
	if len(doc.Records) >= learning.MaxAutomaticReservationRecords {
		return learning.ErrAutomaticAdmissionLimit
	}
	owned := 0
	for _, stored := range doc.Records {
		if stored.Reservation.Principal == principal {
			owned++
		}
	}
	if owned >= learning.MaxAutomaticReservationRecordsPerPrincipal {
		return learning.ErrAutomaticAdmissionLimit
	}
	return nil
}

func sameImmutable(stored entry, req learning.AutomaticReservationRequest) bool {
	r := stored.Reservation
	return r.ID == req.ID && r.AttemptID == req.AttemptID && r.Principal == req.Principal && r.Digest == req.Digest &&
		r.Class == req.Class && r.Tokens == req.Tokens && r.PolicyRevision == req.ExpectedPolicyRevision
}

func (s *Store) admissionError(doc document, req learning.AutomaticReservationRequest, now time.Time) error {
	var globalCount, globalTokens, principalCount, principalTokens uint64
	for _, stored := range doc.Records {
		r := stored.Reservation
		if r.Charge == learning.AutomaticChargeReclaimed {
			continue
		}
		if now.Before(r.DedupeExpiresAt) && r.Digest == req.Digest {
			return learning.ErrAutomaticAdmissionDuplicate
		}
		if req.Class == learning.AdmissionWeighted && r.Principal == req.Principal && now.Before(r.ReservedAt.Add(s.policy.Cooldown)) {
			return learning.ErrAutomaticAdmissionCooldown
		}
		if !now.Before(r.ChargeExpiresAt) {
			continue
		}
		globalCount++
		if r.Tokens > ^uint64(0)-globalTokens {
			return learning.ErrAutomaticAdmissionLimit
		}
		globalTokens += r.Tokens
		if r.Principal == req.Principal {
			principalCount++
			if r.Tokens > ^uint64(0)-principalTokens {
				return learning.ErrAutomaticAdmissionLimit
			}
			principalTokens += r.Tokens
		}
	}
	if globalCount >= s.policy.MaxCount || globalTokens >= s.policy.MaxTokens || req.Tokens > s.policy.MaxTokens-globalTokens ||
		principalCount >= s.policy.MaxCountPerPrincipal || principalTokens >= s.policy.MaxTokensPerPrincipal || req.Tokens > s.policy.MaxTokensPerPrincipal-principalTokens {
		return learning.ErrAutomaticAdmissionLimit
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id learning.AutomaticReservationID) (learning.AutomaticReservation, bool, error) {
	var result learning.AutomaticReservation
	var found bool
	err := s.transaction(ctx, false, func(doc *document) error {
		stored, ok := doc.Records[id]
		result, found = stored.Reservation, ok
		return nil
	})
	return result, found, err
}

// DiscoverExpired atomically claims a bounded set of expired held records using
// this store's authoritative clock.
func (s *Store) DiscoverExpired(ctx context.Context, limit uint32) ([]learning.AutomaticReservation, error) {
	if limit == 0 || limit > learning.MaxAutomaticReservationDiscoveryBatch {
		return nil, learning.ErrInvalidAutomaticReservation
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return nil, learning.ErrInvalidAutomaticReservation
	}
	var result []learning.AutomaticReservation
	err := s.transaction(ctx, true, func(doc *document) error {
		ids := make([]string, 0, len(doc.Records))
		for id, stored := range doc.Records {
			r := stored.Reservation
			if r.Charge == learning.AutomaticChargeHeld && !now.Before(r.Fence.ExpiresAt) {
				ids = append(ids, string(id))
			}
		}
		if len(ids) == 0 {
			return errNoExpiredReservations
		}
		sort.Strings(ids)
		if len(ids) > int(limit) {
			ids = ids[:limit]
		}
		result = make([]learning.AutomaticReservation, 0, len(ids))
		for _, rawID := range ids {
			id := learning.AutomaticReservationID(rawID)
			stored := doc.Records[id]
			version, err := newVersion()
			if err != nil {
				return err
			}
			r := stored.Reservation
			r.Version = version
			r.Fence = learning.AutomaticReservationFence{Generation: r.Fence.Generation + 1, ExpiresAt: now.Add(r.ClaimDuration)}
			if err = r.Validate(); err != nil {
				return fmt.Errorf("automaticstore: invalid discovery mutation: %w", err)
			}
			stored.Reservation = r
			doc.Records[id] = stored
			result = append(result, r)
		}
		return nil
	})
	if errors.Is(err, errNoExpiredReservations) {
		return nil, nil
	}
	return result, err
}

func (s *Store) Reassign(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion) (learning.AutomaticReservation, error) {
	now := s.clock.Now().UTC()
	return s.mutate(ctx, id, expected, func(stored entry) (entry, error) {
		r := stored.Reservation
		if r.Charge != learning.AutomaticChargeHeld {
			return entry{}, learning.ErrAutomaticReservationState
		}
		if now.IsZero() || now.Before(r.Fence.ExpiresAt) {
			return entry{}, learning.ErrAutomaticReservationFence
		}
		version, err := newVersion()
		if err != nil {
			return entry{}, err
		}
		r.Version = version
		r.Fence = learning.AutomaticReservationFence{Generation: r.Fence.Generation + 1, ExpiresAt: now.Add(r.ClaimDuration)}
		stored.Reservation = r
		return stored, nil
	})
}

func (s *Store) Retain(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion, fence learning.AutomaticReservationFence) (learning.AutomaticReservation, error) {
	return s.resolve(ctx, id, expected, fence, learning.AutomaticChargeRetained)
}

func (s *Store) Reclaim(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion, fence learning.AutomaticReservationFence) (learning.AutomaticReservation, error) {
	return s.resolve(ctx, id, expected, fence, learning.AutomaticChargeReclaimed)
}

func (s *Store) resolve(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion, fence learning.AutomaticReservationFence, disposition learning.AutomaticChargeDisposition) (learning.AutomaticReservation, error) {
	now := s.clock.Now().UTC()
	return s.mutate(ctx, id, expected, func(stored entry) (entry, error) {
		r := stored.Reservation
		if r.Charge != learning.AutomaticChargeHeld {
			return entry{}, learning.ErrAutomaticReservationState
		}
		if r.Fence != fence || !r.Fence.ValidAt(now) {
			return entry{}, learning.ErrAutomaticReservationFence
		}
		version, err := newVersion()
		if err != nil {
			return entry{}, err
		}
		r.Version, r.Charge, r.Fence = version, disposition, learning.AutomaticReservationFence{}
		r.AttemptCreated = disposition == learning.AutomaticChargeRetained
		stored.Reservation = r
		return stored, nil
	})
}

func (s *Store) mutate(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion, fn func(entry) (entry, error)) (learning.AutomaticReservation, error) {
	var result learning.AutomaticReservation
	err := s.transaction(ctx, true, func(doc *document) error {
		stored, ok := doc.Records[id]
		if !ok {
			return learning.ErrAutomaticReservationNotFound
		}
		if stored.Reservation.Version != expected {
			return learning.ErrAutomaticReservationVersion
		}
		updated, err := fn(stored)
		if err != nil {
			return err
		}
		if err = updated.Reservation.Validate(); err != nil {
			return fmt.Errorf("automaticstore: invalid mutation: %w", err)
		}
		doc.Records[id] = updated
		result = updated.Reservation
		return nil
	})
	return result, err
}
