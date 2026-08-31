package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// noFSTeamAssets builds the catalog assets a no-fs team test runs over: the two
// flocked memory stores plus a CONNECTED global MCP manager (one "echo" tool),
// so the member surface and the MCPToolNames exemption are exercised against
// the REAL non-read-only tool classes (memory writers AND remote MCP tools).
func noFSTeamAssets(t *testing.T) catalogAssets {
	t.Helper()
	url := newMCPTestServerWithResource(t)
	memStore, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New(memStore): %v", err)
	}
	userStore, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New(userModelStore): %v", err)
	}
	return catalogAssets{
		memStore:       memStore,
		userModelStore: userStore,
		globalMgr:      connectMainManager(t, "globe", url),
	}
}

// noFSTeamWiring drives the REAL buildTeamWiring with noFS=true over the given
// provider and assets — the exact wiring a no-fs session's in-catalog Team tool
// gets (registerTeamTools threads s.noFS into it).
func noFSTeamWiring(t *testing.T, provider port.LLMProvider, a catalogAssets) (server.MemberEngineFactory, *agent.Supervisor, *team.Team) {
	t.Helper()
	cfg := Config{Model: "mock", Shell: "/bin/sh", TrustProject: true, Diagnostics: port.NopDiagnostics{}}
	factory, fk, roFk, _, _ := buildTeamWiring(context.Background(), cfg,
		regForTest(provider, providerMock, cfg.Model), provider, providerMock, cfg.Model,
		a.globalMgr, agents.NewRegistry(nil), nil, a, true)
	if fk != nil || roFk != nil {
		t.Fatalf("no-fs buildTeamWiring returned forkers (fk=%v roFk=%v), want nil/nil — a fork is a filesystem act", fk, roFk)
	}
	tm := team.New("nofs-team")
	sup := agent.NewSupervisor(tm, testEnvironment(nofs.New(), nil),
		func(spec agent.MemberSpec, routedModel string) agent.MemberBuild {
			return factory(tm, spec, routedModel)
		})
	return factory, sup, tm
}

// TestNoFSTeamMemberSurface drives the REAL buildTeamWiring/buildMemberEngine
// with noFS=true and pins the member's tool surface: NO file tools and NO shell
// (even though the config HAS a shell — the no-fs gate beats the shell wiring),
// while WebFetch, both complete lifecycle-capable memory families, the global MCP
// tool, and coordination tools are all present. MemberBuild.MCPToolNames must carry the
// catalog's non-workspace mutators (memory writers + MCP tools) — the
// supervisor's documented exemption the spawn test below depends on.
func TestNoFSTeamMemberSurface(t *testing.T) {
	a := noFSTeamAssets(t)
	factory, _, tm := noFSTeamWiring(t, mockllm.New(mockllm.TextTurn("ok")), a)

	build := factory(tm, agent.MemberSpec{Name: "scout", InitialPrompt: "go"}, "")
	if build.Engine == nil {
		t.Fatal("no-fs member factory returned a nil engine")
	}
	for _, name := range []string{"Read", "Edit", "Write", "Grep", "Glob", "Bash", "Parallel", "SkillDraft"} {
		if build.Engine.HasTool(name) {
			t.Errorf("no-fs member catalog carries %q — a file/shell tool leaked into the file-less member surface", name)
		}
	}
	for _, name := range []string{agent.CurrentSessionToolName, "WebFetch", "FetchMcpResource", "Remember", "Recall", "SearchMemory", "InspectMemory", "ForgetMemory", "UndoMemory", "RememberUser", "RecallUser", "SearchUserModel", "InspectUserMemory", "ForgetUserMemory", "UndoUserMemory", "mcp__globe__echo", "CallMcpWithQuery"} {
		if !build.Engine.HasTool(name) {
			t.Errorf("no-fs member catalog is missing %q — the file-less member surface lost a non-FS family", name)
		}
	}
	for name := range agent.MemberToolNames() {
		if !build.Engine.HasTool(name) {
			t.Errorf("no-fs member catalog is missing coordination tool %q", name)
		}
	}

	// The exemption list: every non-read-only catalog tool that is NOT a
	// workspace mutator (memory writers persist to the flocked stores; MCP tools
	// touch only the remote server). Without these names in MCPToolNames the
	// supervisor's base-sharing read-only backstop rejects the member (pinned by
	// TestNoFSMemberSpawnAdmittedViaExemption).
	exempt := map[string]bool{}
	for _, n := range build.MCPToolNames {
		exempt[n] = true
	}
	for _, want := range []string{"Remember", "ForgetMemory", "UndoMemory", "RememberUser", "ForgetUserMemory", "UndoUserMemory", "mcp__globe__echo"} {
		if !exempt[want] {
			t.Errorf("MemberBuild.MCPToolNames is missing %q (got %v) — the non-workspace-mutator exemption drifted", want, build.MCPToolNames)
		}
	}
}

