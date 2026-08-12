package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// trust_shell_gate_test.go covers the issue-#40 SUBAGENT-SHELL gate on the
// read-only subagent/member shell: an UNTRUSTED workspace (cfg.TrustProject
// false) yields a nil sandboxed runner (buildSandboxedCommandRunner), which
// degrades every worktree-shell surface — the default Subagent explorer, per-def
// subagents, the model-override factory children, and read-only team members —
// to Bash-less Read/Grep/Glob, with no forker wired and an honest Subagent Spec
// note. The deliberate ASYMMETRY: a MUTATING member keeps its hardened shell
// (buildForceCopyRunner, shared with Parallel branches) because a force-copy fork
// involves NO fork-time git invocation (pure FS copy — the worktree-checkout RCE
// the gate closes cannot fire), and its run-time git over the COPIED untrusted
// .git is the accepted main-session-parity residual. The shell gate is
// cfg.TrustProject (the folded workspace-trust decision — the operator vouches
// for the repo's `.git`). The WITH-shell counterparts of these wiring tests live
// in task_shell_test.go / fork_member_bash_test.go (teamCfg sets TrustProject
// true). The shell-less fixtures here use shelllessTeamCfg (untrusted).

// untrustedTeamCfg is the shell-less fixture (untrusted): TrustProject=false, so
// buildSandboxedCommandRunner returns nil.
func untrustedTeamCfg(t *testing.T) Config {
	t.Helper()
	return shelllessTeamCfg(t)
}

// TestUntrustedWorkspaceDisablesSandboxedRunner pins the gate itself: a configured
// shell on an UNTRUSTED workspace still yields a nil sandboxed runner.
func TestUntrustedWorkspaceDisablesSandboxedRunner(t *testing.T) {
	cfg := untrustedTeamCfg(t)
	if buildSandboxedCommandRunner(cfg) != nil {
		t.Fatal("an untrusted workspace must yield a NIL sandboxed runner (the issue-#40 trust gate); got a live one")
	}
}

// TestTrustedWorkspaceKeepsSandboxedRunner is the positive counterpart: trust admits
// the sandboxed runner, so the gate cannot over-fire.
func TestTrustedWorkspaceKeepsSandboxedRunner(t *testing.T) {
	cfg := teamCfg(t) // TrustProject: true
	if buildSandboxedCommandRunner(cfg) == nil {
		t.Fatal("a trusted workspace with a shell must keep the sandboxed runner; the trust gate over-fired")
	}
}

// TestTrustGatesIngestionAndShellTogether pins the final issue-#359 model at
// both consumers: TrustProject is the one effective grant, so trusted means both
// ingestion and shell, while an untrusted raw config gets neither even if its
// posture token is auto (applyPosture is the only ladder projection).
func TestTrustGatesIngestionAndShellTogether(t *testing.T) {
	trusted := teamCfg(t)
	if !projectIngestionAdmitted(trusted) || buildSandboxedCommandRunner(trusted) == nil {
		t.Fatal("trusted workspace must admit project ingestion and the read-only child shell")
	}
	untrusted := shelllessTeamCfg(t)
	untrusted.Posture = PostureAuto
	if projectIngestionAdmitted(untrusted) || buildSandboxedCommandRunner(untrusted) != nil {
		t.Fatal("untrusted workspace must admit neither project ingestion nor the read-only child shell")
	}
}

// TestUntrustedSubagentChildCatalogHasNoBash proves the default Subagent explorer
// degrades to the Bash-less read-only catalog when the runner derivation runs
// through the trust gate, and that a trusted workspace keeps the shell.
func TestUntrustedSubagentChildCatalogHasNoBash(t *testing.T) {
	untrusted := untrustedTeamCfg(t)
	eng := buildChildEngine(untrusted, nil, bashThenEdit(), "", untrusted.Model, buildSandboxedCommandRunner(untrusted))
	events := drainEngine(t, eng)
	if !unknownToolResult(events, "b1") {
		t.Error("untrusted: the default Subagent explorer dispatched Bash; the trust gate must leave it shell-less")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("untrusted: the explorer got Edit; it must stay a read-only explorer")
	}

	trusted := teamCfg(t)
	engTrusted := buildChildEngine(trusted, nil, bashThenEdit(), "", trusted.Model, buildSandboxedCommandRunner(trusted))
	eventsTrusted := drainEngine(t, engTrusted)
	if !sawDispatchedTool(eventsTrusted, "b1") {
		t.Error("trusted: the explorer must keep its worktree shell (the gate must not over-fire)")
	}
}

