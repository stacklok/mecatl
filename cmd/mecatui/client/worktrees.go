package client

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The worktree discovery surface (issue #102): a plain client-owned struct
// mirroring the proto Worktree message, the unary RPC wrapper that maps proto →
// the struct, and the tea.Cmd constructor the ui's /worktrees overlay calls. As
// with the rest of this package, NO proto type leaks past this file — the ui
// renders purely from the structs and msgs below, and the mapping is exercised
// offline against a fake client.

// WorktreeSelector is opaque source-scoped authority issued by the server.
// Its token cannot be read or fabricated outside this package.
type WorktreeSelector struct{ token string }

// NewWorktreeSelector validates and wraps an opaque server-issued selector.
func NewWorktreeSelector(token string) (WorktreeSelector, error) {
	if token == "" {
		return WorktreeSelector{}, fmt.Errorf("worktree selector must not be empty")
	}
	return WorktreeSelector{token: token}, nil
}

// IsZero reports whether the selector is absent or invalid.
func (s WorktreeSelector) IsZero() bool { return s.token == "" }

// Worktree is one display-safe, server-authorized placement choice. Selector is
// opaque authority scoped to the current source session; all other fields are
// display metadata and must never be interpreted as paths.
type Worktree struct {
	Selector WorktreeSelector
	Kind     string
	Label    string
	Branch   string
	Revision string
	Bare     bool
}

// WorktreesMsg carries a ListWorktrees success (the overlay's worktree list).
// Err is set on failure; the overlay renders it as an error line (distinct from
// the empty-list "no worktrees found" path) so the user can see the discovery
// fault.
type WorktreesMsg struct {
	Worktrees []Worktree
	Err       error
}

// ListWorktrees lists server-issued choices scoped to sessionID.
func (c *Client) ListWorktrees(ctx context.Context, sessionID string) ([]Worktree, error) {
	resp, err := c.svc.ListWorktrees(ctx, &mecatlv1.ListWorktreesRequest{SessionId: sessionID})
	if err != nil {
		return nil, err
	}
	return mapWorktrees(resp.GetWorktrees()), nil
}

// mapWorktrees maps proto Worktrees to the plain structs (nil-safe).
func mapWorktrees(in []*mecatlv1.Worktree) []Worktree {
	out := make([]Worktree, 0, len(in))
	for _, w := range in {
		out = append(out, Worktree{
			Selector: WorktreeSelector{token: w.GetSelector()},
			Kind:     w.GetKind(),
			Label:    w.GetLabel(),
			Branch:   w.GetBranch(),
			Revision: w.GetRevision(),
			Bare:     w.GetBare(),
		})
	}
	return out
}

// WorktreeLister is the subset of *Client the ui's /worktrees overlay needs.
// Splitting it out keeps the ui injectable with a fake for offline tests;
// *Client satisfies it. The method name List matches server.WorktreeLister.List
// (house style for Config seam interfaces, matching CommandLister.List).
type WorktreeLister interface {
	List(ctx context.Context, sessionID string) ([]Worktree, error)
}

// List implements WorktreeLister for one owned source session.
func (c *Client) List(ctx context.Context, sessionID string) ([]Worktree, error) {
	return c.ListWorktrees(ctx, sessionID)
}

// ListWorktreesCmd fetches source-session-scoped worktree choices.
func ListWorktreesCmd(ctx context.Context, c WorktreeLister, sessionID string) tea.Cmd {
	return func() tea.Msg {
		wts, err := c.List(ctx, sessionID)
		if err != nil {
			return WorktreesMsg{Err: err}
		}
		return WorktreesMsg{Worktrees: wts}
	}
}
