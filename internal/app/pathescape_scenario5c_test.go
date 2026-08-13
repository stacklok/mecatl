package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// pathescape_scenario5c_test.go pins AC5.1d/AC5.1e
// (docs/acceptance/path-escape-posture.md Scenario 5): a base-SHARING
// (shell-less) read-only team member — the supervisor's base-share fallback
// tier, reached whenever the factory cannot isolate the member (no read-only
// runner / forker wired) — must NOT inherit the main session's relaxed
// workspace. Before the fix selectMemberWorkspace returned s.base VERBATIM
// for that tier, so when the base is the escapeWorkspace-wrapped relaxed osfs
// (WithRelaxedReads/WithRelaxedWrites at auto/yolo) the shell-less member
// silently gained the main session's out-of-root reach — the same
// child-never-relaxes leak task 05 closed for the Subagent nil-forker path,
// through the supervisor's base-share fallback. The fix re-views the shared
// base through the NON-relaxed construction (the SAME root, NO relaxed
// options) for the base-share tier only; the isolated tiers (worktree
// IsolateReadOnly, Mutating force-copy) are untouched.

// TestPathEscapePosture_Scenario5_BaseSharingMemberNotRelaxed pins AC5.1d: a
// base-sharing (shell-less) read-only team member's out-of-root Read is
// DENIED when the main session runs relaxed at auto/yolo. The test drives the
// REAL full Build: the gRPC CreateTeam path builds the team base through the
// relaxed main-session Workspaces factory (osfsWorkspaceFactory at the relaxed
// posture), and the shell-less read-only member (no sandboxed runner ⇒ no
// isolation) falls to the supervisor's base-share fallback. Its out-of-root
// Read must error; the relaxed base itself must still serve the read (the
// positive control proving the relax is genuinely on). Runs at both relaxed
// postures (yolo and auto).
func TestPathEscapePosture_Scenario5_BaseSharingMemberNotRelaxed(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path fixtures")
	}
	for _, posture := range []Posture{PostureYolo, PostureAuto} {
		posture := posture
		t.Run(posture.String(), func(t *testing.T) {
			t.Parallel()
			f := setupEscapeFS(t)

			// A shell-less Build: NoBash nils the sandboxed runner, so the member
			// factory cannot isolate a read-only member (IsolateReadOnly stays
			// false) and the member base-shares. Member turn 1 attempts the
			// out-of-root Read, turn 2 reports; turn 3 is the lead's synthesis
			// turn (the single-member lead synthesises its own round).
			memberRead := session.NewToolCall("m1", "Read", json.RawMessage(`{"path":`+mustJSONStr(t, f.target)+`}`))
			cfg := escapeCfg(t, f, posture,
				mockllm.ToolCallTurn(memberRead),
				mockllm.TextTurn("member done"),
				mockllm.TextTurn("team report"),
			)
			cfg.NoBash = true
			cfg.EnableTeams = true
			built, err := Build(context.Background(), cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()

			var mu sync.Mutex
			var memberReadErr, memberReadOK bool
			sink := func(te agent.TeamEvent) {
				ev := te.Event
				if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == memberRead.ID {
					mu.Lock()
					if ev.ToolResult.IsError {
						memberReadErr = true
					} else {
						memberReadOK = true
					}
					mu.Unlock()
				}
			}
			ctx := context.Background()
			teamID, _, err := built.Service.CreateTeam(ctx, f.workspace, "pathescape", "", 0,
				[]agent.MemberSpec{{Name: "lead", Lead: true, InitialPrompt: "read the file outside the workspace"}})
			if err != nil {
				t.Fatalf("CreateTeam: %v", err)
			}
			if _, err := built.Service.RunTeam(ctx, teamID, sink); err != nil {
				t.Fatalf("RunTeam: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if memberReadOK {
				t.Fatalf("base-sharing read-only member's out-of-root Read SUCCEEDED at %s — the member must not inherit the relaxed base", posture)
			}
			if !memberReadErr {
				t.Fatalf("no tool.result for the member's Read (call m1) at %s — the member turn did not run", posture)
			}
			// Positive control: the relaxed main-session workspace factory
			// itself still serves the read — the member denial above is a real
			// base-share propagation boundary, not a vacuous "the relax was
			// never on".
			relaxed := osfsWorkspaceFactory(port.NopDiagnostics{}, nil)(f.workspace)
			if relaxed == nil {
				t.Fatalf("relaxed base workspace is nil at %s", posture)
			}
			data, rerr := relaxed.Read(context.Background(), f.target)
			if rerr != nil || !strings.Contains(string(data), f.content) {
				t.Fatalf("positive control failed: the relaxed base at %s must serve the out-of-root read (err=%v data=%.80q)", posture, rerr, data)
			}
		})
	}
}

// TestPathEscapePosture_Scenario5_IsolatedMembersUnchanged pins AC5.1e: the
// fix touches ONLY the base-share fallback — the isolated-member tiers are
// unchanged. A read-only member the factory CAN isolate (worktree
// IsolateReadOnly) still denies the out-of-root Read from its throwaway
// checkout; a Mutating member's force-copy fork still denies it; and the
// relaxed base itself still serves the read (the main session still
// relaxes). Runs at both relaxed postures.
func TestPathEscapePosture_Scenario5_IsolatedMembersUnchanged(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("worktree fixtures assume a POSIX filesystem")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, posture := range []Posture{PostureYolo, PostureAuto} {
		posture := posture
		t.Run(posture.String(), func(t *testing.T) {
			t.Parallel()
			f := setupEscapeFS(t)
			// The base is a real git repo so the read-only-isolated member forks
			// into a real worktree (the seam under test) and the Mutating member
			// force-copies from it.
			initGitRepoTest(t, f.workspace)
			writeRepoFile(t, f.workspace, "inroot.txt", "in-root\n")
			gitCommitTest(t, f.workspace, "add inroot")

			base := osfsWorkspaceFactory(port.NopDiagnostics{}, nil)(f.workspace)
			if base == nil {
				t.Fatalf("relaxed base workspace is nil at %s", posture)
			}

			cfg := teamCfg(t)
			runner := buildSandboxedCommandRunner(cfg)
			if runner == nil {
				t.Fatal("precondition: expected a non-nil sandboxed runner (teamCfg wires a trusted workspace + shell)")
			}
			roFk := teamTestForker(t, posture, f.workspace, false)
			mutatingFk := teamTestForker(t, posture, f.workspace, true)

			// The read-only-ISOLATED member: attempt the SAME out-of-root Read
			// from its worktree (its workspace comes from newForkWorkspace —
			// never relaxed — so the Read is denied), then report; then the
			// lead's synthesis turn.
			roRead := session.NewToolCall("r1", "Read", json.RawMessage(`{"path":`+mustJSONStr(t, f.target)+`}`))
			roProvider := mockllm.New(
				mockllm.ToolCallTurn(roRead),
				mockllm.TextTurn("member done"),
				mockllm.TextTurn("team report"),
			)
			roFactory := memberFactoryForTest(cfg, roProvider, nil, agents.NewRegistry(nil), nil, runner, true, nil)

			var mu sync.Mutex
			var roReadErr, roReadOK bool
			roSink := func(te agent.TeamEvent) {
				ev := te.Event
				if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == roRead.ID {
					mu.Lock()
					if ev.ToolResult.IsError {
						roReadErr = true
					} else {
						roReadOK = true
					}
					mu.Unlock()
				}
			}
			runTeamWithForkers(t, base, roFactory, roFk, mutatingFk, agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "read the file outside the workspace"}, roSink)
			mu.Lock()
			if roReadOK {
				t.Fatalf("read-only-isolated member's out-of-root Read SUCCEEDED at %s — the worktree tier must stay unchanged", posture)
			}
			if !roReadErr {
				t.Fatalf("no tool.result for the read-only-isolated member's Read (call r1) at %s — the member turn did not run", posture)
			}
			mu.Unlock()

			// The MUTATING member: attempt the SAME out-of-root Read from its
			// force-copy fork — the fork is built by newForkWorkspace (never
			// relaxed), so the Read is denied. Then report; then synthesis.
			mutRead := session.NewToolCall("w1", "Read", json.RawMessage(`{"path":`+mustJSONStr(t, f.target)+`}`))
			mutProvider := mockllm.New(
				mockllm.ToolCallTurn(mutRead),
				mockllm.TextTurn("member done"),
				mockllm.TextTurn("team report"),
			)
			mutFactory := memberFactoryForTest(cfg, mutProvider, nil, agents.NewRegistry(nil), nil, runner, true, nil)

			var mutReadErr, mutReadOK bool
			mutSink := func(te agent.TeamEvent) {
				ev := te.Event
				if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == mutRead.ID {
					mu.Lock()
					if ev.ToolResult.IsError {
						mutReadErr = true
					} else {
						mutReadOK = true
					}
					mu.Unlock()
				}
			}
			runTeamWithForkers(t, base, mutFactory, roFk, mutatingFk, agent.MemberSpec{Name: "lead", Lead: true, Mutating: true, InitialPrompt: "read the file outside the workspace"}, mutSink)
			mu.Lock()
			if mutReadOK {
				t.Fatalf("Mutating member's out-of-root Read SUCCEEDED at %s — the force-copy tier must stay unchanged", posture)
			}
			if !mutReadErr {
				t.Fatalf("no tool.result for the Mutating member's Read (call w1) at %s — the member turn did not run", posture)
			}
			mu.Unlock()

			// The main session still relaxes: the relaxed base itself serves the
			// out-of-root read unchanged.
			data, rerr := base.Read(context.Background(), f.target)
			if rerr != nil || !strings.Contains(string(data), f.content) {
				t.Fatalf("the main session must still relax at %s (err=%v data=%.80q)", posture, rerr, data)
			}
		})
	}
}