// TestUntrustedSubagentNoForkerWired drives the REAL buildSubagentTool with an
// untrusted (shell-configured) cfg and proves the no-shell/no-forker coupling holds
// for the trust gate exactly as for NoBash: the child's Bash ATTEMPT is actually
// observed bouncing off the catalog (an "unknown tool" error result rides the next
// LLM request — not merely "nothing happened"), the probe file is never written, and
// the real git workspace's .git/worktrees stays empty (no `git worktree add` ever
// ran against the untrusted base).
func TestUntrustedSubagentNoForkerWired(t *testing.T) {
	cfg := untrustedTeamCfg(t)
	if buildSandboxedCommandRunner(cfg) != nil {
		t.Fatal("precondition: expected a nil sandboxed runner for the untrusted cfg")
	}
	// A REAL git repo as the untrusted base: the worktrees assertion below is only
	// meaningful when `git worktree add` against it would have left admin entries.
	initGitRepoTest(t, cfg.Workspace)
	writeRepoFile(t, cfg.Workspace, "f.txt", "x")
	gitCommitTest(t, cfg.Workspace, "init")

	probeDir := t.TempDir()
	probeFile := filepath.Join(probeDir, "probe.txt")
	var (
		reqMu    sync.Mutex
		requests []port.LLMRequest
	)
	childProvider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
		reqMu.Lock()
		requests = append(requests, r)
		reqMu.Unlock()
	})},
		mockllm.ToolCallTurn(session.ToolCall{ID: "p1", Name: "Bash", Args: gitArgs("pwd > " + probeFile)}),
		mockllm.TextTurn("could not run a shell"),
	)

	task, closeFn := taskToolForTest(context.Background(), cfg, childProvider, hookexec.New(nil), regOf(), nil)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}

	res, err := task.Execute(context.Background(),
		session.NewToolCall("c1", "Subagent", json.RawMessage(`{"prompt":"try to run a shell"}`)),
		osfsWSForTest(t, cfg.Workspace))
	if err != nil {
		t.Fatalf("Subagent.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Subagent must complete degraded (Bash-less), not error: %q", res.Content)
	}
	// The Bash attempt was OBSERVED failing: the child's follow-up request replays
	// an unknown-tool error result for p1 (Bash absent from the catalog), so the
	// degradation is proven by the attempt, not assumed from inactivity.
	reqMu.Lock()
	var sawUnknownBash bool
	for _, r := range requests {
		for _, m := range r.Messages {
			if m.ToolResult != nil && m.ToolResult.CallID == "p1" && m.ToolResult.IsError &&
				strings.Contains(m.ToolResult.Content, "unknown tool") {
				sawUnknownBash = true
			}
		}
	}
	reqMu.Unlock()
	if !sawUnknownBash {
		t.Error("the child's Bash attempt was never observed bouncing off the catalog as an unknown-tool error")
	}
	if _, err := os.Stat(probeFile); err == nil {
		t.Fatal("probe file was written; an untrusted workspace must wire NO child shell and NO forker")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error stating probe file: %v", err)
	}
	// No worktree was ever created against the untrusted base repo.
	wtDir := filepath.Join(cfg.Workspace, ".git", "worktrees")
	if entries, err := os.ReadDir(wtDir); err == nil && len(entries) > 0 {
		t.Fatalf(".git/worktrees is non-empty (%d entries); a worktree was forked off the untrusted base", len(entries))
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("unexpected error reading %s: %v", wtDir, err)
	}
}

