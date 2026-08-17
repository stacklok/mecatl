package learning

//revive:disable:exported // skill.go declares the closed public lifecycle vocabulary as one unit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxSkillsPerPartition       = 1024
	MaxSkillVersionsPerSkill    = 32
	MaxSkillReceipts            = 32
	MaxSkillReceiptHistory      = 32768
	MaxSkillEvaluations         = 16
	MaxSkillProposals           = 16
	MaxSkillEvidence            = 32
	MaxSkillSignals             = 16
	MaxSkillFixtures            = 32
	MaxSkillDescriptionBytes    = 1024
	MaxSkillBodyBytes           = 32 << 10
	MaxSkillEvaluationTextBytes = 4096
	MaxSkillOwnerBytes          = 256
	DefaultSkillPageSize        = 50
	MaxSkillPageSize            = 200
)

type SkillID string
type VersionID string
type Revision string
type SkillState string

const (
	SkillDraft     SkillState = "draft"
	SkillEvaluated SkillState = "evaluated"
	SkillStaged    SkillState = "staged"
	SkillActive    SkillState = "active"
	SkillArchived  SkillState = "archived"
	SkillRejected  SkillState = "rejected"
)

func (s SkillState) Valid() bool {
	switch s {
	case SkillDraft, SkillEvaluated, SkillStaged, SkillActive, SkillArchived, SkillRejected:
		return true
	default:
		return false
	}
}

type EvaluationVerdict string

const (
	EvaluationPass    EvaluationVerdict = "pass"
	EvaluationFail    EvaluationVerdict = "fail"
	EvaluationAbstain EvaluationVerdict = "abstain"
	// EvaluationError is the durable, non-activatable marker for an evaluator
	// infrastructure failure. It carries no provider error detail.
	EvaluationError EvaluationVerdict = "error"
)

func (v EvaluationVerdict) Valid() bool {
	return v == EvaluationPass || v == EvaluationFail || v == EvaluationAbstain || v == EvaluationError
}

type SkillPartition struct {
	Principal string `json:"principal"`
	Project   string `json:"project,omitempty"`
}

type SkillBundle struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

type SkillProvenanceOrigin string

const (
	// SkillProvenanceLegacyModel marks an explicitly imported origin:model quarantine draft.
	// It is the sole provenance allowed without proposal, evidence, or signal linkage.
	SkillProvenanceLegacyModel SkillProvenanceOrigin = "legacy_model"
)

type SkillProvenance struct {
	Origin                SkillProvenanceOrigin `json:"origin,omitempty"`
	ValidationDisposition ValidationDisposition `json:"validation_disposition,omitempty"`
	ProposalIDs           []ProposalID          `json:"proposal_ids,omitempty"`
	EvidenceRefs          []EvidenceRef         `json:"evidence_refs,omitempty"`
	Signals               []Signal              `json:"signals,omitempty"`
}

type SkillEvaluation struct {
	Verdict    EvaluationVerdict `json:"verdict"`
	FixtureIDs []string          `json:"fixture_ids,omitempty"`
	Baseline   string            `json:"baseline,omitempty"`
	Treatment  string            `json:"treatment,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	At         time.Time         `json:"at"`
}

type SkillReceipt struct {
	Operation string     `json:"operation"`
	From      SkillState `json:"from"`
	To        SkillState `json:"to"`
	Version   VersionID  `json:"version"`
	At        time.Time  `json:"at"`
}

type SkillReceiptRecord struct {
	ID         string
	SkillID    SkillID
	Name       string
	OwnerAgent string
	Version    VersionID
	Receipt    SkillReceipt
}

type SkillReceiptList struct {
	After string
	Limit int
}

type SkillReceiptPage struct {
	Records []SkillReceiptRecord
	Next    string
}

// SkillReceiptRepository is the optional bounded durable lifecycle-history seam.
// Cursors are opaque and stale/invalid cursors fail with ErrSkillCursor.
type SkillReceiptRepository interface {
	ListSkillReceipts(context.Context, SkillPartition, SkillReceiptList) (SkillReceiptPage, error)
}

// SkillVersion is an immutable body revision plus CAS-controlled lifecycle metadata.
// Bundle, Version, Supersedes, OwnerAgent, Partition, and CreatedAt never change.
type SkillVersion struct {
	ID          SkillID               `json:"id"`
	Version     VersionID             `json:"version"`
	Revision    Revision              `json:"revision"`
	State       SkillState            `json:"state"`
	OwnerAgent  string                `json:"owner_agent"`
	Partition   SkillPartition        `json:"partition"`
	Bundle      SkillBundle           `json:"bundle"`
	Provenance  SkillProvenance       `json:"provenance"`
	Disposition ValidationDisposition `json:"validation_disposition,omitempty"`
	Evaluations []SkillEvaluation     `json:"evaluations,omitempty"`
	Receipts    []SkillReceipt        `json:"receipts,omitempty"`
	Supersedes  VersionID             `json:"supersedes,omitempty"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
}

