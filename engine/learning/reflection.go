package learning

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const (
	// MaxInputMessages bounds one reflection input's transcript.
	MaxInputMessages = 256
	// MaxInputEvents bounds one reflection input's optional event projection.
	MaxInputEvents = 512
	// MaxInputSignals bounds caller-supplied and structurally detected signals.
	MaxInputSignals = 64
	// MaxExistingFacts bounds comparison-only existing memory.
	MaxExistingFacts = 256
	// MaxCandidates bounds one reflection outcome.
	MaxCandidates = 16
	// MaxCandidateEvidence bounds the evidence handles attached to one candidate.
	MaxCandidateEvidence = 16
	// MaxCandidateKeyBytes bounds a fact's stable key.
	MaxCandidateKeyBytes = 256
	// MaxCandidateValueBytes bounds a fact's value.
	MaxCandidateValueBytes = 8 << 10
	// MaxCandidateDescriptionBytes bounds optional explanatory fact text.
	MaxCandidateDescriptionBytes = 2 << 10
	// MaxCandidateTitleBytes bounds a procedure title.
	MaxCandidateTitleBytes = 256
	// MaxCandidateBodyBytes bounds procedure content.
	MaxCandidateBodyBytes = 8 << 10
)

var (
	// ErrInvalidInput reports an invalid or unbounded reflection input.
	ErrInvalidInput = errors.New("learning: invalid reflection input")
	// ErrInvalidOutcome reports a malformed reflection outcome.
	ErrInvalidOutcome = errors.New("learning: invalid reflection outcome")
	// ErrInvalidCandidate reports a candidate that is unsafe, unbounded, or unsupported.
	ErrInvalidCandidate = errors.New("learning: invalid candidate")
	// ErrInvalidEvidence reports an evidence handle not resolvable against the exact input.
	ErrInvalidEvidence = errors.New("learning: invalid evidence reference")
)

// CandidateKind is the closed durable-learning vocabulary. The zero value is invalid.
type CandidateKind string

const (
	// CandidateOperatorFact is a durable operator-wide fact.
	CandidateOperatorFact CandidateKind = "operator_fact"
	// CandidateProjectFact is a durable project-scoped fact.
	CandidateProjectFact CandidateKind = "project_fact"
	// CandidateProcedure is a bounded title/body procedure proposal.
	CandidateProcedure CandidateKind = "procedure"
)

// Valid reports whether k belongs to the closed candidate vocabulary.
func (k CandidateKind) Valid() bool {
	return k == CandidateOperatorFact || k == CandidateProjectFact || k == CandidateProcedure
}

// OutcomeKind is the closed reflector result vocabulary. The zero value is invalid.
type OutcomeKind string

const (
	// OutcomeAbstained means reflection intentionally produced no candidates.
	OutcomeAbstained OutcomeKind = "abstained"
	// OutcomeProposed means reflection produced one or more candidates.
	OutcomeProposed OutcomeKind = "proposed"
)

// Valid reports whether k belongs to the closed outcome vocabulary.
func (k OutcomeKind) Valid() bool { return k == OutcomeAbstained || k == OutcomeProposed }

// SignalKind is a closed reason to admit conservative reflection. A signal never
// proves that a durable candidate should be produced.
type SignalKind string

const (
	// SignalSubstantialSuccess marks a substantial clean successful workflow.
	SignalSubstantialSuccess SignalKind = "substantial_success"
	// SignalRepeatedCorrection marks multiple correction turns in one input.
	SignalRepeatedCorrection SignalKind = "repeated_correction"
	// SignalFailureRecovery marks a same-tool failure followed by success.
	SignalFailureRecovery SignalKind = "failure_recovery"
	// SignalRepeatedToolSequence marks a stable repeated multi-tool sequence.
	SignalRepeatedToolSequence SignalKind = "repeated_tool_sequence"
	// SignalExplicitRemember marks a narrow explicit fact remember/learn request.
	SignalExplicitRemember SignalKind = "explicit_remember"
	// SignalExplicitLearnProcedure marks an explicit request to retain a reusable procedure.
	SignalExplicitLearnProcedure SignalKind = "explicit_learn_procedure"
	// SignalContradiction is supplied by a host with cross-session knowledge.
	SignalContradiction SignalKind = "contradiction"
)

// Valid reports whether k belongs to the closed signal vocabulary.
func (k SignalKind) Valid() bool {
	switch k {
	case SignalSubstantialSuccess, SignalRepeatedCorrection, SignalFailureRecovery,
		SignalRepeatedToolSequence, SignalExplicitRemember, SignalExplicitLearnProcedure, SignalContradiction:
		return true
	default:
		return false
	}
}

