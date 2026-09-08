package client

//revive:disable:exported // complete proto-free reflection surface is declared as one unit

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

const (
	ProposalStatusStaged     = "staged"
	ProposalStatusPromoting  = "promoting"
	ProposalStatusPromoted   = "promoted"
	ProposalStatusRejected   = "rejected"
	ProposalStatusDeferred   = "deferred_unsupported"
	ProposalStatusConflicted = "conflicted"
	ProposalStatusUndone     = "undone"
)

func IsProposalConflict(err error) bool { return grpcstatus.Code(err) == codes.Aborted }

func ReflectionErrorText(err error) string {
	switch grpcstatus.Code(err) {
	case codes.Canceled:
		return "reflection was cancelled"
	case codes.DeadlineExceeded:
		return "reflection timed out"
	case codes.FailedPrecondition:
		return "reflection source is unavailable or changed"
	case codes.ResourceExhausted:
		return "reflection queue is full"
	case codes.InvalidArgument:
		return "reflection request is invalid"
	case codes.Unavailable:
		return "reflection service is unavailable"
	case codes.Unimplemented:
		return "reflection is not configured"
	default:
		return "reflection failed"
	}
}

// ReflectionReceipt is the bounded result of explicit reflection.
type ReflectionReceipt struct {
	ID, Disposition, Reason, Message     string
	Queued, Staged, Promoted, Conflicted int
	Abstained                            bool
}

type LearningEvidence struct {
	SessionID, Locator, ToolCallID, Digest, Availability, Preview string
	Ordinal                                                       int
	EventSeq                                                      int64
	Available                                                     bool
}

type LearningDecision struct {
	Kind, Actor, Reason string
	At                  time.Time
}
type LearningPromotion struct {
	MemoryKey, PreviousVersion, ResultVersion string
	PreviousExists                            bool
}
type LearningProposal struct {
	ID, Version, Status, Kind, Key, Value, Description, Title, Body string
	Evidence                                                        []LearningEvidence
	Triggers                                                        []string
	Decisions                                                       []LearningDecision
	Promotion                                                       *LearningPromotion
	CreatedAt, UpdatedAt                                            time.Time
	ProjectScoped                                                   bool
	Project                                                         string
	PromotionAvailable                                              bool
	PromotionUnavailableReason                                      string
	LearnedSkillID                                                  string
}
type LearningProposalPage struct {
	Proposals          []LearningProposal
	NextCursor         string
	OperatorNextCursor string
	ProjectNextCursor  string
	OperatorDone       bool
	ProjectDone        bool
}

type ReflectionCursors struct {
	Operator     string
	Project      string
	OperatorDone bool
	ProjectDone  bool
}

type ReflectionsMsg struct {
	Page       LearningProposalPage
	Err        error
	Generation uint64
}
type ReflectionMsg struct {
	Proposal   *LearningProposal
	Receipt    *ReflectionReceipt
	Err        error
	RequestID  string
	Generation uint64
}

func (c *Client) ReflectSession(ctx context.Context, sessionID string) (ReflectionReceipt, error) {
	resp, err := c.svc.ReflectSession(withSessionAffinity(ctx, sessionID), &mecatlv1.ReflectSessionRequest{SessionId: sessionID})
	if err != nil {
		return ReflectionReceipt{}, err
	}
	return mapReflectionReceipt(resp.GetReceipt()), nil
}

func mapReflectionReceipt(r *mecatlv1.ReflectionReceipt) ReflectionReceipt {
	if r == nil {
		return ReflectionReceipt{}
	}
	reason := safeReflectionText(r.GetReason())
	message := ""
	switch reason {
	case "no_eligible_evidence":
		message = "No eligible evidence was available for reflection."
	case "mandatory_span_exceeds_bounds":
		message = "The required evidence span exceeds reflection bounds."
	default:
		reason = ""
	}
	return ReflectionReceipt{
		ID: safeReflectionText(r.GetReflectionId()), Disposition: safeReflectionText(r.GetDisposition()),
		Reason: reason, Message: message, Queued: int(r.GetQueued()), Abstained: r.GetAbstained(),
		Staged: int(r.GetStaged()), Promoted: int(r.GetPromoted()), Conflicted: int(r.GetConflicted()),
	}
}

