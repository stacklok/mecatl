package client

//revive:disable:exported // complete proto-free reflection surface is declared as one unit

import (
	"context"
	"sort"
	"time"

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

// ReflectionReceipt is the bounded result of explicit reflection.
type ReflectionReceipt struct {
	ID, Disposition                      string
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
	resp, err := c.svc.ReflectSession(ctx, &mecatlv1.ReflectSessionRequest{SessionId: sessionID})
	if err != nil {
		return ReflectionReceipt{}, err
	}
	r := resp.GetReceipt()
	return ReflectionReceipt{ID: r.GetReflectionId(), Disposition: r.GetDisposition(), Queued: int(r.GetQueued()), Abstained: r.GetAbstained(), Staged: int(r.GetStaged()), Promoted: int(r.GetPromoted()), Conflicted: int(r.GetConflicted())}, nil
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
	out := LearningProposalPage{NextCursor: resp.GetNextCursor(), Proposals: make([]LearningProposal, len(resp.GetProposals()))}
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
	out := LearningProposal{ID: p.GetId(), Version: p.GetVersion(), Status: p.GetStatus(), Kind: p.GetKind(), Key: p.GetKey(), Value: p.GetValue(), Description: p.GetDescription(), Title: p.GetTitle(), Body: p.GetBody(), Triggers: append([]string(nil), p.GetTriggers()...), ProjectScoped: p.GetProjectScoped(), PromotionAvailable: p.GetPromotionAvailable(), PromotionUnavailableReason: p.GetPromotionUnavailableReason()}
	if t := p.GetCreatedAt(); t != nil && t.IsValid() {
		out.CreatedAt = t.AsTime()
	}
	if t := p.GetUpdatedAt(); t != nil && t.IsValid() {
		out.UpdatedAt = t.AsTime()
	}
	for _, e := range p.GetEvidence() {
		out.Evidence = append(out.Evidence, LearningEvidence{SessionID: e.GetSessionId(), Locator: e.GetLocator(), Ordinal: int(e.GetOrdinal()), EventSeq: e.GetEventSeq(), ToolCallID: e.GetToolCallId(), Digest: e.GetDigest(), Available: e.GetAvailable(), Availability: e.GetAvailability(), Preview: e.GetPreview()})
	}
	for _, d := range p.GetDecisions() {
		x := LearningDecision{Kind: d.GetKind(), Actor: d.GetActor(), Reason: d.GetReason()}
		if t := d.GetAt(); t != nil && t.IsValid() {
			x.At = t.AsTime()
		}
		out.Decisions = append(out.Decisions, x)
	}
	if r := p.GetPromotion(); r != nil {
		out.Promotion = &LearningPromotion{MemoryKey: r.GetMemoryKey(), PreviousExists: r.GetPreviousExists(), PreviousVersion: r.GetPreviousVersion(), ResultVersion: r.GetResultVersion()}
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
