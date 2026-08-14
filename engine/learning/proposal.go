package learning

//revive:disable:exported // proposal.go declares the closed public lifecycle vocabulary as one unit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/tool"
)

const (
	MaxProposalsPerPartition = 4096
	MaxProposalDecisions     = 16
	MaxProposalReasonBytes   = 1024
	MaxProposalActorBytes    = 256
	DefaultProposalPageSize  = 50
	MaxProposalPageSize      = 200
)

type ProposalID string
type ProposalVersion string
type ProposalStatus string

const (
	ProposalStaged              ProposalStatus = "staged"
	ProposalPromoting           ProposalStatus = "promoting"
	ProposalPromoted            ProposalStatus = "promoted"
	ProposalRejected            ProposalStatus = "rejected"
	ProposalDeferredUnsupported ProposalStatus = "deferred_unsupported"
	ProposalConflicted          ProposalStatus = "conflicted"
	ProposalUndone              ProposalStatus = "undone"
)

func (s ProposalStatus) Valid() bool {
	switch s {
	case ProposalStaged, ProposalPromoting, ProposalPromoted, ProposalRejected, ProposalDeferredUnsupported, ProposalConflicted, ProposalUndone:
		return true
	}
	return false
}

type DecisionKind string

const (
	DecisionApprove DecisionKind = "approve"
	DecisionReject  DecisionKind = "reject"
	DecisionDefer   DecisionKind = "defer"
)

