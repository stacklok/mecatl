package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// memory_posture_test.go pins the memory self-description affordance (ADR 0070,
// the model-visible-affordance rule in AGENTS.md): answering "what do you
// remember about X?" depends on the model CALLING the memory tools, and the
// memory→docs→repo escalation depends on it knowing the ladder exists. Both are
// model behaviour, so both need a Role-layer instruction AND a test proving the
// instruction lands in the BUILT engine's system prompt via the REAL factory
// path — deleting the applyMemoryPosture wiring must fail CI.

// memoryPostureCatalog builds a catalog carrying the requested families, using
// the SAME registration entry points composition uses (registerMemoryFamilies).
func memoryPostureCatalog(t *testing.T, project, user, grep bool) *tool.Catalog {
	t.Helper()
	cat := tool.NewCatalog()
	if project {
		if err := memory.Register(cat, memmemory.New()); err != nil {
			t.Fatalf("memory.Register: %v", err)
		}
	}
	if user {
		if err := memory.RegisterUserModel(cat, memmemory.New()); err != nil {
			t.Fatalf("memory.RegisterUserModel: %v", err)
		}
	}
	if grep {
		// A stand-in registered under the real name: applyMemoryPosture's gate
		// is the tool NAME in the catalog, exactly as the escalation ladder's
		// rung 2/3 reachability is.
		cat.MustRegister(stubTool{name: "Grep"})
	}
	return cat
}

// stubTool is a minimal read-only tool used only to occupy a catalog name.
type stubTool struct{ name string }

func (s stubTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: s.name, Description: "stub", Schema: []byte(`{"type":"object"}`)}
}
func (s stubTool) ReadOnly() bool { return true }
func (s stubTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(in.ID, ""), nil
}

// TestApplyMemoryPostureClauseGates proves each clause mirrors the catalog
// registration: no memory family at all is a no-op (the model is never told
// about a store it cannot read), a single-scope session is told about only that
// scope, and the docs/repo escalation rungs appear only when workspace search
// exists (a "no-fs" profile has no Grep, so rungs 2 and 3 are unreachable).
func TestApplyMemoryPostureClauseGates(t *testing.T) {
	t.Parallel()

	// Nil catalog and a catalog with no memory family: both no-ops.
	if got := applyMemoryPosture(prompt.Config{Role: "just-role"}, nil); got.Role != "just-role" {
		t.Fatalf("applyMemoryPosture(nil catalog) must be a no-op; got Role=%q", got.Role)
	}
	none := applyMemoryPosture(prompt.Config{Role: "just-role"}, memoryPostureCatalog(t, false, false, true))
	if none.Role != "just-role" {
		t.Fatalf("a session with NO memory family must not carry the note; got Role=%q", none.Role)
	}

	// Empty Role falls back to DefaultRole (the applyNoFSPosture idiom).
	both := applyMemoryPosture(prompt.Config{}, memoryPostureCatalog(t, true, true, true))
	if both.Role == "" {
		t.Fatal("applyMemoryPosture on an empty Config must fall back to DefaultRole")
	}
	for _, clause := range []string{memoryPostureLead, memoryPostureProject, memoryPostureUser, memoryPostureEscalation} {
		if !strings.Contains(both.Role, clause) {
			t.Errorf("Role missing a clause with both families + Grep wired\nmissing=%q", clause)
		}
	}

	// An explicit Role is preserved; the note is appended.
	custom := applyMemoryPosture(prompt.Config{Role: "custom-role"}, memoryPostureCatalog(t, true, true, true))
	if !strings.HasPrefix(custom.Role, "custom-role") {
		t.Fatalf("explicit Role was not preserved; got %q", custom.Role)
	}

	// Project-only: the user clause must be absent.
	projectOnly := applyMemoryPosture(prompt.Config{Role: "r"}, memoryPostureCatalog(t, true, false, true))
	if !strings.Contains(projectOnly.Role, memoryPostureProject) {
		t.Error("project-only session missing the project clause")
	}
	if strings.Contains(projectOnly.Role, memoryPostureUser) {
		t.Error("project-only session carries the USER clause — it has no user-model store to read")
	}

	// User-only (a mecated with no --memory-dir: user memory is on by default).
	userOnly := applyMemoryPosture(prompt.Config{Role: "r"}, memoryPostureCatalog(t, false, true, true))
	if !strings.Contains(userOnly.Role, memoryPostureUser) {
		t.Error("user-only session missing the user clause")
	}
	if strings.Contains(userOnly.Role, memoryPostureProject) {
		t.Error("user-only session carries the PROJECT clause — it has no project store to read")
	}

	// No Grep (the no-fs profile): the docs/repo rungs must not be advertised.
	noSearch := applyMemoryPosture(prompt.Config{Role: "r"}, memoryPostureCatalog(t, true, true, false))
	if !strings.Contains(noSearch.Role, memoryPostureLead) {
		t.Error("a search-less session must still get the memory self-description")
	}
	if strings.Contains(noSearch.Role, memoryPostureEscalation) {
		t.Error("a session with no Grep carries the docs/repo escalation ladder it cannot walk")
	}
}

