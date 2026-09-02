package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/dream"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func fixedDefaultWindow() int { return defaultContextWindowTokens }

// depsTestFixture builds the shared (non-provider) collaborators the two
// Deps-constructing paths consume, so a test can compare baseEngineDeps against
// engineDepsForProvider on equal footing. All offline (mockllm/memstore).
func depsTestFixture(t *testing.T) (
	provider port.LLMProvider,
	store port.SessionStore,
	policy port.PermissionPolicy,
	hooks port.HookRunner,
	mcpProvider mcp.Provider,
	instructions prompt.InstructionAssembler,
) {
	t.Helper()
	provider = mockllm.New(mockllm.TextTurn("ok"))
	store = memstore.New()
	policy = permpolicy.NewPolicy(defaultRules(), nil)
	return provider, store, policy, nil, nil, prompt.RootAssembler{}
}

// TestBaseEngineDepsDelegatesToProviderSeam: baseEngineDeps and
// engineDepsForProvider with the SAME provider+model produce IDENTICAL
// provider-closing fields. They are the same enumeration point; if they drift, a
// per-session engine (S3) could silently bind the wrong provider/model. (This is the
// drift guard the [High] review and the no-per-session-engine memory call for,
// extended to the provider axis.)
//
// The TokenCounter is now derived INSIDE engineDepsForProvider (panel finding #1), so
// the two paths build their own counters: for the SAME model they are semantically
// equivalent (same encoding ⇒ same counts), which the tiktoken assertion below
// verifies by behaviour rather than by pointer identity.
func TestBaseEngineDepsDelegatesToProviderSeam(t *testing.T) {
	cfg := Config{Model: "gpt-5", Compaction: "cascade", Tokenizer: "tiktoken", Workspace: "/repo"}
	provider, store, policy, hooks, mcpP, instr := depsTestFixture(t)

	// baseEngineDeps now resolves the default model's REAL window via reg.meta
	// (issue #63), so the direct comparison must pass the SAME resolved window —
	// not the old hardcoded 0. providerOpenAI + "gpt-5" is catalogued (400k), so a
	// nil-meta test registry still resolves it via the catalog floor.
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	windowFn := reg.windowResolver(cfg, reg.Default(), cfg.Model)
	base := baseEngineDeps(cfg, reg, provider, store, policy, hooks, mcpP, instr)
	direct := engineDepsForProvider(cfg, provider, cfg.Model, windowFn, store, policy, hooks, mcpP, instr)

	if base.Model != direct.Model {
		t.Errorf("Model: base=%q direct=%q", base.Model, direct.Model)
	}
	if base.LLM != direct.LLM {
		t.Error("LLM provider differs between baseEngineDeps and engineDepsForProvider")
	}
	if base.PromptConfig.Env.Model != direct.PromptConfig.Env.Model {
		t.Errorf("PromptConfig.Env.Model: base=%q direct=%q",
			base.PromptConfig.Env.Model, direct.PromptConfig.Env.Model)
	}
	if base.PromptConfig.Role != direct.PromptConfig.Role {
		t.Error("PromptConfig.Role (agency delta) differs")
	}
	if bw, dw := base.ContextWindow(), direct.ContextWindow(); bw != dw {
		t.Errorf("ContextWindow: base=%d direct=%d", bw, dw)
	}
	if base.CompactionRatio != direct.CompactionRatio {
		t.Errorf("CompactionRatio: base=%v direct=%v", base.CompactionRatio, direct.CompactionRatio)
	}
	// The default path's counter must be semantically identical to the seam's: same
	// model ⇒ same encoding ⇒ identical counts on a probe string.
	const probe = "tokenization differences 12345 café 日本語"
	if a, b := base.TokenCounter.Count(probe), direct.TokenCounter.Count(probe); a != b {
		t.Errorf("TokenCounter differs for the default model: base=%d direct=%d", a, b)
	}
	bc, ok := base.Compactor.(agent.CascadeCompactor)
	if !ok {
		t.Fatalf("base Compactor type = %T, want CascadeCompactor", base.Compactor)
	}
	dc, ok := direct.Compactor.(agent.CascadeCompactor)
	if !ok {
		t.Fatalf("direct Compactor type = %T, want CascadeCompactor", direct.Compactor)
	}
	if bc.Model != dc.Model {
		t.Errorf("Compactor.Model: base=%q direct=%q", bc.Model, dc.Model)
	}
	if bc.LLM != dc.LLM {
		t.Error("Compactor.LLM differs")
	}
}

