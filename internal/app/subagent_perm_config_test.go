package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// permCfgWorkspace creates a temp workspace carrying a .mecatl/settings.yaml
// and returns the Build-shaped Config with the permission resolvers folded on
// (exactly what Build does right after the trust fold).
func permCfgWorkspace(t *testing.T, settingsYAML string) Config {
	t.Helper()
	base := t.TempDir()
	if settingsYAML != "" {
		if err := os.MkdirAll(filepath.Join(base, ".mecatl"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(base, ".mecatl", "settings.yaml"), []byte(settingsYAML), 0o644); err != nil {
			t.Fatalf("write settings: %v", err)
		}
	}
	cfg := Config{
		Workspace:               base,
		Model:                   "mock",
		Shell:                   "/bin/sh",
		PermissionsConventional: true,
		permConfigEnv:           isolatedPermConfigEnv(t),
		TrustProject:            true, // trusted: loads project tier AND grants the read-only subagent shell (the shell gate)
	}
	cfg.permResolver = buildPermResolver(cfg)
	cfg.childPermResolver = buildChildPermResolver(cfg)
	return cfg
}

// runSubagentBash drives the REAL wiring — buildSandboxedCommandRunner +
// buildChildEngine (the default explorer shape) + the worktree forker + the
// Subagent tool — with a scripted provider whose single Bash call is `command`.
// The Subagent fork is CLEANED UP after the run, so observable markers must
// land OUTSIDE it (an absolute path — the TestParallelBranchRunnerIsHardened
// precedent). Returns the Subagent result.
func runSubagentBash(t *testing.T, cfg Config, command string) session.ToolResult {
	t.Helper()
	runner := buildSandboxedCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a sandboxed runner (Shell set + trusted)")
	}
	provider := &bashWriteProvider{command: command, marker: "out.txt"}
	childEngine := buildChildEngine(cfg, nil, provider, "", cfg.Model, runner)

	rf := &recordingForker{inner: forker.New(func(root string) (tool.Workspace, error) {
		return osfs.NewWorkspace(root)
	}, forker.WithRunner(func(childRoot string) tool.CommandRunner {
		if cfg.NoBash || cfg.Shell == "" || !cfg.TrustProject {
			return nil
		}
		return newHardenedRunnerForRoot(cfg, childRoot)
	}))}
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(rf))

	baseWS, err := osfs.NewWorkspace(cfg.Workspace)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	res, err := task.Execute(context.Background(),
		session.NewToolCall("c1", "Subagent", json.RawMessage(`{"prompt":"run the command"}`)),
		testEnvironment(baseWS, buildCommandRunner(cfg)))
	if err != nil {
		t.Fatalf("Subagent.Execute: %v", err)
	}
	if roots := rf.roots(); len(roots) != 1 {
		t.Fatalf("expected exactly 1 forked child workspace, got %d: %v", len(roots), roots)
	}
	return res
}

// substFloorCmd builds a substitution-floored, non-isolation-approvable Bash
// line writing an ABSOLUTE marker: the redirection makes the blanked outer
// non-read-only (so A1 cannot clear it, and A2 rejects the redirection), while
// the inner (`ls`) is positively READ-ONLY — the issue-#32 bound requires that;
// only a configured subagent allow can then clear the floored ask. The
// hidden-non-read-only-inner complement is pinned by
// TestSubagentConfigAllowHiddenInnerStillDenied.
func substFloorCmd(marker string) string {
	return "echo $(ls) > " + marker
}

