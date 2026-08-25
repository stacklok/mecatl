package client

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

const sessionInventoryPageSize = 100

// SessionKind classifies a stored session for inventory display.
type SessionKind string

// Server-provided session kind values.
const (
	SessionKindMain           SessionKind = "main"
	SessionKindScheduled      SessionKind = "scheduled"
	SessionKindSubagent       SessionKind = "subagent"
	SessionKindParallelBranch SessionKind = "parallel_branch"
	SessionKindTeamMember     SessionKind = "team_member"
	SessionKindUnknown        SessionKind = "unknown"
)

// CapabilityReason explains why a session action is unavailable.
type CapabilityReason string

// Server-provided unavailable-action reasons.
const (
	CapabilityReasonInspectOnlyKind        CapabilityReason = "inspect_only_kind"
	CapabilityReasonAwaitingApproval       CapabilityReason = "awaiting_approval"
	CapabilityReasonActiveElsewhere        CapabilityReason = "active_elsewhere"
	CapabilityReasonTranscriptUnavailable  CapabilityReason = "transcript_unavailable"
	CapabilityReasonEnvironmentUnavailable CapabilityReason = "environment_unavailable"
	CapabilityReasonStorageUnsupported     CapabilityReason = "storage_unsupported"
	CapabilityReasonProtectedProvenance    CapabilityReason = "protected_provenance"
	CapabilityReasonInvalidTranscript      CapabilityReason = "invalid_transcript"
	CapabilityReasonAdoptionActive         CapabilityReason = "active"
	CapabilityReasonAdoptionLeased         CapabilityReason = "leased"
	CapabilityReasonBindingUnresolved      CapabilityReason = "binding_unresolved"
	CapabilityReasonNotLegacy              CapabilityReason = "not_legacy"
	CapabilityReasonUnknown                CapabilityReason = "unknown"
)

// SessionRelationship identifies the parent, schedule, or team context of a session.
type SessionRelationship struct {
	ParentSessionID string
	CallID          string
	BranchIndex     *int32
	ScheduleName    string
	OriginSessionID string
	TeamID          string
	MemberName      string
}

// SessionInventoryCapabilities declares the actions permitted for an inventory row.
type SessionInventoryCapabilities struct {
	PublicChat              bool
	Inspect                 bool
	AuthoritativeTranscript bool
	ActivityReplay          bool
	CopyID                  bool
	ViewTranscript          bool
	Fork                    bool
	Rename                  bool
	Delete                  bool
}

// SessionInventoryActionReasons carries the server reason for each disabled action.
type SessionInventoryActionReasons struct {
	PublicChat     CapabilityReason
	Inspect        CapabilityReason
	CopyID         CapabilityReason
	ViewTranscript CapabilityReason
	Fork           CapabilityReason
	Rename         CapabilityReason
	Delete         CapabilityReason
}

// SessionListItem is one server-authored stored-session inventory row. ID remains
// the only value sent back to APIs; Kind, Relationship, capabilities and reason
// code are display/action metadata and are never inferred from ID spelling.
type SessionListItem struct {
	ID              string
	ModifiedAt      int64
	State           string
	Turns           int32
	ModelID         string
	CreatedAt       int64
	Title           string
	TitleProvenance string
	Workspace       string
	Kind            SessionKind
	Relationship    SessionRelationship
	Capabilities    SessionInventoryCapabilities
	Reasons         SessionInventoryActionReasons
	ReasonCode      CapabilityReason
}

// SessionsListedMsg carries one session inventory listing result.
type SessionsListedMsg struct {
	Sessions []SessionListItem
	Err      error
}

// ErrSessionInventoryRestart means the server rejected a continuation cursor
// because its catalog generation changed. Callers must restart at page one
// rather than mixing generations.
var ErrSessionInventoryRestart = errors.New("session inventory changed; restart from page one")

// SessionInventoryPage is one proto-free bounded inventory page.
type SessionInventoryPage struct {
	Sessions   []SessionListItem
	NextCursor string
	TotalCount int
}

// SessionInventoryPageMsg carries one progressive page result to Bubble Tea.
type SessionInventoryPageMsg struct {
	Page         SessionInventoryPage
	Cursor       string
	RequestToken uint64
	Err          error
}

// ListSessionPage fetches one bounded page. An ABORTED response is the public
// stale-cursor restart signal; the UI never needs to inspect gRPC status codes.
func (c *Client) ListSessionPage(ctx context.Context, cursor string) (SessionInventoryPage, error) {
	resp, err := c.svc.ListSessions(ctx, &mecatlv1.ListSessionsRequest{PageSize: sessionInventoryPageSize, Cursor: cursor})
	if err != nil {
		if status.Code(err) == codes.Aborted {
			return SessionInventoryPage{}, fmt.Errorf("%w: %v", ErrSessionInventoryRestart, err)
		}
		return SessionInventoryPage{}, err
	}
	if resp.GetNextCursor() != "" && resp.GetNextCursor() == cursor {
		return SessionInventoryPage{}, fmt.Errorf("list sessions: server repeated inventory cursor")
	}
	return SessionInventoryPage{
		Sessions: listSessionsFromProto(resp.GetSessions()), NextCursor: resp.GetNextCursor(),
		TotalCount: int(resp.GetTotalCount()),
	}, nil
}

