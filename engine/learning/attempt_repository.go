package learning

//revive:disable:exported // repository.go declares one closed public storage contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultAttemptPageSize = 50
	MaxAttemptPageSize     = 200
	MaxAttemptDeleteBatch  = 200
	MaxAttemptWorkBatch    = 200
	// MaxAttemptsPerPartition bounds one caller's durable attempt records. Create
	// evicts the oldest terminal record at this boundary, but never queued or
	// running work; a partition containing only nonterminal records is saturated.
	MaxAttemptsPerPartition = 256
)

var (
	ErrAttemptNotFound        = errors.New("learning: attempt not found")
	ErrAttemptCreateConflict  = errors.New("learning: attempt create conflict")
	ErrAttemptVersionConflict = errors.New("learning: attempt version conflict")
	ErrAttemptTransition      = errors.New("learning: invalid attempt transition")
	ErrAttemptClaimConflict   = errors.New("learning: attempt has a live claim")
	ErrAttemptClaimLost       = errors.New("learning: attempt claim expired or superseded")
	ErrAttemptQuotaExceeded   = errors.New("learning: attempt partition quota exceeded")
)

// AttemptPartition is an opaque, one-way owner partition. Repositories must not
// persist or return the identity material from which it was derived.
type AttemptPartition string

// DeriveAttemptPartition creates the repository partition without retaining a
// principal value.
func DeriveAttemptPartition(identity string) (AttemptPartition, error) {
	if identity == "" || len(identity) > MaxAttemptCallerBytes || !utf8.ValidString(identity) {
		return "", fmt.Errorf("%w: partition identity", ErrInvalidAttempt)
	}
	sum := sha256.Sum256([]byte(identity))
	return AttemptPartition(hex.EncodeToString(sum[:])), nil
}

type AttemptCreate struct {
	ID         AttemptID
	Provenance AdmissionProvenance
}

func (c AttemptCreate) Validate() error {
	if !validOpaque(string(c.ID), MaxAttemptIDBytes) || !strings.HasPrefix(string(c.ID), "attempt-") || !c.Provenance.valid() {
		return fmt.Errorf("%w: create", ErrInvalidAttempt)
	}
	return nil
}

type AttemptCheckpointStage string

const (
	AttemptCheckpointNone               AttemptCheckpointStage = ""
	AttemptCheckpointEvidenceVerified   AttemptCheckpointStage = "evidence_verified"
	AttemptCheckpointReflectionComplete AttemptCheckpointStage = "reflection_complete"
	AttemptCheckpointProposalLinked     AttemptCheckpointStage = "proposal_linked"
	AttemptCheckpointSkillLinked        AttemptCheckpointStage = "skill_linked"
)

func (s AttemptCheckpointStage) Valid() bool {
	switch s {
	case AttemptCheckpointNone, AttemptCheckpointEvidenceVerified, AttemptCheckpointReflectionComplete,
		AttemptCheckpointProposalLinked, AttemptCheckpointSkillLinked:
		return true
	default:
		return false
	}
}

func (s AttemptCheckpointStage) rank() int {
	switch s {
	case AttemptCheckpointNone:
		return 0
	case AttemptCheckpointEvidenceVerified:
		return 1
	case AttemptCheckpointReflectionComplete:
		return 2
	case AttemptCheckpointProposalLinked:
		return 3
	case AttemptCheckpointSkillLinked:
		return 4
	default:
		return -1
	}
}

// CanAdvanceTo permits idempotent replay of a committed checkpoint and forward
// progress, but never rollback.
func (s AttemptCheckpointStage) CanAdvanceTo(next AttemptCheckpointStage) bool {
	return s.Valid() && next.Valid() && next != AttemptCheckpointNone && next.rank() >= s.rank()
}

type AttemptCheckpoint struct {
	Stage      AttemptCheckpointStage
	ProposalID ProposalID
	SkillID    SkillID
}

