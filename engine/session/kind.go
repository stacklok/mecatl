package session

import (
	"errors"
	"fmt"
	"time"
)

// SessionKind classifies the trusted producer and continuation posture of a
// durable session. The vocabulary is closed; unrecognised values are invalid.
//
//revive:disable-next-line:exported // SessionKind distinguishes it from unrelated kinds.
type SessionKind string

const (
	// SessionKindUnknown is the fail-closed classification for legacy data whose
	// producer metadata was not persisted.
	SessionKindUnknown SessionKind = "unknown"
	// SessionKindMain is a public interactive chat, peer fork, or carryover.
	SessionKindMain SessionKind = "main"
	// SessionKindScheduled is an autonomous scheduler fire.
	SessionKindScheduled SessionKind = "scheduled"
	// SessionKindSubagent is a child created by the Subagent tool.
	SessionKindSubagent SessionKind = "subagent"
	// SessionKindParallelBranch is one child branch of the Parallel tool.
	SessionKindParallelBranch SessionKind = "parallel_branch"
	// SessionKindTeamMember is a member driven by a team Supervisor.
	SessionKindTeamMember SessionKind = "team_member"
)

// SessionRelationship carries the kind-specific relationship metadata of a
// durable session. ValidateSessionMetadata defines which fields are required
// and forbidden for each SessionKind.
//
//revive:disable-next-line:exported // SessionRelationship distinguishes it from unrelated relationships.
type SessionRelationship struct {
	ScheduleName    string     `json:"schedule_name,omitempty"`
	OriginSessionID SessionID  `json:"origin_session_id,omitempty"`
	ParentSessionID SessionID  `json:"parent_session_id,omitempty"`
	CallID          ToolCallID `json:"call_id,omitempty"`
	BranchIndex     *int       `json:"branch_index,omitempty"`
	TeamID          string     `json:"team_id,omitempty"`
	MemberName      string     `json:"member_name,omitempty"`
}

// ErrInvalidSessionMetadata marks an invalid kind/relationship combination.
var ErrInvalidSessionMetadata = errors.New("session: invalid kind or relationship")

// ValidateSessionMetadata validates the closed SessionKind relationship schema.
func ValidateSessionMetadata(kind SessionKind, rel SessionRelationship) error {
	if kind == "" {
		kind = SessionKindUnknown
	}
	switch kind {
	case SessionKindUnknown, SessionKindMain:
		if rel != (SessionRelationship{}) {
			return fmt.Errorf("%w: %s sessions cannot carry relationships", ErrInvalidSessionMetadata, kind)
		}
	case SessionKindScheduled:
		if !validScheduledRelationship(rel) {
			return fmt.Errorf("%w: scheduled requires only schedule name and optional origin session", ErrInvalidSessionMetadata)
		}
	case SessionKindSubagent:
		if !validSubagentRelationship(rel) {
			return fmt.Errorf("%w: subagent requires parent session and call id", ErrInvalidSessionMetadata)
		}
	case SessionKindParallelBranch:
		if !validParallelBranchRelationship(rel) {
			return fmt.Errorf("%w: parallel branch requires parent session, call id, and non-negative branch index", ErrInvalidSessionMetadata)
		}
	case SessionKindTeamMember:
		if !validTeamMemberRelationship(rel) {
			return fmt.Errorf("%w: team member requires team id and member name with optional parent session", ErrInvalidSessionMetadata)
		}
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidSessionMetadata, kind)
	}
	return nil
}

func validScheduledRelationship(rel SessionRelationship) bool {
	forbidden := rel
	forbidden.ScheduleName = ""
	forbidden.OriginSessionID = ""
	return rel.ScheduleName != "" && forbidden == (SessionRelationship{})
}

func validSubagentRelationship(rel SessionRelationship) bool {
	forbidden := rel
	forbidden.ParentSessionID = ""
	forbidden.CallID = ""
	return rel.ParentSessionID != "" && rel.CallID != "" && forbidden == (SessionRelationship{})
}

func validParallelBranchRelationship(rel SessionRelationship) bool {
	forbidden := rel
	forbidden.ParentSessionID = ""
	forbidden.CallID = ""
	forbidden.BranchIndex = nil
	return rel.ParentSessionID != "" && rel.CallID != "" && rel.BranchIndex != nil && *rel.BranchIndex >= 0 && forbidden == (SessionRelationship{})
}

func validTeamMemberRelationship(rel SessionRelationship) bool {
	forbidden := rel
	forbidden.TeamID = ""
	forbidden.MemberName = ""
	forbidden.ParentSessionID = ""
	return rel.TeamID != "" && rel.MemberName != "" && forbidden == (SessionRelationship{})
}

// RestoreSessionMetadata validates and restores persisted creation metadata.
// An empty kind is legacy data and is restored as SessionKindUnknown.
func (s *Session) RestoreSessionMetadata(kind SessionKind, rel SessionRelationship) error {
	if s.State != StateIdle {
		return fmt.Errorf("%w: restore metadata from %q", ErrIllegalTransition, s.State)
	}
	if kind == "" {
		kind = SessionKindUnknown
	}
	if err := ValidateSessionMetadata(kind, rel); err != nil {
		return err
	}
	s.Kind = kind
	s.Relationship = cloneSessionRelationship(rel)
	return nil
}

func cloneSessionRelationship(rel SessionRelationship) SessionRelationship {
	if rel.BranchIndex != nil {
		branchIndex := *rel.BranchIndex
		rel.BranchIndex = &branchIndex
	}
	return rel
}

// NewScheduled constructs a validated scheduler-fire session.
func NewScheduled(id SessionID, mode PermissionMode, workspace string, limits Limits, createdAt time.Time, scheduleName string, origin SessionID) (*Session, error) {
	return newRelated(id, mode, workspace, limits, createdAt, SessionKindScheduled, SessionRelationship{ScheduleName: scheduleName, OriginSessionID: origin})
}

// NewSubagent constructs a validated Subagent child session.
func NewSubagent(id SessionID, mode PermissionMode, workspace string, limits Limits, createdAt time.Time, parent SessionID, call ToolCallID) (*Session, error) {
	return newRelated(id, mode, workspace, limits, createdAt, SessionKindSubagent, SessionRelationship{ParentSessionID: parent, CallID: call})
}

// NewParallelBranch constructs a validated Parallel branch session.
func NewParallelBranch(id SessionID, mode PermissionMode, workspace string, limits Limits, createdAt time.Time, parent SessionID, call ToolCallID, branchIndex int) (*Session, error) {
	return newRelated(id, mode, workspace, limits, createdAt, SessionKindParallelBranch, SessionRelationship{ParentSessionID: parent, CallID: call, BranchIndex: &branchIndex})
}

// NewTeamMember constructs a validated team-member session. parent is optional
// for a directly-driven team and is present for a tool-driven team.
func NewTeamMember(id SessionID, mode PermissionMode, workspace string, limits Limits, createdAt time.Time, teamID, member string, parent SessionID) (*Session, error) {
	return newRelated(id, mode, workspace, limits, createdAt, SessionKindTeamMember, SessionRelationship{TeamID: teamID, MemberName: member, ParentSessionID: parent})
}

func newRelated(id SessionID, mode PermissionMode, workspace string, limits Limits, createdAt time.Time, kind SessionKind, rel SessionRelationship) (*Session, error) {
	if err := ValidateSessionMetadata(kind, rel); err != nil {
		return nil, err
	}
	s := New(id, mode, workspace, limits, createdAt)
	s.Kind = kind
	s.Relationship = rel
	return s, nil
}
