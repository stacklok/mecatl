package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// teamCfg is the minimal app Config a member engine factory needs: a model and a
// shell so the Mutating branch can attempt to register Bash. The workspace is a
// throwaway temp dir (the command runner roots there, but no command is run in
// these tests). TrustProject: the read-only subagent/member shell is gated on
// cfg.TrustProject (buildSandboxedCommandRunner) — the operator vouches for the
// repo's `.git` (issue #40). The untrusted side lives in trust_shell_gate_test.go
// (shelllessTeamCfg).
func teamCfg(t *testing.T) Config {
	t.Helper()
	return Config{
		Workspace:    t.TempDir(),
		Model:        "mock",
		Shell:        "/bin/sh",
		TrustProject: true, // the shell gate buildSandboxedCommandRunner reads
		Posture:      PostureAuto,
	}
}

// shelllessTeamCfg is teamCfg with workspace trust OFF (TrustProject=false), so
// the read-only subagent shell is gated out (buildSandboxedCommandRunner reads
// cfg.TrustProject).
func shelllessTeamCfg(t *testing.T) Config {
	t.Helper()
	cfg := teamCfg(t)
	cfg.TrustProject = false
	return cfg
}

// TestBuildMemberEngineReadOnlySpawnSucceeds exercises fix A's ACCEPTANCE path: a
// read-only member built by the app factory carries only Read/Grep/Glob + the
// coordination tools and NO workspace-mutating tools, so the supervisor's AddMember
// invariant accepts it and SpawnTeammate succeeds. (If buildMemberEngine wrongly
// handed a read-only member Edit/Write/Bash, AddMember would reject it with
// ErrReadOnlyMemberMutating and the spawn would fail.)
func TestBuildMemberEngineReadOnlySpawnSucceeds(t *testing.T) {
	cfg := teamCfg(t)
	provider := mockllm.New(mockllm.TextTurn("ok"))
	svc := teamServiceWithFactory(t, memberFactoryForTest(cfg, provider, nil, agents.NewRegistry(nil), nil, nil, false, nil))

	ctx := context.Background()
	teamID, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "test", "", 0, nil)
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	// Read-only lead (Mutating defaults false): must be accepted by AddMember.
	if _, err := svc.SpawnTeammate(ctx, teamID, agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "go"}); err != nil {
		t.Fatalf("SpawnTeammate(read-only): %v", err)
	}
}

// TestBuildMemberEngineMutatingSpawnSucceeds asserts a Mutating member (which the
// factory gives Edit/Write — but NOT Bash; see TestMutatingMemberHasEditNotBash) is
// accepted when a Forker is configured — it runs in an isolated fork, so the
// filesystem-mutating tools are permitted. This proves the Mutating branch of
// buildMemberEngine produces a catalog the supervisor admits.
func TestBuildMemberEngineMutatingSpawnSucceeds(t *testing.T) {
	cfg := teamCfg(t)
	provider := mockllm.New(mockllm.TextTurn("ok"))
	svc := teamServiceWithFactory(t, memberFactoryForTest(cfg, provider, nil, agents.NewRegistry(nil), nil, nil, false, nil))

	ctx := context.Background()
	teamID, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "test", "", 0, nil)
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := svc.SpawnTeammate(ctx, teamID, agent.MemberSpec{Name: "writer", Mutating: true}); err != nil {
		t.Fatalf("SpawnTeammate(Mutating): %v", err)
	}
}

