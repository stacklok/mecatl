package client

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// SessionRenamedMsg carries a correlated RenameSession result without exposing
// protobuf types to the UI.
type SessionRenamedMsg struct {
	SessionID       string
	RequestToken    uint64
	Title           string
	TitleProvenance string
	TitleRevision   uint64
	Err             error
}

// SessionDeletedMsg carries a correlated DeleteSession result.
type SessionDeletedMsg struct {
	SessionID string
	Err       error
}

// SessionRenamer renames one stored session.
type SessionRenamer interface {
	RenameSession(ctx context.Context, id, title string) (SessionSnapshot, error)
}

// DeleteSessionOptions widens DeleteSession (ADR 0374). StopActive cancels an
// active run before deleting; RemoveWorktree also removes the session's clean,
// unshared server-created worktree. The zero value is a plain delete.
type DeleteSessionOptions struct {
	StopActive     bool
	RemoveWorktree bool
}

// DeleteSessionResult reports the worktree outcome of a delete. Both fields
// are zero unless RemoveWorktree was requested. WorktreeRetainedReason is
// "dirty", "shared", "remove_failed", or empty.
type DeleteSessionResult struct {
	WorktreeRemoved        bool
	WorktreeRetainedReason string
}

// SessionDeleter permanently deletes one stored session.
type SessionDeleter interface {
	DeleteSession(ctx context.Context, id string, opts DeleteSessionOptions) (DeleteSessionResult, error)
}

// SessionManager is the mutable stored-session inventory surface.
type SessionManager interface {
	SessionRenamer
	SessionDeleter
}

// RenameSession replaces a stored session title and returns the authoritative
// server snapshot, including title provenance.
func (c *Client) RenameSession(ctx context.Context, id, title string) (SessionSnapshot, error) {
	resp, err := c.svc.RenameSession(withSessionAffinity(ctx, id), &mecatlv1.RenameSessionRequest{SessionId: id, Title: title})
	if err != nil {
		return SessionSnapshot{}, fmt.Errorf("rename session: %w", err)
	}
	return snapshotFrom(resp.GetSession()), nil
}

// DeleteSession permanently removes a stored session.
func (c *Client) DeleteSession(ctx context.Context, id string, opts DeleteSessionOptions) (DeleteSessionResult, error) {
	resp, err := c.svc.DeleteSession(withSessionAffinity(ctx, id), &mecatlv1.DeleteSessionRequest{
		SessionId: id, StopActive: opts.StopActive, RemoveWorktree: opts.RemoveWorktree,
	})
	if err != nil {
		return DeleteSessionResult{}, fmt.Errorf("delete session: %w", err)
	}
	return DeleteSessionResult{
		WorktreeRemoved:        resp.GetWorktreeRemoved(),
		WorktreeRetainedReason: validText(resp.GetWorktreeRetainedReason()),
	}, nil
}

// IsDeleteRefusedActive reports a DeleteSession refused with failed
// precondition — how a server answers a delete of a session that is running or
// awaiting approval when it does not (or, predating ADR 0374, cannot) stop it.
func IsDeleteRefusedActive(err error) bool {
	return grpcstatus.Code(err) == codes.FailedPrecondition
}

// RenameSessionCmdWithToken correlates an asynchronous rename with a UI request.
func RenameSessionCmdWithToken(ctx context.Context, r SessionRenamer, id, title string, requestToken uint64) tea.Cmd {
	return func() tea.Msg {
		snapshot, err := r.RenameSession(ctx, id, title)
		return SessionRenamedMsg{SessionID: id, RequestToken: requestToken, Title: snapshot.Title, TitleProvenance: snapshot.TitleProvenance, TitleRevision: snapshot.TitleRevision, Err: err}
	}
}

// DeleteSessionCmd performs DeleteSession off the reducer goroutine.
func DeleteSessionCmd(ctx context.Context, d SessionDeleter, id string) tea.Cmd {
	return func() tea.Msg {
		_, err := d.DeleteSession(ctx, id, DeleteSessionOptions{})
		return SessionDeletedMsg{SessionID: id, Err: err}
	}
}