// TestUntrustedReadOnlyMemberHasNoBash drives the REAL buildTeamWiring with an
// untrusted cfg: a read-only member gets NO Bash (and is not flagged for worktree
// isolation), even though a shell is configured.
func TestUntrustedReadOnlyMemberHasNoBash(t *testing.T) {
	cfg := untrustedTeamCfg(t)
	prov := bashThenEdit()
	factory, _, _, _, _ := buildTeamWiring(context.Background(), cfg, regForTest(prov, providerMock, cfg.Model), prov, providerMock, cfg.Model, nil, agents.NewRegistry(nil), nil, nil, catalogAssets{}, false)
	build := factory(team.New("t"), agent.MemberSpec{Name: "reader", Mutating: false}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	if build.IsolateReadOnly {
		t.Error("untrusted read-only member must NOT be IsolateReadOnly (no shell ⇒ base-share)")
	}
	events := drainEngine(t, build.Engine)
	if !unknownToolResult(events, "b1") {
		t.Error("untrusted: a read-only member dispatched Bash; the trust gate must withhold the worktree shell")
	}
}

// TestUntrustedMutatingMemberKeepsBash is the ASYMMETRY pin (issue #40): through the
// SAME untrusted buildTeamWiring, a MUTATING member still gets Bash — its force-copy
// fork is created without any git invocation (the fork-time checkout RCE the trust
// gate closes cannot fire), and its run-time git over the copied untrusted .git is
// the accepted main-session-parity residual (the Parallel rationale —
// buildForceCopyRunner). Edit survives too.
func TestUntrustedMutatingMemberKeepsBash(t *testing.T) {
	cfg := untrustedTeamCfg(t)
	prov := bashThenEdit()
	factory, _, _, _, _ := buildTeamWiring(context.Background(), cfg, regForTest(prov, providerMock, cfg.Model), prov, providerMock, cfg.Model, nil, agents.NewRegistry(nil), nil, nil, catalogAssets{}, false)
	build := factory(team.New("t"), agent.MemberSpec{Name: "writer", Mutating: true}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	events := drainEngine(t, build.Engine)
	if !sawDispatchedTool(events, "b1") {
		t.Error("untrusted: a MUTATING member lost Bash; the trust gate must withhold only the WORKTREE shell (force-copy forks keep theirs)")
	}
	if !sawDispatchedTool(events, "e1") {
		t.Error("untrusted: a MUTATING member lost Edit; untrust must not strip the mutating fork toolset")
	}
}

// TestUntrustedSubagentSpecCarriesNoShellNote proves the model-facing honesty fix:
// the Subagent tool built over a shell-less workspace (no subagent-shell grant)
// REPLACES the worktree-shell promise with the no-shell note (naming --posture
// auto), while the shell-bearing build keeps the historical shell-bearing
// description.
func TestUntrustedSubagentSpecCarriesNoShellNote(t *testing.T) {
	untrusted := untrustedTeamCfg(t)
	task, closeFn := taskToolForTest(context.Background(), untrusted, mockllm.New(), hookexec.New(nil), regOf(), nil)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}
	desc := task.Spec().Description
	if strings.Contains(desc, "throwaway worktree") {
		t.Errorf("shell-less Subagent spec still promises the worktree shell:\n%s", desc)
	}
	for _, want := range []string{"untrusted", "--trust-project"} {
		if !strings.Contains(desc, want) {
			t.Errorf("shell-less Subagent spec must carry the no-shell note naming %q, got:\n%s", want, desc)
		}
	}

	trusted := teamCfg(t)
	taskTrusted, closeTrusted := taskToolForTest(context.Background(), trusted, mockllm.New(), hookexec.New(nil), regOf(), nil)
	if closeTrusted != nil {
		defer func() { _ = closeTrusted() }()
	}
	descTrusted := taskTrusted.Spec().Description
	if !strings.Contains(descTrusted, "throwaway worktree") {
		t.Errorf("shell-bearing Subagent spec must keep claiming the worktree shell, got:\n%s", descTrusted)
	}
	if strings.Contains(descTrusted, "posture is below auto") {
		t.Errorf("shell-bearing Subagent spec must carry no no-shell note, got:\n%s", descTrusted)
	}
}

// TestNoBashFlagNoteDistinctFromUntrusted pins the note's CAUSE attribution: a
// shell-less deployment (--no-bash, or an empty shell) must NOT produce the
// posture-below-auto no-shell note — those causes keep the historical description
// unchanged (the pre-#40 behaviour), whether the workspace is trusted or not.
func TestNoBashFlagNoteDistinctFromUntrusted(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"no-bash trusted":     func(c *Config) { c.NoBash = true },
		"no-bash untrusted":   func(c *Config) { c.NoBash = true; c.TrustProject = false },
		"empty-shell trusted": func(c *Config) { c.Shell = "" },
		// Empty shell + untrusted: the EMPTY SHELL must win the blame — there is no
		// shell for --posture auto to enable, so the no-shell note (and its
		// "--posture auto" remedy) must not appear.
		"empty-shell untrusted": func(c *Config) { c.Shell = ""; c.TrustProject = false },
	} {
		cfg := teamCfg(t)
		mutate(&cfg)
		task, closeFn := taskToolForTest(context.Background(), cfg, mockllm.New(), hookexec.New(nil), regOf(), nil)
		if closeFn != nil {
			defer func() { _ = closeFn() }()
		}
		desc := task.Spec().Description
		if strings.Contains(desc, "posture is below auto") {
			t.Errorf("%s: the no-shell note must be reserved for the shell-grant cause, got:\n%s", name, desc)
		}
		if !strings.Contains(desc, "throwaway worktree") {
			t.Errorf("%s: a shell-less deployment keeps the historical (byte-stable) description, got:\n%s", name, desc)
		}
	}
}