func (c AttemptCheckpoint) Validate() error {
	if !c.Stage.Valid() || c.Stage == AttemptCheckpointNone ||
		!validOptionalAttemptLink(string(c.ProposalID)) ||
		!validOptionalAttemptLink(string(c.SkillID)) {
		return fmt.Errorf("%w: checkpoint", ErrInvalidAttempt)
	}
	switch c.Stage {
	case AttemptCheckpointEvidenceVerified, AttemptCheckpointReflectionComplete:
		if c.ProposalID != "" || c.SkillID != "" {
			return fmt.Errorf("%w: checkpoint links", ErrInvalidAttempt)
		}
	case AttemptCheckpointProposalLinked:
		if c.ProposalID == "" || c.SkillID != "" {
			return fmt.Errorf("%w: proposal checkpoint", ErrInvalidAttempt)
		}
	case AttemptCheckpointSkillLinked:
		if c.SkillID == "" {
			return fmt.Errorf("%w: skill checkpoint", ErrInvalidAttempt)
		}
	}
	return nil
}

type AttemptFinalization struct {
	State       AttemptState
	Outcome     AttemptOutcome
	FailureCode AttemptFailureCode
}

func (f AttemptFinalization) Validate() error {
	valid := false
	switch f.State {
	case AttemptCompleted:
		valid = (f.Outcome == AttemptOutcomeSucceeded || f.Outcome == AttemptOutcomeAbstained) && f.FailureCode == FailureNone
	case AttemptFailed:
		valid = f.Outcome == AttemptOutcomeFailed && f.FailureCode.Valid() && f.FailureCode != FailureNone
	case AttemptAbandoned:
		valid = f.Outcome == AttemptOutcomeAbandoned && f.FailureCode == FailureNone
	}
	if !valid {
		return fmt.Errorf("%w: finalization", ErrAttemptTransition)
	}
	return nil
}

type AttemptList struct {
	After AttemptID
	Limit int
	State AttemptState
}

func (q AttemptList) Validate() error {
	if q.Limit < 0 || q.Limit > MaxAttemptPageSize || (q.After != "" && !validOpaque(string(q.After), MaxAttemptIDBytes)) || (q.State != "" && !q.State.Valid()) {
		return fmt.Errorf("%w: list", ErrInvalidAttempt)
	}
	return nil
}

type AttemptPage struct {
	Records []AttemptRecord
	Next    AttemptID
}

// AttemptWork identifies one repository-authoritative queued attempt or running
// attempt whose claim has expired. Partition is opaque infrastructure routing
// metadata and must never be projected to callers.
type AttemptWork struct {
	Partition AttemptPartition
	Record    AttemptRecord
}

type AttemptWorkCursor struct {
	Partition AttemptPartition
	ID        AttemptID
}

type AttemptWorkList struct {
	After AttemptWorkCursor
	Now   time.Time
	Limit int
}

func (q AttemptWorkList) Validate() error {
	cursorEmpty := q.After == (AttemptWorkCursor{})
	cursorValid := q.After.Partition != "" && validOpaque(string(q.After.Partition), 128) && validOpaque(string(q.After.ID), MaxAttemptIDBytes)
	if q.Now.IsZero() || q.Limit < 1 || q.Limit > MaxAttemptWorkBatch || (!cursorEmpty && !cursorValid) {
		return fmt.Errorf("%w: work list", ErrInvalidAttempt)
	}
	return nil
}

type AttemptWorkPage struct {
	Work []AttemptWork
	Next AttemptWorkCursor
}

// ValidAttemptTransition is the closed lifecycle table. Same-state running is
// reserved for claim renewal, successor acquisition, and checkpoints. Failed
// attempts may be retried; completed and abandoned attempts are immutable.
func ValidAttemptTransition(from, to AttemptState) bool {
	switch from {
	case AttemptQueued:
		return to == AttemptRunning || to == AttemptAbandoned
	case AttemptRunning:
		return to == AttemptRunning || to == AttemptQueued || to.Terminal()
	case AttemptFailed:
		return to == AttemptQueued
	default:
		return false
	}
}

