package sessiondebug_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/sessiondebug"
)

type countingMCP struct{ calls int }

func (*countingMCP) Spec() tool.ToolSpec { return tool.ToolSpec{Name: "mcp__test__read"} }
func (*countingMCP) ReadOnly() bool      { return true }
func (t *countingMCP) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.calls++
	return session.NewToolResult(call.ID, "ok"), nil
}

func TestSelectedMCPRevalidatesTargetAfterApprovalWait(t *testing.T) {
	ctx := t.Context()
	store := memstore.New()
	target := session.New("target", session.ModeDefault, "", session.Limits{}, time.Unix(1, 0))
	if err := store.Save(ctx, target); err != nil {
		t.Fatal(err)
	}
	remote := &countingMCP{}
	bound := sessiondebug.BindSelectedMCP(remote, store, target.ID, session.DebugTargetFingerprint(target), target.Owner, false)

	// Simulate replacement while an already-issued permission ask is waiting.
	if err := store.Delete(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	replacement := session.New(target.ID, session.ModeDefault, "", session.Limits{}, time.Unix(2, 0))
	if err := store.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	result, err := bound.Execute(ctx, session.NewToolCall("call", remote.Spec().Name, []byte(`{}`)), tool.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || remote.calls != 0 {
		t.Fatalf("replacement result=%+v remote calls=%d, want inaccessible/0", result, remote.calls)
	}
}