// TestBaseSubagentToolsUntrustedExcludesBash pins the diagnostic-only name-set
// computation (issue #40, fix 2): baseSubagentTools runs Bash
// availability through the TRUST-GATED path (cfg.TrustProject),
// so an untrusted workspace's base set excludes Bash and a def
// allow-listing it draws the ACCURATE "shell unavailable … untrusted"
// diagnostic — not the misleading generic unknown-tool one. A trusted
// workspace keeps Bash in the base (the historical "mutating; dropped"
// diagnostic path).
func TestBaseSubagentToolsUntrustedExcludesBash(t *testing.T) {
	untrusted := untrustedTeamCfg(t) // untrusted → no shell
	base := baseSubagentTools(untrusted)
	if _, ok := base["Bash"]; ok {
		t.Fatal("shell-less: Bash must be excluded from the subagent base toolset")
	}
	def := agents.AgentDef{Name: "inspector", Tools: []string{"Read", "Bash"}}
	names, diags := scopedToolNames(def, base, bashScopeMissReason(untrusted))
	if strings.Join(names, ",") != "Read" {
		t.Fatalf("shell-less scoped names = %v, want [Read]", names)
	}
	var bashReason string
	for _, d := range diags {
		if d.tool == "Bash" {
			bashReason = d.reason
		}
	}
	for _, want := range []string{"shell unavailable", "untrusted", "--trust-project"} {
		if !strings.Contains(bashReason, want) {
			t.Errorf("shell-less Bash scope diagnostic must say %q (accurate cause), got %q", want, bashReason)
		}
	}
	if strings.Contains(bashReason, "unknown tool") {
		t.Errorf("shell-less Bash scope diagnostic must not be the misleading generic unknown-tool one: %q", bashReason)
	}

	if _, ok := baseSubagentTools(teamCfg(t))["Bash"]; !ok {
		t.Fatal("trusted: Bash must stay in the subagent base toolset")
	}
}

