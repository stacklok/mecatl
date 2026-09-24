package permpolicy_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// Compile-time assertion that Policy satisfies the frozen port interface.
var _ port.PermissionPolicy = (*permpolicy.Policy)(nil)

func shellCall(cmd string) session.ToolCall {
	args, _ := json.Marshal(map[string]string{"command": cmd})
	return session.NewToolCall("c1", "Shell", args)
}

func fileCall(toolName, p string) session.ToolCall {
	args, _ := json.Marshal(map[string]string{"file_path": p})
	return session.NewToolCall("c1", toolName, args)
}

const sid = session.SessionID("s1")

// gauntlet #8 end-to-end through the port: deny beats allow across scopes.
func TestPolicyDenyBeatsAllow(t *testing.T) {
	p := permpolicy.NewPolicy([]governance.Rule{
		{Scope: governance.ScopeManaged, Tool: "Shell", Pattern: "rm *", Effect: governance.Allow},
		{Scope: governance.ScopeUser, Tool: "Shell", Pattern: "rm *", Effect: governance.Deny},
	}, nil)
	got := p.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("rm x"), nil).Decision
	if got.Effect != governance.Deny {
		t.Fatalf("expected Deny, got %v", got.Effect)
	}
}

// gauntlet #3 through the port: plan mode denies Write, allows Read.
func TestPolicyPlanMode(t *testing.T) {
	p := permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil)

	if got := p.Evaluate(context.Background(), sid, session.ModePlan, fileCall("Write", "/x"), nil).Decision; got.Effect != governance.Deny {
		t.Fatalf("plan mode Write: expected Deny, got %v", got.Effect)
	}
	if got := p.Evaluate(context.Background(), sid, session.ModePlan, fileCall("Read", "/x"), nil).Decision; got.Effect != governance.Allow {
		t.Fatalf("plan mode Read: expected Allow, got %v", got.Effect)
	}
	// Non-plan mode lets the mutating tool through (rule allows it).
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, fileCall("Write", "/x"), nil).Decision; got.Effect != governance.Allow {
		t.Fatalf("default mode Write: expected Allow, got %v", got.Effect)
	}
}

// TestPolicyAcceptEditsAutoAllowsEditWrite is the issue-#674 regression:
// session.ModeAccept ("accept-edits") must auto-allow Edit/Write against the
// built-in mutate-ask floor, without any per-call "allow always" or config
// rule -- unlike the pre-fix behaviour where accept-edits mode was never
// consulted by Evaluate at all and every Edit/Write kept asking.
func TestPolicyAcceptEditsAutoAllowsEditWrite(t *testing.T) {
	p := permpolicy.NewPolicy(nil, nil) // defaultRules-shaped: no rules -> default Ask floor.

	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, fileCall("Edit", "/x"), nil).Decision; got.Effect != governance.Ask {
		t.Fatalf("default mode Edit: expected Ask (no rule matched), got %v", got.Effect)
	}
	if got := p.Evaluate(context.Background(), sid, session.ModeAccept, fileCall("Edit", "/x"), nil).Decision; got.Effect != governance.Allow {
		t.Fatalf("accept-edits mode Edit: expected Allow, got %v", got.Effect)
	}
	if got := p.Evaluate(context.Background(), sid, session.ModeAccept, fileCall("Write", "/x"), nil).Decision; got.Effect != governance.Allow {
		t.Fatalf("accept-edits mode Write: expected Allow, got %v", got.Effect)
	}
	// A second, differently-shaped Edit call in the SAME session must ALSO
	// auto-allow -- accept-edits does not depend on the narrow per-call
	// learned-rule pattern the "allow always" verdict derives.
	if got := p.Evaluate(context.Background(), sid, session.ModeAccept, fileCall("Edit", "/y"), nil).Decision; got.Effect != governance.Allow {
		t.Fatalf("accept-edits mode second Edit: expected Allow, got %v", got.Effect)
	}
	// Shell is untouched: accept-edits auto-accepts file edits only.
	if got := p.Evaluate(context.Background(), sid, session.ModeAccept, shellCall("rm x"), nil).Decision; got.Effect != governance.Ask {
		t.Fatalf("accept-edits mode Shell: expected Ask (unaffected), got %v", got.Effect)
	}
}