// TestEngineDepsForProviderRebindsModel: the CONTAMINATION guard. A non-default
// model must re-derive the model-keyed fields (Model, PromptConfig.Env.Model, the
// agency delta, the Compactor.Model, and the model-specific TokenCounter) — NOT
// inherit the default model's. A shallow clone that swapped only LLM would fail this.
//
// The TokenCounter half is made LOAD-BEARING with Tokenizer:"tiktoken" and two models
// whose tiktoken encodings differ (gpt-4 → cl100k_base vs gpt-4o → o200k_base): the
// counters must produce DIFFERENT counts on a probe string (panel finding #2). Under
// the heuristic counter the two would be indistinguishable (a zero-size value), so the
// guard would pass by accident; tiktoken proves the counter is actually model-keyed.
func TestEngineDepsForProviderRebindsModel(t *testing.T) {
	cfg := Config{Model: "gpt-4o", Compaction: "cascade", Tokenizer: "tiktoken"}
	provider, store, policy, hooks, mcpP, instr := depsTestFixture(t)

	const altModel = "gpt-4" // cl100k_base, a DIFFERENT encoding from gpt-4o (o200k_base)

	deps := engineDepsForProvider(cfg, provider, altModel, func() int { return defaultContextWindowTokens }, store, policy, hooks, mcpP, instr)

	if deps.Model != altModel {
		t.Errorf("Deps.Model = %q, want %q (not the default gpt-4o)", deps.Model, altModel)
	}
	if deps.PromptConfig.Env.Model != altModel {
		t.Errorf("PromptConfig.Env.Model = %q, want %q", deps.PromptConfig.Env.Model, altModel)
	}
	cc, ok := deps.Compactor.(agent.CascadeCompactor)
	if !ok {
		t.Fatalf("Compactor type = %T, want CascadeCompactor", deps.Compactor)
	}
	if cc.Model != altModel {
		t.Errorf("Compactor.Model = %q, want %q (compaction would route to the WRONG model)", cc.Model, altModel)
	}

	// LOAD-BEARING counter assertion (finding #2): the counter derived for altModel
	// (cl100k_base) must DIFFER from a counter built for the default cfg.Model
	// (o200k_base) on a probe string. If engineDepsForProvider leaked the default
	// model's counter, these would be equal and the guard would be vacuous.
	defaultDeps := engineDepsForProvider(cfg, provider, cfg.Model, func() int { return defaultContextWindowTokens }, store, policy, hooks, mcpP, instr)
	const probe = "tokenization differences 12345 café 日本語"
	altCount := deps.TokenCounter.Count(probe)
	defCount := defaultDeps.TokenCounter.Count(probe)
	if altCount == defCount {
		t.Errorf("TokenCounter did not re-key on model: gpt-4 count=%d == gpt-4o count=%d "+
			"(the counter half of the contamination guard is not load-bearing)", altCount, defCount)
	}
	// The Compactor's Counter must be the SAME counter the trigger uses (one per model).
	if cc.Counter.Count(probe) != altCount {
		t.Error("Compactor.Counter and Deps.TokenCounter disagree — they must be ONE counter for the model")
	}

	// The agency Role is keyed on the model: assert it re-derived for the requested
	// model (the contract is uniform across families since issue #49, but the Role
	// must still be rebuilt from the requested model's promptConfig, not leaked).
	wantRole := promptConfig(Config{Model: altModel}, "").Role
	if deps.PromptConfig.Role != wantRole {
		t.Errorf("PromptConfig.Role did not re-derive for the alternate model (agency-delta contamination):\n got %q\nwant %q",
			deps.PromptConfig.Role, wantRole)
	}
}