// TestBashScopeMissReasonPreciseCause pins the per-cause attribution of the per-def
// Bash-miss diagnostic (issue #40 follow-up): each disable cause
// names ITSELF — in particular, --no-bash or an empty shell must NOT suggest
// --trust-project (there is no trust problem to fix), and only the untrusted
// cause carries the --trust-project remedy.
func TestBashScopeMissReasonPreciseCause(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate       func(*Config)
		want, reject string
	}{
		"no-bash (trusted)":     {func(c *Config) { c.NoBash = true }, "--no-bash", "--trust-project"},
		"no-bash untrusted":     {func(c *Config) { c.NoBash = true; c.TrustProject = false }, "--no-bash", "--trust-project"},
		"empty-shell (trusted)": {func(c *Config) { c.Shell = "" }, "no shell configured", "--trust-project"},
		"empty-shell untrusted": {func(c *Config) { c.Shell = ""; c.TrustProject = false }, "no shell configured", "--trust-project"},
		// The trust axis: an untrusted workspace withholds the shell; the remedy
		// is --trust-project.
		"untrusted": {func(c *Config) { c.TrustProject = false }, "--trust-project", "--no-bash"},
	} {
		cfg := teamCfg(t)
		tc.mutate(&cfg)
		got := bashScopeMissReason(cfg)
		if !strings.Contains(got, "shell unavailable") {
			t.Errorf("%s: reason must lead with the shell-unavailable cause, got %q", name, got)
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: reason must name the precise cause %q, got %q", name, tc.want, got)
		}
		if strings.Contains(got, tc.reject) {
			t.Errorf("%s: reason must NOT carry the misattributed remedy %q, got %q", name, tc.reject, got)
		}
	}
}

// TestUntrustedMutatingDefMemberKeepsBash is the DEF-TIER asymmetry pin (issue #40):
// an untrusted workspace's trust-gated base excludes Bash, so a MUTATING member
// adopting a def that allow-lists Bash keeps it ONLY via buildMemberEngine's
// mutating re-add (`spec.Mutating && mutatingRunner != nil` putting the ungated
// force-copy runner's Bash back into the base before scoping). Delete that re-add
// and this test fails (mutation-verified) — the def-tier mutating member would
// silently lose its shell while the default-tier one kept it.
func TestUntrustedMutatingDefMemberKeepsBash(t *testing.T) {
	cfg := untrustedTeamCfg(t)
	prov := bashThenEdit()
	def := agents.AgentDef{Name: "builder", Tools: []string{"Read", "Bash", "Edit"}}
	factory, _, _, _, _ := buildTeamWiring(context.Background(), cfg, regForTest(prov, providerMock, cfg.Model), prov, providerMock, cfg.Model, nil, regOf(def), nil, nil, catalogAssets{}, false)
	build := factory(team.New("t"), agent.MemberSpec{Name: "writer", AgentType: "builder", Mutating: true}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	events := drainEngine(t, build.Engine)
	if !sawDispatchedTool(events, "b1") {
		t.Error("untrusted: a MUTATING per-def member lost Bash; the def base re-add (spec.Mutating && mutatingRunner != nil) must restore the ungated force-copy shell")
	}
	if !sawDispatchedTool(events, "e1") {
		t.Error("untrusted: a MUTATING per-def member lost Edit; the def allowlist must keep mutating tools for a Mutating member")
	}
}

// TestUntrustedReadOnlyMemberPromptCarriesShellNote pins the one-line member-prompt
// honesty fix (issue #40): a read-only member built over an untrusted workspace is
// TOLD it has no shell (so it plans around Read/Grep/Glob), while a Mutating member
// (which keeps its fork shell) and a trusted read-only member carry no such note.
func TestUntrustedReadOnlyMemberPromptCarriesShellNote(t *testing.T) {
	systemFor := func(cfg Config, spec agent.MemberSpec) string {
		var (
			mu  sync.Mutex
			sys string
		)
		prov := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
			mu.Lock()
			sys = r.System.Render()
			mu.Unlock()
		})}, mockllm.TextTurn("done"))
		factory, _, _, _, _ := buildTeamWiring(context.Background(), cfg, regForTest(prov, providerMock, cfg.Model), prov, providerMock, cfg.Model, nil, agents.NewRegistry(nil), nil, nil, catalogAssets{}, false)
		build := factory(team.New("t"), spec, "")
		if build.Engine == nil {
			t.Fatal("factory returned a nil engine")
		}
		drainEngine(t, build.Engine)
		mu.Lock()
		defer mu.Unlock()
		return sys
	}

	if sys := systemFor(untrustedTeamCfg(t), agent.MemberSpec{Name: "reader", Mutating: false}); !strings.Contains(sys, untrustedMemberShellNote) {
		t.Errorf("untrusted read-only member system prompt must carry the no-shell note, got:\n%s", sys)
	}
	if sys := systemFor(untrustedTeamCfg(t), agent.MemberSpec{Name: "writer", Mutating: true}); strings.Contains(sys, untrustedMemberShellNote) {
		t.Errorf("a MUTATING member keeps its fork shell and must NOT carry the no-shell note, got:\n%s", sys)
	}
	if sys := systemFor(teamCfg(t), agent.MemberSpec{Name: "reader", Mutating: false}); strings.Contains(sys, untrustedMemberShellNote) {
		t.Errorf("a TRUSTED read-only member must NOT carry the no-shell note, got:\n%s", sys)
	}
}