type Decision struct {
	Kind   DecisionKind `json:"kind"`
	Actor  string       `json:"actor,omitempty"`
	Reason string       `json:"reason,omitempty"`
	At     time.Time    `json:"at"`
}
type PromotionReceipt struct {
	MemoryKey       string `json:"memory_key"`
	PreviousExists  bool   `json:"previous_exists"`
	PreviousVersion string `json:"previous_version,omitempty"`
	ResultVersion   string `json:"result_version"`
}
type ProposalPartition struct {
	Principal string `json:"principal"`
	Project   string `json:"project,omitempty"`
}
type ProposalRecord struct {
	ID          ProposalID        `json:"id"`
	Version     ProposalVersion   `json:"version"`
	Status      ProposalStatus    `json:"status"`
	Partition   ProposalPartition `json:"partition"`
	InputDigest string            `json:"input_digest"`
	Candidate   Candidate         `json:"candidate"`
	Signals     []Signal          `json:"signals,omitempty"`
	Decisions   []Decision        `json:"decisions,omitempty"`
	Receipt     *PromotionReceipt `json:"receipt,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}
type ProposalList struct {
	After  ProposalID
	Limit  int
	Status ProposalStatus
}
type ProposalPage struct {
	Records []ProposalRecord
	Next    ProposalID
}

var (
	ErrInvalidProposal         = errors.New("learning: invalid proposal")
	ErrProposalNotFound        = errors.New("learning: proposal not found")
	ErrProposalVersionConflict = errors.New("learning: proposal version conflict")
	ErrProposalTransition      = errors.New("learning: invalid proposal transition")
	ErrProposalLimit           = errors.New("learning: proposal limit exceeded")
)

type ProposalRepository interface {
	StageBatch(context.Context, ProposalPartition, string, []Candidate, []Signal) ([]ProposalRecord, error)
	List(context.Context, ProposalPartition, ProposalList) (ProposalPage, error)
	Get(context.Context, ProposalPartition, ProposalID) (ProposalRecord, bool, error)
	ClaimDecision(context.Context, ProposalPartition, ProposalID, ProposalVersion, Decision) (ProposalRecord, error)
	ClaimPromotion(context.Context, ProposalPartition, ProposalID, ProposalVersion) (ProposalRecord, error)
	Finalize(context.Context, ProposalPartition, ProposalID, ProposalVersion, ProposalStatus, *PromotionReceipt, Decision) (ProposalRecord, error)
}

// DeterministicProposalID hashes identity, scope, input, candidate, and evidence digests; it never includes transcript text.
func DeterministicProposalID(p ProposalPartition, input string, c Candidate) (ProposalID, error) {
	if err := ValidateProposalMaterial(p, input, c, nil); err != nil {
		return "", err
	}
	evidence := make([]string, len(c.Evidence))
	for i, ref := range c.Evidence {
		evidence[i] = ref.Digest
	}
	sort.Strings(evidence)
	c.Evidence = nil
	material := struct {
		Principal, Project, Input string
		Candidate                 Candidate
		Evidence                  []string
	}{digestString(p.Principal), digestString(p.Project), strings.ToLower(input), c, evidence}
	raw, _ := json.Marshal(material)
	sum := sha256.Sum256(raw)
	return ProposalID("proposal-" + hex.EncodeToString(sum[:16])), nil
}
func digestString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
func validProposalDigest(s string) bool {
	if len(s) < 16 || len(s) > 128 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// ValidateProposalMaterial validates the bounded persistence shape without requiring transcript data.
//
//nolint:gocyclo // validation covers the complete bounded persistence trust boundary
func ValidateProposalMaterial(p ProposalPartition, input string, c Candidate, signals []Signal) error {
	if strings.TrimSpace(p.Principal) == "" || len(p.Principal) > 1024 || len(p.Project) > 4096 || !utf8.ValidString(p.Principal) || !utf8.ValidString(p.Project) || !validProposalDigest(input) {
		return fmt.Errorf("%w: partition or digest", ErrInvalidProposal)
	}
	if !c.Kind.Valid() || !candidateTextValid(c) || unsafeCandidateText(c) || len(c.Evidence) == 0 || len(c.Evidence) > MaxCandidateEvidence {
		return fmt.Errorf("%w: candidate", ErrInvalidProposal)
	}
	if c.Kind == CandidateProcedure {
		if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.Body) == "" || c.Key != "" || c.Value != "" || c.Description != "" {
			return fmt.Errorf("%w: procedure shape", ErrInvalidProposal)
		}
	} else if strings.TrimSpace(c.Key) == "" || strings.TrimSpace(c.Value) == "" || c.Title != "" || c.Body != "" {
		return fmt.Errorf("%w: fact shape", ErrInvalidProposal)
	} else {
		entry := tool.MemoryEntry{Key: c.Key, Value: c.Value, Description: c.Description}
		attr := tool.MemoryAttribution{Writer: tool.MemoryWriterModel, Origin: tool.MemoryOriginLearning}
		if tool.CanonicalMemoryText(c.Key) != c.Key || tool.CanonicalMemoryText(c.Value) != c.Value ||
			tool.CanonicalMemoryText(c.Description) != c.Description || tool.ValidateMemoryEntryWrite(entry, attr) != nil {
			return fmt.Errorf("%w: fact is not exact durable memory material", ErrInvalidProposal)
		}
	}
	seen := map[string]bool{}
	for _, ref := range c.Evidence {
		if ref.SessionID == "" || ref.Ordinal < 0 || !validProposalDigest(ref.Digest) || ref.Locator != EvidenceMessage && ref.Locator != EvidenceEvent {
			return fmt.Errorf("%w: evidence", ErrInvalidProposal)
		}
		id := evidenceIdentity(ref)
		if seen[id] {
			return fmt.Errorf("%w: duplicate evidence", ErrInvalidProposal)
		}
		seen[id] = true
	}
	if len(signals) > MaxInputSignals {
		return ErrProposalLimit
	}
	for _, signal := range signals {
		if !signal.Kind.Valid() || len(signal.Evidence) > MaxCandidateEvidence {
			return fmt.Errorf("%w: signal", ErrInvalidProposal)
		}
		for _, ref := range signal.Evidence {
			if ref.SessionID == "" || ref.Ordinal < 0 || !validProposalDigest(ref.Digest) {
				return fmt.Errorf("%w: signal evidence", ErrInvalidProposal)
			}
		}
	}
	return nil
}
func ValidateDecision(d Decision) error {
	if d.Kind != DecisionApprove && d.Kind != DecisionReject && d.Kind != DecisionDefer || len(d.Actor) > MaxProposalActorBytes || len(d.Reason) > MaxProposalReasonBytes || !utf8.ValidString(d.Actor) || !utf8.ValidString(d.Reason) {
		return fmt.Errorf("%w: decision", ErrInvalidProposal)
	}
	return nil
}