// runTeamWithForkers drives a one-member team over base through the real
// server.Service/CreateTeam/RunTeam wiring with the given forked tiers (nil
// forkers select the base-share fallback). It deliberately wires NO
// SharedBaseWorkspace re-view: this helper exists for the ISOLATED tiers only
// (worktree IsolateReadOnly + Mutating force-copy, AC5.1e), which never
// consult the re-view.
func runTeamWithForkers(t *testing.T, base tool.Workspace, factory server.MemberEngineFactory, roFk, mutatingFk tool.EnvironmentForker, spec agent.MemberSpec, sink func(agent.TeamEvent)) {
	t.Helper()
	svc, err := server.NewService(server.Config{
		Engine:         noopEngine(),
		Store:          memstore.New(),
		Workspaces:     func(string) tool.Workspace { return base },
		Now:            func() time.Time { return time.Unix(0, 0) },
		MemberEngine:   factory,
		Forker:         mutatingFk,
		ReadOnlyForker: roFk,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	teamID, _, err := svc.CreateTeam(ctx, base.Root(), "pathescape", "", 0, []agent.MemberSpec{spec})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := svc.RunTeam(ctx, teamID, sink); err != nil {
		t.Fatalf("RunTeam: %v", err)
	}
}

// teamTestForker builds the real child-family forker shape over the shared
// newForkWorkspace constructor (worktree + dirty overlay for the read-only
// tier, force-copy for the Mutating tier) — the SAME shapes buildTeamWiring
// wires.
func teamTestForker(t *testing.T, _ Posture, _ string, forceCopy bool) tool.EnvironmentForker {
	t.Helper()
	if forceCopy {
		return forker.New(newForkWorkspace(nil), forker.WithForceCopy())
	}
	return forker.New(newForkWorkspace(nil), forker.WithDirtyOverlay())
}