// TestTeamsEnabledEndToEnd is the end-to-end wiring proof: a Service built with the
// app member-engine factory runs a single-member team to quiescence with no error.
// A single LEAD member runs its InitialPrompt against a mockllm scripted with one
// text turn; the team then plans no further work and reaches quiescence. (A single
// member keeps the shared-provider scripting deterministic — multi-member
// coordination is covered by the supervisor unit tests.)
func TestTeamsEnabledEndToEnd(t *testing.T) {
	cfg := teamCfg(t)
	// Two turns: round-0 work, then the lead's synthesis turn (the deliverable).
	provider := mockllm.New(mockllm.TextTurn("all done"), mockllm.TextTurn("CONSOLIDATED all done"))
	svc := teamServiceWithFactory(t, memberFactoryForTest(cfg, provider, nil, agents.NewRegistry(nil), nil, nil, false, nil))

	ctx := context.Background()
	// Atomic create+populate: the initial roster is enrolled by CreateTeam itself, so
	// no separate SpawnTeammate call is needed before RunTeam.
	teamID, enrolled, err := svc.CreateTeamOnDefaultPlacement(ctx, "test", "", 0,
		[]agent.MemberSpec{{Name: "lead", Lead: true, InitialPrompt: "go"}})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if errors.Is(err, server.ErrTeamsDisabled) {
		t.Fatal("CreateTeam returned ErrTeamsDisabled with a factory wired")
	}
	if len(enrolled) != 1 || enrolled[0].Name != "lead" {
		t.Fatalf("CreateTeam enrolled roster = %+v, want one member named lead", enrolled)
	}

	outcome, err := svc.RunTeam(ctx, teamID, func(agent.TeamEvent) {})
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
	}
	if !outcome.Quiescent {
		t.Errorf("RunTeam outcome: Quiescent = false, want true (outcome=%+v)", outcome)
	}
	if !strings.Contains(outcome.Report, "CONSOLIDATED") {
		t.Errorf("RunTeam outcome.Report = %q, want the lead's synthesis", outcome.Report)
	}
}

// TestMaxTeamTokensPropagates mirrors TestMaxRunTokensPropagatesToParentAndChild for
// the team-aggregate budget: it pins that Config.MaxTeamTokens reaches BOTH the gRPC
// CreateTeam path (applyTeamConfig → server.Config.TeamTokenBudget → the supervisor)
// AND the in-catalog Team tool site (NewTeamTool's WithTeamToolTokenBudget). The first
// half is a direct applyTeamConfig assertion; the second drives a real CreateTeam/RunTeam
// over that svcCfg and asserts the budget actually trips a usage-bearing team. Both
// NewTeamTool sites (build-time and per-session Half B) read the SAME cfg.MaxTeamTokens,
// so threading it once covers them.
func TestMaxTeamTokensPropagates(t *testing.T) {
	const budget = 500
	cfg := teamCfg(t)
	cfg.MaxTeamTokens = budget
	cfg.EnableTeams = true

	// Each member turn carries usage; the lead spends 600 in round 0 (crosses 500).
	usageTurn := func(text string, in int) mockllm.Turn {
		return mockllm.ChunksTurn(
			mockllm.TextChunk(text),
			mockllm.UsageChunk(session.Usage{InputTokens: in}),
			mockllm.DoneChunk(session.StopEndTurn),
		)
	}
	provider := mockllm.New(
		usageTurn("round-0 work", 600),
		usageTurn("CONSOLIDATED report", 50),
		usageTurn("EXTRA (should never run)", 600),
	)
	reg := regForTest(provider, providerMock, cfg.Model)

	// --- Half 1: applyTeamConfig threads svcCfg.TeamTokenBudget --------------
	var svcCfg server.Config
	applyTeamConfig(&svcCfg, cfg, reg, provider, nil, agents.NewRegistry(nil), nil, catalogAssets{})
	if svcCfg.TeamTokenBudget != budget {
		t.Fatalf("svcCfg.TeamTokenBudget = %d, want %d (applyTeamConfig must thread MaxTeamTokens)", svcCfg.TeamTokenBudget, budget)
	}

	// --- Half 2: the threaded budget actually trips a usage-bearing team -----
	// Build a Service over the SAME svcCfg.MemberEngine + svcCfg.TeamTokenBudget
	// applyTeamConfig produced, so the budget that trips is the one that propagated.
	root := t.TempDir()
	svc, err := newTestServerService(server.Config{
		Engine: noopEngine(),
		Store:  memstore.New(),

		SharedEngineRoot: root,
		Now:              func() time.Time { return time.Unix(0, 0) },
		MemberEngine:     svcCfg.MemberEngine,
		Forker:           forker.New(func(root string) (tool.Workspace, error) { return osfs.NewWorkspace(root) }),
		ReadOnlyForker:   forker.New(func(root string) (tool.Workspace, error) { return osfs.NewWorkspace(root) }),
		TeamTokenBudget:  svcCfg.TeamTokenBudget,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	ctx := context.Background()
	teamID, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "test", "do one round", 0,
		[]agent.MemberSpec{{Name: "lead", Lead: true, InitialPrompt: "do the work then stop"}})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	out, err := svc.RunTeam(ctx, teamID, func(agent.TeamEvent) {})
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
	}
	if !out.BudgetExhausted {
		t.Errorf("RunTeam outcome.BudgetExhausted = false, want true (threaded budget %d, round-0 spent 600)", budget)
	}
}

