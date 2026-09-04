package client

import (
	"context"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// LocalSessionContextGetter retrieves the opt-in privileged local root for an
// already-owned session. The root is observational only.
type LocalSessionContextGetter interface {
	GetLocalSessionContext(context.Context, string) (string, error)
}

// GetLocalSessionContext reads ADR 0296's optional local-client projection.
// An unavailable service is returned as its normal gRPC error; callers retain
// their launch-directory fallback.
func (c *Client) GetLocalSessionContext(ctx context.Context, sessionID string) (string, error) {
	resp, err := c.localContext.GetLocalSessionContext(withSessionAffinity(ctx, sessionID), &mecatlv1.GetLocalSessionContextRequest{SessionId: sessionID})
	if err != nil {
		return "", err
	}
	return resp.GetWorkspacePath(), nil
}