// TestPolicyAcceptEditsDefersToConfiguredAskAndDeny: accept-edits only loosens
// the built-in floor -- a deliberately configured (above-floor) Ask or Deny for
// Edit/Write still wins.
func TestPolicyAcceptEditsDefersToConfiguredAskAndDeny(t *testing.T) {
	pAsk := permpolicy.NewPolicy([]governance.Rule{
		{Scope: governance.ScopeUser, Tool: "Edit", Pattern: "/secret", Effect: governance.Ask},
	}, nil)
	if got := pAsk.Evaluate(context.Background(), sid, session.ModeAccept, fileCall("Edit", "/secret"), nil).Decision; got.Effect != governance.Ask {
		t.Fatalf("accept-edits must defer to a configured Ask, got %v", got.Effect)
	}

	pDeny := permpolicy.NewPolicy([]governance.Rule{
		{Scope: governance.ScopeManaged, Tool: "Write", Pattern: "/etc/*", Effect: governance.Deny},
	}, nil)
	if got := pDeny.Evaluate(context.Background(), sid, session.ModeAccept, fileCall("Write", "/etc/passwd"), nil).Decision; got.Effect != governance.Deny {
		t.Fatalf("accept-edits must defer to a configured Deny, got %v", got.Effect)
	}
}

// Learn then Evaluate: an allow-always learned for `git status` makes the next
// `git status` resolve Allow (it would otherwise be the default Ask).
func TestPolicyLearnThenAllow(t *testing.T) {
	p := permpolicy.NewPolicy(nil, permstore.New())
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("git status"), nil).Decision; got.Effect != governance.Ask {
		t.Fatalf("pre-learn: expected Ask, got %v", got.Effect)
	}
	p.Learn(sid, shellCall("git status"))
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("git status"), nil).Decision; got.Effect != governance.Allow {
		t.Fatalf("post-learn: expected Allow, got %v", got.Effect)
	}
}

// A learned allow NEVER overrides a static deny.
func TestPolicyLearnedAllowCannotOverrideDeny(t *testing.T) {
	p := permpolicy.NewPolicy([]governance.Rule{
		{Scope: governance.ScopeManaged, Tool: "Shell", Pattern: "git status", Effect: governance.Deny},
	}, permstore.New())
	p.Learn(sid, shellCall("git status"))
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("git status"), nil).Decision; got.Effect != governance.Deny {
		t.Fatalf("expected Deny to survive a learned allow, got %v", got.Effect)
	}
}

// A learned allow does NOT bypass plan-mode mutation denial.
func TestPolicyLearnedAllowDoesNotBypassPlanMode(t *testing.T) {
	p := permpolicy.NewPolicy(nil, permstore.New())
	p.Learn(sid, fileCall("Write", "/x"))
	if got := p.Evaluate(context.Background(), sid, session.ModePlan, fileCall("Write", "/x"), nil).Decision; got.Effect != governance.Deny {
		t.Fatalf("plan mode must still deny Write despite learned allow, got %v", got.Effect)
	}
}

// Cross-session isolation: a rule learned in session A is invisible to session B.
func TestPolicyLearnedRuleSessionIsolation(t *testing.T) {
	p := permpolicy.NewPolicy(nil, permstore.New())
	p.Learn("A", shellCall("git status"))
	if got := p.Evaluate(context.Background(), "A", session.ModeDefault, shellCall("git status"), nil).Decision; got.Effect != governance.Allow {
		t.Fatalf("session A should see its learned allow, got %v", got.Effect)
	}
	if got := p.Evaluate(context.Background(), "B", session.ModeDefault, shellCall("git status"), nil).Decision; got.Effect != governance.Ask {
		t.Fatalf("session B must NOT see session A's learned rule, got %v", got.Effect)
	}
}

// A compound Shell command is refused by Learn (no-op): nothing is recorded, so a
// later single `git status` still asks (the compound's first segment was NOT
// silently learned as a tool+pattern allow).
func TestPolicyLearnRefusesCompound(t *testing.T) {
	p := permpolicy.NewPolicy(nil, permstore.New())
	p.Learn(sid, shellCall("git status; rm -rf /"))
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("git status"), nil).Decision; got.Effect != governance.Ask {
		t.Fatalf("a compound learn must not have recorded `git status`, got %v", got.Effect)
	}
}

// With a nil store, Learn is a no-op and Evaluate is the pure static policy.
func TestPolicyNilStoreLearnIsNoop(t *testing.T) {
	p := permpolicy.NewPolicy(nil, nil)
	p.Learn(sid, shellCall("git status")) // must not panic
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("git status"), nil).Decision; got.Effect != governance.Ask {
		t.Fatalf("nil-store policy should stay Ask, got %v", got.Effect)
	}
}