// TestEngineDepsCarryWallClock is the issue #53 regression guard: every
// production Deps-constructing path must inject the wall clock, or the loop's
// latency instrumentation (EvTurnEnd.DurationMs/TTFT/inter-token and tool
// queued/took) silently reads zero forever. It pins the concrete type too —
// the production clock is engine/adapter/wallclock, never a fake.
func TestEngineDepsCarryWallClock(t *testing.T) {
	cfg := Config{Model: "gpt-5", Workspace: "/repo"}
	provider, store, policy, hooks, mcpP, instr := depsTestFixture(t)

	base := baseEngineDeps(cfg, regForTest(provider, providerOpenAI, cfg.Model), provider, store, policy, hooks, mcpP, instr)
	if base.Clock == nil {
		t.Fatal("baseEngineDeps Deps.Clock is nil (latency metrics dead, issue #53)")
	}
	if _, ok := base.Clock.(wallclock.Clock); !ok {
		t.Fatalf("baseEngineDeps Deps.Clock = %T, want wallclock.Clock", base.Clock)
	}

	direct := engineDepsForProvider(cfg, provider, cfg.Model, func() int { return defaultContextWindowTokens }, store, policy, hooks, mcpP, instr)
	if direct.Clock == nil {
		t.Fatal("engineDepsForProvider Deps.Clock is nil (latency metrics dead, issue #53)")
	}
	if _, ok := direct.Clock.(wallclock.Clock); !ok {
		t.Fatalf("engineDepsForProvider Deps.Clock = %T, want wallclock.Clock", direct.Clock)
	}

	// Children INHERIT the clock — childEngineDepsForProvider clears the telemetry
	// seams (Sink/ToolCallRecorder) but must NOT clear Clock.
	child := childEngineDepsForProvider(cfg, "member:explorer", provider, cfg.Model, func() int { return defaultContextWindowTokens }, tool.NewCatalog(), promptConfig(cfg, ""), nil)
	if child.Clock == nil {
		t.Fatal("childEngineDepsForProvider Deps.Clock is nil (children must inherit the wall clock)")
	}
	if _, ok := child.Clock.(wallclock.Clock); !ok {
		t.Fatalf("childEngineDepsForProvider Deps.Clock = %T, want wallclock.Clock", child.Clock)
	}

	// The DEFAULT-provider child shape (newChildEngineWithHooks → childEngineDeps:
	// the default Subagent explorer, Parallel branch/judge, usermodel-review
	// children) builds its own Deps literal — assert its Clock too, or deleting
	// the field there would pass the suite while silently zeroing child latency.
	defChild := childEngineDeps(cfg, "explorer", provider, tool.NewCatalog(), cfg.Model, fixedDefaultWindow, promptConfig(cfg, ""), nil)
	if defChild.Clock == nil {
		t.Fatal("childEngineDeps Deps.Clock is nil (default child engines must carry the wall clock)")
	}
	if _, ok := defChild.Clock.(wallclock.Clock); !ok {
		t.Fatalf("childEngineDeps Deps.Clock = %T, want wallclock.Clock", defChild.Clock)
	}
}

func TestRequestManifestGatePropagatesToEveryEngineShape(t *testing.T) {
	provider, store, policy, hooks, mcpP, instr := depsTestFixture(t)
	for _, enabled := range []bool{false, true} {
		cfg := Config{Model: "model", enableDurableEvidence: enabled}
		reg := regForTest(provider, providerOpenAI, cfg.Model)
		base := baseEngineDeps(cfg, reg, provider, store, policy, hooks, mcpP, instr)
		perSession := engineDepsForProvider(cfg, provider, cfg.Model, fixedDefaultWindow, store, policy, hooks, mcpP, instr)
		child := childEngineDepsForProvider(cfg, "member:lead", provider, cfg.Model, fixedDefaultWindow, tool.NewCatalog(), promptConfig(cfg, ""), nil)
		defaultChild := childEngineDeps(cfg, "task", provider, tool.NewCatalog(), cfg.Model, fixedDefaultWindow, promptConfig(cfg, ""), nil)
		for name, deps := range map[string]agent.Deps{
			"main": base, "per-session": perSession, "child/provider": child, "child/default": defaultChild,
		} {
			if deps.EnableDurableEvidence != enabled {
				t.Errorf("enabled=%v %s EnableDurableEvidence=%v", enabled, name, deps.EnableDurableEvidence)
			}
		}
	}
}

