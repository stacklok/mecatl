package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

// ReflectionReceipt is the bounded result of an explicit reflection request.
type ReflectionReceipt struct {
	ID          string
	Disposition string
	Queued      int
	Abstained   bool
	Staged      int
	Promoted    int
	Conflicted  int
}

// ExplicitReflector submits one caller-owned completed session for reflection.
type ExplicitReflector func(context.Context, *session.Session) (ReflectionReceipt, error)

// ProposalPromoter applies an approved proposal through the configured memory target.
type ProposalPromoter func(context.Context, learning.ProposalPartition, learning.ProposalID, learning.ProposalVersion, bool) (learning.ProposalRecord, error)

// ProposalUndoer commits a compensating memory revision for a promoted proposal.
type ProposalUndoer func(context.Context, learning.ProposalPartition, learning.ProposalID, learning.ProposalVersion) (learning.ProposalRecord, error)

func reflectionPrincipal(p *session.Principal) string {
	value := "ownerless"
	if p != nil {
		value = p.Issuer + "\x00" + p.Subject
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Service) proposalPartition(ctx context.Context, project string) (learning.ProposalPartition, error) {
	principalValue := session.PrincipalFromContext(ctx)
	if s.cfg.OwnershipEnforced && principalValue == nil {
		return learning.ProposalPartition{}, fmt.Errorf("%w: verified proposal principal unavailable", ErrFailedPrecondition)
	}
	principalFn := s.cfg.ProposalPrincipal
	if principalFn == nil {
		principalFn = reflectionPrincipal
	}
	principal := principalFn(principalValue)
	if principal == "" {
		return learning.ProposalPartition{}, fmt.Errorf("%w: proposal principal unavailable", ErrFailedPrecondition)
	}
	return learning.ProposalPartition{Principal: principal, Project: project}, nil
}

// ReflectSession explicitly reflects a completed caller-owned session.
func (s *Service) ReflectSession(ctx context.Context, id session.SessionID) (*mecatlv1.ReflectionReceipt, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: session id is required", ErrInvalidArgument)
	}
	if s.cfg.ReflectSession == nil {
		return nil, ErrLearningUnavailable
	}
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess.State != session.StateCompleted {
		return nil, fmt.Errorf("%w: reflection requires a completed session", ErrFailedPrecondition)
	}
	if s.placementBinder != nil {
		binding, bindErr := s.ReattachPlacement(ctx, sess.EnvironmentRef)
		if bindErr != nil {
			return nil, bindErr
		}
		ctx = memory.WithWorkspace(ctx, binding.Environment.Workspace().Root())
	}
	r, err := s.cfg.ReflectSession(ctx, sess)
	if err != nil {
		return nil, fmt.Errorf("%w: explicit reflection failed", ErrInternal)
	}
	return &mecatlv1.ReflectionReceipt{ReflectionId: validLearningText(r.ID), Disposition: validLearningText(r.Disposition), Queued: int32(r.Queued), Abstained: r.Abstained, Staged: int32(r.Staged), Promoted: int32(r.Promoted), Conflicted: int32(r.Conflicted)}, nil //nolint:gosec // coordinator counts are bounded far below int32
}

// ListLearningProposals returns one bounded partition page.
func (s *Service) ListLearningProposals(ctx context.Context, statusValue, cursor string, limit int, project string) (*mecatlv1.ListLearningProposalsResponse, error) {
	if s.cfg.Proposals == nil {
		return nil, ErrLearningUnavailable
	}
	part, err := s.proposalPartition(ctx, project)
	if err != nil {
		return nil, err
	}
	status := learning.ProposalStatus(statusValue)
	if statusValue != "" && !status.Valid() {
		return nil, fmt.Errorf("%w: unknown proposal status", ErrInvalidArgument)
	}
	if limit < 0 || limit > learning.MaxProposalPageSize {
		return nil, fmt.Errorf("%w: proposal limit must be between 0 and %d", ErrInvalidArgument, learning.MaxProposalPageSize)
	}
	if limit == 0 {
		limit = learning.DefaultProposalPageSize
	}
	page, err := s.cfg.Proposals.List(ctx, part, learning.ProposalList{After: learning.ProposalID(cursor), Limit: limit, Status: status})
	if err != nil {
		return nil, proposalServiceError(err)
	}
	out := make([]*mecatlv1.LearningProposal, len(page.Records))
	for i := range page.Records {
		out[i] = s.toProtoLearningProposal(ctx, page.Records[i])
	}
	return &mecatlv1.ListLearningProposalsResponse{Proposals: out, NextCursor: validLearningText(string(page.Next))}, nil
}