// TestSubagentConfigAllowEndToEnd drives the issue-#32 ALLOW direction through
// the real wiring: a project .mecatl/settings.yaml `subagent: allow:` block
// clears a substitution-floored child Bash that would otherwise headless-deny —
// observable as the marker file the command writes.
func TestSubagentConfigAllowEndToEnd(t *testing.T) {
	t.Run("without config the command is auto-denied", func(t *testing.T) {
		cfg := permCfgWorkspace(t, "")
		marker := filepath.Join(t.TempDir(), "out.txt")
		_ = runSubagentBash(t, cfg, substFloorCmd(marker))
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("control: the floored command must NOT run without a configured allow (err=%v)", err)
		}
	})
	t.Run("with a subagent allow it executes", func(t *testing.T) {
		cfg := permCfgWorkspace(t, "permissions:\n  subagent:\n    allow:\n      - \"Bash(echo:*)\"\n")
		marker := filepath.Join(t.TempDir(), "out.txt")
		res := runSubagentBash(t, cfg, substFloorCmd(marker))
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("the configured subagent allow should have cleared the floored command (marker missing: %v); result: %s",
				err, res.Content)
		}
	})
}

// TestSubagentConfigAllowHiddenInnerStillDenied is the e2e half of the
// issue-#32 ship-blocker bound through the REAL wiring: the SAME configured
// subagent allow that clears the read-only-inner command must NOT clear one
// whose substitution hides a NON-read-only inner (`zap` stand-in) — the child
// auto-denies headless and the command never runs.
func TestSubagentConfigAllowHiddenInnerStillDenied(t *testing.T) {
	cfg := permCfgWorkspace(t, "permissions:\n  subagent:\n    allow:\n      - \"Bash(echo:*)\"\n")
	marker := filepath.Join(t.TempDir(), "out.txt")
	res := runSubagentBash(t, cfg, "echo $(zap) > "+marker)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a configured outer allow must NOT auto-run a hidden non-read-only inner (err=%v); result: %s", err, res.Content)
	}
	if res.IsError {
		t.Fatalf("the denied child degrades, it does not hard-fail: %s", res.Content)
	}
}

// TestSubagentConfigDenyEndToEnd drives the DENY direction: a `subagent: deny:`
// block blocks even a PLAIN child Bash the allow-all floor would have run, and
// the child still degrades to a deliverable (no hard error).
func TestSubagentConfigDenyEndToEnd(t *testing.T) {
	cfg := permCfgWorkspace(t, "permissions:\n  subagent:\n    deny:\n      - \"Bash(echo:*)\"\n")
	marker := filepath.Join(t.TempDir(), "out.txt")
	res := runSubagentBash(t, cfg, "echo denied > "+marker)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the configured subagent deny must block the command (err=%v)", err)
	}
	if res.IsError {
		t.Fatalf("a denied child Bash degrades, it does not hard-fail the Subagent call: %s", res.Content)
	}
}

// TestChildEngineDepsForProviderSubagentPolicy pins the child deps builder's
// policy shape (issue #32) by EVALUATING it: subagent-block rules bind (ask is
// configured, allow clears the floor, deny blocks), top-level main rules do
// NOT, and the resolver is workspace-PINNED (a nil per-call workspace still
// resolves the project rules).
func TestChildEngineDepsForProviderSubagentPolicy(t *testing.T) {
	cfg := permCfgWorkspace(t, `
permissions:
  ask:
    - "Bash(go vet:*)"
  subagent:
    allow:
      - "Bash(cat:*)"
    ask:
      - "Bash(go test:*)"
    deny:
      - "Bash(curl:*)"
`)
	provider := &bashWriteProvider{command: "true", marker: "x"}
	deps := childEngineDepsForProvider(cfg, "task", provider, cfg.Model, func() int { return defaultContextWindowTokens }, tool.NewCatalog(), explorerPromptConfig(modelCfgFor(cfg, cfg.Model)), nil)

	eval := func(cmd string) governance.PermissionDecision {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		// nil workspace: the PINNED resolver must still serve the project rules.
		return deps.Policy.Evaluate(context.Background(), "s1", session.ModeDefault,
			session.NewToolCall("c1", "Bash", args), nil)
	}

	if got := eval("go test ./..."); got.Effect != governance.Ask || !got.ConfiguredAsk {
		t.Fatalf("subagent ask must bind the child policy as a CONFIGURED ask; got %+v", got)
	}
	if got := eval("curl example.com"); got.Effect != governance.Deny {
		t.Fatalf("subagent deny must bind the child policy; got %+v", got)
	}
	// `cat $(ls) > out.txt`: read-only inner, NON-read-only outer (redirection,
	// so A1 cannot clear it) covered by the configured `cat*` allow — the
	// floored-configured-allow shape.
	if got := eval("cat $(ls) > out.txt"); got.Effect != governance.Ask || !got.FlooredConfiguredAllow {
		t.Fatalf("subagent allow must mark the floored read-only-inner substitution FlooredConfiguredAllow; got %+v", got)
	}
	if got := eval("cat $(zap)"); got.Effect != governance.Ask || got.FlooredConfiguredAllow {
		t.Fatalf("a hidden non-read-only inner must NOT mark FlooredConfiguredAllow; got %+v", got)
	}
	// The top-level (AudienceMain) ask must NOT bind a child: the floor allow-all
	// resolves it.
	if got := eval("go vet ./..."); got.Effect != governance.Allow {
		t.Fatalf("a top-level main ask must be invisible to the child policy; got %+v", got)
	}
}