// ListSessions fetches every bounded inventory page for non-interactive callers
// such as --resume-latest. Interactive pickers use ListSessionPage directly so
// page one can render before continuation work begins.
func (c *Client) ListSessions(ctx context.Context) ([]SessionListItem, error) {
	var out []SessionListItem
	cursor := ""
	for {
		page, err := c.ListSessionPage(ctx, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Sessions...)
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

func listSessionsFromProto(in []*mecatlv1.SessionSummary) []SessionListItem {
	out := make([]SessionListItem, 0, len(in))
	for _, s := range in {
		rel := s.GetRelationship()
		var branchIndex *int32
		if rel != nil && rel.BranchIndex != nil {
			v := rel.GetBranchIndex()
			branchIndex = &v
		}
		caps := s.GetCapabilities()
		reasons := caps.GetReasons()
		out = append(out, SessionListItem{
			ID: s.GetSessionId(), ModifiedAt: s.GetModifiedAtUnix(), State: s.GetState(),
			Turns: s.GetTurns(), ModelID: s.GetModelId(), CreatedAt: s.GetCreatedAtUnix(), Title: s.GetTitle(),
			TitleProvenance: s.GetTitleProvenance(), Workspace: s.GetWorkspace(), Kind: SessionKind(s.GetKind()),
			Relationship: SessionRelationship{
				ParentSessionID: rel.GetParentSessionId(), CallID: rel.GetCallId(), BranchIndex: branchIndex,
				ScheduleName: rel.GetScheduleName(), OriginSessionID: rel.GetOriginSessionId(),
				TeamID: rel.GetTeamId(), MemberName: rel.GetMemberName(),
			},
			Capabilities: SessionInventoryCapabilities{
				PublicChat: caps.GetPublicChat(), Inspect: caps.GetInspect(),
				AuthoritativeTranscript: caps.GetAuthoritativeTranscript(), ActivityReplay: caps.GetActivityReplay(),
				CopyID: caps.GetCopyId(), ViewTranscript: caps.GetViewTranscript(), Fork: caps.GetFork(),
				Rename: caps.GetRename(), Delete: caps.GetDelete(),
			},
			Reasons: SessionInventoryActionReasons{
				PublicChat: CapabilityReason(reasons.GetPublicChat()), Inspect: CapabilityReason(reasons.GetInspect()),
				CopyID: CapabilityReason(reasons.GetCopyId()), ViewTranscript: CapabilityReason(reasons.GetViewTranscript()),
				Fork: CapabilityReason(reasons.GetFork()), Rename: CapabilityReason(reasons.GetRename()), Delete: CapabilityReason(reasons.GetDelete()),
			},
			ReasonCode: CapabilityReason(s.GetReasonCode()),
		})
	}
	return out
}

// SessionPager fetches one bounded stored-session inventory page.
type SessionPager interface {
	ListSessionPage(ctx context.Context, cursor string) (SessionInventoryPage, error)
}

// SessionLister lists all stored sessions for non-interactive selection.
type SessionLister interface {
	ListSessions(ctx context.Context) ([]SessionListItem, error)
}

// SessionReplayer remains the optional activity-replay seam used by the live
// delivery catch-up path. It is not authoritative conversation context.
type SessionReplayer interface {
	StreamSessionEvents(ctx context.Context, id string) (*EventStream, error)
}

// LiveStreamer opens the live event feed for a session.
type LiveStreamer interface {
	StreamSessionLive(ctx context.Context, id string) (*EventStream, error)
}

var _ SessionReplayer = (*Client)(nil)
var _ LiveStreamer = (*Client)(nil)

// ListSessionsPageCmd returns a command that fetches exactly one inventory page.
func ListSessionsPageCmd(ctx context.Context, s SessionPager, cursor string, requestToken uint64) tea.Cmd {
	return func() tea.Msg {
		page, err := s.ListSessionPage(ctx, cursor)
		return SessionInventoryPageMsg{Page: page, Cursor: cursor, RequestToken: requestToken, Err: err}
	}
}

// ListSessionsCmd returns the legacy all-pages command used by non-progressive callers.
func ListSessionsCmd(ctx context.Context, s SessionLister) tea.Cmd {
	return func() tea.Msg {
		sessions, err := s.ListSessions(ctx)
		if err != nil {
			return SessionsListedMsg{Err: err}
		}
		return SessionsListedMsg{Sessions: sessions}
	}
}
