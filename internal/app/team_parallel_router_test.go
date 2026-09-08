package app

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// team_parallel_router_test.go covers the composition half of issue #100 (ADR 0034):
// extending the ADR-0031 model router to team members and Parallel branches.
//
//   - buildMemberEngine honours routedModel for an UNDEFINED member and IGNORES it for a
//     DEFINED member (the def pins its own model).
//   - buildParallelEngineFactory mints a contamination-safe routed branch engine
//     (window/compactor/counter re-derived for the routed model, not a clone-and-swap).
//   - End-to-end through the REAL composition Build: a routed member / branch actually
//     RUNS on the classifier-chosen model; OFF is byte-identical.

// ---- buildMemberEngine: routedModel honoured / ignored ---------------------------------

// An UNDEFINED member built with a non-empty routedModel runs on THAT model (not the
// def-less default) — its LLM request carries it and its window is re-derived for it.
func TestMemberEngineHonoursRoutedModelForUndefined(t *testing.T) {
	var (
		mu     sync.Mutex
		models []string
	)
	prov := observedProvider(&models, &mu, mockllm.TextTurn("done"))
	// SubagentModel is set to a DIFFERENT model than the routed one, so the test proves the
	// routed model WINS over the def-less default chain (a regression that re-ran
	// resolveDefaultChildModel would pick SubagentModel and discard the routed id).
	cfg := Config{Model: "parent-model", SubagentModel: "gpt-5-mini"}
	factory := buildMemberEngine(cfg, regForTest(prov, providerAnthropic, cfg.Model), prov, providerAnthropic, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, false, nil, catalogAssets{}, false)

	build := factory(team.New("t"), agent.MemberSpec{Name: "m"}, catAnthropicModel)
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	if got := build.Engine.ContextWindow(); got != catAnthropicCtx {
		t.Fatalf("routed member ContextWindow = %d, want the routed model's catalogued window %d (re-derived, not the default)", got, catAnthropicCtx)
	}
	drainEngine(t, build.Engine)
	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 || models[0] != catAnthropicModel {
		t.Fatalf("routed member LLM request models = %v, want the routed model %q (not SubagentModel)", models, catAnthropicModel)
	}
}

// A DEFINED member IGNORES routedModel: its agent def pins the model, so the router never
// fires for it (the supervisor gate skips defined members) and even a non-empty routedModel
// passed to the factory must not override the def's pinned model.
func TestMemberEngineIgnoresRoutedModelForDefined(t *testing.T) {
	var (
		mu     sync.Mutex
		models []string
	)
	prov := observedProvider(&models, &mu, mockllm.TextTurn("done"))
	def := agents.AgentDef{Name: "specialist", Body: "be a specialist", Model: "gpt-5-mini"}
	cfg := Config{Model: "parent-model"}
	factory := buildMemberEngine(cfg, regForTest(prov, providerAnthropic, cfg.Model), prov, providerAnthropic, cfg.Model,
		hookexec.New(nil), agents.NewRegistry([]agents.AgentDef{def}), nil, nil, nil, false, nil, catalogAssets{}, false)

	// Pass a routedModel that should be IGNORED (a defined member never routes).
	build := factory(team.New("t"), agent.MemberSpec{Name: "specialist", AgentType: "specialist"}, catAnthropicModel)
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	drainEngine(t, build.Engine)
	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 || models[0] != "gpt-5-mini" {
		t.Fatalf("defined member LLM request models = %v, want the def's pinned model gpt-5-mini (routedModel must be ignored)", models)
	}
}

// An empty routedModel (router OFF / miss / zero-caps) leaves the undefined member on the
// def-less default chain — byte-identical to today.
func TestMemberEngineEmptyRoutedModelIsDefault(t *testing.T) {
	var (
		mu     sync.Mutex
		models []string
	)
	prov := observedProvider(&models, &mu, mockllm.TextTurn("done"))
	cfg := Config{Model: "parent-model", SubagentModel: catAnthropicModel}
	factory := buildMemberEngine(cfg, regForTest(prov, providerAnthropic, cfg.Model), prov, providerAnthropic, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, false, nil, catalogAssets{}, false)

	build := factory(team.New("t"), agent.MemberSpec{Name: "m"}, "") // empty routedModel
	drainEngine(t, build.Engine)
	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 || models[0] != catAnthropicModel {
		t.Fatalf("empty-routedModel member models = %v, want the def-less default SubagentModel %q", models, catAnthropicModel)
	}
}