// GetLearningProposal returns one proposal from the caller's selected partition.
func (s *Service) GetLearningProposal(ctx context.Context, id, project string) (*mecatlv1.LearningProposal, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: proposal id is required", ErrInvalidArgument)
	}
	if s.cfg.Proposals == nil {
		return nil, ErrLearningUnavailable
	}
	part, err := s.proposalPartition(ctx, project)
	if err != nil {
		return nil, err
	}
	record, found, err := s.cfg.Proposals.Get(ctx, part, learning.ProposalID(id))
	if err != nil {
		return nil, proposalServiceError(err)
	}
	if !found {
		return nil, fmt.Errorf("%w: proposal %q", ErrNotFound, id)
	}
	return s.toProtoLearningProposal(ctx, record), nil
}

// DecideLearningProposal rejects or approves a staged proposal using version CAS.
//
//nolint:gocyclo // fact and learned-procedure approval share validation and evidence checks
func (s *Service) DecideLearningProposal(ctx context.Context, id, expected, decision, reason, project string) (*mecatlv1.LearningProposal, error) {
	if s.cfg.Proposals == nil {
		return nil, ErrLearningUnavailable
	}
	if id == "" || expected == "" || len(reason) > learning.MaxProposalReasonBytes {
		return nil, fmt.Errorf("%w: id and expected_version are required; reason must be bounded", ErrInvalidArgument)
	}
	part, err := s.proposalPartition(ctx, project)
	if err != nil {
		return nil, err
	}
	var record learning.ProposalRecord
	switch learning.DecisionKind(decision) {
	case learning.DecisionReject:
		record, err = s.cfg.Proposals.ClaimDecision(ctx, part, learning.ProposalID(id), learning.ProposalVersion(expected), learning.Decision{Kind: learning.DecisionReject, Actor: "operator", Reason: validLearningText(reason), At: time.Now()})
	case learning.DecisionApprove:
		if project != "" && (s.cfg.ProjectPromotionAllowed == nil || !s.cfg.ProjectPromotionAllowed(project)) {
			return nil, fmt.Errorf("%w: project promotion requires exact trusted launch root", ErrFailedPrecondition)
		}
		if s.cfg.PromoteProposal == nil {
			return nil, fmt.Errorf("%w: proposal promotion is not configured", ErrFailedPrecondition)
		}
		current, found, getErr := s.cfg.Proposals.Get(ctx, part, learning.ProposalID(id))
		if getErr != nil {
			return nil, proposalServiceError(getErr)
		}
		if !found {
			return nil, fmt.Errorf("%w: proposal %q", ErrNotFound, id)
		}
		if current.Candidate.Kind == learning.CandidateProcedure {
			if s.cfg.LearnedSkills == nil {
				return nil, fmt.Errorf("%w: learned-skill lifecycle is unavailable", ErrFailedPrecondition)
			}
			if project != "" && (s.cfg.ProjectPromotionAllowed == nil || !s.cfg.ProjectPromotionAllowed(project)) {
				return nil, fmt.Errorf("%w: project skill activation requires exact trusted launch root", ErrFailedPrecondition)
			}
		} else if available, reason := s.proposalActionAvailable(project); !available {
			return nil, fmt.Errorf("%w: %s", ErrFailedPrecondition, reason)
		}
		for _, evidence := range current.Candidate.Evidence {
			if available, _ := s.learningEvidenceAvailable(ctx, evidence); !available {
				return nil, fmt.Errorf("%w: proposal evidence is unavailable or changed", ErrFailedPrecondition)
			}
		}
		record, err = s.cfg.PromoteProposal(ctx, part, learning.ProposalID(id), learning.ProposalVersion(expected), true)
	default:
		return nil, fmt.Errorf("%w: decision must be approve or reject", ErrInvalidArgument)
	}
	if err != nil {
		return nil, proposalServiceError(err)
	}
	return s.toProtoLearningProposal(ctx, record), nil
}