// EvidenceLocator identifies which bounded projection an EvidenceRef addresses.
type EvidenceLocator string

const (
	// EvidenceMessage identifies a trajectory message ordinal.
	EvidenceMessage EvidenceLocator = "message"
	// EvidenceEvent identifies a supplied event ordinal.
	EvidenceEvent EvidenceLocator = "event"
)

// EvidenceRef is a content-addressed handle into the exact Input supplied to a
// reflector. Ordinal is zero-based in Trajectory.Messages or Input.Events.
type EvidenceRef struct {
	SessionID  session.SessionID  `json:"session_id"`
	Locator    EvidenceLocator    `json:"locator"`
	Ordinal    int                `json:"ordinal"`
	EventSeq   *int64             `json:"event_seq,omitempty"`
	ToolCallID session.ToolCallID `json:"tool_call_id,omitempty"`
	Digest     string             `json:"digest"`
}

// Signal is a host-supplied or structurally detected reflection admission hint.
// Evidence may be empty for a caller-supplied cross-session signal.
type Signal struct {
	Kind     SignalKind    `json:"kind"`
	Evidence []EvidenceRef `json:"evidence,omitempty"`
}

// Candidate is a bounded durable-learning proposal. Facts use Key, Value, and
// optional Description. Procedures use Title and Body; new skill-materializable
// procedures also carry Name. Name remains optional here so historical title/body
// procedure proposals remain readable.
type Candidate struct {
	Kind        CandidateKind `json:"kind"`
	Key         string        `json:"key,omitempty"`
	Value       string        `json:"value,omitempty"`
	Description string        `json:"description,omitempty"`
	Name        string        `json:"name,omitempty"`
	Title       string        `json:"title,omitempty"`
	Body        string        `json:"body,omitempty"`
	Evidence    []EvidenceRef `json:"evidence"`
}

// ExistingFact is bounded comparison data. It is never evidence and cannot carry
// procedure or promotion metadata.
type ExistingFact struct {
	Kind        CandidateKind `json:"kind"`
	Key         string        `json:"key"`
	Value       string        `json:"value"`
	Description string        `json:"description,omitempty"`
}

// Input is the bounded, owned material available to one reflection. Construct it
// with NewInput so transcript, event, signal, and existing-fact storage does not
// alias caller-owned values.
type Input struct {
	Trajectory Trajectory          `json:"trajectory"`
	Events     []EvidenceEventData `json:"events,omitempty"`
	Signals    []Signal            `json:"signals,omitempty"`
	Existing   []ExistingFact      `json:"existing,omitempty"`
}

// NewInput constructs an owned reflection input from existing session data.
func NewInput(trajectory Trajectory, events []session.Event, signals []Signal, existing []ExistingFact) Input {
	ownedTrajectory := NewTrajectory(trajectory.SessionID, trajectory.Workspace, trajectory.Stop, trajectory.Usage, canonicalMessages(trajectory.Messages))
	ownedTrajectory.Principal = trajectory.Principal.Clone()
	return Input{
		Trajectory: ownedTrajectory,
		Events:     projectSessionEvents(events),
		Signals:    cloneSignals(signals),
		Existing:   append([]ExistingFact(nil), existing...),
	}
}

// Outcome is either explicit abstention with no candidates or a non-empty set of proposals.
type Outcome struct {
	Kind       OutcomeKind `json:"kind"`
	Candidates []Candidate `json:"candidates,omitempty"`
}

// Reflector conservatively proposes durable learning from bounded evidence.
type Reflector interface {
	Reflect(context.Context, Input) (Outcome, error)
}

// ValidateInput enforces domain bounds and validates caller-supplied signals.
func ValidateInput(in Input) error {
	if in.Trajectory.SessionID == "" {
		return fmt.Errorf("%w: trajectory session id is required", ErrInvalidInput)
	}
	if len(in.Trajectory.Messages) > MaxInputMessages || len(in.Events) > MaxInputEvents ||
		len(in.Signals) > MaxInputSignals || len(in.Existing) > MaxExistingFacts {
		return fmt.Errorf("%w: collection limit exceeded", ErrInvalidInput)
	}
	existingKeys := make(map[string]struct{}, len(in.Existing))
	for i, existing := range in.Existing {
		if err := validateExistingFact(existing); err != nil {
			return fmt.Errorf("%w: existing fact %d: %v", ErrInvalidInput, i, err)
		}
		key := strings.ToLower(strings.TrimSpace(existing.Key))
		if _, ok := existingKeys[key]; ok {
			return fmt.Errorf("%w: duplicate existing fact key %q", ErrInvalidInput, existing.Key)
		}
		existingKeys[key] = struct{}{}
	}
	for i, signal := range in.Signals {
		if !signal.Kind.Valid() || len(signal.Evidence) > MaxCandidateEvidence {
			return fmt.Errorf("%w: signal %d is invalid", ErrInvalidInput, i)
		}
		for _, ref := range signal.Evidence {
			if err := ResolveEvidence(in, ref); err != nil {
				return fmt.Errorf("%w: signal %d: %v", ErrInvalidInput, i, err)
			}
		}
	}
	return nil
}

