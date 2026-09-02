package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

func authorityParent() session.Authority {
	return session.Authority{CapabilitySet: governance.CapabilitySet{
		Tools: []string{"Read", "Write", "mcp__github__issues"}, RemainingDelegationDepth: 2, FileSystem: true, DirectWrite: true,
	}, Provenance: "composed_root", DefinitionIdentity: "root"}
}

func TestADR_0233_AuthorityEvaluator_Scenario4_ChildGetsIntersectionOnEverySeam(t *testing.T) {
	parent := authorityParent()
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "parent", Revision: "test-v1"}, memfs.NewWorkspace("/ws"), nil)
	childEngine := NewEngine(Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog()})
	store := memstore.New()
	subagent := NewSubagentTool(childEngine, WithSubagentStore(store)).(*SubagentTool)
	caps := parentCaps{authority: parent, authorityBound: true, parentSessionID: "parent", parentIncarnation: session.NewIncarnationID()}
	if _, err := subagent.ExecuteWithParent(context.Background(), session.ToolCall{ID: "sub", Name: subagentToolName, Args: []byte(`{"prompt":"work"}`)}, env, nil, caps); err != nil {
		t.Fatalf("Subagent: %v", err)
	}
	child, err := store.Load(context.Background(), "subagent-parent-sub")
	if err != nil {
		t.Fatalf("load subagent: %v", err)
	}
	assertDerivedAuthority(t, child, parent, []string{})

	parallelStore := memstore.New()
	parallelEngine := NewEngine(Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog()})
	parallel := NewParallelTool(parallelEngine, &authorityCountingForker{}, WithParallelStore(parallelStore)).(*ParallelTool)
	if _, err := parallel.ExecuteWithParent(context.Background(), session.ToolCall{ID: "parallel", Name: parallelToolName, Args: []byte(`{"tasks":["work"]}`)}, env, nil, caps); err != nil {
		t.Fatalf("Parallel: %v", err)
	}
	branch, err := parallelStore.Load(context.Background(), "parallel-parent-parallel-0")
	if err != nil {
		t.Fatalf("load parallel branch: %v", err)
	}
	assertDerivedAuthority(t, branch, parent, []string{})

	tm := team.New("authority-team")
	sup := NewSupervisor(tm, env, func(MemberSpec, string) MemberBuild { return MemberBuild{Engine: childEngine} }, withParentCaps(caps))
	if err := sup.AddMember(context.Background(), MemberSpec{Name: "member"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	assertDerivedAuthority(t, sup.members["member"].sess, parent, []string{})
}

func TestManagedSpecialistAuthorityCeilingIsModeSpecific(t *testing.T) {
	store := memstore.New()
	readOnlyEngine := NewEngine(Deps{LLM: mockllm.New(mockllm.TextTurn("read done")), Catalog: tool.NewCatalog()})
	writableEngine := NewEngine(Deps{LLM: mockllm.New(mockllm.TextTurn("write done")), Catalog: tool.NewCatalog()})
	subagent := NewSubagentTool(readOnlyEngine,
		WithSubagentStore(store),
		WithAgentEngines(map[string]*Engine{"managed": readOnlyEngine}, []AgentMeta{{
			Name: "managed", Managed: true,
			AuthorityCeiling:         governance.CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 9, FileSystem: true},
			WritableAuthorityCeiling: governance.CapabilitySet{Tools: []string{"Read", "Write"}, RemainingDelegationDepth: 9, FileSystem: true, DirectWrite: true},
		}}),
		WithAgentWritableEngineFactory(func(string) (*Engine, bool) { return writableEngine, true }),
	).(*SubagentTool)
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "parent", Revision: "test-v1"}, memfs.NewWorkspace("/ws"), nil)
	caps := parentCaps{authority: authorityParent(), authorityBound: true, parentSessionID: "parent", parentIncarnation: session.NewIncarnationID()}

	for _, tc := range []struct {
		id       session.ToolCallID
		args     string
		writable bool
	}{
		{id: "read", args: `{"prompt":"inspect","agent":" managed "}`},
		{id: "write", args: `{"prompt":"change","agent":"managed","mode":"read-write"}`, writable: true},
	} {
		if result, err := subagent.ExecuteWithParent(context.Background(), session.ToolCall{ID: tc.id, Name: subagentToolName, Args: []byte(tc.args)}, env, nil, caps); err != nil || result.IsError {
			t.Fatalf("ExecuteWithParent(%s) = (%+v, %v)", tc.id, result, err)
		}
		child, err := store.Load(context.Background(), session.SessionID("subagent-parent-"+string(tc.id)))
		if err != nil {
			t.Fatalf("load %s: %v", tc.id, err)
		}
		authority, bound := child.BoundAuthority()
		if !bound || authority.CapabilitySet.DirectWrite != tc.writable || !authority.CapabilitySet.AllowsTool("Read") || authority.CapabilitySet.AllowsTool("mcp__github__issues") {
			t.Fatalf("%s authority = %+v, bound=%t", tc.id, authority, bound)
		}
		if authority.CapabilitySet.AllowsTool("Write") != tc.writable {
			t.Fatalf("%s Write authority = %t, want %t", tc.id, authority.CapabilitySet.AllowsTool("Write"), tc.writable)
		}
		if authority.DefinitionIdentity != "explicit:managed" {
			t.Fatalf("%s definition identity = %q, want canonical managed identity", tc.id, authority.DefinitionIdentity)
		}
	}
}