// A MUTATING undefined member also honors routedModel: Mutating is orthogonal to the
// undefined/defined model branch (it only shapes the catalog/runner), so applyMemberRoute
// runs for it too. Directly asserted because the other member-routing tests use a read-only
// (non-mutating) member.
func TestMemberEngineHonoursRoutedModelForMutating(t *testing.T) {
	var (
		mu     sync.Mutex
		models []string
	)
	prov := observedProvider(&models, &mu, mockllm.TextTurn("done"))
	cfg := Config{Model: "parent-model", SubagentModel: "gpt-5-mini"}
	factory := buildMemberEngine(cfg, regForTest(prov, providerAnthropic, cfg.Model), prov, providerAnthropic, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, false, nil, catalogAssets{}, false)

	build := factory(team.New("t"), agent.MemberSpec{Name: "m", Mutating: true}, catAnthropicModel)
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	if got := build.Engine.ContextWindow(); got != catAnthropicCtx {
		t.Fatalf("routed mutating member ContextWindow = %d, want the routed model's window %d", got, catAnthropicCtx)
	}
	drainEngine(t, build.Engine)
	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 || models[0] != catAnthropicModel {
		t.Fatalf("routed mutating member models = %v, want the routed model %q", models, catAnthropicModel)
	}
}

// ---- buildParallelEngineFactory: contamination-safe routed branch engine ---------------

// The factory mints a routed branch engine on the routed model with its window re-derived
// (the contamination-safe path), NOT a clone-and-swap. A blank model is unroutable.
func TestParallelEngineFactoryRoutesContaminationSafe(t *testing.T) {
	var (
		mu     sync.Mutex
		models []string
	)
	prov := observedProvider(&models, &mu, mockllm.TextTurn("done"))
	cfg := Config{Model: "parent-model", SubagentModel: "gpt-5-mini"}
	factory := buildParallelEngineFactory(cfg, regForTest(prov, providerAnthropic, cfg.Model), prov, providerAnthropic, cfg.Model, nil)

	// Blank model: unroutable.
	if eng, ok := factory(""); ok || eng != nil {
		t.Fatalf("a blank model must be unroutable; got (%v, %v)", eng, ok)
	}

	eng, ok := factory(catAnthropicModel)
	if !ok || eng == nil {
		t.Fatal("a non-blank model must mint a branch engine")
	}
	if got := eng.ContextWindow(); got != catAnthropicCtx {
		t.Fatalf("routed branch ContextWindow = %d, want the routed model's catalogued window %d (re-derived)", got, catAnthropicCtx)
	}
	drainEngine(t, eng)
	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 || models[0] != catAnthropicModel {
		t.Fatalf("routed branch LLM request models = %v, want the routed model %q (not SubagentModel)", models, catAnthropicModel)
	}
}

// The factory's returned closure satisfies the engine-layer type the option takes
// (func(string)(*Engine,bool)) — a compile-time-shaped guard that the signatures line up.
func TestParallelEngineFactorySatisfiesOptionShape(_ *testing.T) {
	cfg := Config{Model: "m"}
	prov := mockllm.New()
	f := buildParallelEngineFactory(cfg, regForTest(prov, providerMock, cfg.Model), prov, providerMock, cfg.Model, nil)
	// WithParallelEngineFactory accepts exactly this shape (compile-time assertion).
	_ = agent.WithParallelEngineFactory(f)
}

// ---- routingProvider: a deterministic content-routing port.LLMProvider for the e2e ------

// routingProvider is a deterministic, concurrency-safe port.LLMProvider for the team /
// parallel routing e2e. Unlike the shared-cursor mockllm (fragile under the concurrent
// member/branch turns), it routes on REQUEST CONTENT:
//
//   - a CLASSIFIER request (the buildModelRoutePrompt marker) → a category verdict picked
//     by a keyword in the fenced task ("alpha"→large, else small);
//   - the PARENT's first request (the Team/Parallel tool is in its catalog and no tool
//     result is present yet) → the scripted parentCall tool call;
//   - any other request (a member/branch turn, synthesis, or the parent's final turn) →
//     a short text reply.
//
// It records every (model) seen so the test can assert which model each routed child ran
// on. Concurrency-safe: the only mutable state is the recorded slice under a mutex.
type routingProvider struct {
	mu         sync.Mutex
	models     []string
	parentTool string           // "Team" or "Parallel"
	parentCall session.ToolCall // the tool call the parent emits on its first turn
	parentDone bool             // guards the one-shot parent tool call
}

