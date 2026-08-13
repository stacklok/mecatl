package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// shell_less_posture_test.go covers the ADR-0070 model-visible affordance for the
// shell-less default-FS posture, truthed per-request against the LIVE
// tool.Environment in engine/agent.buildRequest (issue #462 review): when the
// Environment handed to Run has no CommandRunner (env.CommandRunner() == nil) and
// is not the no-FS profile, buildRequest DROPS the Bash spec from the advertised
// tools and appends ONE shell-less posture clause to the per-request system
// prompt's VOLATILE suffix. This is the capability-truth point: the shared
// engine's catalog/prompt are built once from server config and may advertise
// Bash a per-run Environment override (ACP/editor, --no-bash, a runner that could
// not be built) cannot serve. An ACP override with a shell-less Environment and a
// shared engine that HAS Bash converges here, independent of the shared Engine's
// catalog — so the model is told "NO shell" and a stale/hallucinated Bash call
// bounces off dispatch as an honest unknown-tool error.

// factoryForShellTest builds a per-session engine through the REAL
// sessionEngineFactory and returns the engine + the factory result (for Close).
// The cfg's Shell/Workspace control whether the catalog registers Bash; the
// Environment handed to Run is the capability truth. policy defaults to
// defaultRules() (Ask on Bash); pass an allow-all policy for tests that drive a
// stale Bash call all the way to the no-shell tool error without an interactive
// router to answer the ask.
func factoryForShellTest(t *testing.T, cfg Config, provider port.LLMProvider, policy port.PermissionPolicy) (server.SessionEngineResult, *agent.Engine) {
	t.Helper()
	if policy == nil {
		policy = permpolicy.NewPolicy(defaultRules(), nil)
	}
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	factory := sessionEngineFactory(cfg, reg, provider, memstore.New(),
		policy, hookexec.New(nil), nil,
		prompt.RootAssembler{}, catalogAssets{}, nil)
	ctx := context.Background()
	res, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	return res, res.Engine
}

// hasBashSpec reports whether the advertised tool specs include the Bash tool.
func hasBashSpec(tools []tool.ToolSpec) bool {
	for _, ts := range tools {
		if ts.Name == tool.BashToolName {
			return true
		}
	}
	return false
}

// TestACPShellLessEnvironmentDropsBashAndDocumentsPosture is the KEY ACP finding:
// a shared engine built from a SHELL-BEARING server config (the catalog HAS Bash,
// registered via registerCoreTools because buildCommandRunner(cfg) != nil) is run
// against a SHELL-LESS Environment (env.CommandRunner() == nil — the ACP/editor
// override shape). buildRequest must, for that request: (a) DROP the Bash spec
// from the advertised tools, and (b) append the shell-less posture clause to the
// per-request system prompt's VOLATILE suffix (NOT the cache-stable prefix —
// environment capability is per-run/per-turn). It must NOT duplicate the no-FS
// wording (this is a default-FS profile, so noFSPostureNote is absent).
func TestACPShellLessEnvironmentDropsBashAndDocumentsPosture(t *testing.T) {
	const sessionModel = "gpt-5"
	cfg := Config{Model: sessionModel, Shell: "/bin/sh", Workspace: t.TempDir()}
	var captured port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) { captured = req }),
	}, mockllm.TextTurn("ok"))

	res, eng := factoryForShellTest(t, cfg, provider, nil)
	defer func() { _ = res.Close() }()

	// The factory-built engine's catalog DOES carry Bash (shell-bearing cfg).
	if !eng.HasTool(tool.BashToolName) {
		t.Fatalf("precondition: the shell-bearing engine's catalog must carry Bash")
	}

	// Run against a SHELL-LESS Environment (nil runner) — the ACP override shape.
	sess := session.New("acp", session.ModeDefault, cfg.Workspace, session.Limits{MaxTurns: 1}, time.Now())
	shellLessEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: cfg.Workspace},
		memfs.NewWorkspace(cfg.Workspace), nil)
	run := eng.Run(context.Background(), sess, shellLessEnv, agent.RunRequest{Text: "run a build"})
	for range run.Events() {
	}

	// (a) the outgoing request advertises NO Bash spec.
	if hasBashSpec(captured.Tools) {
		t.Errorf("a shell-less Environment must drop the Bash spec from the advertised tools; got Bash in %d specs",
			len(captured.Tools))
	}
	// (b) the shell-less posture clause is on the VOLATILE suffix, not the prefix.
	for _, clause := range []string{"NO shell", "Bash tool is not available", "Do not attempt to run commands"} {
		if !strings.Contains(captured.System.VolatileSuffix, clause) {
			t.Errorf("shell-less VolatileSuffix missing clause %q\ngot suffix:\n%s",
				clause, firstN(captured.System.VolatileSuffix, 500))
		}
		if strings.Contains(captured.System.StablePrefix, "NO shell") {
			t.Errorf("shell-less clause must NOT be baked into the cache-stable StablePrefix " +
				"(environment capability is per-run/per-turn)")
		}
	}
	// (c) the no-FS note must NOT also appear (this is default-FS, not no-FS) — no
	// duplicate contradictory clauses.
	if strings.Contains(captured.System.StablePrefix, "NO filesystem") || strings.Contains(captured.System.VolatileSuffix, "NO filesystem") {
		t.Errorf("a default-FS shell-less request must NOT carry the no-FS note (would duplicate/contradict)")
	}
}

