package client

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// SessionRenamedMsg carries a correlated RenameSession result without exposing
// protobuf types to the UI.
type SessionRenamedMsg struct {
	SessionID       string
	RequestToken    uint64
	Title           string
	TitleProvenance string
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

// SessionDeleter permanently deletes one stored session.
type SessionDeleter interface {
	DeleteSession(ctx context.Context, id string) error
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
func (c *Client) DeleteSession(ctx context.Context, id string) error {
	_, err := c.svc.DeleteSession(withSessionAffinity(ctx, id), &mecatlv1.DeleteSessionRequest{SessionId: id})
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// RenameSessionCmd performs RenameSession off the reducer goroutine.
func RenameSessionCmd(ctx context.Context, r SessionRenamer, id, title string) tea.Cmd {
	return RenameSessionCmdWithToken(ctx, r, id, title, 0)
}

// RenameSessionCmdWithToken correlates an asynchronous rename with a UI request.
func RenameSessionCmdWithToken(ctx context.Context, r SessionRenamer, id, title string, requestToken uint64) tea.Cmd {
	return func() tea.Msg {
		snapshot, err := r.RenameSession(ctx, id, title)
		return SessionRenamedMsg{SessionID: id, RequestToken: requestToken, Title: snapshot.Title, TitleProvenance: snapshot.TitleProvenance, Err: err}
	}
}

// DeleteSessionCmd performs DeleteSession off the reducer goroutine.
func DeleteSessionCmd(ctx context.Context, d SessionDeleter, id string) tea.Cmd {
	return func() tea.Msg {
		return SessionDeletedMsg{SessionID: id, Err: d.DeleteSession(ctx, id)}
	}
}