// TestAutoTierChildLoosensMutateFloorNotSubstitution pins the AUTO tier's main/child
// asymmetry (posture ladder): Config{AllowAllTools:true} WITHOUT LooseChildSubstitution
// is the AUTO tier (allow-all, main substitution loosened, child injection-defense ON).
// The MAIN policy loosens the substitution floor (a non-read-only $() resolves Allow),
// but a CHILD's substitution floor stays at Ask — resolved through the child-ask model,
// because childEvaluatorOptions(cfg) omits WithLooseSubstitution at auto (only yolo, via
// LooseChildSubstitution, loosens children — pinned by
// TestChildSubstitutionLooseningIsTierDependent). A plain (non-substitution) child
// mutate is Allow, but note that is the BLANKET child floor allowing it (children have
// no mutate-ask floor), NOT the allow-all rule — see
// TestChildPolicyAutoApprovesNonSubstitution; asserted here only to document the
// contrast with the still-floored substitution case at auto.
func TestAutoTierChildLoosensMutateFloorNotSubstitution(t *testing.T) {
	// AUTO tier: allow-all, but the child substitution defense stays ON.
	cfg := Config{Workspace: "", Model: "mock", AllowAllTools: true, LooseChildSubstitution: false}
	mutateArgs, _ := json.Marshal(map[string]string{"command": "zap -rf build"}) // unknown-verb mutate stand-in
	mutateCall := session.NewToolCall("c1", "Bash", mutateArgs)
	subArgs, _ := json.Marshal(map[string]string{"command": "cat $(zap)"}) // non-read-only inner
	subCall := session.NewToolCall("c2", "Bash", subArgs)

	// Mirror buildEngine's main-policy construction (rules + evaluator options +
	// the build-once resolver — nil here, no config sources).
	mainPolicy := permpolicy.NewPolicyWithResolver(mainRules(cfg), nil, cfg.permResolver, mainEvaluatorOptions(cfg)...)
	if got := mainPolicy.Evaluate(context.Background(), "s1", session.ModeDefault, subCall, nil); got.Effect != governance.Allow {
		t.Fatalf("auto-tier main policy should loosen the substitution floor; got %+v", got)
	}

	childPolicy := childPermPolicy(cfg)
	// Plain mutate: the blanket child floor allows it (no mutate-ask floor exists
	// for a child to loosen) → Allow regardless of tier.
	if got := childPolicy.Evaluate(context.Background(), "s1", session.ModeDefault, mutateCall, nil); got.Effect != governance.Allow {
		t.Fatalf("child plain mutate should be Allow (blanket floor); got %+v", got)
	}
	// Substitution with a non-read-only inner: at AUTO the child loosening is OFF, so the
	// child still floors at Ask — the load-bearing safety assertion.
	if got := childPolicy.Evaluate(context.Background(), "s1", session.ModeDefault, subCall, nil); got.Effect != governance.Ask {
		t.Fatalf("auto tier must NOT loosen a CHILD's substitution floor; got %+v", got)
	}
}