// TestACPShellLessEnvironmentStaleBashCallIsHonestToolError proves the BEHAVIOR
// half: on a shell-less Environment with a shared engine whose catalog DOES carry
// Bash, a stale/hallucinated Bash call (the model was told "NO shell" and the
// spec was dropped, but it calls Bash anyway) is NOT a silent pass and NOT a
// hang: the catalog still resolves it (Bash is only dropped from ADVERTISED
// specs, not the catalog), it runs against the nil-runner Environment, and Bash
// surfaces the honest "[command failed to run: no shell available]" tool error —
// the SAME byte-identical composer every nil-runner site uses (bashNoShellResult).
// An allow-all policy is wired so the call reaches Bash.Execute without an
// interactive permission ask (which has no router to answer it here).
func TestACPShellLessEnvironmentStaleBashCallIsHonestToolError(t *testing.T) {
	const sessionModel = "gpt-5"
	cfg := Config{Model: sessionModel, Shell: "/bin/sh", Workspace: t.TempDir()}
	provider := mockllm.NewWith([]mockllm.Option{},
		mockllm.ToolCallTurn(session.ToolCall{ID: "b1", Name: "Bash", Args: []byte(`{"command":"echo hi"}`)}),
		mockllm.TextTurn("no shell; using file tools instead"),
	)

	res, eng := factoryForShellTest(t, cfg, provider, permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil))
	defer func() { _ = res.Close() }()

	if !eng.HasTool(tool.BashToolName) {
		t.Fatalf("precondition: the shell-bearing engine's catalog must carry Bash")
	}
	sess := session.New("acp2", session.ModeDefault, cfg.Workspace, session.Limits{MaxTurns: 3}, time.Now())
	shellLessEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: cfg.Workspace},
		memfs.NewWorkspace(cfg.Workspace), nil)
	run := eng.Run(context.Background(), sess, shellLessEnv, agent.RunRequest{Text: "run echo hi"})
	var sawNoShell bool
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "b1" {
			if ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "no shell available") {
				sawNoShell = true
			}
		}
	}
	if !sawNoShell {
		t.Error("a stale Bash call on a shell-less Environment must surface the honest " +
			"'no shell available' tool error (Bash resolves from the catalog but the nil-runner " +
			"Environment has no shell), not a silent pass or a hang")
	}
}

// TestShellBearingEnvironmentAdvertisesBashAndLacksNote proves the positive case:
// a shell-bearing Environment (env.CommandRunner() != nil) still advertises Bash
// and does NOT carry the shell-less posture clause. The capability truth is the
// LIVE Environment, not the shared engine config, so a shell-bearing override
// over a shell-less shared engine would also advertise Bash — but the simplest
// real path is a shell-bearing cfg + a shell-bearing Environment.
func TestShellBearingEnvironmentAdvertisesBashAndLacksNote(t *testing.T) {
	const sessionModel = "gpt-5"
	cfg := Config{Model: sessionModel, Shell: "/bin/sh", Workspace: t.TempDir()}
	var captured port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) { captured = req }),
	}, mockllm.TextTurn("ok"))

	res, eng := factoryForShellTest(t, cfg, provider, nil)
	defer func() { _ = res.Close() }()

	// A shell-bearing Environment: the main runner from buildCommandRunner.
	runner := buildCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: buildCommandRunner must return a non-nil runner for a shell-bearing cfg")
	}
	shellEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: cfg.Workspace},
		memfs.NewWorkspace(cfg.Workspace), runner)
	sess := session.New("sh", session.ModeDefault, cfg.Workspace, session.Limits{MaxTurns: 1}, time.Now())
	run := eng.Run(context.Background(), sess, shellEnv, agent.RunRequest{Text: "hello"})
	for range run.Events() {
	}

	if !hasBashSpec(captured.Tools) {
		t.Error("a shell-bearing Environment must still advertise the Bash spec")
	}
	for _, layer := range []string{captured.System.StablePrefix, captured.System.VolatileSuffix} {
		if strings.Contains(layer, "NO shell") {
			t.Errorf("a shell-bearing Environment must NOT carry the shell-less posture clause; got:\n%s",
				firstN(layer, 500))
		}
	}
}

