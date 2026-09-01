package learning

//revive:disable:exported // this file declares one closed public storage contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const MaxAutomaticReservationIDBytes = 96

var (
	ErrInvalidAutomaticReservation  = errors.New("learning: invalid automatic reservation")
	ErrAutomaticAdmissionDuplicate  = errors.New("learning: automatic admission duplicate")
	ErrAutomaticAdmissionCooldown   = errors.New("learning: automatic admission cooldown")
	ErrAutomaticAdmissionLimit      = errors.New("learning: automatic admission limit")
	ErrAutomaticReservationConflict = errors.New("learning: automatic reservation identity conflict")
	ErrAutomaticReservationNotFound = errors.New("learning: automatic reservation not found")
	ErrAutomaticReservationVersion  = errors.New("learning: automatic reservation version conflict")
	ErrAutomaticReservationFence    = errors.New("learning: automatic reservation fence lost")
	ErrAutomaticReservationState    = errors.New("learning: invalid automatic reservation transition")
)

type AutomaticReservationID string
type AutomaticReservationVersion string
type AutomaticReservationGeneration uint64

// AutomaticReservationIDForAttempt derives the sole reservation identity from
// the durable attempt identity. Retries and replicas therefore converge before
// either the ledger or attempt repository is mutated.
func AutomaticReservationIDForAttempt(attemptID AttemptID) (AutomaticReservationID, error) {
	if !validOpaque(string(attemptID), MaxAttemptIDBytes) || !strings.HasPrefix(string(attemptID), "attempt-") {
		return "", fmt.Errorf("%w: attempt id", ErrInvalidAutomaticReservation)
	}
	sum := sha256.Sum256([]byte(attemptID))
	return AutomaticReservationID("reservation-" + hex.EncodeToString(sum[:16])), nil
}

// AutomaticAdmissionPolicy is supplied to the atomic Reserve operation. All
// replicas sharing a ledger must use the same policy. Zero limits are closed,
// not unlimited.
type AutomaticAdmissionPolicy struct {
	Window                   time.Duration
	Cooldown                 time.Duration
	DedupeWindow             time.Duration
	MaxCount                 uint64
	MaxTokens                uint64
	MaxCountPerPrincipal     uint64
	MaxTokensPerPrincipal    uint64
	ReservationClaimDuration time.Duration
}

func (p AutomaticAdmissionPolicy) Validate() error {
	if p.Window <= 0 || p.Cooldown < 0 || p.DedupeWindow < p.Window ||
		p.MaxCount == 0 || p.MaxTokens == 0 || p.MaxCountPerPrincipal == 0 || p.MaxTokensPerPrincipal == 0 ||
		p.ReservationClaimDuration <= 0 || p.ReservationClaimDuration > p.Window {
		return fmt.Errorf("%w: policy", ErrInvalidAutomaticReservation)
	}
	return nil
}

// AutomaticReservationRequest is bounded, content-free admission material.
// Principal is an opaque one-way partition and Digest is the canonical input
// digest. Only weighted and genuine-current-user hard decisions are automatic;
// host-requested reflection remains an explicit path outside this ledger.
type AutomaticReservationRequest struct {
	ID        AutomaticReservationID
	AttemptID AttemptID
	Principal AttemptPartition
	Digest    CanonicalDigest
	Class     AdmissionClass
	Tokens    uint64
	Now       time.Time
	Policy    AutomaticAdmissionPolicy
}

func (r AutomaticReservationRequest) Validate() error {
	expected, err := AutomaticReservationIDForAttempt(r.AttemptID)
	if err != nil || r.ID != expected || r.Principal == "" || !validDigest(r.Digest) ||
		(r.Class != AdmissionWeighted && r.Class != AdmissionHard) || r.Tokens == 0 || r.Now.IsZero() ||
		r.Policy.Validate() != nil || r.Tokens > r.Policy.MaxTokens || r.Tokens > r.Policy.MaxTokensPerPrincipal {
		return fmt.Errorf("%w: request", ErrInvalidAutomaticReservation)
	}
	return nil
}

type AutomaticChargeDisposition string

const (
	AutomaticChargeHeld      AutomaticChargeDisposition = "held"
	AutomaticChargeRetained  AutomaticChargeDisposition = "retained"
	AutomaticChargeReclaimed AutomaticChargeDisposition = "reclaimed"
)

func (d AutomaticChargeDisposition) Valid() bool {
	return d == AutomaticChargeHeld || d == AutomaticChargeRetained || d == AutomaticChargeReclaimed
}

// AutomaticReservationFence is the opaque ownership token for resolving a
// reservation. Expiry is exclusive; reassignment mints a newer generation.
type AutomaticReservationFence struct {
	Generation AutomaticReservationGeneration
	ExpiresAt  time.Time
}

func (f AutomaticReservationFence) Valid() bool {
	return f.Generation > 0 && !f.ExpiresAt.IsZero()
}