func TestWritableResumeRefusesPersistedReadOnlyAuthorityBeforeDrive(t *testing.T) {
	store := memstore.New()
	seed := session.New("subagent-old", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/discarded-worktree", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := seed.BindAuthority(session.Authority{
		CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}, FileSystem: true},
		Provenance:    "delegated", DefinitionIdentity: "explicit:managed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := seed.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := seed.RecordAssistant(session.NewAssistantMessage("done", "", nil)); err != nil {
		t.Fatal(err)
	}
	if err := seed.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), seed); err != nil {
		t.Fatal(err)
	}

	writableLLM := mockllm.New(mockllm.TextTurn("must not run"))
	subagent := NewSubagentTool(NewEngine(Deps{Catalog: tool.NewCatalog()}),
		WithSubagentStore(store),
		WithWritableChildEngine(NewEngine(Deps{LLM: writableLLM, Catalog: tool.NewCatalog()})),
	).(*SubagentTool)
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "parent", Revision: "test-v1"}, memfs.NewWorkspace("/ws"), nil)
	result, err := subagent.ExecuteWithParent(context.Background(), session.ToolCall{
		ID: "resume", Name: subagentToolName,
		Args: []byte(`{"prompt":"now edit","resume":"subagent-old","mode":"read-write"}`),
	}, env, nil, parentCaps{authority: authorityParent(), authorityBound: true, parentSessionID: "parent", parentIncarnation: session.NewIncarnationID()})
	if err != nil {
		t.Fatalf("ExecuteWithParent: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "persisted child authority does not permit direct write") {
		t.Fatalf("resume result = %+v", result)
	}
	if writableLLM.Calls() != 0 {
		t.Fatalf("writable engine calls = %d, want 0", writableLLM.Calls())
	}
}

func assertDerivedAuthority(t *testing.T, child *session.Session, parent session.Authority, absent []string) {
	t.Helper()
	got, bound := child.BoundAuthority()
	if !bound || got.CapabilitySet.RemainingDelegationDepth != parent.CapabilitySet.RemainingDelegationDepth-1 || !parent.CapabilitySet.Contains(got.CapabilitySet) {
		t.Fatalf("child authority = %+v bound=%t, parent=%+v", got, bound, parent)
	}
	for _, name := range absent {
		if got.CapabilitySet.AllowsTool(name) {
			t.Fatalf("child retained excluded tool %q in %+v", name, got.CapabilitySet)
		}
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario4_RefusalAcquiresNoRuntimeResource(t *testing.T) {
	forker := &authorityCountingForker{}
	childEngine := NewEngine(Deps{Catalog: tool.NewCatalog()})
	subagent := NewSubagentTool(childEngine, WithChildForker(forker)).(*SubagentTool)
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "parent", Revision: "test-v1"}, memfs.NewWorkspace("/ws"), nil)
	_, err := subagent.ExecuteWithParent(context.Background(), session.ToolCall{ID: "call", Name: subagentToolName, Args: []byte(`{"prompt":"work"}`)}, env, nil, parentCaps{authorityBound: true})
	if err != nil {
		t.Fatalf("ExecuteWithParent: %v", err)
	}
	if forker.calls != 0 {
		t.Fatalf("forks = %d, want 0 after authority refusal", forker.calls)
	}
}

type authorityCountingForker struct{ calls int }

func (f *authorityCountingForker) Fork(_ context.Context, base tool.Environment, _ string) (tool.Environment, func() error, string, error) {
	f.calls++
	return base, func() error { return nil }, "", nil
}