// NewCandidate validates and constructs a candidate against the exact input.
func NewCandidate(in Input, candidate Candidate) (Candidate, error) {
	if err := ValidateCandidate(in, candidate); err != nil {
		return Candidate{}, err
	}
	candidate.Evidence = append([]EvidenceRef(nil), candidate.Evidence...)
	return candidate, nil
}

// ValidateCandidate rejects unsupported, transient, unsafe, duplicate, or
// unevidenced durable proposals.
//
//nolint:gocyclo // closed candidate shape, durable-memory validation, and evidence checks stay together
func ValidateCandidate(in Input, candidate Candidate) error {
	if !candidate.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidCandidate, candidate.Kind)
	}
	if !candidateTextValid(candidate) {
		return fmt.Errorf("%w: text is empty, oversized, or invalid UTF-8", ErrInvalidCandidate)
	}
	switch candidate.Kind {
	case CandidateProcedure:
		if strings.TrimSpace(candidate.Title) == "" || strings.TrimSpace(candidate.Body) == "" ||
			(candidate.Name != "" && !ValidLearnedSkillName(candidate.Name)) ||
			candidate.Key != "" || candidate.Value != "" || candidate.Description != "" {
			return fmt.Errorf("%w: procedure requires title/body, optional valid name, and forbids fact fields", ErrInvalidCandidate)
		}
	default:
		if strings.TrimSpace(candidate.Key) == "" || strings.TrimSpace(candidate.Value) == "" ||
			candidate.Name != "" || candidate.Title != "" || candidate.Body != "" {
			return fmt.Errorf("%w: fact requires key/value and forbids procedure fields", ErrInvalidCandidate)
		}
		entry := tool.MemoryEntry{Key: candidate.Key, Value: candidate.Value, Description: candidate.Description}
		attr := tool.MemoryAttribution{Writer: tool.MemoryWriterModel, Origin: tool.MemoryOriginLearning}
		if tool.CanonicalMemoryText(candidate.Key) != candidate.Key || tool.CanonicalMemoryText(candidate.Value) != candidate.Value ||
			tool.CanonicalMemoryText(candidate.Description) != candidate.Description || tool.ValidateMemoryEntryWrite(entry, attr) != nil {
			return fmt.Errorf("%w: fact is not exact durable memory material", ErrInvalidCandidate)
		}
	}
	if len(candidate.Evidence) == 0 || len(candidate.Evidence) > MaxCandidateEvidence {
		return fmt.Errorf("%w: evidence count must be between 1 and %d", ErrInvalidCandidate, MaxCandidateEvidence)
	}
	if unsafeCandidateText(candidate) {
		return fmt.Errorf("%w: unsafe or transient durable content", ErrInvalidCandidate)
	}
	seenEvidence := make(map[string]struct{}, len(candidate.Evidence))
	for _, ref := range candidate.Evidence {
		if err := ResolveEvidence(in, ref); err != nil {
			return err
		}
		key := evidenceIdentity(ref)
		if _, ok := seenEvidence[key]; ok {
			return fmt.Errorf("%w: duplicate evidence", ErrInvalidCandidate)
		}
		seenEvidence[key] = struct{}{}
	}
	return nil
}

// ValidateSkillCandidate requires the named procedure shape used for skill materialization.
func ValidateSkillCandidate(in Input, candidate Candidate) error {
	if err := ValidateCandidate(in, candidate); err != nil {
		return err
	}
	if candidate.Kind != CandidateProcedure || !ValidLearnedSkillName(candidate.Name) {
		return fmt.Errorf("%w: skill candidate requires procedure kind and name", ErrInvalidCandidate)
	}
	return ValidateSkillBundle(SkillBundle{Name: candidate.Name, Description: candidate.Title, Body: candidate.Body})
}

