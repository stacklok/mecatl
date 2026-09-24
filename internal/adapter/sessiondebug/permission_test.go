package sessiondebug_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/sessiondebug"
)

type policyStub struct {
	decision governance.PermissionDecision
	learned  int
}

func (p *policyStub) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) port.PermissionResult {
	return port.PermissionResult{Decision: p.decision}
}
func (p *policyStub) Learn(session.SessionID, session.ToolCall) { p.learned++ }

type classifiedTool struct {
	name     string
	readOnly bool
}

func (t classifiedTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (t classifiedTool) ReadOnly() bool { return t.readOnly }
func (classifiedTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}

func TestDebugMCPPermissionPolicy(t *testing.T) {
	store := memstore.New()
	target := session.New("target", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/target", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := store.Save(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	read := classifiedTool{name: "mcp__github__get_issue", readOnly: true}
	write := classifiedTool{name: "mcp__github__create_issue"}
	call := func(name string) session.ToolCall { return session.NewToolCall("c", name, json.RawMessage(`{}`)) }
	newPolicy := func(base port.PermissionPolicy, headless bool, mounted []tool.Tool) *sessiondebug.PermissionPolicy {
		return sessiondebug.NewPermissionPolicy(base, store, target.ID, session.DebugTargetFingerprint(target), target.Owner, false, headless, mounted)
	}

	for _, tc := range []struct {
		name string
		base governance.PermissionDecision
		want governance.PermissionDecision
	}{
		{"inspect preserves deny", governance.PermissionDecision{Effect: governance.Deny, Reason: "managed deny"}, governance.PermissionDecision{Effect: governance.Deny, Reason: "managed deny"}},
		{"inspect preserves configured ask", governance.PermissionDecision{Effect: governance.Ask, Reason: "operator ask", ConfiguredAsk: true}, governance.PermissionDecision{Effect: governance.Ask, Reason: "operator ask", ConfiguredAsk: true}},
		{"inspect floors default ask", governance.PermissionDecision{Effect: governance.Ask, Reason: "default ask"}, governance.PermissionDecision{Effect: governance.Allow, Reason: "target-bound debug evidence is read-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := newPolicy(&policyStub{decision: tc.base}, false, nil).Evaluate(t.Context(), "debug", session.ModeDefault, call(sessiondebug.ToolName), nil).Decision
			if got != tc.want {
				t.Fatalf("decision=%+v want %+v", got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name string
		base governance.Effect
		tool classifiedTool
		want governance.Effect
	}{
		{"read allow becomes ask", governance.Allow, read, governance.Ask},
		{"write allow becomes ask", governance.Allow, write, governance.Ask},
		{"configured ask stays ask", governance.Ask, write, governance.Ask},
		{"deny dominates", governance.Deny, write, governance.Deny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := &policyStub{decision: governance.PermissionDecision{Effect: tc.base}}
			p := newPolicy(base, false, []tool.Tool{tc.tool})
			if got := p.Evaluate(t.Context(), "debug", session.ModeDefault, call(tc.tool.name), nil).Decision.Effect; got != tc.want {
				t.Fatalf("effect=%v want %v", got, tc.want)
			}
		})
	}

	configuredBase := &policyStub{decision: governance.PermissionDecision{Effect: governance.Ask, ConfiguredAsk: true, Reason: "configured"}}
	configured := newPolicy(configuredBase, false, []tool.Tool{write})
	configuredDecision := configured.Evaluate(t.Context(), "debug", session.ModeDefault, call(write.name), nil).Decision
	if !configuredDecision.ConfiguredAsk || configuredDecision.Reason != "configured" {
		t.Fatalf("debug policy did not preserve the configured ask: %+v", configuredDecision)
	}
	if again := configured.Evaluate(t.Context(), "debug", session.ModeDefault, call(write.name), nil); again.Decision.Effect != governance.Ask {
		t.Fatalf("second mutation effect=%v want ask", again.Decision.Effect)
	}

	base := &policyStub{decision: governance.PermissionDecision{Effect: governance.Allow}}
	headless := newPolicy(base, true, []tool.Tool{write})
	if got := headless.Evaluate(t.Context(), "debug", session.ModeDefault, call(write.name), nil).Decision.Effect; got != governance.Deny {
		t.Fatalf("headless mutation effect=%v want deny", got)
	}
	p := newPolicy(base, false, []tool.Tool{read, write})
	p.Learn("debug", call(write.name))
	if base.learned != 0 {
		t.Fatal("mutating debug MCP allow-always was learned")
	}
	p.Learn("debug", call(read.name))
	if base.learned != 0 {
		t.Fatal("read-only debug MCP allow-always was learned")
	}
	if err := store.Delete(t.Context(), target.ID); err != nil {
		t.Fatal(err)
	}
	if got := p.Evaluate(t.Context(), "debug", session.ModeDefault, call(write.name), nil).Decision.Effect; got != governance.Deny {
		t.Fatalf("deleted target effect=%v want deny", got)
	}
}