// fakeResolver returns a different rule set PER ws.Root(), so a single Policy can
// be exercised against two workspaces and observed to decide differently. It is
// the per-session-resolution seam (issue #13) in miniature.
type fakeResolver struct {
	byRoot map[string][]governance.Rule
}

func (f fakeResolver) Resolve(_ context.Context, ws tool.WorkspaceReader) []governance.Rule {
	if ws == nil {
		return nil
	}
	return f.byRoot[ws.Root()]
}

// Same Policy + two workspaces with different resolved rules → different decisions
// for the SAME tool call. This is the core per-session-resolution acceptance proof
// at the policy level: the workspace, not the session, selects the config.
func TestPolicyResolverPerWorkspace(t *testing.T) {
	resolver := fakeResolver{byRoot: map[string][]governance.Rule{
		"/ws-a": {{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: governance.Allow}},
		"/ws-b": {{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: governance.Deny}},
	}}
	// Built-in floor: Shell asks. A config allow loosens it; a config deny tightens.
	p := permpolicy.NewPolicyWithResolver(
		[]governance.Rule{{Scope: governance.ScopeBuiltinDefault, Tool: "Shell", Effect: governance.Ask}},
		nil, resolver)

	wsA := memfs.NewWorkspace("/ws-a")
	wsB := memfs.NewWorkspace("/ws-b")
	call := shellCall("go test ./...")

	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, call, wsA).Decision; got.Effect != governance.Allow {
		t.Fatalf("ws-a should allow (config allow loosens built-in ask), got %v", got.Effect)
	}
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, call, wsB).Decision; got.Effect != governance.Deny {
		t.Fatalf("ws-b should deny (config deny), got %v", got.Effect)
	}
	// A nil workspace falls back to the built-in floor (resolver returns nothing).
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, call, nil).Decision; got.Effect != governance.Ask {
		t.Fatalf("nil ws should stay at the built-in ask, got %v", got.Effect)
	}
}

// A project deny resolved for a workspace beats a per-session LEARNED allow: both
// ride the same lowest-scope extra channel, and the fold is deny-dominant.
func TestPolicyResolverDenyBeatsLearnedAllow(t *testing.T) {
	resolver := fakeResolver{byRoot: map[string][]governance.Rule{
		"/ws": {{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "git status", Effect: governance.Deny}},
	}}
	p := permpolicy.NewPolicyWithResolver(nil, permstore.New(), resolver)
	p.Learn(sid, shellCall("git status")) // learn an allow for the very same call
	ws := memfs.NewWorkspace("/ws")
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("git status"), ws).Decision; got.Effect != governance.Deny {
		t.Fatalf("project deny must beat a learned allow, got %v", got.Effect)
	}
}

// TestPolicyAllowAllLoosensBuiltinFloor: a ScopeCLI allow-all rule loosens the
// built-in ScopeBuiltinDefault Ask floor for mutating tools (the operator
// allow-all posture). See docs/adr/0022-allow-all-posture.md.
func TestPolicyAllowAllLoosensBuiltinFloor(t *testing.T) {
	pShell := permpolicy.NewPolicy([]governance.Rule{
		{Scope: governance.ScopeBuiltinDefault, Tool: "Shell", Effect: governance.Ask},
		{Scope: governance.ScopeCLI, Effect: governance.Allow},
	}, nil)
	if got := pShell.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("ls"), nil).Decision; got.Effect != governance.Allow {
		t.Fatalf("allow-all should loosen the built-in Shell Ask floor, got %v", got.Effect)
	}

	pEdit := permpolicy.NewPolicy([]governance.Rule{
		{Scope: governance.ScopeBuiltinDefault, Tool: "Edit", Effect: governance.Ask},
		{Scope: governance.ScopeCLI, Effect: governance.Allow},
	}, nil)
	if got := pEdit.Evaluate(context.Background(), sid, session.ModeDefault, fileCall("Edit", "/x"), nil).Decision; got.Effect != governance.Allow {
		t.Fatalf("allow-all should loosen the built-in Edit Ask floor, got %v", got.Effect)
	}
}