func TestADR_0233_AuthorityEvaluator_Scenario4_OnlyManagedTierSuppliesACeiling(t *testing.T) {
	child := NewEngine(Deps{Catalog: tool.NewCatalog()})
	subagent := NewSubagentTool(child, WithAgentEngines(map[string]*Engine{"managed": child, "project": child}, []AgentMeta{
		{Name: "managed", Managed: true,
			AuthorityCeiling:         governance.CapabilitySet{Tools: []string{"Read", "mcp__github__issues"}, RemainingDelegationDepth: 9, FileSystem: true},
			WritableAuthorityCeiling: governance.CapabilitySet{Tools: []string{"Read", "Write"}, RemainingDelegationDepth: 9, FileSystem: true, DirectWrite: true}},
		{Name: "project", AuthorityCeiling: governance.CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 9, FileSystem: true}},
	})).(*SubagentTool)
	managed, ok := subagent.agentCeiling("managed", false)
	if !ok || !managed.AllowsTool("mcp__github__issues") {
		t.Fatalf("managed ceiling = %+v, ok=%t", managed, ok)
	}
	if _, ok := subagent.agentCeiling("project", false); ok {
		t.Fatal("non-managed definition established a specialist ceiling")
	}
	writable, ok := subagent.agentCeiling("managed", true)
	if !ok || !writable.DirectWrite || !writable.AllowsTool("Write") || writable.AllowsTool("mcp__github__issues") {
		t.Fatalf("managed writable ceiling = %+v, ok=%t", writable, ok)
	}
	got, err := deriveDelegatedAuthority(authorityParent(), authorityParent().CapabilitySet, managed, nil)
	if err != nil || got.CapabilitySet.AllowsTool("Write") || !got.CapabilitySet.AllowsTool("mcp__github__issues") {
		t.Fatalf("managed ceiling result = %+v, err = %v", got, err)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario4_CallTighteningCannotWiden(t *testing.T) {
	parent := authorityParent()
	tooDeep := 3
	filesystem := true
	directWrite := true
	for _, tightening := range []*DelegationTightening{
		{Tools: []string{"Grep"}},
		{RemainingDelegationDepth: &tooDeep},
		{FileSystem: &filesystem},
		{DirectWrite: &directWrite},
	} {
		parent.CapabilitySet.FileSystem = false
		parent.CapabilitySet.DirectWrite = false
		if _, err := deriveDelegatedAuthority(parent, parent.CapabilitySet, nil, tightening); err == nil {
			t.Fatalf("widening tightening %+v succeeded", tightening)
		}
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario4_OwnerAndSetAreIndependentlyStamped(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}
	child := session.New("child", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	derived, err := deriveDelegatedAuthority(authorityParent(), governance.CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 1, FileSystem: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := stampDelegatedLabels(child, owner, derived); err != nil {
		t.Fatal(err)
	}
	got, bound := child.BoundAuthority()
	if !bound || !child.Owner.SameIdentity(owner) || !got.CapabilitySet.AllowsTool("Read") {
		t.Fatalf("owner=%+v authority=%+v bound=%t", child.Owner, got, bound)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario5_ResumedChildCannotExceedCurrentParent(t *testing.T) {
	persisted, err := deriveDelegatedAuthority(authorityParent(), governance.CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 1, FileSystem: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	current := authorityParent()
	current.CapabilitySet.Tools = []string{"Write"}
	if err := validateResumedAuthority(current, persisted, true); err == nil {
		t.Fatal("resume widened beyond current parent")
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario5_ResumeSpendsNoAdditionalHop(t *testing.T) {
	persisted := session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 0, FileSystem: true}, Provenance: "delegated", DefinitionIdentity: "root"}
	if err := validateResumedAuthority(authorityParent(), persisted, true); err != nil {
		t.Fatalf("resume spent another hop: %v", err)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario5_NoWideningAcrossRestart(t *testing.T) {
	persisted := session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 0, FileSystem: true}, Provenance: "delegated", DefinitionIdentity: "root"}
	if err := validateResumedAuthority(authorityParent(), persisted, true); err != nil {
		t.Fatal(err)
	}
	if err := validateResumedAuthority(authorityParent(), session.Authority{}, false); err == nil {
		t.Fatal("legacy child was upgraded")
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario6_ComposedRootCanDelegateOnEverySeam(t *testing.T) {
	for _, seam := range []string{"subagent", "specialist", "parallel", "team"} {
		t.Run(seam, func(t *testing.T) {
			got, err := deriveDelegatedAuthority(authorityParent(), authorityParent().CapabilitySet, nil, nil)
			if err != nil || len(got.CapabilitySet.Tools) == 0 {
				t.Fatalf("child=%+v err=%v", got, err)
			}
		})
	}
}