// ValidateOutcome enforces abstention/proposal structure and duplicate keys.
func ValidateOutcome(in Input, out Outcome) error {
	if !out.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidOutcome, out.Kind)
	}
	if out.Kind == OutcomeAbstained {
		if len(out.Candidates) != 0 {
			return fmt.Errorf("%w: abstention must contain zero candidates", ErrInvalidOutcome)
		}
		return nil
	}
	if len(out.Candidates) == 0 || len(out.Candidates) > MaxCandidates {
		return fmt.Errorf("%w: proposal candidate count must be between 1 and %d", ErrInvalidOutcome, MaxCandidates)
	}
	keys := make(map[string]struct{}, len(out.Candidates))
	for i, candidate := range out.Candidates {
		if err := ValidateCandidate(in, candidate); err != nil {
			return fmt.Errorf("%w: candidate %d: %v", ErrInvalidOutcome, i, err)
		}
		identity := "fact\x00" + strings.ToLower(strings.TrimSpace(candidate.Key))
		if candidate.Kind == CandidateProcedure {
			procedureName := candidate.Name
			if procedureName == "" {
				procedureName = candidate.Title
			}
			identity = "procedure\x00" + strings.ToLower(strings.TrimSpace(procedureName))
		}
		if _, ok := keys[identity]; ok {
			return fmt.Errorf("%w: duplicate candidate key", ErrInvalidOutcome)
		}
		keys[identity] = struct{}{}
	}
	return nil
}

func validateExistingFact(fact ExistingFact) error {
	if fact.Kind != CandidateOperatorFact && fact.Kind != CandidateProjectFact ||
		strings.TrimSpace(fact.Key) == "" || strings.TrimSpace(fact.Value) == "" ||
		len(fact.Key) > MaxCandidateKeyBytes || len(fact.Value) > MaxCandidateValueBytes ||
		len(fact.Description) > MaxCandidateDescriptionBytes || !utf8.ValidString(fact.Key) ||
		!utf8.ValidString(fact.Value) || !utf8.ValidString(fact.Description) {
		return errors.New("invalid bounded fact")
	}
	candidate := Candidate{Kind: fact.Kind, Key: fact.Key, Value: fact.Value, Description: fact.Description}
	if unsafeCandidateText(candidate) {
		return errors.New("unsafe fact")
	}
	return nil
}

func candidateTextValid(candidate Candidate) bool {
	return utf8.ValidString(candidate.Key) && utf8.ValidString(candidate.Value) && utf8.ValidString(candidate.Name) &&
		utf8.ValidString(candidate.Description) && utf8.ValidString(candidate.Title) && utf8.ValidString(candidate.Body) &&
		len(candidate.Key) <= MaxCandidateKeyBytes && len(candidate.Value) <= MaxCandidateValueBytes && len(candidate.Name) <= 64 &&
		len(candidate.Description) <= MaxCandidateDescriptionBytes && len(candidate.Title) <= MaxCandidateTitleBytes &&
		len(candidate.Body) <= MaxCandidateBodyBytes
}

var (
	transientNumber = regexp.MustCompile(`(?i)\b(?:issue|pull[ -]?request|pr)\s*#\s*[0-9]+\b`)
	fullCommitSHA   = regexp.MustCompile(`(?i)(?:^|[^0-9a-f])[0-9a-f]{40}(?:$|[^0-9a-f])`)
	namedCommitSHA  = regexp.MustCompile(`(?i)\b(?:commit|revision|sha)\s*(?:is|=|:)?\s*[0-9a-f]{7,40}\b`)
	concreteBranch  = regexp.MustCompile(`(?i)\b(?:(?:use|checkout|switch to|on)\s+(?:the\s+)?branch|branch\s*(?:is|=))\s+[a-z0-9._/-]+`)
)

func unsafeCandidateText(c Candidate) bool {
	all := strings.TrimSpace(strings.Join([]string{c.Key, c.Value, c.Description, c.Name, c.Title, c.Body}, "\n"))
	for _, value := range []string{c.Key, c.Value, c.Description, c.Name, c.Title, c.Body} {
		if tool.SecretShapedMemoryValue(c.Key, value) {
			return true
		}
	}
	if projectionCredential.MatchString(all) || tool.DirectiveShapedUserMemory(all) {
		return true
	}
	lower := strings.ToLower(all)
	if transientNumber.MatchString(lower) || fullCommitSHA.MatchString(lower) || namedCommitSHA.MatchString(lower) ||
		concreteBranch.MatchString(lower) ||
		unsupportedNegativeCapability(lower) || strings.Contains(lower, "mandatory update") ||
		strings.Contains(lower, "must update ") || strings.Contains(lower, "always update ") {
		return true
	}
	for _, marker := range []string{"/tmp/", "\\temp\\", ".scratch/", "current task", "this task", "current branch", "temporary task state"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func unsupportedNegativeCapability(s string) bool {
	for _, phrase := range []string{" cannot ", " can't ", " does not support ", " is unsupported", " never supports "} {
		if strings.Contains(" "+s+" ", phrase) {
			return true
		}
	}
	return false
}