type SkillList struct {
	After      SkillID
	Limit      int
	Name       string
	State      SkillState
	OwnerAgent string
}

type SkillPage struct {
	Versions []SkillVersion
	Next     SkillID
}

type SkillEvaluationRequest struct {
	Partition  SkillPartition
	OwnerAgent string
	Version    SkillVersion
}

// SkillDraftInput is a validated body-only draft ready for repository creation.
type SkillDraftInput struct {
	Partition  SkillPartition
	OwnerAgent string
	Bundle     SkillBundle
	Provenance SkillProvenance
}

type SkillValidator interface {
	Validate(context.Context, SkillValidationRequest) (SkillValidation, error)
}

// SkillEvaluator is trusted host admission control. ABSTAIN is a deliberate,
// validated-eligible decision; infrastructure failures must return an error and
// are persisted by the lifecycle as the distinct, non-activatable ERROR verdict.
type SkillEvaluator interface {
	Evaluate(context.Context, SkillEvaluationRequest) (SkillEvaluation, error)
}

// ValidatedSkillActivator is an optional repository capability for the lower-
// assurance automatic path. Implementations must atomically enforce evidence,
// exact/accepted validation, ABSTAIN, staged state, ownership, partition, and CAS.
type ValidatedSkillActivator interface {
	ActivateValidated(context.Context, SkillPartition, string, SkillID, VersionID, Revision) (SkillVersion, error)
}

type SkillRepository interface {
	CreateDraft(context.Context, SkillPartition, string, SkillBundle, SkillProvenance) (SkillVersion, error)
	Get(context.Context, SkillPartition, string, SkillID, VersionID) (SkillVersion, bool, error)
	List(context.Context, SkillPartition, SkillList) (SkillPage, error)
	RecordEvaluation(context.Context, SkillPartition, string, SkillID, VersionID, Revision, SkillEvaluation) (SkillVersion, error)
	Stage(context.Context, SkillPartition, string, SkillID, VersionID, Revision) (SkillVersion, error)
	Activate(context.Context, SkillPartition, string, SkillID, VersionID, Revision) (SkillVersion, error)
	Reject(context.Context, SkillPartition, string, SkillID, VersionID, Revision) (SkillVersion, error)
	Archive(context.Context, SkillPartition, string, SkillID, VersionID, Revision) (SkillVersion, error)
	Rollback(context.Context, SkillPartition, string, SkillID, Revision, VersionID) (SkillVersion, error)
}

var (
	ErrInvalidSkill       = errors.New("learning: invalid skill")
	ErrSkillNotFound      = errors.New("learning: skill not found")
	ErrSkillConflict      = errors.New("learning: skill revision conflict")
	ErrSkillTransition    = errors.New("learning: invalid skill transition")
	ErrSkillLimit         = errors.New("learning: skill limit exceeded")
	ErrSkillOwnerMismatch = errors.New("learning: skill owner mismatch")
	ErrSkillNameCollision = errors.New("learning: skill name collision")
	ErrSkillCursor        = errors.New("learning: invalid or stale skill cursor")
)

// SkillVersionID content-addresses a body-only bundle. Provenance and lifecycle do not affect it.
func SkillVersionID(bundle SkillBundle) (VersionID, error) {
	if err := ValidateSkillBundle(bundle); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(bundle)
	sum := sha256.Sum256(raw)
	return VersionID("skill-version-" + hex.EncodeToString(sum[:16])), nil
}

func ValidateSkillPartition(partition SkillPartition, ownerAgent string) error {
	if strings.TrimSpace(partition.Principal) == "" || len(partition.Principal) > 1024 || len(partition.Project) > 4096 ||
		strings.TrimSpace(ownerAgent) == "" || len(ownerAgent) > MaxSkillOwnerBytes ||
		!utf8.ValidString(partition.Principal) || !utf8.ValidString(partition.Project) || !utf8.ValidString(ownerAgent) {
		return fmt.Errorf("%w: partition or owner", ErrInvalidSkill)
	}
	return nil
}

func ValidLearnedSkillName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	first := name[0]
	if (first < 'a' || first > 'z') && (first < '0' || first > '9') {
		return false
	}
	for _, r := range name {
		if r != '-' && r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func ValidateSkillBundle(bundle SkillBundle) error {
	if !ValidLearnedSkillName(bundle.Name) || strings.TrimSpace(bundle.Description) == "" || strings.TrimSpace(bundle.Body) == "" ||
		len(bundle.Description) > MaxSkillDescriptionBytes || len(bundle.Body) > MaxSkillBodyBytes ||
		!utf8.ValidString(bundle.Description) || !utf8.ValidString(bundle.Body) || strings.ContainsAny(bundle.Description, "\r\n\t") ||
		hasUnsafeControls(bundle.Description) || hasUnsafeControls(bundle.Body) {
		return fmt.Errorf("%w: bundle", ErrInvalidSkill)
	}
	return nil
}

func hasUnsafeControls(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && r != '\n' && r != '\t') || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}