// TestTeamsDisabledWhenNoFactory asserts that with EnableTeams effectively off
// (MemberEngine nil) CreateTeam returns ErrTeamsDisabled. It also drives the real
// Build seam to prove the flag controls the wiring: Build with EnableTeams:false
// yields a Service whose CreateTeam is disabled.
func TestTeamsDisabledWhenNoFactory(t *testing.T) {
	built, err := Build(context.Background(), Config{
		Workspace: t.TempDir(),
		Model:     "mock",
		UseMock:   true,
		// EnableTeams omitted → false.
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	_, _, err = built.Service.CreateTeamOnDefaultPlacement(context.Background(), "test", "", 0, nil)
	if !errors.Is(err, server.ErrTeamsDisabled) {
		t.Fatalf("CreateTeam (teams off): err = %v, want ErrTeamsDisabled", err)
	}
}

// TestBuildEnableTeamsRunsTeam proves the FULL Build path: app.Build with
// EnableTeams:true yields a Service whose CreateTeam succeeds (not ErrTeamsDisabled)
// and whose single-member team runs to quiescence — the flag really wires the
// factory, forker, and team hooks into server.Config.
func TestBuildEnableTeamsRunsTeam(t *testing.T) {
	root := t.TempDir()
	built, err := Build(context.Background(), Config{
		Workspace:   root,
		Model:       "mock",
		UseMock:     true,
		EnableTeams: true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	ctx := context.Background()
	teamID, _, err := built.Service.CreateTeamOnDefaultPlacement(ctx, "test", "", 0, nil)
	if errors.Is(err, server.ErrTeamsDisabled) {
		t.Fatal("CreateTeam returned ErrTeamsDisabled with EnableTeams:true")
	}
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := built.Service.SpawnTeammate(ctx, teamID, agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "go"}); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}
	outcome, err := built.Service.RunTeam(ctx, teamID, func(agent.TeamEvent) {})
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
	}
	if !outcome.Quiescent {
		t.Errorf("RunTeam outcome: Quiescent = false, want true (outcome=%+v)", outcome)
	}
}

// TestReadOnlyMemberRunsGitInWorktreeEndToEnd is the key proof of this feature: a
// READ-ONLY team member, driven through the real Service/Supervisor wiring, runs git
// (log/show) over a cheap git WORKTREE that shares the base repo's .git — so it sees
// the full commit history — confined to a throwaway checkout, and the worktree is
// cleaned up afterwards (no leak). The member never edits anything.
func TestReadOnlyMemberRunsGitInWorktreeEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cfg := teamCfg(t)

	// A real git repo with two commits whose subjects we assert the member can read.
	repo := t.TempDir()
	initGitRepoTest(t, repo)
	writeRepoFile(t, repo, "alpha.txt", "alpha\n")
	gitCommitTest(t, repo, "add alpha")
	writeRepoFile(t, repo, "beta.txt", "beta\n")
	gitCommitTest(t, repo, "add beta")

	// Scope worktrees under a known dir so we can assert they are cleaned up.
	worktreeBase := t.TempDir()
	// WithRunner (issue #462): the forkers mint a BOUND runner for each child
	// namespace so a forked member's Bash observes its OWN worktree/copy. The
	// builder applies the SAME hardening buildSandboxedCommandRunner/
	// buildForceCopyRunner do — trust-gated for the read-only worktree forker,
	// ungated for the force-copy mutating forker.
	sandboxedRunnerBuilder := func(childRoot string) tool.CommandRunner {
		if cfg.NoBash || cfg.Shell == "" || !cfg.TrustProject {
			return nil
		}
		return newHardenedRunnerForRoot(cfg, childRoot)
	}
	forceCopyRunnerBuilder := func(childRoot string) tool.CommandRunner {
		if cfg.NoBash || cfg.Shell == "" {
			return nil
		}
		return newHardenedRunnerForRoot(cfg, childRoot)
	}
	roFk := forker.New(func(root string) (tool.Workspace, error) { return osfs.NewWorkspace(root) },
		forker.WithTempBase(worktreeBase), forker.WithRunner(sandboxedRunnerBuilder))
	mutatingFk := forker.New(func(root string) (tool.Workspace, error) { return osfs.NewWorkspace(root) },
		forker.WithForceCopy(), forker.WithRunner(forceCopyRunnerBuilder))
	runner := buildSandboxedCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil sandboxed runner")
	}

	// The read-only lead: git log --oneline, then git show --stat HEAD, then a probe
	// of the workspace's .git (worktree pointer is a FILE; a force-copy would be a
	// DIR), then report.
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "g1", Name: "Bash", Args: gitArgs("git log --oneline")}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "g2", Name: "Bash", Args: gitArgs("git show --stat HEAD")}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "g3", Name: "Bash", Args: gitArgs("if [ -f .git ]; then echo DOTGIT_IS_FILE; elif [ -d .git ]; then echo DOTGIT_IS_DIR; else echo DOTGIT_MISSING; fi")}),
		mockllm.TextTurn("inspection done"),
	)
	factory := memberFactoryForTest(cfg, provider, nil, agents.NewRegistry(nil), nil, runner, true, nil)

	svc, err := newTestServerService(server.Config{
		Engine: noopEngine(),
		Store:  memstore.New(),

		SharedEngineRoot: repo,
		Now:              func() time.Time { return time.Unix(0, 0) },
		MemberEngine:     factory,
		Forker:           mutatingFk,
		ReadOnlyForker:   roFk,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	ctx := context.Background()
	teamID, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "inspect", "", 0,
		[]agent.MemberSpec{{Name: "lead", Lead: true, InitialPrompt: "inspect the history"}})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	// Collect the member's Bash tool.result bodies from the event stream.
	var mu sync.Mutex
	var bashOut strings.Builder
	sink := func(te agent.TeamEvent) {
		ev := te.Event
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			mu.Lock()
			bashOut.WriteString(ev.ToolResult.Content)
			bashOut.WriteString("\n")
			mu.Unlock()
		}
	}
	if _, err := svc.RunTeam(ctx, teamID, sink); err != nil {
		t.Fatalf("RunTeam: %v", err)
	}

	got := bashOut.String()
	for _, want := range []string{"add alpha", "add beta"} {
		if !strings.Contains(got, want) {
			t.Errorf("member git output missing %q; got:\n%s", want, got)
		}
	}
	// git show --stat HEAD names the file added in the HEAD commit.
	if !strings.Contains(got, "beta.txt") {
		t.Errorf("git show --stat HEAD output missing beta.txt; got:\n%s", got)
	}

	// The member's workspace must be a git WORKTREE (cheap, shares the base .git),
	// NOT a force-copy: in a worktree the child's `.git` is a FILE (a gitdir
	// pointer), whereas a recursive copy would leave a `.git` DIRECTORY. The
	// history/leak checks above pass for a force-copy too, so this is what actually
	// distinguishes the worktree path.
	if !strings.Contains(got, "DOTGIT_IS_FILE") {
		t.Errorf("read-only member workspace is not a git worktree (.git is not a pointer file); got:\n%s", got)
	}
	if strings.Contains(got, "DOTGIT_IS_DIR") {
		t.Errorf("read-only member workspace has a .git DIRECTORY (force-copy), expected a worktree pointer file; got:\n%s", got)
	}

	// The worktree must be cleaned up: no leftover child dir under worktreeBase.
	entries, err := os.ReadDir(worktreeBase)
	if err != nil {
		t.Fatalf("read worktree base: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("worktree base not cleaned up; leftover entries: %v", entries)
	}
}