func (f AutomaticReservationFence) ValidAt(now time.Time) bool {
	return f.Valid() && !now.IsZero() && now.Before(f.ExpiresAt)
}

// AutomaticReservation records one count charge and Tokens token charges.
// ChargeExpiresAt bounds budget accounting; DedupeExpiresAt may outlive it.
// AttemptCreated is monotonic and requires ChargeRetained.
type AutomaticReservation struct {
	ID              AutomaticReservationID
	AttemptID       AttemptID
	Version         AutomaticReservationVersion
	Principal       AttemptPartition
	Digest          CanonicalDigest
	Class           AdmissionClass
	Tokens          uint64
	Charge          AutomaticChargeDisposition
	AttemptCreated  bool
	ReservedAt      time.Time
	ChargeExpiresAt time.Time
	DedupeExpiresAt time.Time
	ClaimDuration   time.Duration
	Fence           AutomaticReservationFence
}

func (r AutomaticReservation) Validate() error {
	expected, err := AutomaticReservationIDForAttempt(r.AttemptID)
	if err != nil || !r.validIdentity(expected) || !r.validAccounting() {
		return fmt.Errorf("%w: record", ErrInvalidAutomaticReservation)
	}
	if !r.validChargeState() {
		return fmt.Errorf("%w: %s record", ErrInvalidAutomaticReservation, r.Charge)
	}
	return nil
}

func (r AutomaticReservation) validIdentity(expected AutomaticReservationID) bool {
	return r.ID == expected && validOpaque(string(r.Version), MaxAttemptVersionBytes) &&
		r.Principal != "" && validDigest(r.Digest) &&
		(r.Class == AdmissionWeighted || r.Class == AdmissionHard)
}

func (r AutomaticReservation) validAccounting() bool {
	return r.Tokens > 0 && r.Charge.Valid() && !r.ReservedAt.IsZero() &&
		r.ChargeExpiresAt.After(r.ReservedAt) && r.DedupeExpiresAt.After(r.ReservedAt) &&
		!r.DedupeExpiresAt.Before(r.ChargeExpiresAt) && r.ClaimDuration > 0 &&
		r.ClaimDuration <= r.ChargeExpiresAt.Sub(r.ReservedAt)
}

func (r AutomaticReservation) validChargeState() bool {
	switch r.Charge {
	case AutomaticChargeHeld:
		return !r.AttemptCreated && r.Fence.Valid()
	case AutomaticChargeRetained:
		return r.AttemptCreated && !r.Fence.Valid()
	case AutomaticChargeReclaimed:
		return !r.AttemptCreated && !r.Fence.Valid()
	default:
		return false
	}
}

// AutomaticAdmissionLedger is the authoritative storage-neutral accounting
// seam for automatic learning admission. It is accounting and deduplication,
// not a work queue; admitted work enters AttemptRepository's existing queue.
//
// Reserve atomically applies one global and one Principal-scoped sliding count
// and token window, global Digest deduplication, and Principal cooldown. A hard
// request bypasses cooldown only; it remains subject to dedupe and every budget.
// Same-ID retries with identical immutable material are idempotent. Changed
// immutable material returns ErrAutomaticReservationConflict. A different ID
// with a live dedupe digest returns ErrAutomaticAdmissionDuplicate. Limit and
// cooldown refusals return ErrAutomaticAdmissionLimit and
// ErrAutomaticAdmissionCooldown without creating a record.
//
// Every mutation atomically matches expected and the current fence. Stale CAS
// returns ErrAutomaticReservationVersion; expired or superseded ownership
// returns ErrAutomaticReservationFence, both without a write. Reassign accepts
// only a held reservation at or after fence expiry, requires expiresAt to equal
// now plus the original ClaimDuration, mints a strictly newer generation, and
// never adds a second count/token charge. Retain records that
// the linked attempt was created and makes its charge non-reclaimable; later
// attempt failure, timeout, or abandonment does not refund it. Reclaim is valid
// only before attempt creation and releases count, token, cooldown, and dedupe
// effects atomically. Thus a successor first reassigns an expired uncertain
// reservation, checks AttemptRepository, then retains or reclaims exactly once.
// Natural charge and dedupe expiry are evaluated against caller-supplied now by
// Reserve, so implementations need no process-local sweeper.
type AutomaticAdmissionLedger interface {
	Reserve(context.Context, AutomaticReservationRequest) (AutomaticReservation, error)
	Get(context.Context, AutomaticReservationID) (AutomaticReservation, bool, error)
	Reassign(context.Context, AutomaticReservationID, AutomaticReservationVersion, time.Time, time.Time) (AutomaticReservation, error)
	Retain(context.Context, AutomaticReservationID, AutomaticReservationVersion, AutomaticReservationFence, time.Time) (AutomaticReservation, error)
	Reclaim(context.Context, AutomaticReservationID, AutomaticReservationVersion, AutomaticReservationFence, time.Time) (AutomaticReservation, error)
}