// UndoLearningPromotion compensates the current linked promotion using version CAS.
func (s *Service) UndoLearningPromotion(ctx context.Context, id, expected, project string) (*mecatlv1.LearningProposal, error) {
	if s.cfg.Proposals == nil {
		return nil, ErrLearningUnavailable
	}
	if id == "" || expected == "" {
		return nil, fmt.Errorf("%w: id and expected_version are required", ErrInvalidArgument)
	}
	if s.cfg.UndoProposal == nil {
		return nil, fmt.Errorf("%w: proposal undo is not configured", ErrFailedPrecondition)
	}
	part, err := s.proposalPartition(ctx, project)
	if err != nil {
		return nil, err
	}
	if available, reason := s.proposalActionAvailable(project); !available {
		return nil, fmt.Errorf("%w: %s", ErrFailedPrecondition, reason)
	}
	record, err := s.cfg.UndoProposal(ctx, part, learning.ProposalID(id), learning.ProposalVersion(expected))
	if err != nil {
		return nil, proposalServiceError(err)
	}
	return s.toProtoLearningProposal(ctx, record), nil
}

func (s *Service) proposalActionAvailable(project string) (bool, string) {
	if s.cfg.ProposalActionAvailable != nil {
		return s.cfg.ProposalActionAvailable(project)
	}
	if project != "" && (s.cfg.ProjectPromotionAllowed == nil || !s.cfg.ProjectPromotionAllowed(project)) {
		return false, "project promotion requires exact trusted launch root"
	}
	return true, ""
}

func proposalServiceError(err error) error {
	switch {
	case errors.Is(err, learning.ErrProposalNotFound):
		return fmt.Errorf("%w: proposal", ErrNotFound)
	case errors.Is(err, learning.ErrProposalVersionConflict), errors.Is(err, learning.ErrProposalTransition):
		return fmt.Errorf("%w: %v", ErrProposalConflict, err)
	case errors.Is(err, learning.ErrInvalidProposal):
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	default:
		return fmt.Errorf("%w: proposal operation failed", ErrInternal)
	}
}

func (s *Service) toProtoLearningProposal(ctx context.Context, r learning.ProposalRecord) *mecatlv1.LearningProposal {
	candidate := r.Candidate
	available, unavailableReason := s.proposalActionAvailable(r.Partition.Project)
	if candidate.Kind == learning.CandidateProcedure {
		available = s.cfg.LearnedSkills != nil
		unavailableReason = ""
		if available && r.Partition.Project != "" {
			available, unavailableReason = s.proposalActionAvailable(r.Partition.Project)
		}
		if !available && unavailableReason == "" {
			unavailableReason = "learned-skill lifecycle is unavailable"
		}
	}
	out := &mecatlv1.LearningProposal{
		Id: validLearningText(string(r.ID)), Version: validLearningText(string(r.Version)), Status: validLearningText(string(r.Status)), Kind: validLearningText(string(candidate.Kind)),
		Key: validLearningText(candidate.Key), Value: safeLearningText(candidate.Key, candidate.Value), Description: safeLearningText(candidate.Key, candidate.Description),
		Title: safeLearningText("procedure/title", candidate.Title), Body: safeLearningText(candidate.Title, candidate.Body),
		CreatedAt: timestampOrNil(r.CreatedAt), UpdatedAt: timestampOrNil(r.UpdatedAt), ProjectScoped: r.Partition.Project != "",
		PromotionAvailable: available, PromotionUnavailableReason: validLearningText(unavailableReason),
		LearnedSkillId: validLearningText(string(r.SkillID)),
	}
	out.Evidence = make([]*mecatlv1.LearningEvidenceRef, len(candidate.Evidence))
	for i, ref := range candidate.Evidence {
		available, availability, preview := s.learningEvidenceStatus(ctx, ref)
		eventSeq := int64(0)
		if ref.EventSeq != nil {
			eventSeq = *ref.EventSeq
		}
		out.Evidence[i] = &mecatlv1.LearningEvidenceRef{SessionId: validLearningText(string(ref.SessionID)), Locator: validLearningText(string(ref.Locator)), Ordinal: int32(ref.Ordinal), EventSeq: eventSeq, ToolCallId: validLearningText(string(ref.ToolCallID)), Digest: validLearningText(ref.Digest), Available: available, Availability: validLearningText(availability), Preview: safeLearningText("evidence/preview", preview)} //nolint:gosec // validated reflection ordinals are bounded to 512
	}
	for _, signal := range r.Signals {
		out.Triggers = append(out.Triggers, validLearningText(string(signal.Kind)))
	}
	for _, d := range r.Decisions {
		out.Decisions = append(out.Decisions, &mecatlv1.LearningDecision{Kind: validLearningText(string(d.Kind)), Actor: validLearningText(d.Actor), Reason: safeLearningText("decision/reason", d.Reason), At: timestampOrNil(d.At)})
	}
	if r.Receipt != nil {
		out.Promotion = &mecatlv1.LearningPromotionReceipt{MemoryKey: validLearningText(r.Receipt.MemoryKey), PreviousExists: r.Receipt.PreviousExists, PreviousVersion: validLearningText(r.Receipt.PreviousVersion), ResultVersion: validLearningText(r.Receipt.ResultVersion)}
	}
	return out
}