func (*routingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *routingProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.mu.Lock()
	p.models = append(p.models, req.Model)
	p.mu.Unlock()

	var chunks []port.Chunk
	switch {
	case isClassifierRequest(req):
		cat := "small"
		if strings.Contains(strings.ToLower(lastUserText(req)), "alpha") {
			cat = "large"
		}
		chunks = []port.Chunk{
			mockllm.TextChunk(`{"category":"` + cat + `"}`),
			mockllm.DoneChunk(session.StopEndTurn),
		}
	case p.isParentFirstTurn(req):
		chunks = []port.Chunk{
			mockllm.ToolCallChunk(p.parentCall),
			mockllm.DoneChunk(session.StopEndTurn),
		}
	default:
		chunks = []port.Chunk{
			mockllm.TextChunk("ok"),
			mockllm.DoneChunk(session.StopEndTurn),
		}
	}
	return func(yield func(port.Chunk, error) bool) {
		for _, c := range chunks {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !yield(c, nil) {
				return
			}
		}
	}, nil
}

// isParentFirstTurn reports whether req is the parent's first turn (the parent tool is in
// its catalog, and the one-shot tool call has not yet been emitted). One-shot: it flips
// parentDone so the parent's SUBSEQUENT turn (after the tool result) falls through to text.
func (p *routingProvider) isParentFirstTurn(req port.LLMRequest) bool {
	hasParentTool := false
	for _, ts := range req.Tools {
		if ts.Name == p.parentTool {
			hasParentTool = true
			break
		}
	}
	if !hasParentTool {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.parentDone {
		return false
	}
	p.parentDone = true
	return true
}

func (p *routingProvider) recordedModels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.models))
	copy(out, p.models)
	return out
}

// isClassifierRequest detects the model-router classifier turn by the buildModelRoutePrompt
// marker (operator-authored, stable). The classifier is a one-turn tool-less engine.
func isClassifierRequest(req port.LLMRequest) bool {
	// The classifier prompt (buildModelRoutePrompt) rides the user PROMPT, not the system
	// prompt — RunModelRouter passes it as the run's content. Match either to be robust.
	return strings.Contains(req.System.Render(), "automated task router") ||
		strings.Contains(lastUserText(req), "automated task router")
}

// lastUserText returns the last user message's text (the classifier's fenced task rides
// here), for the keyword-based category pick.
func lastUserText(req port.LLMRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == session.RoleUser {
			return req.Messages[i].Text
		}
	}
	return ""
}

// routerE2ECfg is the shared router-on Build config for the e2e tests.
func routerE2ECfg(workspace string, parentCtor func() port.LLMProvider, enable func(*Config)) Config {
	cfg := Config{
		Workspace: workspace,
		NoSoul:    true,
		Model:     "gpt-5",
		// ADR 0042: the taxonomy is the enable — no flag needed to turn the router on.
		RouterCategories: []permconfig.RouterCategory{
			{Name: "small", Description: "trivial mechanical tasks", Model: routerSmall},
			{Name: "large", Description: "deep reasoning and architecture", Model: routerLarge},
		},
		RouterDefaultCategory: "small",
		AllowAllTools:         true,
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor:   func(_ Config, _, _, _ string) port.LLMProvider { return parentCtor() },
	}
	enable(&cfg)
	return cfg
}

// END-TO-END (Team): two UNDEFINED members steer to different categories (the lead's role
// mentions "alpha" → large; the worker's does not → small), so each member's engine runs
// on its routed model. The parent forms the team, the team runs, synthesis completes.
func TestTeamRoutesMembersToCategoryModelsE2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	teamCall := session.NewToolCall("c1", "Team", []byte(`{"goal":"do work","members":[`+
		`{"name":"lead","role":"alpha: architect the deep redesign"},`+
		`{"name":"worker","role":"rename a variable"}]}`))
	prov := &routingProvider{parentTool: "Team", parentCall: teamCall}

	built, err := Build(ctx, routerE2ECfg(workspace, func() port.LLMProvider { return prov },
		func(c *Config) { c.EnableTeams = true }))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	drainRun(run) // the team must complete without wedging

	models := prov.recordedModels()
	// The lead (role mentions alpha → large) must have run on routerLarge; the worker
	// (→ small) on routerSmall. Assert BOTH routed models appear among the member turns.
	var sawLarge, sawSmall bool
	for _, m := range models {
		switch m {
		case routerLarge:
			sawLarge = true
		case routerSmall:
			sawSmall = true
		}
	}
	if !sawLarge {
		t.Fatalf("no member ran on the large-category model %q; models=%v", routerLarge, models)
	}
	if !sawSmall {
		t.Fatalf("no member ran on the small-category model %q; models=%v", routerSmall, models)
	}
}

