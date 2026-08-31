package learning

//revive:disable:exported // attempt.go declares one closed public domain vocabulary

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

const (
	MaxAttemptIDBytes      = 96
	MaxAttemptVersionBytes = 128
	MaxAttemptLinkIDBytes  = 128
	MaxDurableRunIDBytes   = 256
	MaxAttemptCallerBytes  = 1024
)

var ErrInvalidAttempt = errors.New("learning: invalid attempt")

type AttemptID string
type AttemptVersion string
type DurableRunID string
type CanonicalDigest string
type ProvenanceBinding string
type AttemptGeneration uint64
type ClaimGeneration uint64

type AttemptState string

const (
	AttemptQueued    AttemptState = "queued"
	AttemptRunning   AttemptState = "running"
	AttemptCompleted AttemptState = "completed"
	AttemptFailed    AttemptState = "failed"
	AttemptAbandoned AttemptState = "abandoned"
)

func (s AttemptState) Valid() bool {
	switch s {
	case AttemptQueued, AttemptRunning, AttemptCompleted, AttemptFailed, AttemptAbandoned:
		return true
	default:
		return false
	}
}

func (s AttemptState) Terminal() bool {
	return s == AttemptCompleted || s == AttemptFailed || s == AttemptAbandoned
}

type AttemptOutcome string

const (
	AttemptOutcomeNone      AttemptOutcome = ""
	AttemptOutcomeSucceeded AttemptOutcome = "succeeded"
	AttemptOutcomeAbstained AttemptOutcome = "abstained"
	AttemptOutcomeFailed    AttemptOutcome = "failed"
	AttemptOutcomeAbandoned AttemptOutcome = "abandoned"
)

func (o AttemptOutcome) Valid() bool {
	switch o {
	case AttemptOutcomeNone, AttemptOutcomeSucceeded, AttemptOutcomeAbstained, AttemptOutcomeFailed, AttemptOutcomeAbandoned:
		return true
	default:
		return false
	}
}

type AttemptFailureCode string

const (
	FailureNone                AttemptFailureCode = ""
	FailureEvidenceUnavailable AttemptFailureCode = "evidence_unavailable"
	FailureEvaluationRejected  AttemptFailureCode = "evaluation_rejected"
	FailurePublicationFailed   AttemptFailureCode = "publication_failed"
	FailureClaimExpired        AttemptFailureCode = "claim_expired"
	FailureRetryExhausted      AttemptFailureCode = "retry_exhausted"
	FailureUnavailable         AttemptFailureCode = "unavailable"
	FailureInternal            AttemptFailureCode = "internal"
)

func (c AttemptFailureCode) Valid() bool {
	switch c {
	case FailureNone, FailureEvidenceUnavailable, FailureEvaluationRejected, FailurePublicationFailed,
		FailureClaimExpired, FailureRetryExhausted, FailureUnavailable, FailureInternal:
		return true
	default:
		return false
	}
}

type PromptOrigin string

const (
	PromptOriginCurrentPrincipal PromptOrigin = "current_principal"
	// PromptOriginSynthetic is an explicit invalid sentinel used by admission boundaries.
	PromptOriginSynthetic PromptOrigin = "synthetic"
)

func (o PromptOrigin) Valid() bool { return o == PromptOriginCurrentPrincipal }

type AttemptSource struct {
	SessionID       session.SessionID `json:"session_id"`
	RunID           DurableRunID      `json:"run_id"`
	CanonicalDigest CanonicalDigest   `json:"canonical_digest"`
}

type CurrentPromptBinding struct {
	Ordinal int             `json:"ordinal"`
	Digest  CanonicalDigest `json:"digest"`
	Origin  PromptOrigin    `json:"origin"`
}

// AdmissionProvenance is immutable after creation. Validate must be called at
// every persistence or worker boundary; Binding detects field substitution.
type AdmissionProvenance struct {
	Class         AdmissionClass       `json:"class"`
	Source        AttemptSource        `json:"source"`
	CurrentPrompt CurrentPromptBinding `json:"current_prompt"`
	Binding       ProvenanceBinding    `json:"binding"`
}

