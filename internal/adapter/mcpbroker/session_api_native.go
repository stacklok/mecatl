package mcpbroker

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/stacklok/mecatl/engine/session"
)

type durableNativeKey struct{}
type durableNativeCall struct {
	attachment *Attachment
	hash       [32]byte
	filter     string
	used       atomic.Bool
}

// Only dispatchInvocation creates this single-use, exact native-call exemption,
// after process-local admission. It conveys no authorization or tokens.
func (t *sessionTool) claimSessionCallLocked(ctx context.Context, grant *oauthGrant, call session.ToolCall, hash [32]byte) error {
	claim, ok := ctx.Value(durableNativeKey{}).(*durableNativeCall)
	if !ok {
		return claimGrantCallLocked(grant, call, hash)
	}
	if claim.attachment != t.attachment || claim.hash != hash || claim.filter != t.queryFilter || !claim.used.CompareAndSwap(false, true) {
		return errors.New("broker durable native call mismatch or already consumed")
	}
	return nil
}
