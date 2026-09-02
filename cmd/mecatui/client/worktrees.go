package client

import (
	"context"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The worktree discovery surface (issue #102): a plain client-owned struct
// mirroring the proto Worktree message, the unary RPC wrapper that maps proto →
// the struct, and the tea.Cmd constructor the ui's /worktrees overlay calls. As
// with the rest of this package, NO proto type leaks past this file — the ui
// renders purely from the structs and msgs below, and the mapping is exercised
// offline against a fake client.

// Worktree is one discovered git worktree (proto Worktree, proto-free): the
// absolute working-tree path (the value handed to CreateSession to bind a
// session there), the checked-out branch (empty for detached HEAD), the commit
// SHA the worktree is at, and whether it is bare. The ui's /worktrees overlay
// lists these and, on select, creates a NEW session rooted at Path.
type Worktree struct {
	Path   string
	Branch string
	Head   string
	Bare   bool
}

// WorktreesMsg carries a ListWorktrees success (the overlay's worktree list).
// Err is set on failure; the overlay renders it as an error line (distinct from
// the empty-list "no worktrees found" path) so the user can see the discovery
// fault.
type WorktreesMsg struct {
	Worktrees []Worktree
	Err       error
}

// ListWorktrees lists the git worktrees of the repo rooted at workspace
// ("" => empty). It is the proto-build point for the /worktrees overlay.
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
			Path:   w.GetLabel(),
			Branch: w.GetBranch(),
			Head:   w.GetRevision(),
			Bare:   w.GetBare(),
		})
	}
	return out
}

// WorktreeLister is the subset of *Client the ui's /worktrees overlay needs.
// Splitting it out keeps the ui injectable with a fake for offline tests;
// *Client satisfies it. The method name List matches server.WorktreeLister.List
// (house style for Config seam interfaces, matching CommandLister.List).
type WorktreeLister interface {
	List(ctx context.Context, workspace string) ([]Worktree, error)
}

// List implements WorktreeLister. It delegates to ListWorktrees so *Client
// satisfies the interface while keeping the public ListWorktrees name stable.
func (c *Client) List(ctx context.Context, workspace string) ([]Worktree, error) {
	return c.ListWorktrees(ctx, workspace)
}

// ListWorktreesCmd fetches the worktrees for workspace off the update goroutine;
// the result (success or error) arrives as a WorktreesMsg.
func ListWorktreesCmd(ctx context.Context, c WorktreeLister, workspace string) tea.Cmd {
	return func() tea.Msg {
		wts, err := c.List(ctx, workspace)
		if err != nil {
			return WorktreesMsg{Err: err}
		}
		return WorktreesMsg{Worktrees: wts}
	}
}
