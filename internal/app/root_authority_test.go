package app

import (
	"context"
	"os"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestADR_0233_AuthorityEvaluator_Scenario6_MintPopulatesEveryFieldExplicitly(t *testing.T) {
	catalog := tool.NewCatalog()
	catalog.MustRegister(rootAuthorityTestTool{name: "Read", readOnly: true})
	catalog.MustRegister(rootAuthorityTestTool{name: "Write"})

	resourceCapability := governance.MCPResourceCapability("resource-only")
	got := mintRootAuthority(catalog, []string{resourceCapability}, session.SessionKindMain)
	if got.Provenance != rootAuthorityProvenance || got.DefinitionIdentity != rootAuthorityDefinition {
		t.Fatalf("root labels = %+v, want explicit root provenance and definition", got)
	}
	set := got.CapabilitySet
	if len(set.Tools) != 3 || set.Tools[0] != "Read" || set.Tools[1] != "Write" || set.Tools[2] != resourceCapability {
		t.Fatalf("root tools = %v, want complete catalog inventory", set.Tools)
	}
	if set.RemainingDelegationDepth != rootDelegationDepth {
		t.Fatalf("root depth = %d, want explicit %d", set.RemainingDelegationDepth, rootDelegationDepth)
	}
	for _, origin := range []tool.AgentOrigin{tool.AgentOriginProject, tool.AgentOriginUser, tool.AgentOriginDriver} {
		if managedDefinitionAuthority(tool.AgentDef{Origin: origin}) {
			t.Fatalf("origin %q unexpectedly establishes an authority ceiling", origin)
		}
	}
	if !managedDefinitionAuthority(tool.AgentDef{Origin: tool.AgentOriginExplicit}) {
		t.Fatal("explicit origin must be the sole ceiling-eligible tier")
	}
}

func TestAgentDefinitionAuthorityCeilingUsesResolvedToolsAndMCP(t *testing.T) {
	def := tool.AgentDef{Origin: tool.AgentOriginExplicit, DisallowedTools: []string{"Write"}}
	got := agentDefinitionAuthorityCeiling(def, []string{"Read", "Write", "mcp__github__issues", "mcp__github__issues"}, []string{governance.MCPResourceCapability("github")})
	if got.AllowsTool("Write") || !got.AllowsTool("Read") || !got.AllowsTool("mcp__github__issues") || !got.AllowsTool(governance.MCPResourceCapability("github")) || len(got.Tools) != 3 {
		t.Fatalf("ceiling = %+v", got)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario6_MintedRootCanDescend(t *testing.T) {
	root := mintRootAuthority(rootAuthorityCatalog(t), nil, session.SessionKindMain)
	child, err := governance.ConsumeDelegationHop(root.CapabilitySet)
	if err != nil {
		t.Fatalf("ConsumeDelegationHop(root): %v", err)
	}
	if child.RemainingDelegationDepth != rootDelegationDepth-1 {
		t.Fatalf("child depth = %d, want %d", child.RemainingDelegationDepth, rootDelegationDepth-1)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario6_NonSpawnDerivationPointsAreExplicit(t *testing.T) {
	root := mintRootAuthority(rootAuthorityCatalog(t), nil, session.SessionKindMain)
	svc, err := server.NewService(server.Config{
		Engine:        agent.NewEngine(agent.Deps{Catalog: tool.NewCatalog()}),
		Store:         memstore.New(),
		Workspaces:    func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		RootAuthority: func(session.SessionKind) session.Authority { return root },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	main, err := svc.CreateSession(context.Background(), "/workspace", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mainAuthority, bound := main.BoundAuthority()
	if !bound {
		t.Fatal("ordinary root has no authority")
	}
	forkID, err := svc.ForkSession(context.Background(), main.ID, "", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	fork, err := svc.GetSession(context.Background(), forkID)
	if err != nil {
		t.Fatalf("GetSession(fork): %v", err)
	}
	forkAuthority, bound := fork.BoundAuthority()
	if !bound || !forkAuthority.CapabilitySet.Contains(mainAuthority.CapabilitySet) || !mainAuthority.CapabilitySet.Contains(forkAuthority.CapabilitySet) || forkAuthority.Provenance != mainAuthority.Provenance {
		t.Fatalf("fork authority = %+v bound=%t, want verbatim source authority %+v", forkAuthority, bound, mainAuthority)
	}
	scheduled, err := svc.CreateSessionWithProfile(context.Background(), "/workspace", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault,
		server.WithScheduledRelationship("nightly", main.ID))
	if err != nil {
		t.Fatalf("CreateSessionWithProfile(scheduled): %v", err)
	}
	if scheduled.Kind != session.SessionKindScheduled {
		t.Fatalf("scheduled kind = %q, want %q", scheduled.Kind, session.SessionKindScheduled)
	}
	if got, bound := scheduled.BoundAuthority(); !bound || got.Provenance != rootAuthorityProvenance {
		t.Fatalf("scheduled authority = %+v bound=%t, want composed root", got, bound)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario3_AbsentEvaluatorIsExplicitAndAnnounced(t *testing.T) {
	evaluator, adapter, err := selectAuthorityEvaluator("noop", "")
	if err != nil || evaluator == nil {
		t.Fatalf("select noop evaluator = (%T, %q, %v), want explicit evaluator", evaluator, adapter, err)
	}
	if adapter != "noop" {
		t.Fatalf("adapter = %q, want noop", adapter)
	}
	if _, _, err := selectAuthorityEvaluator("missing", ""); err == nil {
		t.Fatal("unknown evaluator silently selected")
	}
	diag := &rootAuthorityDiag{}
	built, err := Build(context.Background(), Config{Workspace: t.TempDir(), UseMock: true, AuthorityEvaluator: "noop", Diagnostics: diag})
	if err != nil {
		t.Fatalf("Build(noop authority evaluator): %v", err)
	}
	defer built.Close()
	if got := diag.count(authorityEvaluatorPostureLine("noop")); got != 1 {
		t.Fatalf("noop evaluator posture lines = %d, want one", got)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario6_PostureLineReportsEvaluator(t *testing.T) {
	if got := authorityEvaluatorPostureLine("local"); got != "authority evaluator posture: adapter=local enforcement=true" {
		t.Fatalf("local posture = %q", got)
	}
	if got := authorityEvaluatorPostureLine("noop"); got != "authority evaluator posture: adapter=noop enforcement=false" {
		t.Fatalf("noop posture = %q", got)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario7_PolicyLoadFailureIsFatal(t *testing.T) {
	t.Parallel()

	if _, _, err := selectAuthorityEvaluator("cedar", ""); err == nil {
		t.Fatal("cedar selection without an operator policy succeeded")
	}
	if _, _, err := selectAuthorityEvaluator("cedar", t.TempDir()+"/missing.cedar"); err == nil {
		t.Fatal("cedar selection with a missing policy succeeded")
	}
	policyPath := t.TempDir() + "/authority.cedar"
	if err := os.WriteFile(policyPath, []byte(`permit(principal, action, resource);`), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	built, err := Build(context.Background(), Config{Workspace: t.TempDir(), UseMock: true, AuthorityEvaluator: "cedar", CedarAuthorityPolicy: policyPath})
	if err != nil {
		t.Fatalf("Build(cedar authority evaluator): %v", err)
	}
	defer built.Close()
}

func rootAuthorityCatalog(t *testing.T) *tool.Catalog {
	t.Helper()
	catalog := tool.NewCatalog()
	catalog.MustRegister(rootAuthorityTestTool{name: "Read", readOnly: true})
	catalog.MustRegister(rootAuthorityTestTool{name: "Write"})
	return catalog
}

type rootAuthorityDiag struct{ messages []string }

func (d *rootAuthorityDiag) Log(_ context.Context, _ port.Level, message string, _ ...any) {
	d.messages = append(d.messages, message)
}
func (d *rootAuthorityDiag) With(...any) port.Diagnostics { return d }
func (d *rootAuthorityDiag) count(want string) int {
	count := 0
	for _, message := range d.messages {
		if message == want {
			count++
		}
	}
	return count
}

type rootAuthorityTestTool struct {
	name     string
	readOnly bool
}

func (t rootAuthorityTestTool) Spec() tool.ToolSpec { return tool.ToolSpec{Name: t.name} }
func (t rootAuthorityTestTool) ReadOnly() bool      { return t.readOnly }
func (rootAuthorityTestTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}