// gitArgs builds the Bash tool's JSON args for a command. The command strings are
// innocuous read-only git inspection (log/show).
func gitArgs(command string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": command})
	return b
}

func initGitRepoTest(t *testing.T, dir string) {
	t.Helper()
	runGitTest(t, dir, "init")
	runGitTest(t, dir, "config", "user.email", "test@example.com")
	runGitTest(t, dir, "config", "user.name", "Test")
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func gitCommitTest(t *testing.T, dir, msg string) {
	t.Helper()
	runGitTest(t, dir, "add", "-A")
	runGitTest(t, dir, "commit", "-m", msg)
}

func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// teamServiceWithFactory builds a server.Service wired with the given member-engine
// factory, the osfs-backed forker (so Mutating members can fork a real base dir), an
// osfs workspace factory, and a deterministic clock. osfs (not memfs) is used because
// the forker copies the base tree off real disk for a Mutating member.
func teamServiceWithFactory(t *testing.T, factory server.MemberEngineFactory) *server.Service {
	t.Helper()
	root := t.TempDir()
	svc, err := newTestServerService(server.Config{
		Engine: noopEngine(),
		Store:  memstore.New(),

		SharedEngineRoot: root,
		Now:              func() time.Time { return time.Unix(0, 0) },
		MemberEngine:     factory,
		Forker:           forker.New(func(root string) (tool.Workspace, error) { return osfs.NewWorkspace(root) }),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestTeamReturnsConsolidatedReportEndToEnd is the gauntlet-style integration test:
// a real server.Service drives a 2-member team (lead + worker) through CreateTeam →
// RunTeam, and the outcome's Report is the lead's synthesis (NOT a header-only
// concatenation). It also asserts both member sessions are persisted to the service's
// Store under collision-free, namespaced ids that match agent.MemberSessionID — the
// ids the InspectMember tool derives — so a human/RPC can load a member transcript
// after the team finishes.
func TestTeamReturnsConsolidatedReportEndToEnd(t *testing.T) {
	store := memstore.New()

	// Per-member scripts: the worker records a finding; the lead delegates then
	// synthesises a consolidated report from the ledger.
	recordFinding := session.NewToolCall("w1", "RecordFinding",
		json.RawMessage(`{"finding":"WORKER_FINDING the leak is in the cache"}`))
	scripts := map[string][]mockllm.Turn{
		"lead": {
			mockllm.TextTurn("delegating to worker"),
			mockllm.TextTurn("CONSOLIDATED REPORT: the leak is in the cache; fix applied."),
		},
		"worker": {
			mockllm.ToolCallTurn(recordFinding),
			mockllm.TextTurn("recorded"),
		},
	}
	factory := scriptedMemberFactory(t, scripts)

	svc, err := newTestServerService(server.Config{
		Engine: noopEngine(),
		Store:  store,

		Now:          func() time.Time { return time.Unix(0, 0) },
		MemberEngine: factory,
		Forker:       forker.New(func(root string) (tool.Workspace, error) { return osfs.NewWorkspace(root) }),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	ctx := context.Background()
	teamID, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "e2e", "find and fix the leak", 0,
		[]agent.MemberSpec{
			{Name: "lead", Lead: true, InitialPrompt: "coordinate"},
			{Name: "worker", InitialPrompt: "investigate"},
		})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	outcome, err := svc.RunTeam(ctx, teamID, func(agent.TeamEvent) {})
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
	}
	if !strings.Contains(outcome.Report, "CONSOLIDATED REPORT") {
		t.Errorf("outcome.Report = %q, want the lead's synthesis", outcome.Report)
	}
	if strings.Contains(outcome.Report, "=== ") {
		t.Errorf("Report must not be a header-only concatenation: %q", outcome.Report)
	}

	// Both member sessions are loadable from the store under namespaced ids.
	for _, name := range []string{"lead", "worker"} {
		id := agent.MemberSessionID(teamID, name)
		sess, lerr := store.Load(ctx, id)
		if lerr != nil {
			t.Fatalf("member session %q not persisted: %v", id, lerr)
		}
		if sess.ID != id {
			t.Errorf("loaded session id = %q, want %q", sess.ID, id)
		}
	}
}

// TestTeamRunTeamPathSurfacesTeamID pins AC-6 for the SECOND entry point (gRPC
// RunTeam). Per decision D-6, the wire client already knows the team id — CreateTeam
// RETURNS it — and the model never consumes the RunTeam outcome string model-to-model
// (the in-process Team-tool path, where a model DOES consume the ToolResult, is the
// one that needed the in-result header; it is covered by
// TestParentDiscoversTeamIDFromResultAndInspects). So this path's discovery contract
// is: CreateTeam returns the id, RunTeam accepts THAT id, and member sessions persist
// under ids the consumer can derive from it via MemberSessionID (so an out-of-band
// InspectMember works). This test pins that the second entry point is served and its
// id round-trips to the persisted member ids.
func TestTeamRunTeamPathSurfacesTeamID(t *testing.T) {
	store := memstore.New()
	scripts := map[string][]mockllm.Turn{
		"lead":   {mockllm.TextTurn("coordinating"), mockllm.TextTurn("REPORT")},
		"worker": {mockllm.TextTurn("working"), mockllm.TextTurn("done")},
	}
	svc, err := newTestServerService(server.Config{
		Engine: noopEngine(),
		Store:  store,

		Now:          func() time.Time { return time.Unix(0, 0) },
		MemberEngine: scriptedMemberFactory(t, scripts),
		Forker:       forker.New(func(root string) (tool.Workspace, error) { return osfs.NewWorkspace(root) }),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	ctx := context.Background()
	teamID, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "e2e", "do it", 0,
		[]agent.MemberSpec{
			{Name: "lead", Lead: true, InitialPrompt: "coordinate"},
			{Name: "worker", InitialPrompt: "work"},
		})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if teamID == "" {
		t.Fatal("CreateTeam returned an empty team id (the wire client's discovery source)")
	}

	if _, err := svc.RunTeam(ctx, teamID, func(agent.TeamEvent) {}); err != nil {
		t.Fatalf("RunTeam(%q): %v", teamID, err)
	}

	// The returned id round-trips to the persisted member sessions via MemberSessionID
	// — so a wire consumer holding the CreateTeam id can drive InspectMember.
	for _, name := range []string{"lead", "worker"} {
		id := agent.MemberSessionID(teamID, name)
		if _, lerr := store.Load(ctx, id); lerr != nil {
			t.Fatalf("member %q not persisted under MemberSessionID(%q,%q)=%q: %v", name, teamID, name, id, lerr)
		}
	}
}

// TestAgencyDeltaReachesTeamMemberAndLead pins AC-9: the persistence "agency"
// delta reaches the system prompt of a team member/lead engine built by the REAL
// composition factory, for EVERY model family — Claude included (issue #49: the
// per-Claude omission was reversed). It is the regression tripwire for a future
// refactor that drops the delta from childEngineDepsForProvider's PromptConfig.
func TestAgencyDeltaReachesTeamMemberAndLead(t *testing.T) {
	const agencyMarker = "Keep going until the task is actually resolved"

	capturePrompt := func(t *testing.T, parentModel string) string {
		t.Helper()
		var captured string
		obs := mockllm.WithRequestObserver(func(req port.LLMRequest) {
			if captured == "" {
				captured = req.System.StablePrefix + "\n" + req.System.VolatileSuffix
			}
		})
		prov := mockllm.NewWith([]mockllm.Option{obs}, mockllm.TextTurn("ok"))
		cfg := Config{Workspace: t.TempDir(), Model: parentModel}
		factory := memberFactoryForTest(cfg, prov, hookexec.New(nil), agents.NewRegistry(nil), nil, nil, false, nil)

		tm := team.New("t")
		sup := agent.NewSupervisor(tm, memEnvironment("/ws"),
			func(spec agent.MemberSpec, routedModel string) agent.MemberBuild {
				return factory(tm, spec, routedModel)
			})
		if err := sup.AddMember(context.Background(), agent.MemberSpec{
			Name: "lead", Lead: true, AgentType: "", InitialPrompt: "go",
		}); err != nil {
			t.Fatalf("AddMember: %v", err)
		}
		sup.Run(context.Background(), func(agent.TeamEvent) {})
		if captured == "" {
			t.Fatal("no request reached the member provider; cannot assert the system prompt")
		}
		return captured
	}

	// A non-Claude model (here a gpt-style id) MUST get the persistence delta.
	if got := capturePrompt(t, "gpt-5.2"); !strings.Contains(got, agencyMarker) {
		t.Fatalf("non-Claude member system prompt is MISSING the agency persistence delta:\n%s", got)
	}
	// A Claude model MUST get it too (issue #49: Claude announced actions without
	// taking them, so the contract now applies to every family).
	if got := capturePrompt(t, "claude-sonnet-4"); !strings.Contains(got, agencyMarker) {
		t.Fatalf("Claude member system prompt is MISSING the agency persistence delta:\n%s", got)
	}
}

// scriptedMemberFactory builds a server.MemberEngineFactory that scripts each member
// by name with the given turns and registers that member's coordination tools — a
// minimal but REAL per-member engine, sufficient to exercise the Service/Supervisor/
// synthesis/persistence path end to end.
func scriptedMemberFactory(t *testing.T, scripts map[string][]mockllm.Turn) server.MemberEngineFactory {
	t.Helper()
	allow := permpolicy.NewPolicy(defaultRules(), nil)
	return func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		turns, ok := scripts[spec.Name]
		if !ok {
			t.Fatalf("scriptedMemberFactory: no script for member %q", spec.Name)
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: mockllm.New(turns...), Catalog: cat, Policy: allow, Model: "mock",
		})}
	}
}

// noopEngine is a minimal engine for the Service's plain-session path, unused by the
// team tests but required by NewService.
func noopEngine() *agent.Engine {
	return agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("x")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(defaultRules(), nil),
		Model:   "mock",
	})
}