func safeReflectionText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.C, r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(value, "\uFFFD"))
}

func boundedReflectionPreview(value string) string {
	value = safeReflectionText(value)
	if len(value) <= 1024 {
		return value
	}
	for len(value) > 1024 {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}

func (c *Client) ListLearningProposals(ctx context.Context, status, cursor string, limit int, project string) (LearningProposalPage, error) {
	if limit < 0 {
		limit = 0
	}
	if limit > 200 {
		limit = 200
	}
	resp, err := c.svc.ListLearningProposals(ctx, &mecatlv1.ListLearningProposalsRequest{Status: status, Cursor: cursor, Limit: int32(limit), Project: project}) //nolint:gosec // clamped to the wire maximum above
	if err != nil {
		return LearningProposalPage{}, err
	}
	out := LearningProposalPage{NextCursor: safeReflectionText(resp.GetNextCursor()), Proposals: make([]LearningProposal, len(resp.GetProposals()))}
	for i, p := range resp.GetProposals() {
		out.Proposals[i] = mapLearningProposal(p)
	}
	return out, nil
}
func (c *Client) GetLearningProposal(ctx context.Context, id, project string) (LearningProposal, error) {
	resp, err := c.svc.GetLearningProposal(ctx, &mecatlv1.GetLearningProposalRequest{Id: id, Project: project})
	if err != nil {
		return LearningProposal{}, err
	}
	return mapLearningProposal(resp.GetProposal()), nil
}
func (c *Client) DecideLearningProposal(ctx context.Context, id, version, decision, reason, project string) (LearningProposal, error) {
	resp, err := c.svc.DecideLearningProposal(ctx, &mecatlv1.DecideLearningProposalRequest{Id: id, ExpectedVersion: version, Decision: decision, Reason: reason, Project: project})
	if err != nil {
		return LearningProposal{}, err
	}
	return mapLearningProposal(resp.GetProposal()), nil
}
func (c *Client) UndoLearningPromotion(ctx context.Context, id, version, project string) (LearningProposal, error) {
	resp, err := c.svc.UndoLearningPromotion(ctx, &mecatlv1.UndoLearningPromotionRequest{Id: id, ExpectedVersion: version, Project: project})
	if err != nil {
		return LearningProposal{}, err
	}
	return mapLearningProposal(resp.GetProposal()), nil
}

func mapLearningProposal(p *mecatlv1.LearningProposal) LearningProposal {
	if p == nil {
		return LearningProposal{}
	}
	out := LearningProposal{ID: safeReflectionText(p.GetId()), Version: safeReflectionText(p.GetVersion()), Status: safeReflectionText(p.GetStatus()), Kind: safeReflectionText(p.GetKind()), Key: safeReflectionText(p.GetKey()), Value: safeReflectionText(p.GetValue()), Description: safeReflectionText(p.GetDescription()), Title: safeReflectionText(p.GetTitle()), Body: safeReflectionText(p.GetBody()), ProjectScoped: p.GetProjectScoped(), PromotionAvailable: p.GetPromotionAvailable(), PromotionUnavailableReason: safeReflectionText(p.GetPromotionUnavailableReason()), LearnedSkillID: safeReflectionText(p.GetLearnedSkillId())}
	for _, trigger := range p.GetTriggers() {
		out.Triggers = append(out.Triggers, safeReflectionText(trigger))
	}
	if t := p.GetCreatedAt(); t != nil && t.IsValid() {
		out.CreatedAt = t.AsTime()
	}
	if t := p.GetUpdatedAt(); t != nil && t.IsValid() {
		out.UpdatedAt = t.AsTime()
	}
	for _, e := range p.GetEvidence() {
		out.Evidence = append(out.Evidence, LearningEvidence{SessionID: safeReflectionText(e.GetSessionId()), Locator: safeReflectionText(e.GetLocator()), Ordinal: int(e.GetOrdinal()), EventSeq: e.GetEventSeq(), ToolCallID: safeReflectionText(e.GetToolCallId()), Digest: safeReflectionText(e.GetDigest()), Available: e.GetAvailable(), Availability: safeReflectionText(e.GetAvailability()), Preview: boundedReflectionPreview(e.GetPreview())})
	}
	for _, d := range p.GetDecisions() {
		x := LearningDecision{Kind: safeReflectionText(d.GetKind()), Actor: safeReflectionText(d.GetActor()), Reason: safeReflectionText(d.GetReason())}
		if t := d.GetAt(); t != nil && t.IsValid() {
			x.At = t.AsTime()
		}
		out.Decisions = append(out.Decisions, x)
	}
	if r := p.GetPromotion(); r != nil {
		out.Promotion = &LearningPromotion{MemoryKey: safeReflectionText(r.GetMemoryKey()), PreviousExists: r.GetPreviousExists(), PreviousVersion: safeReflectionText(r.GetPreviousVersion()), ResultVersion: safeReflectionText(r.GetResultVersion())}
	}
	return out
}

type ReflectionClient interface {
	ListLearningProposals(context.Context, string, string, int, string) (LearningProposalPage, error)
	GetLearningProposal(context.Context, string, string) (LearningProposal, error)
	DecideLearningProposal(context.Context, string, string, string, string, string) (LearningProposal, error)
	UndoLearningPromotion(context.Context, string, string, string) (LearningProposal, error)
	ReflectSession(context.Context, string) (ReflectionReceipt, error)
}

func ListReflectionsCmd(ctx context.Context, c ReflectionClient, status string, cursors ReflectionCursors, project string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		page := LearningProposalPage{OperatorDone: cursors.OperatorDone, ProjectDone: cursors.ProjectDone || project == ""}
		if !cursors.OperatorDone {
			operatorPage, err := c.ListLearningProposals(ctx, status, cursors.Operator, 50, "")
			if err != nil {
				return ReflectionsMsg{Err: err, Generation: generation}
			}
			page.Proposals = append(page.Proposals, operatorPage.Proposals...)
			page.OperatorNextCursor = operatorPage.NextCursor
			page.OperatorDone = operatorPage.NextCursor == ""
		}
		if project != "" && !cursors.ProjectDone {
			projectPage, projectErr := c.ListLearningProposals(ctx, status, cursors.Project, 50, project)
			if projectErr != nil {
				return ReflectionsMsg{Err: projectErr, Generation: generation}
			}
			page.ProjectNextCursor = projectPage.NextCursor
			page.ProjectDone = projectPage.NextCursor == ""
			for i := range projectPage.Proposals {
				projectPage.Proposals[i].Project = project
			}
			page.Proposals = append(page.Proposals, projectPage.Proposals...)
		}
		sort.SliceStable(page.Proposals, func(i, j int) bool {
			iPending := page.Proposals[i].Status == ProposalStatusStaged || page.Proposals[i].Status == ProposalStatusConflicted
			jPending := page.Proposals[j].Status == ProposalStatusStaged || page.Proposals[j].Status == ProposalStatusConflicted
			if iPending != jPending {
				return iPending
			}
			return page.Proposals[i].UpdatedAt.After(page.Proposals[j].UpdatedAt)
		})
		return ReflectionsMsg{Page: page, Generation: generation}
	}
}
func ReflectSessionCmd(ctx context.Context, c ReflectionClient, sessionID string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		r, e := c.ReflectSession(ctx, sessionID)
		return ReflectionMsg{Receipt: &r, Err: e, RequestID: sessionID, Generation: generation}
	}
}
func GetReflectionCmd(ctx context.Context, c ReflectionClient, id, project string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		p, e := c.GetLearningProposal(ctx, id, project)
		p.Project = project
		return ReflectionMsg{Proposal: &p, Err: e, RequestID: id, Generation: generation}
	}
}
func DecideReflectionCmd(ctx context.Context, c ReflectionClient, p LearningProposal, decision, project string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		x, e := c.DecideLearningProposal(ctx, p.ID, p.Version, decision, "", project)
		x.Project = project
		return ReflectionMsg{Proposal: &x, Err: e, RequestID: p.ID, Generation: generation}
	}
}
func UndoReflectionCmd(ctx context.Context, c ReflectionClient, p LearningProposal, project string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		x, e := c.UndoLearningPromotion(ctx, p.ID, p.Version, project)
		x.Project = project
		return ReflectionMsg{Proposal: &x, Err: e, RequestID: p.ID, Generation: generation}
	}
}