func NewAdmissionProvenance(class AdmissionClass, source AttemptSource, prompt CurrentPromptBinding) (AdmissionProvenance, error) {
	p := AdmissionProvenance{Class: class, Source: source, CurrentPrompt: prompt}
	if !validAdmissionBindingMaterial(class, source, prompt) {
		return AdmissionProvenance{}, fmt.Errorf("%w: admission provenance", ErrInvalidAttempt)
	}
	p.Binding = provenanceBinding(class, source, prompt)
	return p, nil
}

func (p AdmissionProvenance) Validate(expected AttemptSource, expectedPrompt CurrentPromptBinding) error {
	if !validAdmissionBindingMaterial(p.Class, p.Source, p.CurrentPrompt) || p.Source != expected || p.CurrentPrompt != expectedPrompt ||
		p.Binding != provenanceBinding(p.Class, p.Source, p.CurrentPrompt) {
		return fmt.Errorf("%w: admission provenance", ErrInvalidAttempt)
	}
	return nil
}

func (p AdmissionProvenance) valid() bool {
	return validAdmissionBindingMaterial(p.Class, p.Source, p.CurrentPrompt) && p.Binding == provenanceBinding(p.Class, p.Source, p.CurrentPrompt)
}

func validAdmissionBindingMaterial(class AdmissionClass, source AttemptSource, prompt CurrentPromptBinding) bool {
	return class.Valid() && class != AdmissionSkipped && source.Valid() && prompt.Ordinal >= 0 &&
		validDigest(prompt.Digest) && prompt.Origin.Valid()
}

func (s AttemptSource) Valid() bool {
	return validOpaque(string(s.SessionID), MaxAttemptLinkIDBytes) &&
		validOpaque(string(s.RunID), MaxDurableRunIDBytes) && validDigest(s.CanonicalDigest)
}

func provenanceBinding(class AdmissionClass, source AttemptSource, prompt CurrentPromptBinding) ProvenanceBinding {
	sum := digestParts(string(class), string(source.SessionID), string(source.RunID), string(source.CanonicalDigest), fmt.Sprint(prompt.Ordinal), string(prompt.Digest), string(prompt.Origin))
	return ProvenanceBinding(hex.EncodeToString(sum[:]))
}

// DeterministicAttemptID partitions attempts by caller and exact source while
// retaining only a one-way digest of that identity material.
func DeterministicAttemptID(caller string, source AttemptSource) (AttemptID, error) {
	if caller == "" || len(caller) > MaxAttemptCallerBytes || !utf8.ValidString(caller) || !source.Valid() {
		return "", fmt.Errorf("%w: identity material", ErrInvalidAttempt)
	}
	sum := digestParts(caller, string(source.SessionID), string(source.RunID), string(source.CanonicalDigest))
	return AttemptID("attempt-" + hex.EncodeToString(sum[:16])), nil
}