// --- validateDefaultModel fail-fast posture (issue #21) -------------------------
//
// The server-configured deployment-wide default (--default-provider /
// --default-model) is validated FAIL-FAST at Build through the REAL composition
// (the offline envDetector/providerConstructor/liveModelHTTPClient seams):
// an unknown/unavailable provider or an uncatalogued model is a startup error
// naming the flag, the value, and the reason — stricter than per-session
// selectors (which allow passthrough), because a deployment default must be
// known-good. UseMock skips the validation entirely.

// buildWithDefaults runs the real app.Build offline with the given Default*
// fields over an openai+openrouter-keyed environment (openai is the preferred
// default; openrouter is an available NON-preferred provider for the
// provider-override and cross-provider-coherence cases), returning the build
// error (the caller closes a successful build).
func buildWithDefaults(t *testing.T, defaultProvider, defaultModel string, diag port.Diagnostics) (*Built, error) {
	t.Helper()
	return Build(context.Background(), Config{
		Workspace:       t.TempDir(),
		NoSoul:          true,
		DefaultProvider: defaultProvider,
		DefaultModel:    defaultModel,
		Diagnostics:     diag,
		envDetector: fakeEnv(map[string]string{
			"OPENAI_API_KEY":     "sk-x",
			"OPENROUTER_API_KEY": "sk-x",
		}),
		// Strictly offline: refuse any (keyed) live model fetch ⇒ embedded floor.
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("ok"))
		},
	})
}

// TestBuildFailsOnUnavailableDefaultProvider: --default-provider naming a
// provider with no resolved key is a fail-fast startup error naming the flag.
func TestBuildFailsOnUnavailableDefaultProvider(t *testing.T) {
	built, err := buildWithDefaults(t, "anthropic", "", nil)
	if err == nil {
		built.Close()
		t.Fatal("Build(DefaultProvider=anthropic, no key) succeeded, want a fail-fast startup error")
	}
	if !strings.Contains(err.Error(), "--default-provider") || !strings.Contains(err.Error(), "anthropic") {
		t.Errorf("error must name the flag --default-provider and the value, got: %v", err)
	}
}

// TestBuildFailsOnUncataloguedDefaultModel: --default-model naming a model the
// embedded catalog does not list for the resolved default provider is a
// fail-fast startup error naming the flag (per-session selectors still allow
// passthrough; the deployment default does not).
func TestBuildFailsOnUncataloguedDefaultModel(t *testing.T) {
	const bogus = "totally-bogus-model-9000"
	built, err := buildWithDefaults(t, "", bogus, nil)
	if err == nil {
		built.Close()
		t.Fatalf("Build(DefaultModel=%q) succeeded, want a fail-fast startup error", bogus)
	}
	if !strings.Contains(err.Error(), "--default-model") || !strings.Contains(err.Error(), bogus) {
		t.Errorf("error must name the flag --default-model and the value, got: %v", err)
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Errorf("error must name the resolved default provider (openai), got: %v", err)
	}
}