// TestChildRulesFloorScopeNeutral pins the childRules() re-scope (allow-all at
// ScopeBuiltinDefault instead of the legacy zero Scope) as behaviour-neutral
// with no config: identical effects to the historical bare allow-all policy
// across the decision surface, and the floor allow-all never registers as a
// CONFIGURED allow (no FlooredConfiguredAllow without a real config rule).
func TestChildRulesFloorScopeNeutral(t *testing.T) {
	legacy := governance.NewEvaluator([]governance.Rule{{Effect: governance.Allow}},
		governance.WithAudience(governance.AudienceSubagent))
	current := governance.NewEvaluator(childRules(Config{}), childEvaluatorOptions(Config{})...)

	cmds := []string{
		"ls",
		"git status",
		"zap -rf build", // unknown-verb mutate stand-in (no destructive literals in tests)
		"go test ./...",
		"cat $(zap)",
		"git status && zap x",
		"echo hi > f.txt",
		"",
	}
	for _, cmd := range cmds {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		got := current.Evaluate("Bash", args, false)
		want := legacy.Evaluate("Bash", args, false)
		if got.Effect != want.Effect {
			t.Fatalf("childRules() not neutral for %q: got %v, legacy %v", cmd, got.Effect, want.Effect)
		}
		if got.FlooredConfiguredAllow {
			t.Fatalf("the floor allow-all must never register as a configured allow (%q)", cmd)
		}
		if got.ConfiguredAsk {
			t.Fatalf("no configured ask exists in childRules() (%q)", cmd)
		}
	}
}

// TestPerSessionChildResolverPinsSessionRoot (issue #32 panel finding): a
// per-session engine built for a session over root X (≠ cfg.Workspace) must
// resolve its CHILDREN's project permission rules from X — X's own `subagent:`
// block binds the child policy, and the SERVER root's rules do not leak in.
// Asserted through the same childPermResolverFor derivation the
// sessionEngineFactory applies at session-engine assembly. The fork-root
// exclusion keeps holding: the pinned resolver ignores the per-call (fork)
// workspace entirely.
func TestPerSessionChildResolverPinsSessionRoot(t *testing.T) {
	// The SERVER root carries a deny on `Bash(zap:*)`; the SESSION root carries
	// an allow-clearing subagent ask on `Bash(go test:*)`. The child policy for
	// the session must see the session root's rules, not the server root's.
	serverCfg := permCfgWorkspace(t, "permissions:\n  subagent:\n    deny:\n      - \"Bash(zap:*)\"\n")
	sessionRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sessionRoot, ".mecatl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, ".mecatl", "settings.yaml"),
		[]byte("permissions:\n  subagent:\n    ask:\n      - \"Bash(go test:*)\"\n"), 0o644); err != nil {
		t.Fatalf("write session settings: %v", err)
	}

	// The per-session re-pin sessionEngineFactory applies at assembly.
	cfg := serverCfg
	cfg.childPermResolver = childPermResolverFor(cfg, sessionRoot)
	policy := childPermPolicy(cfg)

	eval := func(cmd string) governance.PermissionDecision {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		// nil per-call workspace: a child evaluates over its FORK workspace,
		// which the pin must ignore — the session root still drives resolution.
		return policy.Evaluate(context.Background(), "s1", session.ModeDefault,
			session.NewToolCall("c1", "Bash", args), nil)
	}
	if got := eval("go test ./..."); got.Effect != governance.Ask || !got.ConfiguredAsk {
		t.Fatalf("the SESSION root's subagent ask must bind the per-session child policy; got %+v", got)
	}
	if got := eval("zap"); got.Effect != governance.Allow {
		t.Fatalf("the SERVER root's subagent deny must NOT leak into another session's children; got %+v", got)
	}

	// Control: the build-time (shared-engine) derivation still pins the server root.
	shared := childPermPolicy(serverCfg)
	args, _ := json.Marshal(map[string]string{"command": "zap"})
	if got := shared.Evaluate(context.Background(), "s2", session.ModeDefault,
		session.NewToolCall("c2", "Bash", args), nil); got.Effect != governance.Deny {
		t.Fatalf("the shared engine's children must keep the server-root pin; got %+v", got)
	}
}

