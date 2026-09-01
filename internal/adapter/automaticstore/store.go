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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/learning"
)

const (
	documentName = "automatic-admission.json"
	lockName     = "automatic-admission.lock"
	documentType = "mecatl-automatic-admission/1"
	maxDocument  = 16 << 20
	lockRetry    = 5 * time.Millisecond
	lockTimeout  = 5 * time.Second
)

type entry struct {
	Reservation learning.AutomaticReservation     `json:"reservation"`
	Policy      learning.AutomaticAdmissionPolicy `json:"policy"`
}

type document struct {
	Format  string                                    `json:"format"`
	Records map[learning.AutomaticReservationID]entry `json:"records"`
}

// Store is a durable learning.AutomaticAdmissionLedger for cooperating
// processes on one host filesystem. Each operation reloads under flock; no
// process-local cache participates in admission decisions.
type Store struct {
	mu   sync.Mutex
	dir  string
	path string
	lock *flock.Flock
}

var _ learning.AutomaticAdmissionLedger = (*Store)(nil)

// New prepares a ledger rooted at dir.
func New(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("automaticstore: directory required")
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
	return &Store{dir: abs, path: filepath.Join(abs, documentName), lock: flock.New(filepath.Join(abs, lockName))}, nil
}

func emptyDocument() document {
	return document{Format: documentType, Records: make(map[learning.AutomaticReservationID]entry)}
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
		return emptyDocument(), nil
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
	return doc, nil
}

func validateDocument(doc document) error {
	if doc.Format != documentType || doc.Records == nil {
		return errors.New("automaticstore: invalid ledger document")
	}
	for id, stored := range doc.Records {
		if id != stored.Reservation.ID || stored.Reservation.Validate() != nil || stored.Policy.Validate() != nil {
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
	if err := req.Validate(); err != nil || len(req.Principal) > 128 {
		return learning.AutomaticReservation{}, learning.ErrInvalidAutomaticReservation
	}
	var result learning.AutomaticReservation
	err := s.transaction(ctx, true, func(doc *document) error {
		pruneResolved(doc, req.Now)
		if stored, ok := doc.Records[req.ID]; ok {
			if sameImmutable(stored, req) {
				result = stored.Reservation
				return nil
			}
			return learning.ErrAutomaticReservationConflict
		}
		if err := admissionError(*doc, req); err != nil {
			return err
		}
		version, err := newVersion()
		if err != nil {
			return err
		}
		result = learning.AutomaticReservation{
			ID: req.ID, AttemptID: req.AttemptID, Version: version, Principal: req.Principal,
			Digest: req.Digest, Class: req.Class, Tokens: req.Tokens, Charge: learning.AutomaticChargeHeld,
			ReservedAt: req.Now, ChargeExpiresAt: req.Now.Add(req.Policy.Window), DedupeExpiresAt: req.Now.Add(req.Policy.DedupeWindow),
			ClaimDuration: req.Policy.ReservationClaimDuration,
			Fence:         learning.AutomaticReservationFence{Generation: 1, ExpiresAt: req.Now.Add(req.Policy.ReservationClaimDuration)},
		}
		doc.Records[req.ID] = entry{Reservation: result, Policy: req.Policy}
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

func sameImmutable(stored entry, req learning.AutomaticReservationRequest) bool {
	r := stored.Reservation
	return r.ID == req.ID && r.AttemptID == req.AttemptID && r.Principal == req.Principal && r.Digest == req.Digest &&
		r.Class == req.Class && r.Tokens == req.Tokens && stored.Policy == req.Policy
}

func admissionError(doc document, req learning.AutomaticReservationRequest) error {
	var globalCount, globalTokens, principalCount, principalTokens uint64
	for _, stored := range doc.Records {
		r := stored.Reservation
		if r.Charge == learning.AutomaticChargeReclaimed {
			continue
		}
		if req.Now.Before(r.DedupeExpiresAt) && r.Digest == req.Digest {
			return learning.ErrAutomaticAdmissionDuplicate
		}
		if req.Class == learning.AdmissionWeighted && r.Principal == req.Principal && req.Now.Before(r.ReservedAt.Add(stored.Policy.Cooldown)) {
			return learning.ErrAutomaticAdmissionCooldown
		}
		if !req.Now.Before(r.ChargeExpiresAt) {
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
	if globalCount >= req.Policy.MaxCount || globalTokens >= req.Policy.MaxTokens || req.Tokens > req.Policy.MaxTokens-globalTokens ||
		principalCount >= req.Policy.MaxCountPerPrincipal || principalTokens >= req.Policy.MaxTokensPerPrincipal || req.Tokens > req.Policy.MaxTokensPerPrincipal-principalTokens {
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

func (s *Store) Reassign(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion, now, expiresAt time.Time) (learning.AutomaticReservation, error) {
	return s.mutate(ctx, id, expected, func(stored entry) (entry, error) {
		r := stored.Reservation
		if r.Charge != learning.AutomaticChargeHeld {
			return entry{}, learning.ErrAutomaticReservationState
		}
		if now.Before(r.Fence.ExpiresAt) || expiresAt != now.Add(r.ClaimDuration) {
			return entry{}, learning.ErrAutomaticReservationFence
		}
		version, err := newVersion()
		if err != nil {
			return entry{}, err
		}
		r.Version = version
		r.Fence = learning.AutomaticReservationFence{Generation: r.Fence.Generation + 1, ExpiresAt: expiresAt}
		stored.Reservation = r
		return stored, nil
	})
}

func (s *Store) Retain(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion, fence learning.AutomaticReservationFence, now time.Time) (learning.AutomaticReservation, error) {
	return s.resolve(ctx, id, expected, fence, now, learning.AutomaticChargeRetained)
}

func (s *Store) Reclaim(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion, fence learning.AutomaticReservationFence, now time.Time) (learning.AutomaticReservation, error) {
	return s.resolve(ctx, id, expected, fence, now, learning.AutomaticChargeReclaimed)
}

func (s *Store) resolve(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion, fence learning.AutomaticReservationFence, now time.Time, disposition learning.AutomaticChargeDisposition) (learning.AutomaticReservation, error) {
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