// TestBuildAcceptsCataloguedDefaultPair: a catalogued (provider, model) pair
// builds successfully and the build-once INFO fact fires exactly once — and
// STAYS at one across session creation (a zero-selector session AND a selector
// session that mints a per-session engine), pinning build-once rather than
// once-per-sessionless-build (the TestBuildNarratesSubagentModelExactlyOnce
// discipline).
func TestBuildAcceptsCataloguedDefaultPair(t *testing.T) {
	ctx := context.Background()
	diag := newCapturingDiagnostics()
	built, err := buildWithDefaults(t, "openai", "gpt-5-mini", diag)
	if err != nil {
		t.Fatalf("Build(catalogued default pair): %v", err)
	}
	defer built.Close()
	if n := diag.countContaining("server-configured default model ACTIVE"); n != 1 {
		t.Fatalf("configured-default INFO emitted %d times, want exactly 1 (build-once fact)", n)
	}

	if _, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits()); err != nil {
		t.Fatalf("CreateSession(zero-selector): %v", err)
	}
	if _, err := built.Service.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenRouter}); err != nil {
		t.Fatalf("CreateSessionWithProvider(selector): %v", err)
	}
	if n := diag.countContaining("server-configured default model ACTIVE"); n != 1 {
		t.Fatalf("configured-default INFO emitted %d times after 2 sessions, want still exactly 1 (build-once, not per-session)", n)
	}
}

// TestBuildFailsOnCrossProviderDefaultPair pins the pair-coherence gate: a
// --default-provider together with a --default-model catalogued only for a
// DIFFERENT provider (here the bare openai id "gpt-5-mini", which openrouter
// catalogues as "openai/gpt-5-mini") is a fail-fast startup error naming
// --default-model and the resolved provider.
func TestBuildFailsOnCrossProviderDefaultPair(t *testing.T) {
	built, err := buildWithDefaults(t, "openrouter", "gpt-5-mini", nil)
	if err == nil {
		built.Close()
		t.Fatal("Build(DefaultProvider=openrouter, DefaultModel=gpt-5-mini) succeeded, want a fail-fast error (the id is catalogued for openai, not openrouter)")
	}
	if !strings.Contains(err.Error(), "--default-model") || !strings.Contains(err.Error(), "gpt-5-mini") {
		t.Errorf("error must name the flag --default-model and the value, got: %v", err)
	}
	if !strings.Contains(err.Error(), "openrouter") {
		t.Errorf("error must name the resolved default provider (openrouter), got: %v", err)
	}
}

// TestBuildAcceptsProviderOnlyDefault: --default-provider alone (no
// --default-model) builds, the INFO ACTIVE fact fires once, and the effective
// default the service echoes is the configured provider + ITS builtin default
// model (the validator's provider-only arm).
func TestBuildAcceptsProviderOnlyDefault(t *testing.T) {
	ctx := context.Background()
	diag := newCapturingDiagnostics()
	built, err := buildWithDefaults(t, "openrouter", "", diag)
	if err != nil {
		t.Fatalf("Build(provider-only default): %v", err)
	}
	defer built.Close()
	if n := diag.countContaining("server-configured default model ACTIVE"); n != 1 {
		t.Fatalf("configured-default INFO emitted %d times, want exactly 1", n)
	}

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession(zero-selector): %v", err)
	}
	got := built.Service.ResolvedModel(sess.ID)
	if got.ProviderID != providerOpenRouter || got.ModelID != "openai/gpt-5" {
		t.Fatalf("zero-selector ResolvedModel = %+v, want (openrouter, openai/gpt-5) — the configured provider + its builtin default", got)
	}
}

// kvDiag is a port.Diagnostics double recording messages WITH their key/value
// args (capturingDiagnostics drops args), so the fact-arm tests below can pin
// the EFFECTIVE resolved pair the INFO carries. Concurrency-safe; With returns
// the same recorder.
type kvDiag struct {
	mu   sync.Mutex
	msgs []string
	args [][]any
	lvl  []port.Level
}

func (d *kvDiag) Log(_ context.Context, lvl port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.msgs = append(d.msgs, msg)
	d.args = append(d.args, args)
	d.lvl = append(d.lvl, lvl)
}

func (d *kvDiag) With(...any) port.Diagnostics { return d }