// TestMemoryPostureLandsInBuiltEngineSystemPrompt is the ADR-0070 gate: the
// instruction must reach the model through the REAL sessionEngineFactory path,
// not the helper in isolation. Asserted on the StablePrefix (the Role layer
// applyMemoryPosture owns) rather than the combined Render(): the memory tools'
// own Spec descriptions also ride the rendered prompt, so a Render() oracle
// would stay green if the factory wiring were deleted.
func TestMemoryPostureLandsInBuiltEngineSystemPrompt(t *testing.T) {
	ctx := context.Background()
	const sessionModel = "gpt-5"

	capture := func(assets catalogAssets) prompt.Layered {
		t.Helper()
		var captured prompt.Layered
		var invoked bool
		provider := mockllm.NewWith([]mockllm.Option{
			mockllm.WithRequestObserver(func(req port.LLMRequest) {
				captured = req.System
				invoked = true
			}),
		}, mockllm.TextTurn("ok"))
		cfg := Config{Model: sessionModel}
		reg := regForTest(provider, providerOpenAI, sessionModel)
		factory := sessionEngineFactory(cfg, reg, provider, memstore.New(),
			permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
			prompt.RootAssembler{}, assets, nil)
		res, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer func() { _ = res.Close() }()

		sess := session.New("s1", session.ModeDefault,
			session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
			session.Limits{MaxTurns: 1}, time.Now())
		run := res.Engine.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi", Parts: nil})
		for range run.Events() {
		}
		if !invoked {
			t.Fatal("the LLM was not invoked; the mock script may be insufficient")
		}
		return captured
	}

	withMemory := capture(catalogAssets{memStore: memmemory.New(), userModelStore: memmemory.New()})
	for _, clause := range []string{memoryPostureLead, memoryPostureProject, memoryPostureUser, memoryPostureEscalation} {
		if !strings.Contains(withMemory.StablePrefix, clause) {
			t.Errorf("StablePrefix missing a memory posture clause (the Role-layer applyMemoryPosture wiring)\nmissing=%q\ngot StablePrefix (first 900):\n%s",
				clause, firstN(withMemory.StablePrefix, 900))
		}
	}

	// The honest-absence half: no memory store wired ⇒ no memory tools ⇒ no
	// instruction about a memory the session does not have.
	withoutMemory := capture(catalogAssets{})
	if strings.Contains(withoutMemory.StablePrefix, memoryPostureLead) {
		t.Fatal("a session with NO memory store claims a durable memory in its StablePrefix")
	}
}

// TestMemoryPostureLandsOnSharedEngine pins the SECOND wiring site: a
// default-profile, zero-selector session on the launch-root workspace rides the
// SHARED engine (sessionNeedsPerFactory false — the plain mecatui launch), whose
// deps are assembled in buildEngine, not sessionEngineFactory. Without the
// applyMemoryPosture call there the commonest deployment would never see the
// instruction. Driven through a full app.Build over a scripted provider.
func TestMemoryPostureLandsOnSharedEngine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var captured prompt.Layered
	var invoked bool
	llm := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) {
			captured = req.System
			invoked = true
		}),
	}, mockllm.TextTurn("ok"))

	// NoUserModel is what actually reaches the shared engine: a resolvable
	// user-model dir builds a learned-skill repository, and Service.createSession
	// routes EVERY session through the per-session factory when
	// cfg.LearnedSkills != nil. Without it this test would silently assert the
	// factory site a second time. The cost is that this deployment has only
	// PROJECT memory, so the user clause must be absent — which is itself the
	// per-clause honesty gate, exercised here end-to-end.
	built, err := Build(ctx, Config{
		Workspace:    t.TempDir(),
		Model:        "mock",
		StoreDir:     t.TempDir(),
		MemoryDir:    t.TempDir(),
		NoUserModel:  true,
		MockProvider: llm,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(built.Close)

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRunContent(ctx, sess.ID, "hello", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	for range run.Events() {
	}
	if !invoked {
		t.Fatal("the LLM was not invoked; the mock script may be insufficient")
	}
	for _, clause := range []string{memoryPostureLead, memoryPostureProject, memoryPostureEscalation} {
		if !strings.Contains(captured.StablePrefix, clause) {
			t.Errorf("the SHARED engine's StablePrefix is missing a memory posture clause — applyMemoryPosture must run on the shared engine's deps in buildEngine (ADR 0070)\nmissing=%q", clause)
		}
	}
	if strings.Contains(captured.StablePrefix, memoryPostureUser) {
		t.Error("a --no-user-model deployment claims a USER memory it has no store for")
	}
}