func digestParts(parts ...string) [sha256.Size]byte {
	h := sha256.New()
	var size [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(part))
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

type AttemptClaim struct {
	Generation ClaimGeneration `json:"generation"`
	ExpiresAt  time.Time       `json:"expires_at"`
}

func (c AttemptClaim) Valid() bool { return c.Generation > 0 && !c.ExpiresAt.IsZero() }

// ValidAt reports whether the claim is live at now. Expiry is exclusive so a
// successor may acquire at exactly ExpiresAt.
func (c AttemptClaim) ValidAt(now time.Time) bool {
	return c.Valid() && !now.IsZero() && now.Before(c.ExpiresAt)
}

type AttemptRecord struct {
	ID                AttemptID              `json:"id"`
	Version           AttemptVersion         `json:"version"`
	State             AttemptState           `json:"state"`
	Outcome           AttemptOutcome         `json:"outcome,omitempty"`
	FailureCode       AttemptFailureCode     `json:"failure_code,omitempty"`
	Provenance        AdmissionProvenance    `json:"provenance"`
	AttemptGeneration AttemptGeneration      `json:"attempt_generation"`
	ClaimGeneration   ClaimGeneration        `json:"claim_generation,omitempty"`
	ClaimExpiresAt    time.Time              `json:"claim_expires_at,omitempty"`
	CheckpointStage   AttemptCheckpointStage `json:"checkpoint_stage,omitempty"`
	ProposalID        ProposalID             `json:"proposal_id,omitempty"`
	SkillID           SkillID                `json:"skill_id,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

type AttemptProjection struct {
	ID                AttemptID              `json:"id"`
	Version           AttemptVersion         `json:"version"`
	State             AttemptState           `json:"state"`
	Outcome           AttemptOutcome         `json:"outcome,omitempty"`
	FailureCode       AttemptFailureCode     `json:"failure_code,omitempty"`
	Source            AttemptSource          `json:"source"`
	AttemptGeneration AttemptGeneration      `json:"attempt_generation"`
	ClaimGeneration   ClaimGeneration        `json:"claim_generation,omitempty"`
	ClaimExpiresAt    time.Time              `json:"claim_expires_at,omitempty"`
	CheckpointStage   AttemptCheckpointStage `json:"checkpoint_stage,omitempty"`
	ProposalID        ProposalID             `json:"proposal_id,omitempty"`
	SkillID           SkillID                `json:"skill_id,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

func ProjectAttempt(record AttemptRecord) (AttemptProjection, error) {
	if err := ValidateAttemptRecord(record); err != nil {
		return AttemptProjection{}, err
	}
	return AttemptProjection{
		ID: record.ID, Version: record.Version, State: record.State, Outcome: record.Outcome,
		FailureCode: record.FailureCode, Source: record.Provenance.Source,
		AttemptGeneration: record.AttemptGeneration, ClaimGeneration: record.ClaimGeneration,
		ClaimExpiresAt: record.ClaimExpiresAt, CheckpointStage: record.CheckpointStage,
		ProposalID: record.ProposalID, SkillID: record.SkillID,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}

// ValidateAttemptRecord validates the complete bounded persistence shape.
//
//nolint:gocyclo // the closed state/outcome matrix is clearest in one validation boundary
func ValidateAttemptRecord(record AttemptRecord) error {
	invalid := func() error { return fmt.Errorf("%w: record", ErrInvalidAttempt) }
	if !validOpaque(string(record.ID), MaxAttemptIDBytes) || !strings.HasPrefix(string(record.ID), "attempt-") ||
		!validOpaque(string(record.Version), MaxAttemptVersionBytes) || !record.State.Valid() || !record.Outcome.Valid() ||
		!record.FailureCode.Valid() || !validStoredCheckpoint(record.CheckpointStage, record.ProposalID, record.SkillID) ||
		record.AttemptGeneration == 0 || record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) ||
		!validOptionalAttemptLink(string(record.ProposalID)) || !validOptionalAttemptLink(string(record.SkillID)) ||
		!record.Provenance.valid() {
		return invalid()
	}
	if (record.State == AttemptRunning) != (record.ClaimGeneration > 0) ||
		(record.ClaimGeneration == 0) != record.ClaimExpiresAt.IsZero() {
		return invalid()
	}
	switch record.State {
	case AttemptQueued, AttemptRunning:
		if record.Outcome != AttemptOutcomeNone || record.FailureCode != FailureNone {
			return invalid()
		}
	case AttemptCompleted:
		if record.Outcome != AttemptOutcomeSucceeded && record.Outcome != AttemptOutcomeAbstained || record.FailureCode != FailureNone {
			return invalid()
		}
	case AttemptFailed:
		if record.Outcome != AttemptOutcomeFailed || record.FailureCode == FailureNone {
			return invalid()
		}
	case AttemptAbandoned:
		if record.Outcome != AttemptOutcomeAbandoned || record.FailureCode != FailureNone {
			return invalid()
		}
	}
	return nil
}

func validStoredCheckpoint(stage AttemptCheckpointStage, proposalID ProposalID, skillID SkillID) bool {
	if !stage.Valid() {
		return false
	}
	switch stage {
	case AttemptCheckpointNone, AttemptCheckpointEvidenceVerified, AttemptCheckpointReflectionComplete:
		return proposalID == "" && skillID == ""
	case AttemptCheckpointProposalLinked:
		return proposalID != "" && skillID == ""
	case AttemptCheckpointSkillLinked:
		return skillID != ""
	default:
		return false
	}
}

func validDigest(value CanonicalDigest) bool {
	text := string(value)
	if len(text) != sha256.Size*2 || strings.ToLower(text) != text {
		return false
	}
	_, err := hex.DecodeString(text)
	return err == nil
}

func validOptionalAttemptLink(value string) bool {
	return value == "" || validOpaque(value, MaxAttemptLinkIDBytes)
}

func validOpaque(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}