func TestConsolidationDiagnosticsContainCountsOnly(t *testing.T) {
	const privateMemory = "never-log-this-memory-content"
	diag := &kvDiag{}
	logConsolidationReport(context.Background(), diag, "memory consolidation", dream.Report{
		Planned: 5, Applied: 1, Conflicted: 1, Skipped: 1, Failed: 2,
	}, errors.New(privateMemory))

	if len(diag.msgs) != 1 || diag.msgs[0] != "memory consolidation completed with failures" {
		t.Fatalf("messages = %v", diag.msgs)
	}
	want := []any{"planned", 5, "applied", 1, "conflicted", 1, "skipped", 1, "failed", 2}
	if !reflect.DeepEqual(diag.args[0], want) {
		t.Fatalf("diagnostic args = %v, want %v", diag.args[0], want)
	}
	if strings.Contains(fmt.Sprint(diag.msgs, diag.args), privateMemory) {
		t.Fatal("diagnostics leaked memory/provider content")
	}
}

// TestValidateDefaultModelFactArms pins the two HONEST arms of the build-once
// fact: (a) provider-only config logs the ACTIVE fact with the EFFECTIVE
// resolved pair (the configured provider + its builtin model — never an empty
// model kv); (b) a configured default model alongside an explicit --model logs
// the SUPERSEDED fact (tier 1 wins), never the ACTIVE claim.
func TestValidateDefaultModelFactArms(t *testing.T) {
	env := fakeEnv(map[string]string{
		"OPENAI_API_KEY":     "sk",
		"OPENROUTER_API_KEY": "sk",
	})

	// (a) provider-only ⇒ ACTIVE with the effective pair.
	cfgA := Config{DefaultProvider: "openrouter"}
	regA, err := buildProviderRegistry(cfgA, env)
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	dA := &kvDiag{}
	cfgA.Diagnostics = dA
	if err := validateDefaultModel(cfgA, regA); err != nil {
		t.Fatalf("validateDefaultModel(provider-only): %v", err)
	}
	if len(dA.msgs) != 1 || !strings.Contains(dA.msgs[0], "ACTIVE") {
		t.Fatalf("provider-only fact = %v, want exactly one ACTIVE INFO", dA.msgs)
	}
	wantPair := []any{"provider", "openrouter", "model", "openai/gpt-5"}
	if !reflect.DeepEqual(dA.args[0], wantPair) {
		t.Fatalf("ACTIVE fact args = %v, want the EFFECTIVE pair %v (never an empty model kv)", dA.args[0], wantPair)
	}

	// (b) configured model + explicit --model ⇒ SUPERSEDED, never ACTIVE.
	cfgB := Config{Model: "gpt-5", DefaultModel: "gpt-5-mini"}
	regB, err := buildProviderRegistry(cfgB, env)
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	dB := &kvDiag{}
	cfgB.Diagnostics = dB
	if err := validateDefaultModel(cfgB, regB); err != nil {
		t.Fatalf("validateDefaultModel(superseded): %v", err)
	}
	if len(dB.msgs) != 1 || !strings.Contains(dB.msgs[0], "superseded by --model") {
		t.Fatalf("superseded fact = %v, want exactly one supersession INFO", dB.msgs)
	}
	if strings.Contains(dB.msgs[0], "ACTIVE") {
		t.Fatalf("supersession fact must not claim ACTIVE: %q", dB.msgs[0])
	}
}

// TestBuildIgnoresDefaultsUnderUseMock: UseMock skips the validation entirely —
// junk Default* fields must NOT error (the mock provider isn't catalogued and
// the mock path never consults the resolved default).
func TestBuildIgnoresDefaultsUnderUseMock(t *testing.T) {
	built, err := Build(context.Background(), Config{
		Workspace:       t.TempDir(),
		Model:           "mock",
		UseMock:         true,
		DefaultProvider: "no-such-provider",
		DefaultModel:    "no-such-model",
	})
	if err != nil {
		t.Fatalf("Build(UseMock + junk Default*) = %v, want nil (validator must be a no-op under UseMock)", err)
	}
	built.Close()
}