// END-TO-END byte-identical-when-OFF (Team): with the router OFF, the SAME team script runs
// with NO classifier turn and every request is the session model — no routed model appears.
func TestTeamRouterOffByteIdenticalE2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	teamCall := session.NewToolCall("c1", "Team", []byte(`{"goal":"do work","members":[`+
		`{"name":"lead","role":"alpha: architect"},`+
		`{"name":"worker","role":"rename"}]}`))
	prov := &routingProvider{parentTool: "Team", parentCall: teamCall}

	cfg := routerE2ECfg(workspace, func() port.LLMProvider { return prov },
		func(c *Config) { c.EnableTeams = true })
	cfg.RouterDisabled = true // OFF via the ADR 0042 kill-switch
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	drainRun(run)

	for _, m := range prov.recordedModels() {
		if m != "gpt-5" {
			t.Fatalf("a request carried %q with the router OFF; every request must be the session model gpt-5 (models=%v)", m, prov.recordedModels())
		}
	}
}

// END-TO-END (Parallel): two branches steer to different categories (the branch task
// mentioning "alpha" → large, the other → small), so each branch's child runs on its routed
// model; the join completes.
func TestParallelRoutesBranchesToCategoryModelsE2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	parCall := session.NewToolCall("c1", "Parallel", []byte(`{"tasks":[`+
		`"alpha: design the deep refactor",`+
		`"fix a typo"]}`))
	prov := &routingProvider{parentTool: "Parallel", parentCall: parCall}

	built, err := Build(ctx, routerE2ECfg(workspace, func() port.LLMProvider { return prov },
		func(c *Config) { c.EnableParallel = true }))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	drainRun(run)

	models := prov.recordedModels()
	var sawLarge, sawSmall bool
	for _, m := range models {
		switch m {
		case routerLarge:
			sawLarge = true
		case routerSmall:
			sawSmall = true
		}
	}
	if !sawLarge {
		t.Fatalf("no branch ran on the large-category model %q; models=%v", routerLarge, models)
	}
	if !sawSmall {
		t.Fatalf("no branch ran on the small-category model %q; models=%v", routerSmall, models)
	}
}

// TestBuiltCloseReapsPreservedParallelWinner proves the composition-owned reaper
// follows Built's graceful lifecycle, rather than the tool's per-call lifecycle.
func TestBuiltCloseReapsPreservedParallelWinner(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	forkBase := t.TempDir()
	t.Setenv("TMPDIR", forkBase)
	parallelCall := session.NewToolCall("c1", "Parallel", []byte(`{"tasks":["one","two"],"join":"first"}`))
	prov := &routingProvider{parentTool: "Parallel", parentCall: parallelCall}
	built, err := Build(ctx, routerE2ECfg(workspace, func() port.LLMProvider { return prov },
		func(c *Config) { c.EnableParallel = true }))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(built.Close)

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	drainRun(run)

	stored, err := built.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	var result string
	for _, message := range stored.Conversation.Messages {
		if message.Role == session.RoleTool && message.ToolResult != nil && message.ToolResult.CallID == "c1" {
			result = message.ToolResult.Content
			break
		}
	}
	if !strings.Contains(result, "winner artifact (PRESERVED):") {
		t.Fatalf("Parallel result did not report an opaque winner artifact: %q", result)
	}
	if strings.Contains(result, forkBase) {
		t.Fatalf("Parallel result leaked the private fork root: %q", result)
	}

	entries, err := os.ReadDir(forkBase)
	if err != nil {
		t.Fatalf("read fork base: %v", err)
	}
	var roots []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "mecatlfork-") {
			roots = append(roots, filepath.Join(forkBase, entry.Name()))
		}
	}
	if len(roots) != 1 {
		t.Fatalf("preserved winner roots before shutdown = %v, want exactly one", roots)
	}
	root := roots[0]

	built.Close()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("preserved winner workspace remains after Built.Close: %v", err)
	}
}

// END-TO-END byte-identical-when-OFF (Parallel): router OFF ⇒ no classifier turn, every
// request on the session model.
func TestParallelRouterOffByteIdenticalE2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	parCall := session.NewToolCall("c1", "Parallel", []byte(`{"tasks":["alpha task","plain task"]}`))
	prov := &routingProvider{parentTool: "Parallel", parentCall: parCall}

	cfg := routerE2ECfg(workspace, func() port.LLMProvider { return prov },
		func(c *Config) { c.EnableParallel = true })
	cfg.RouterDisabled = true // OFF via the ADR 0042 kill-switch
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	drainRun(run)

	for _, m := range prov.recordedModels() {
		if m != "gpt-5" {
			t.Fatalf("a request carried %q with the Parallel router OFF; want only gpt-5 (models=%v)", m, prov.recordedModels())
		}
	}
}
