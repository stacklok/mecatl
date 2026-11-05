package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type migratedApprovalWrite struct{ calls int }

func (*migratedApprovalWrite) Spec() tool.ToolSpec { return tool.ToolSpec{Name: "Write"} }
func (*migratedApprovalWrite) ReadOnly() bool      { return false }
func (w *migratedApprovalWrite) Execute(_ context.Context, c session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	w.calls++
	return session.NewToolResult(c.ID, "written"), nil
}

func TestLegacyContinuationCannotRestoreApprovalAuthority(t *testing.T) {
	for _, calls := range []string{`[{"ID":"old-provider","Name":"Write","Args":{"path":"old.txt"}}]`, `[{"ID":"old-provider","Name":"Write"},{"ID":"old-provider","Name":"Write"}]`} {
		raw := `{"id":"old-approval","state":"awaiting","mode":"default","messages":[{"role":"assistant","tool_calls":` + calls + `}],"pending":{"AskID":"old-ask","Tool":"Write","call":"old-provider","origin":"permission"},"environment_ref":{"Kind":"mem","ID":"/ws","Revision":"v1"}}`
		if _, err := sessnap.Unmarshal([]byte(raw)); err == nil {
			t.Fatal("legacy pending authority restored")
		}
	}
	_, err := eventcodec.Unmarshal([]byte(`{"Type":"approval","Approval":{"Call":"old-provider","Tool":"Write","Origin":"permission","Verdict":"allow_always","AllowAlways":true}}`))
	if !errors.Is(err, eventcodec.ErrSkipLegacyRecord) {
		t.Fatalf("old approval became replayable: %v", err)
	}
}