// TestNoFSMemberSpawnAdmittedViaExemption proves the nonReadOnlyToolNames →
// MCPToolNames exemption MATTERS: a no-fs member is BASE-SHARING (no forks
// exist), so the supervisor's read-only backstop scans its catalog for
// non-read-only tools — and the memory writers (Remember/RememberUser) and the
// global MCP tool all report ReadOnly()==false. AddMember admits the member
// ONLY because those names ride MemberBuild.MCPToolNames.
//
// MUTATION-DRILLED: dropping the exemption (MCPToolNames: nil in
// buildMemberEngine's noFS branch) fails this test with
// ErrReadOnlyMemberMutating naming Remember/RememberUser/mcp__globe__echo — the supervisor
// rejects every memory-tool-bearing no-fs member at spawn.
func TestNoFSMemberSpawnAdmittedViaExemption(t *testing.T) {
	a := noFSTeamAssets(t)
	_, sup, _ := noFSTeamWiring(t, mockllm.New(mockllm.TextTurn("ok")), a)

	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "scout", InitialPrompt: "go"}); err != nil {
		t.Fatalf("AddMember(read-only no-fs member) = %v, want admitted (the non-workspace-mutator exemption must cover the memory writers + MCP tools)", err)
	}
}

// TestNoFSMutatingMemberFailsLoudly pins the Mutating-member posture under
// no-fs: there is no force-copy forker (a fork is a filesystem act), so a
// Mutating spec fails the spawn LOUDLY with ErrNoForker — never a silent
// degrade to a base-sharing mutator.
func TestNoFSMutatingMemberFailsLoudly(t *testing.T) {
	a := noFSTeamAssets(t)
	_, sup, _ := noFSTeamWiring(t, mockllm.New(mockllm.TextTurn("ok")), a)

	err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "writer", Mutating: true, InitialPrompt: "go"})
	if err == nil {
		t.Fatal("AddMember(Mutating) succeeded under no-fs — a Mutating member needs a filesystem fork that cannot exist here")
	}
	if !errors.Is(err, agent.ErrNoForker) {
		t.Fatalf("AddMember(Mutating) error = %v, want errors.Is(_, agent.ErrNoForker)", err)
	}
}

// TestNoFSTeamLeadSynthesisSmoke runs a single-lead team to completion under
// the no-fs wiring: the lead's round turn and its SYNTHESIS turn both run on
// the file-less engine, every system prompt carries the no-FS posture note
// (and no cwd claim), and the outcome's Report is the synthesis text — the
// no-fs profile changes the tool surface, never the deliverable contract.
func TestNoFSTeamLeadSynthesisSmoke(t *testing.T) {
	var (
		mu      sync.Mutex
		systems []string
	)
	provider := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			mu.Lock()
			systems = append(systems, req.System.Render())
			mu.Unlock()
		})},
		mockllm.TextTurn("scout findings noted"),
		mockllm.TextTurn("CONSOLIDATED nofs report"),
	)
	a := noFSTeamAssets(t)
	_, sup, _ := noFSTeamWiring(t, provider, a)

	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "go"}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}
	outcome := sup.Run(context.Background(), func(agent.TeamEvent) {})
	if !strings.Contains(outcome.Report, "CONSOLIDATED nofs report") {
		t.Fatalf("outcome.Report = %q, want the lead's synthesis text — synthesis must run under the no-fs posture too", outcome.Report)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(systems) < 2 {
		t.Fatalf("captured %d member requests, want >= 2 (round turn + synthesis turn)", len(systems))
	}
	for i, sys := range systems {
		// The FULL member note, not a bare substring (the vacuous-oracle lesson
		// from the main-engine posture test applies here too).
		if !strings.Contains(sys, noFSMemberNote) {
			t.Errorf("member request %d system prompt missing the no-FS member posture note:\n%s", i, sys)
		}
	}
}
