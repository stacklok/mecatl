package client

import (
	"context"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The stored-session inventory surface (issue #245 Phase 2): a plain
// client-owned struct mirroring the proto SessionSummary, the unary RPC wrapper
// that maps proto → the struct, the narrow interface for test injectability, and
// the tea.Cmd constructor the ui's "open existing session" picker calls. As
// with the rest of this package, NO proto type leaks past this file — the ui
// renders purely from the structs and msgs below, and the mapping is exercised
// offline against a fake client.

// SessionListItem is one stored session's picker metadata (proto SessionSummary,
// proto-free): id, timestamps, state, turn count, and the resolved model id. It
// carries NO conversation content — it is the cheap row a client renders in an
// "open existing session" picker. Mirrors SessionSnapshot/Worktree. ModifiedAt
// is the sort key (Unix seconds, most-recently-active first).
type SessionListItem struct {
	ID         string
	ModifiedAt int64 // Unix seconds; the sort key
	State      string
	Turns      int32
	ModelID    string
	CreatedAt  int64 // Unix seconds
	Title      string
}

// SessionsListedMsg carries a ListSessions success (the picker's session list).
// Err is set on failure; the picker renders it as an error line (distinct from
// the empty-list "no sessions found" path) so the user can see the discovery
// fault.
type SessionsListedMsg struct {
	Sessions []SessionListItem
	Err      error
}

// ListSessions lists the stored-session inventory — the picker metadata a client
// renders in an "open existing session" picker. It is the proto-build point for
// the picker. The server returns the list sorted by modified_at descending.
func (c *Client) ListSessions(ctx context.Context) ([]SessionListItem, error) {
	resp, err := c.svc.ListSessions(ctx, &mecatlv1.ListSessionsRequest{})
	if err != nil {
		return nil, err
	}
	return listSessionsFromProto(resp.GetSessions()), nil
}

// listSessionsFromProto maps proto SessionSummary values to the plain structs
// (nil-safe via the generated getters). A nil summary INSIDE the slice maps to
// the zero SessionListItem (nil-safe), like mapWorktrees/snapshotFrom.
func listSessionsFromProto(in []*mecatlv1.SessionSummary) []SessionListItem {
	out := make([]SessionListItem, 0, len(in))
	for _, s := range in {
		out = append(out, SessionListItem{
			ID:         s.GetSessionId(),
			ModifiedAt: s.GetModifiedAtUnix(),
			State:      s.GetState(),
			Turns:      s.GetTurns(),
			ModelID:    s.GetModelId(),
			CreatedAt:  s.GetCreatedAtUnix(),
			Title:      s.GetTitle(),
		})
	}
	return out
}

// SessionLister is the narrow subset of *Client the ui's session picker needs.
// Splitting it out keeps the ui injectable with a fake for offline tests; *Client
// satisfies it (the SessionGetter/WorktreeLister pattern).
type SessionLister interface {
	ListSessions(ctx context.Context) ([]SessionListItem, error)
}

// SessionReplayer is the narrow subset of *Client the ui's read-only transcript
// viewer needs: opening the durable-event-log replay stream for one stored session
// (issue #245 Phase 2/3, cloud-native Phase 3a read-back). Split out from *Client
// so the ui is injectable with a fake for offline tests; *Client satisfies it (the
// StreamSessionEvents method on events.go is the concrete implementation).
type SessionReplayer interface {
	StreamSessionEvents(ctx context.Context, id string) (*EventStream, error)
}

// Compile-time assertion: *Client satisfies SessionReplayer (the picker's
// replay-stream collaborator). Mirrors the SessionLister pattern.
var _ SessionReplayer = (*Client)(nil)

// ListSessionsCmd fetches the stored-session inventory off the update goroutine;
// the result (success or error) arrives as a SessionsListedMsg. Mirrors
// ListWorktreesCmd.
func ListSessionsCmd(ctx context.Context, s SessionLister) tea.Cmd {
	return func() tea.Msg {
		sessions, err := s.ListSessions(ctx)
		if err != nil {
			return SessionsListedMsg{Err: err}
		}
		return SessionsListedMsg{Sessions: sessions}
	}
}