// TestMainPolicyIgnoresSubagentBlock is the AudienceMain-pin KILL test (issue
// #32 panel 6b): a FILE-BACKED `subagent: allow:` block driven through the REAL
// main-policy construction (mainRules + mainEvaluatorOptions + the build-once
// resolver) must NOT loosen the MAIN engine's mutate-ask floor. Deleting the
// WithAudience(AudienceMain) pin from mainEvaluatorOptions makes the
// subagent-tagged allow bind the main evaluator and fails this test.
func TestMainPolicyIgnoresSubagentBlock(t *testing.T) {
	cfg := permCfgWorkspace(t, "permissions:\n  subagent:\n    allow:\n      - \"Bash(echo:*)\"\n")
	policy := permpolicy.NewPolicyWithResolver(mainRules(cfg), nil, cfg.permResolver, mainEvaluatorOptions(cfg)...)
	ws, err := osfs.NewWorkspace(cfg.Workspace)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	args, _ := json.Marshal(map[string]string{"command": "echo hi > f.txt"})
	got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("c1", "Bash", args), ws)
	if got.Effect != governance.Ask {
		t.Fatalf("a subagent-block allow must be INVISIBLE to the main policy (mutate-ask floor stands); got %+v", got)
	}
}

// TestChildPermResolverNilWorkspace pins the empty-root arm by name (issue #32
// panel 7): with no workspace the pin is a nil WorkspaceReader — the resolver
// serves user/CLI rules only, never a project lookup — and the pinned decorator
// genuinely IGNORES the per-call workspace in both directions.
func TestChildPermResolverNilWorkspace(t *testing.T) {
	var gotWS []tool.WorkspaceReader
	inner := recordingRuleResolver{got: &gotWS, rules: permpolicy.AllowAllFloorRules()}
	cfg := Config{}
	cfg.permResolver = inner

	// Empty root → the pin is nil; the per-call (fork) workspace is dropped.
	pinned := childPermResolverFor(cfg, "")
	someWS, err := osfs.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	_ = pinned.Resolve(context.Background(), someWS)
	if len(gotWS) != 1 || gotWS[0] != nil {
		t.Fatalf("empty-root pin must resolve with a NIL workspace (user/CLI rules only); inner saw %v", gotWS)
	}

	// Non-empty root → the pin is the root, still not the per-call workspace.
	gotWS = nil
	root := t.TempDir()
	pinned = childPermResolverFor(cfg, root)
	_ = pinned.Resolve(context.Background(), someWS)
	// Canonicalize the expected root the same way osfs.NewWorkspace does
	// (EvalSymlinks), so the comparison is canonical-to-canonical and does
	// not diverge on a symlinked temp root (macOS /var -> /private/var).
	wantRoot, err := osfs.ResolveRoot(root)
	if err != nil {
		t.Fatalf("resolve expected root %q: %v", root, err)
	}
	if len(gotWS) != 1 || gotWS[0] == nil || gotWS[0].Root() != wantRoot {
		t.Fatalf("rooted pin must resolve with the PINNED workspace, not the per-call one; inner saw %v", gotWS)
	}
}

// recordingRuleResolver records the workspace each Resolve call received.
type recordingRuleResolver struct {
	got   *[]tool.WorkspaceReader
	rules []governance.Rule
}

func (r recordingRuleResolver) Resolve(_ context.Context, ws tool.WorkspaceReader) []governance.Rule {
	*r.got = append(*r.got, ws)
	return r.rules
}