// TestNoBashDeploymentDocumentsShellLessOnVolatileSuffix proves the --no-bash
// deployment path converges at the SAME buildRequest choke point: a NoBash=true
// engine (catalog has no Bash) run against a shell-less Environment drops the
// (already absent) Bash spec and appends the shell-less clause to the VOLATILE
// suffix — NOT the StablePrefix (the old composition wiring baked it into the Role;
// the per-request choke point moves it to the volatile suffix honestly).
func TestNoBashDeploymentDocumentsShellLessOnVolatileSuffix(t *testing.T) {
	const sessionModel = "gpt-5"
	cfg := Config{Model: sessionModel, NoBash: true}
	var captured port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) { captured = req }),
	}, mockllm.TextTurn("ok"))

	res, eng := factoryForShellTest(t, cfg, provider, nil)
	defer func() { _ = res.Close() }()

	if eng.HasTool(tool.BashToolName) {
		t.Fatal("precondition: a NoBash engine's catalog must NOT carry Bash")
	}
	sess := session.New("nobash", session.ModeDefault, "/ws", session.Limits{MaxTurns: 1}, time.Now())
	run := eng.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "run a build"})
	for range run.Events() {
	}

	if hasBashSpec(captured.Tools) {
		t.Error("a NoBash deployment must not advertise the Bash spec")
	}
	if !strings.Contains(captured.System.VolatileSuffix, "NO shell") {
		t.Errorf("NoBash shell-less clause must be on the VolatileSuffix\ngot suffix:\n%s",
			firstN(captured.System.VolatileSuffix, 500))
	}
	if strings.Contains(captured.System.StablePrefix, "NO shell") {
		t.Errorf("the shell-less clause must NOT be baked into the StablePrefix (per-request, not cache-stable)")
	}
}

// TestNoFSProfileDoesNotDuplicateShellLessClause proves the no-FS profile keeps
// its OWN noFSPostureNote (baked into the StablePrefix by composition) and does
// NOT also get the per-request shell-less clause (which is WITHHELD for the no-FS
// kind): no duplicate/contradictory clauses. The no-FS catalog already omits Bash.
func TestNoFSProfileDoesNotDuplicateShellLessClause(t *testing.T) {
	const sessionModel = "gpt-5"
	cfg := Config{Model: sessionModel, NoBash: true} // shell-less regardless
	var captured port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) { captured = req }),
	}, mockllm.TextTurn("ok"))

	reg := regForTest(provider, providerOpenAI, cfg.Model)
	factory := sessionEngineFactory(cfg, reg, provider, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, catalogAssets{}, nil)
	ctx := context.Background()
	res, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileNoFS, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()

	sess := session.New("nofs", session.ModeDefault, "", session.Limits{MaxTurns: 1}, time.Now())
	// A no-FS Environment: the nofs ref + nofs workspace + nil runner is what the
	// service installs (buildSessionEnvironment / SetSessionEnvironment). This is
	// the capability truth buildRequest reads: env.Ref().Kind == EnvKindNoFS ⇒
	// the shell-less clause is WITHHELD even though env.CommandRunner() == nil.
	noFSEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: ""}, nofs.New(), nil)
	run := res.Engine.Run(ctx, sess, noFSEnv, agent.RunRequest{Text: "hello"})
	for range run.Events() {
	}

	// The no-FS note (composition, StablePrefix) is present.
	if !strings.Contains(captured.System.StablePrefix, "NO filesystem") {
		t.Errorf("no-FS StablePrefix must carry the noFSPostureNote\ngot prefix:\n%s",
			firstN(captured.System.StablePrefix, 500))
	}
	// The per-request shell-less clause is WITHHELD (would duplicate "no shell").
	if strings.Contains(captured.System.VolatileSuffix, "NO shell") {
		t.Errorf("no-FS must NOT also get the per-request shell-less clause (noFSPostureNote already says no shell)\ngot suffix:\n%s",
			firstN(captured.System.VolatileSuffix, 500))
	}
	if hasBashSpec(captured.Tools) {
		t.Error("a no-FS profile must not advertise the Bash spec")
	}
}
