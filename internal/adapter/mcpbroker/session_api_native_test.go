package mcpbroker

import (
	"context"
	"github.com/stacklok/mecatl/engine/session"
	"testing"
)

func TestSessionAPINativeClaimSingleUseAndDonorLedger(t *testing.T) {
	attachment := &Attachment{}
	target := &sessionTool{attachment: attachment}
	call := session.NewToolCall("same", "mcp__private__read", []byte(`{}`))
	grant := &oauthGrant{executed: make(map[session.ToolCallID][32]byte)}
	hash := callHash(call)
	ctx := context.WithValue(t.Context(), durableNativeKey{}, &durableNativeCall{attachment: attachment, hash: hash})
	if err := target.claimSessionCallLocked(ctx, grant, call, hash); err != nil {
		t.Fatal(err)
	}
	if err := target.claimSessionCallLocked(ctx, grant, call, hash); err == nil {
		t.Fatal("native durable marker reused")
	}
	if len(grant.executed) != 0 {
		t.Fatal("durable path used donor ledger")
	}
	if err := target.claimSessionCallLocked(t.Context(), grant, call, hash); err != nil {
		t.Fatal(err)
	}
	if err := target.claimSessionCallLocked(t.Context(), grant, call, hash); err == nil {
		t.Fatal("donor duplicate accepted")
	}
	changed := session.NewToolCall("same", "mcp__private__read", []byte(`{ }`))
	ctx = context.WithValue(t.Context(), durableNativeKey{}, &durableNativeCall{attachment: attachment, hash: hash})
	if err := target.claimSessionCallLocked(ctx, grant, changed, callHash(changed)); err == nil {
		t.Fatal("marker accepted changed arguments")
	}
}