// TestUntrustedWorkspaceSubagentRunsBashless is the end-to-end degradation proof: a
// real parent engine drives the REAL buildSubagentTool over an untrusted workspace;
// the scripted child attempts Bash, which is simply ABSENT (unknown tool), the child
// loop completes degraded (the parent receives a non-error summary), and no shell
// ever runs anywhere (the external probe file is never written ⇒ no worktree was
// created for it either).
func TestUntrustedWorkspaceSubagentRunsBashless(t *testing.T) {
	cfg := untrustedTeamCfg(t)

	probeDir := t.TempDir()
	probeFile := filepath.Join(probeDir, "probe.txt")
	childProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "p1", Name: "Bash", Args: gitArgs("pwd > " + probeFile)}),
		mockllm.TextTurn("no shell available; proceeding with read-only findings"),
	)

	task, closeFn := taskToolForTest(context.Background(), cfg, childProvider, hookexec.New(nil), regOf(), nil)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}

	// SAME-ARTIFACT tie: the ONE built tool whose child runs Bash-less below must
	// ALSO carry the no-shell Spec note — note-presence ⇔ Bash-absence in a single
	// artifact, so the description can never promise a shell this exact tool lacks
	// (the spec-note and gate tests alone could each pass against two different
	// builds).
	desc := task.Spec().Description
	if strings.Contains(desc, "throwaway worktree") {
		t.Errorf("the SAME Subagent tool whose child runs Bash-less still promises the worktree shell:\n%s", desc)
	}
	for _, want := range []string{"untrusted", "--trust-project"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the SAME Subagent tool whose child runs Bash-less must carry the no-shell note naming %q, got:\n%s", want, desc)
		}
	}

	parentProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "t1", Name: "Subagent", Args: json.RawMessage(`{"prompt":"investigate"}`)}),
		mockllm.TextTurn("parent done"),
	)
	parentWS := osfsWSForTest(t, cfg.Workspace)
	parentCat := tool.NewCatalog()
	parentCat.MustRegister(task)
	parentEng := newChildEngine(cfg, "", parentProvider, parentCat, cfg.Model, promptConfig(cfg, cfg.gitStatus))

	sess := session.New("parent", session.ModeDefault, cfg.Workspace, session.Limits{MaxTurns: 5}, time.Now())
	run := parentEng.Run(context.Background(), sess, parentWS, agent.RunRequest{Text: "go"})
	var sawResult bool
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "t1" {
			sawResult = true
			if ev.ToolResult.IsError {
				t.Fatalf("Subagent must complete degraded, not error: %q", ev.ToolResult.Content)
			}
			if !strings.Contains(ev.ToolResult.Content, "read-only findings") {
				t.Errorf("parent should receive the child's degraded summary, got: %q", ev.ToolResult.Content)
			}
		}
	}
	if !sawResult {
		t.Fatal("parent never observed the Subagent tool result")
	}
	if _, err := os.Stat(probeFile); err == nil {
		t.Fatal("probe file was written; the untrusted child must have NO shell (and so no worktree)")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error stating probe file: %v", err)
	}
}