// TestPolicyAllowAllLosesToManagedDeny: deny-dominance is absolute — a
// ScopeManaged Deny beats the ScopeCLI allow-all rule.
func TestPolicyAllowAllLosesToManagedDeny(t *testing.T) {
	p := permpolicy.NewPolicy([]governance.Rule{
		{Scope: governance.ScopeBuiltinDefault, Tool: "Shell", Effect: governance.Ask},
		{Scope: governance.ScopeCLI, Effect: governance.Allow},
		{Scope: governance.ScopeManaged, Tool: "Shell", Pattern: "rm *", Effect: governance.Deny},
	}, nil)
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("rm x"), nil).Decision; got.Effect != governance.Deny {
		t.Fatalf("a managed Deny must beat allow-all, got %v", got.Effect)
	}
}

// TestPolicyAllowAllDefersToConfiguredAsk: a deliberately configured (non-builtin)
// Ask still asks under allow-all — the posture only loosens the built-in floor.
func TestPolicyAllowAllDefersToConfiguredAsk(t *testing.T) {
	p := permpolicy.NewPolicy([]governance.Rule{
		{Scope: governance.ScopeBuiltinDefault, Tool: "Shell", Effect: governance.Ask},
		{Scope: governance.ScopeCLI, Effect: governance.Allow},
		{Scope: governance.ScopeUser, Tool: "Shell", Pattern: "git push*", Effect: governance.Ask},
	}, nil)
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("git push origin"), nil).Decision; got.Effect != governance.Ask {
		t.Fatalf("allow-all must defer to a configured Ask, got %v", got.Effect)
	}
}

// TestPolicyAllowAllCompoundShellDenyWins: the substitution/newline-aware bash gate
// still wins under allow-all — a configured Deny matching one segment of a compound
// command denies the whole command.
func TestPolicyAllowAllCompoundShellDenyWins(t *testing.T) {
	p := permpolicy.NewPolicy([]governance.Rule{
		{Scope: governance.ScopeBuiltinDefault, Tool: "Shell", Effect: governance.Ask},
		{Scope: governance.ScopeCLI, Effect: governance.Allow},
		{Scope: governance.ScopeUser, Tool: "Shell", Pattern: "rm *", Effect: governance.Deny},
	}, nil)
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("ls && rm x"), nil).Decision; got.Effect != governance.Deny {
		t.Fatalf("compound-bash deny must win under allow-all, got %v", got.Effect)
	}
}

// --- Audience forwarding (issue #32) -----------------------------------------

// TestPolicyAudienceOptionForwarded pins that a governance.WithAudience option
// passed to NewPolicyWithResolver reaches the underlying Evaluator: a
// subagent-audience policy honours AudienceSubagent rules and ignores
// AudienceMain ones (and vice versa) — including rules arriving via the
// resolver's extra channel.
func TestPolicyAudienceOptionForwarded(t *testing.T) {
	resolver := fakeResolver{byRoot: map[string][]governance.Rule{
		"/ws": {
			{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: governance.Allow, Audience: governance.AudienceSubagent},
			{Scope: governance.ScopeSharedProject, Tool: "Shell", Pattern: "go vet*", Effect: governance.Allow, Audience: governance.AudienceMain},
		},
	}}
	floor := []governance.Rule{{Scope: governance.ScopeBuiltinDefault, Tool: "Shell", Effect: governance.Ask}}
	ws := memfs.NewWorkspace("/ws")

	subPolicy := permpolicy.NewPolicyWithResolver(floor, nil, resolver,
		governance.WithAudience(governance.AudienceSubagent))
	if got := subPolicy.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("go test ./..."), ws).Decision; got.Effect != governance.Allow {
		t.Fatalf("subagent policy must honour an AudienceSubagent resolver allow, got %v (%s)", got.Effect, got.Reason)
	}
	if got := subPolicy.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("go vet ./..."), ws).Decision; got.Effect != governance.Ask {
		t.Fatalf("subagent policy must IGNORE an AudienceMain resolver allow, got %v (%s)", got.Effect, got.Reason)
	}

	mainPolicy := permpolicy.NewPolicyWithResolver(floor, nil, resolver,
		governance.WithAudience(governance.AudienceMain))
	if got := mainPolicy.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("go vet ./..."), ws).Decision; got.Effect != governance.Allow {
		t.Fatalf("main policy must honour an AudienceMain resolver allow, got %v (%s)", got.Effect, got.Reason)
	}
	if got := mainPolicy.Evaluate(context.Background(), sid, session.ModeDefault, shellCall("go test ./..."), ws).Decision; got.Effect != governance.Ask {
		t.Fatalf("main policy must IGNORE an AudienceSubagent resolver allow, got %v (%s)", got.Effect, got.Reason)
	}
}