// AttemptRepository is the authoritative storage-neutral attempt lifecycle.
//
// Every mutation is atomic. expected is an opaque CAS version from the latest
// returned record; stale values return ErrAttemptVersionConflict without a
// write. Create is idempotent for the same partition, ID, and immutable
// provenance and returns the existing record; an identity collision with
// different immutable material returns ErrAttemptCreateConflict. Each partition
// retains at most MaxAttemptsPerPartition records. Create at that boundary
// evicts only the oldest terminal record (UpdatedAt, then ID); if none exists it
// returns ErrAttemptQuotaExceeded without changing queued or running work. The
// limit and cleanup are partition-local, so saturation cannot block a peer.
//
// AcquireClaim accepts queued attempts or running attempts whose claim is
// expired at now. It allocates a strictly newer ClaimGeneration and returns the
// exact claim required by RenewClaim, Checkpoint, ReleaseClaim, and Finalize.
// Those operations must atomically match expected, claim generation, and an
// expiry strictly after now; otherwise they return ErrAttemptClaimLost without
// a write. AcquireClaim on a live claim returns ErrAttemptClaimConflict.
// ReleaseClaim returns running work to queued and clears the lease. Retry is
// failed-to-queued only, increments AttemptGeneration, and clears the claim.
// Abandon follows ValidAttemptTransition and must reject a live claim.
//
// Checkpoint is monotonic by AttemptCheckpointStage. Replaying the same stage
// and links is permitted with the current version; rollback or changing links
// at a committed stage returns ErrAttemptTransition. Finalize accepts only
// AttemptFinalization.Validate terminal shapes and clears the claim.
//
// Delete requires a terminal attempt or an unclaimed queued attempt. It must
// reject every claimed nonterminal attempt, including an expired claim, until
// a successor acquisition/release/abandon transition resolves it.
// DeleteTerminalBefore removes at most limit terminal records older than before
// from exactly one partition; limit must be in [1, MaxAttemptDeleteBatch].
// DiscoverWork returns one bounded page of queued attempts and running attempts
// whose claim expires at or before query.Now, ordered by opaque partition then ID.
// Its cursor is an exclusive, disposable scan position; it grants no authority.
// This is the infrastructure worker's durable discovery seam, not a caller list
// or watch API.
type AttemptRepository interface {
	Create(context.Context, AttemptPartition, AttemptCreate) (AttemptRecord, error)
	Get(context.Context, AttemptPartition, AttemptID) (AttemptRecord, bool, error)
	List(context.Context, AttemptPartition, AttemptList) (AttemptPage, error)
	DiscoverWork(context.Context, AttemptWorkList) (AttemptWorkPage, error)
	AcquireClaim(context.Context, AttemptPartition, AttemptID, AttemptVersion, time.Time, time.Time) (AttemptRecord, AttemptClaim, error)
	RenewClaim(context.Context, AttemptPartition, AttemptID, AttemptVersion, AttemptClaim, time.Time, time.Time) (AttemptRecord, AttemptClaim, error)
	Checkpoint(context.Context, AttemptPartition, AttemptID, AttemptVersion, AttemptClaim, time.Time, AttemptCheckpoint) (AttemptRecord, error)
	ReleaseClaim(context.Context, AttemptPartition, AttemptID, AttemptVersion, AttemptClaim, time.Time) (AttemptRecord, error)
	Finalize(context.Context, AttemptPartition, AttemptID, AttemptVersion, AttemptClaim, time.Time, AttemptFinalization) (AttemptRecord, error)
	Retry(context.Context, AttemptPartition, AttemptID, AttemptVersion, time.Time) (AttemptRecord, error)
	Abandon(context.Context, AttemptPartition, AttemptID, AttemptVersion, time.Time) (AttemptRecord, error)
	Delete(context.Context, AttemptPartition, AttemptID, AttemptVersion, time.Time) error
	DeleteTerminalBefore(context.Context, AttemptPartition, time.Time, int) (int, error)
}