// ValidateSkillProvenance validates bounded proposal, evidence, and signal linkage.
//
//nolint:gocyclo // validation covers every bounded provenance collection and nested reference
func ValidateSkillProvenance(p SkillProvenance) error {
	links := len(p.ProposalIDs) + len(p.EvidenceRefs) + len(p.Signals)
	if len(p.ProposalIDs) > MaxSkillProposals || len(p.EvidenceRefs) > MaxSkillEvidence || len(p.Signals) > MaxSkillSignals ||
		(p.ValidationDisposition != "" && !p.ValidationDisposition.Valid()) ||
		(links == 0 && p.Origin != SkillProvenanceLegacyModel) || (links != 0 && p.Origin == SkillProvenanceLegacyModel) ||
		(p.Origin != "" && p.Origin != SkillProvenanceLegacyModel) {
		return fmt.Errorf("%w: provenance", ErrInvalidSkill)
	}
	seen := map[string]bool{}
	for _, id := range p.ProposalIDs {
		if id == "" || len(id) > 256 || !utf8.ValidString(string(id)) || hasUnsafeControls(string(id)) || seen["p\x00"+string(id)] {
			return fmt.Errorf("%w: proposal provenance", ErrInvalidSkill)
		}
		seen["p\x00"+string(id)] = true
	}
	for _, ref := range p.EvidenceRefs {
		if !validSkillEvidence(ref) || seen["e\x00"+evidenceIdentity(ref)] {
			return fmt.Errorf("%w: evidence provenance", ErrInvalidSkill)
		}
		seen["e\x00"+evidenceIdentity(ref)] = true
	}
	for _, signal := range p.Signals {
		if !signal.Kind.Valid() || len(signal.Evidence) > MaxCandidateEvidence {
			return fmt.Errorf("%w: signal provenance", ErrInvalidSkill)
		}
		for _, ref := range signal.Evidence {
			if !validSkillEvidence(ref) {
				return fmt.Errorf("%w: signal evidence provenance", ErrInvalidSkill)
			}
		}
	}
	return nil
}

func validSkillEvidence(ref EvidenceRef) bool {
	if ref.SessionID == "" || len(ref.SessionID) > 256 || ref.Ordinal < 0 || !validProposalDigest(ref.Digest) ||
		len(ref.ToolCallID) > 256 || !utf8.ValidString(string(ref.SessionID)) || !utf8.ValidString(string(ref.ToolCallID)) ||
		hasUnsafeControls(string(ref.SessionID)) || hasUnsafeControls(string(ref.ToolCallID)) {
		return false
	}
	if ref.Locator == EvidenceMessage {
		return ref.EventSeq == nil
	}
	return ref.Locator == EvidenceEvent && ref.EventSeq != nil && *ref.EventSeq >= 0
}

func ValidateSkillEvaluation(e SkillEvaluation) error {
	if !e.Verdict.Valid() || ((e.Verdict == EvaluationPass || e.Verdict == EvaluationFail) && len(e.FixtureIDs) == 0) || len(e.FixtureIDs) > MaxSkillFixtures ||
		len(e.Baseline) > MaxSkillEvaluationTextBytes || len(e.Treatment) > MaxSkillEvaluationTextBytes || len(e.Reason) > MaxSkillEvaluationTextBytes ||
		!utf8.ValidString(e.Baseline) || !utf8.ValidString(e.Treatment) || !utf8.ValidString(e.Reason) {
		return fmt.Errorf("%w: evaluation", ErrInvalidSkill)
	}
	seen := map[string]bool{}
	for _, id := range e.FixtureIDs {
		if strings.TrimSpace(id) == "" || len(id) > 256 || !utf8.ValidString(id) || hasUnsafeControls(id) || seen[id] {
			return fmt.Errorf("%w: fixture", ErrInvalidSkill)
		}
		seen[id] = true
	}
	return nil
}

// ValidationDisposition describes logical admission without mutating a repository.
type ValidationDisposition string

func (d ValidationDisposition) Valid() bool {
	return d == ValidationAccept || d == ValidationExactDuplicate || d == ValidationSimilarStageHint
}

const (
	ValidationAccept           ValidationDisposition = "accept"
	ValidationExactDuplicate   ValidationDisposition = "exact_duplicate"
	ValidationSimilarStageHint ValidationDisposition = "similar_stage_hint"
)

type SkillInventoryItem struct {
	Name       string
	OwnerAgent string
	AgentOwned bool
	Bundle     SkillBundle
	SkillID    SkillID
	Version    VersionID
}

type SkillValidationRequest struct {
	Partition  SkillPartition
	OwnerAgent string
	Bundle     SkillBundle
	Provenance SkillProvenance
	Assets     []string
	Inventory  []SkillInventoryItem
}

type SkillValidation struct {
	Disposition ValidationDisposition
	Duplicate   *SkillInventoryItem
	Similar     []SkillInventoryItem
}
