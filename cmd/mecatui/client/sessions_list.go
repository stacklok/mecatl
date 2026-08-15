package client

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

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

// ListSessions fetches every bounded inventory page so the picker can search all
// rows the server exposed. Each individual RPC remains capped at 100 rows.
func (c *Client) ListSessions(ctx context.Context) ([]SessionListItem, error) {
	var out []SessionListItem
	cursor := ""
	for {
		resp, err := c.svc.ListSessions(ctx, &mecatlv1.ListSessionsRequest{PageSize: sessionInventoryPageSize, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		out = append(out, listSessionsFromProto(resp.GetSessions())...)
		next := resp.GetNextCursor()
		if next == "" {
			return out, nil
		}
		if next == cursor {
			return nil, fmt.Errorf("list sessions: server repeated inventory cursor")
		}
		cursor = next
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

// SessionLister lists stored sessions for the inventory.
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

// ListSessionsCmd returns a command that lists stored sessions.
func ListSessionsCmd(ctx context.Context, s SessionLister) tea.Cmd {
	return func() tea.Msg {
		sessions, err := s.ListSessions(ctx)
		if err != nil {
			return SessionsListedMsg{Err: err}
		}
		return SessionsListedMsg{Sessions: sessions}
	}
}