func (s *Service) learningEvidenceAvailable(ctx context.Context, ref learning.EvidenceRef) (bool, string) {
	available, availability, _ := s.learningEvidenceStatus(ctx, ref)
	return available, availability
}

func (s *Service) learningEvidenceStatus(ctx context.Context, ref learning.EvidenceRef) (bool, string, string) {
	if s.cfg.Store == nil {
		return false, "source unavailable", ""
	}
	sess, err := s.GetSession(ctx, ref.SessionID)
	if err != nil {
		return false, "source unavailable", ""
	}
	stop, _ := sess.StopReason()
	workspace, ok := s.learningWorkspace(ctx, sess)
	if !ok {
		return false, "source unavailable", ""
	}
	trajectory := learning.NewTrajectory(sess.ID, workspace, stop, sess.Usage, sess.Conversation.Messages)
	var (
		actual learning.EvidenceRef
		input  learning.Input
	)
	switch ref.Locator {
	case learning.EvidenceMessage:
		if ref.Ordinal < 0 || ref.Ordinal >= len(sess.Conversation.Messages) {
			return false, "evidence unavailable", ""
		}
		input = learning.NewInput(trajectory, nil, nil, nil)
		actual, err = learning.MessageEvidenceRef(input, ref.Ordinal, ref.ToolCallID)
		if err != nil {
			return false, "evidence changed", ""
		}
	case learning.EvidenceEvent:
		if s.cfg.EventLog == nil || ref.Ordinal < 0 || ref.Ordinal >= learning.MaxInputEvents {
			return false, "event evidence unavailable", ""
		}
		events := make([]session.Event, 0, ref.Ordinal+1)
		for event, eventErr := range s.cfg.EventLog.Read(ctx, ref.SessionID) {
			if eventErr != nil {
				return false, "event evidence unavailable", ""
			}
			events = append(events, event)
			if len(events) > ref.Ordinal {
				break
			}
		}
		if len(events) <= ref.Ordinal {
			return false, "event evidence unavailable", ""
		}
		input = learning.NewInput(trajectory, events, nil, nil)
		actual, err = learning.EventEvidenceRef(input, ref.Ordinal, ref.ToolCallID)
		if err != nil {
			return false, "evidence changed", ""
		}
	default:
		return false, "evidence unavailable", ""
	}
	if actual.Digest != ref.Digest || !sameEventSequence(actual.EventSeq, ref.EventSeq) {
		return false, "evidence changed", ""
	}
	preview, err := learning.EvidencePreview(input, ref)
	if err != nil {
		return false, "evidence unavailable", ""
	}
	return true, "available", preview
}

func (s *Service) learningWorkspace(ctx context.Context, sess *session.Session) (string, bool) {
	if s.placementBinder == nil {
		return "", true
	}
	binding, err := s.ReattachPlacement(ctx, sess.EnvironmentRef)
	if err != nil {
		return "", false
	}
	return binding.Environment.Workspace().Root(), true
}

func sameEventSequence(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func timestampOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func validLearningText(value string) string { return tool.CanonicalMemoryText(value) }

func safeLearningText(key, value string) string {
	value = validLearningText(value)
	if tool.SecretShapedMemoryValue(key, value) {
		return "[withheld: secret-shaped proposal text]"
	}
	return value
}
