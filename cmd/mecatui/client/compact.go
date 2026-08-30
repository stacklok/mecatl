package client

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// SessionCompactor is the narrow manual-compaction collaborator used by the UI.
type SessionCompactor interface {
	CompactSession(ctx context.Context, sessionID string) (bool, error)
}

// SessionCompactedMsg reports completion of one session-correlated compact RPC.
type SessionCompactedMsg struct {
	SessionID    string
	RequestToken uint64
	Compacted    bool
	Err          error
}

// CompactSession invokes one out-of-band compaction pass.
func (c *Client) CompactSession(ctx context.Context, sessionID string) (bool, error) {
	resp, err := c.svc.CompactSession(ctx, &mecatlv1.CompactSessionRequest{SessionId: sessionID})
	if err != nil {
		return false, fmt.Errorf("compact session: %w", err)
	}
	return resp.GetCompacted(), nil
}

// CompactSessionCmd performs compaction off the Bubble Tea update goroutine.
func CompactSessionCmd(ctx context.Context, c SessionCompactor, sessionID string, requestToken uint64) tea.Cmd {
	return func() tea.Msg {
		compacted, err := c.CompactSession(ctx, sessionID)
		return SessionCompactedMsg{SessionID: sessionID, RequestToken: requestToken, Compacted: compacted, Err: err}
	}
}
