package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

// router_test.go covers the OPT-IN semantic Subagent model router (ADR 0031, Phase 5):
// the buildModelRouterTask closure (category→model mapping, precedence, fail-soft,
// breaker) and the end-to-end proof through the REAL composition that a routed
// delegation mints the child on the classifier-chosen model.

const (
	routerLarge = "claude-haiku-4-5" // catalogued (catAnthropicModel); the "large" category target
	routerSmall = "gpt-5-mini"       // the "small" category target
)

func routerTaxonomyCfg() Config {
	return Config{
		Model: "session-model",
		RouterCategories: []permconfig.RouterCategory{
			{Name: "small", Description: "trivial mechanical tasks", Model: routerSmall},
			{Name: "large", Description: "deep reasoning, architecture", Model: routerLarge},
		},
		RouterDefaultCategory: "small",
	}
}

// OFF (ADR 0042): the TAXONOMY is the enable, with a kill-switch override. A non-empty
// taxonomy that is NOT disabled returns a non-nil closure; an empty taxonomy OR the
// kill-switch (RouterDisabled) returns nil — the engine then carries no router and the
// run() hook's routeTask is nil (byte-identical to no router).
func TestBuildModelRouterTaskOffWhenDisabled(t *testing.T) {
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "session-model")

	// Taxonomy present, not disabled → ON (non-nil): the enable is the taxonomy.
	if fn := buildModelRouterTask(routerTaxonomyCfg(), reg, prov, providerAnthropic, "session-model"); fn == nil {
		t.Fatal("router task must be non-nil when a taxonomy is present and not disabled (taxonomy is the enable)")
	}

	// Empty taxonomy → OFF (byte-identical OFF-when-unconfigured).
	cfg := routerTaxonomyCfg()
	cfg.RouterCategories = nil
	if fn := buildModelRouterTask(cfg, reg, prov, providerAnthropic, cfg.Model); fn != nil {
		t.Fatal("router task must be nil when there is no taxonomy")
	}

	// Kill-switch: a taxonomy is present but RouterDisabled forces it OFF.
	cfg = routerTaxonomyCfg()
	cfg.RouterDisabled = true
	if fn := buildModelRouterTask(cfg, reg, prov, providerAnthropic, cfg.Model); fn != nil {
		t.Fatal("router task must be nil when the kill-switch (RouterDisabled) is set despite a taxonomy")
	}
}

// The closure classifies and maps the chosen category to its resolved concrete model,
// and propagates the classifier's token spend as its 3rd return (the #92 seam this layer
// widened): buildModelRouterTask must pass RunModelRouter's session.Usage through to the
// dispatch-path routeTask fold on BOTH the hit and the miss path (see the sibling miss
// test below). A regression that zeroed the hit return (return category, id,
// session.Usage{}, true) would drop classifier spend from the parent budget — CWE-770.
func TestBuildModelRouterTaskMapsCategoryToModel(t *testing.T) {
	const inputTok, outputTok = 200, 100
	prov := mockllm.New(mockllm.ChunksTurn(
		mockllm.TextChunk(`{"category":"large"}`),
		mockllm.UsageChunk(session.Usage{InputTokens: inputTok, OutputTokens: outputTok}),
		mockllm.DoneChunk(session.StopEndTurn),
	))
	reg := regForTest(prov, providerAnthropic, "session-model")

	fn := buildModelRouterTask(routerTaxonomyCfg(), reg, prov, providerAnthropic, "session-model")
	if fn == nil {
		t.Fatal("router task must be non-nil when enabled with a taxonomy")
	}
	cat, model, usage, reason, ok := fn(context.Background(), "redesign the storage layer")
	if !ok {
		t.Fatal("a valid classification must resolve")
	}
	if cat != "large" || model != routerLarge {
		t.Fatalf("routed (category, model) = (%q, %q), want (large, %q)", cat, model, routerLarge)
	}
	if reason != "" {
		t.Fatalf("a successful route must carry no miss reason; got %q", reason)
	}
	if usage.TotalTokens() != inputTok+outputTok {
		t.Fatalf("hit usage.TotalTokens() = %d, want %d (classifier spend must propagate through the composition closure)", usage.TotalTokens(), inputTok+outputTok)
	}
}

// MISS-path usage propagation (the #92 seam, sibling of the hit assertion above): even a
// fail-soft miss (garbage verdict → ok=false) must surface the classifier's actual spend
// as the 3rd return so the dispatch-path routeTask folds it unconditionally. A regression
// that returned session.Usage{} on the miss branch of buildModelRouterTask would leak
// classifier spend on every misclassified delegation — CWE-770.
func TestBuildModelRouterTaskPropagatesUsageOnMiss(t *testing.T) {
	const inputTok, outputTok = 150, 90
	prov := mockllm.New(mockllm.ChunksTurn(
		mockllm.TextChunk("I am not sure, sorry — just prose."),
		mockllm.UsageChunk(session.Usage{InputTokens: inputTok, OutputTokens: outputTok}),
		mockllm.DoneChunk(session.StopEndTurn),
	))
	reg := regForTest(prov, providerAnthropic, "session-model")

	fn := buildModelRouterTask(routerTaxonomyCfg(), reg, prov, providerAnthropic, "session-model")
	cat, model, usage, reason, ok := fn(context.Background(), "x")
	if ok || cat != "" || model != "" {
		t.Fatalf("a garbage verdict must be a fail-soft miss; got (cat=%q, model=%q, ok=%v)", cat, model, ok)
	}
	// The engine-side reason (bad-verdict) passes through the composition closure unchanged.
	if reason != agent.RouterMissBadVerdict {
		t.Fatalf("garbage-verdict miss reason = %q, want the engine reason %q passed through", reason, agent.RouterMissBadVerdict)
	}
	if usage.TotalTokens() != inputTok+outputTok {
		t.Fatalf("miss usage.TotalTokens() = %d, want %d (miss path must still propagate spent usage)", usage.TotalTokens(), inputTok+outputTok)
	}
}

// Alias interaction: a category whose Model selector is an ALIAS resolves through the
// operator-merged alias map (operator taxonomy targets are uncapped — the operator is
// authoritative).
func TestBuildModelRouterTaskResolvesCategoryAlias(t *testing.T) {
	prov := mockllm.New(mockllm.TextTurn(`{"category":"small"}`))
	reg := regForTest(prov, providerAnthropic, "session-model")

	cfg := routerTaxonomyCfg()
	cfg.RouterCategories[0].Model = "tiny"                            // category "small" → alias "tiny"
	cfg.ModelAliases = map[string]string{"tiny": "resolved-tiny-1.0"} // alias → concrete id

	fn := buildModelRouterTask(cfg, reg, prov, providerAnthropic, "session-model")
	_, model, _, _, ok := fn(context.Background(), "rename a var")
	if !ok || model != "resolved-tiny-1.0" {
		t.Fatalf("aliased category routed to (%q, %v), want resolved-tiny-1.0 true", model, ok)
	}
}

// Fail-soft: a classifier MISS (garbage/unknown category) yields ok=false — the caller
// inherits the default model.
func TestBuildModelRouterTaskFailSoftOnMiss(t *testing.T) {
	prov := mockllm.New(mockllm.TextTurn("I am not sure, sorry."))
	reg := regForTest(prov, providerAnthropic, "session-model")

	fn := buildModelRouterTask(routerTaxonomyCfg(), reg, prov, providerAnthropic, "session-model")
	if _, _, _, reason, ok := fn(context.Background(), "x"); ok || reason != agent.RouterMissBadVerdict {
		t.Fatalf("a classifier miss must be fail-soft with the engine reason passed through; ok=%v reason=%q", ok, reason)
	}
}

// Fail-soft: a category mapping to an UNRESOLVABLE selector yields ok=false.
func TestBuildModelRouterTaskFailSoftOnUnresolvableTarget(t *testing.T) {
	prov := mockllm.New(mockllm.TextTurn(`{"category":"small"}`))
	reg := regForTest(prov, providerAnthropic, "session-model")

	cfg := routerTaxonomyCfg()
	cfg.RouterCategories[0].Model = "sonnet" // a built-in alias meaning inherit → unresolvable
	fn := buildModelRouterTask(cfg, reg, prov, providerAnthropic, "session-model")
	// The classifier picks "small" (RouterCategories[0]); its target "sonnet" resolves to
	// inherit → a COMPOSITION-side miss naming the category + selector (issue #287).
	_, _, _, reason, ok := fn(context.Background(), "x")
	if ok {
		t.Fatal("an unresolvable category target must be fail-soft (ok=false)")
	}
	if !strings.HasPrefix(reason, "category-target-unresolvable") || !strings.Contains(reason, "category=small") || !strings.Contains(reason, "selector=sonnet") {
		t.Fatalf("unresolvable-target reason = %q, want a category-target-unresolvable reason naming category=small selector=sonnet", reason)
	}
}

// Fail-soft: a category mapping to an EMPTY selector yields ok=false with the
// composition-side category-selector-empty reason (issue #287).
func TestBuildModelRouterTaskFailSoftOnEmptySelector(t *testing.T) {
	prov := mockllm.New(mockllm.TextTurn(`{"category":"small"}`))
	reg := regForTest(prov, providerAnthropic, "session-model")

	cfg := routerTaxonomyCfg()
	cfg.RouterCategories[0].Model = "" // the chosen category's selector is empty
	fn := buildModelRouterTask(cfg, reg, prov, providerAnthropic, "session-model")
	_, _, _, reason, ok := fn(context.Background(), "x")
	if ok {
		t.Fatal("an empty category selector must be fail-soft (ok=false)")
	}
	if !strings.HasPrefix(reason, "category-selector-empty") || !strings.Contains(reason, "category=small") {
		t.Fatalf("empty-selector reason = %q, want a category-selector-empty reason naming category=small", reason)
	}
}

// foldOperatorModelRouter with no resolver (or no router block) is a no-op.
func TestFoldOperatorModelRouterNoOpWithoutResolver(t *testing.T) {
	cfg := Config{Model: "m"}
	if got := foldOperatorModelRouter(cfg); got.RouterCategories != nil {
		t.Fatal("foldOperatorModelRouter with no resolver must be a no-op")
	}
	// A real resolver but NO models.router block is also a no-op.
	path := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(path, []byte("permissions:\n  allow: []\n"), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	if got := foldOperatorModelRouter(Config{Model: "m", permResolver: res}); got.RouterCategories != nil {
		t.Fatal("a resolver with no models.router block must fold nothing")
	}
}

// foldOperatorModelRouter DROPS a malformed category (empty name/description/model)
// fail-soft with a WARN while keeping the valid one. A regression inverting/deleting the
// `name=="" || desc=="" || model==""` drop loop fails here.
func TestFoldOperatorModelRouterDropsMalformed(t *testing.T) {
	const yamlCfg = `
models:
  router:
    default-category: good
    classifier-slot: cheap
    categories:
      - name: good
        description: a valid category
        model: gpt-4o-mini
      - name: nodesc
        description: ""
        model: gpt-4o-mini
      - name: ""
        description: missing name
        model: gpt-4o-mini
      - name: nomodel
        description: missing model
        model: ""
`
	path := filepath.Join(t.TempDir(), "router.yaml")
	if err := os.WriteFile(path, []byte(yamlCfg), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	if res == nil {
		t.Fatal("resolver should be non-nil")
	}
	diag := newCapturingDiagnostics()
	cfg := foldOperatorModelRouter(Config{Model: "m", Diagnostics: diag, permResolver: res})

	if len(cfg.RouterCategories) != 1 {
		t.Fatalf("want exactly 1 surviving category (the 3 malformed dropped), got %d: %+v", len(cfg.RouterCategories), cfg.RouterCategories)
	}
	if cfg.RouterCategories[0].Name != "good" || cfg.RouterCategories[0].Model != "gpt-4o-mini" {
		t.Fatalf("the valid category did not survive intact: %+v", cfg.RouterCategories[0])
	}
	if cfg.RouterDefaultCategory != "good" || cfg.RouterClassifierSlot != "cheap" {
		t.Fatalf("router header not folded: default=%q classifier-slot=%q", cfg.RouterDefaultCategory, cfg.RouterClassifierSlot)
	}
	if n := diag.countContaining("DROPPING a malformed category"); n != 3 {
		t.Fatalf("want 3 malformed-drop WARNs, got %d", n)
	}
}

// foldOperatorModelRouter ORs the YAML `disabled:` kill-switch into cfg.RouterDisabled
// (ADR 0042, mirroring foldOperatorGuardrails). A regression dropping the OR fails here.
func TestFoldOperatorModelRouterFoldsDisabled(t *testing.T) {
	const yamlCfg = `
models:
  router:
    disabled: true
    categories:
      - name: good
        description: a valid category
        model: gpt-4o-mini
`
	path := filepath.Join(t.TempDir(), "router.yaml")
	if err := os.WriteFile(path, []byte(yamlCfg), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	if res == nil {
		t.Fatal("resolver should be non-nil")
	}
	cfg := foldOperatorModelRouter(Config{Model: "m", permResolver: res})
	if !cfg.RouterDisabled {
		t.Fatal("models.router.disabled: true must OR into cfg.RouterDisabled")
	}
	if len(cfg.RouterCategories) != 1 {
		t.Fatalf("the taxonomy must still fold (disabled is the kill-switch, not a parse drop); got %d categories", len(cfg.RouterCategories))
	}

	// A CLI kill-switch already set must STAY set when the YAML does not disable.
	const enabledYAML = `
models:
  router:
    categories:
      - name: good
        description: a valid category
        model: gpt-4o-mini
`
	path2 := filepath.Join(t.TempDir(), "router2.yaml")
	if err := os.WriteFile(path2, []byte(enabledYAML), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res2 := permconfig.New(permconfig.Options{ExplicitFiles: []string{path2}})
	cfg2 := foldOperatorModelRouter(Config{Model: "m", RouterDisabled: true, permResolver: res2})
	if !cfg2.RouterDisabled {
		t.Fatal("a CLI-set RouterDisabled must survive a fold of a non-disabled YAML (the two OR together)")
	}
}

// logModelRouterFacts (ADR 0042): no taxonomy → SILENT; taxonomy + disabled → a one-time
// DISABLED WARN; taxonomy + not disabled → the ACTIVE INFO (category count + classifier).
func TestLogModelRouterFacts(t *testing.T) {
	t.Run("no taxonomy → silent", func(t *testing.T) {
		diag := newCapturingDiagnostics()
		logModelRouterFacts(Config{Model: "m", Diagnostics: diag})
		if n := diag.countContaining("model router"); n != 0 {
			t.Fatalf("no taxonomy must be silent (byte-identical OFF), got %d lines", n)
		}
	})
	t.Run("taxonomy + disabled → DISABLED WARN", func(t *testing.T) {
		diag := newCapturingDiagnostics()
		logModelRouterFacts(Config{
			Model:            "m",
			RouterDisabled:   true,
			Diagnostics:      diag,
			RouterCategories: []permconfig.RouterCategory{{Name: "small", Description: "x", Model: "gpt-4o-mini"}},
		})
		if n := diag.countContaining("subagent model router is DISABLED"); n != 1 {
			t.Fatalf("want 1 DISABLED WARN when a taxonomy is kill-switched, got %d", n)
		}
		if n := diag.countContaining("model router ACTIVE"); n != 0 {
			t.Fatalf("must NOT narrate ACTIVE when disabled, got %d", n)
		}
	})
	t.Run("taxonomy + not disabled → ACTIVE naming the classifier model", func(t *testing.T) {
		// Use a slogdiag buffer (not the message-only capturingDiagnostics) so the
		// structured `classifier` arg is rendered into the asserted line — the L1
		// alignment fix is that the LOGGED classifier matches what a session classifies on.
		var buf bytes.Buffer
		diag := slogdiag.New(&buf, false, port.LevelDebug)
		cfg := Config{
			Model:            "session-model",
			Diagnostics:      diag,
			ModelSlots:       map[string]string{slotRouter: "tiny"},
			ModelAliases:     map[string]string{"tiny": "classifier-id-1.0"},
			RouterCategories: []permconfig.RouterCategory{{Name: "small", Description: "x", Model: "gpt-4o-mini"}},
		}
		logModelRouterFacts(cfg)
		log := buf.String()
		if !strings.Contains(log, "subagent model router ACTIVE") {
			t.Fatalf("want the ACTIVE INFO; got:\n%s", log)
		}
		if !strings.Contains(log, "classifier-id-1.0") {
			t.Fatalf("the ACTIVE INFO must name the resolved classifier model classifier-id-1.0; got:\n%s", log)
		}
		if !strings.Contains(log, "categories=1") {
			t.Fatalf("the ACTIVE INFO must name the category count; got:\n%s", log)
		}
		// The ACTIVE line must carry the per-delegation spend hint (the silently-flipped-on
		// operator's one cost signal).
		if !strings.Contains(log, "extra classifier") {
			t.Fatalf("the ACTIVE INFO must name the extra per-delegation classifier spend; got:\n%s", log)
		}
		// The DISABLED WARN must NOT fire on the enabled path (symmetry with the DISABLED
		// subtree above, which asserts ACTIVE does not fire).
		if strings.Contains(log, "subagent model router is DISABLED") {
			t.Fatalf("the DISABLED WARN must NOT fire when a taxonomy is present and not disabled; got:\n%s", log)
		}
	})
}

// The `router` slot (or classifier-slot) actually changes which model the CLASSIFIER
// engine is built on — its LLM request carries THAT model, not the session model.
func TestRouterClassifierRunsOnSlotModel(t *testing.T) {
	var (
		mu     sync.Mutex
		models []string
	)
	prov := observedProvider(&models, &mu, mockllm.TextTurn(`{"category":"large"}`))
	reg := regForTest(prov, providerAnthropic, "session-model")

	cfg := routerTaxonomyCfg()
	// Route the classifier onto a DISTINCT model via the `router` slot.
	cfg.ModelSlots = map[string]string{slotRouter: "classifier-only"}
	cfg.ModelAliases = map[string]string{"classifier-only": catAnthropicModel}

	fn := buildModelRouterTask(cfg, reg, prov, providerAnthropic, "session-model")
	if fn == nil {
		t.Fatal("router task must be non-nil")
	}
	cat, _, _, _, ok := fn(context.Background(), "classify this")
	if !ok || cat != "large" {
		t.Fatalf("classification failed: cat=%q ok=%v", cat, ok)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 || models[0] != catAnthropicModel {
		t.Fatalf("classifier LLM request model = %v, want the `router`-slot model %q (not the session model)", models, catAnthropicModel)
	}
}

// routerModelsObserved builds a provider whose request observer records every model and
// replays a parent→classifier→child→parent script. The classifier turn names "large".
func routerE2EProvider(models *[]string, mu *sync.Mutex) *mockllm.Provider {
	return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
		mu.Lock()
		*models = append(*models, r.Model)
		mu.Unlock()
	})},
		mockllm.ToolCallTurn(session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"redesign storage"}`))),
		mockllm.TextTurn(`{"category":"large"}`), // the classifier turn
		mockllm.TextTurn("CHILD SUMMARY"),        // the routed child's turn
		mockllm.TextTurn("parent done"),
	)
}

// END-TO-END: a plain Subagent delegation under an enabled router is classified "large"
// and the child runs on the large model; the parent's requests stay on the session
// model. The shared mock cursor serialises: parent → classifier → child → parent.
func TestRouterRoutesChildToClassifiedModelE2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	var (
		mu     sync.Mutex
		models []string
	)
	built, err := Build(ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		Model:     "gpt-5",
		// ADR 0042: the taxonomy is the enable — no flag needed to turn the router on.
		RouterCategories: []permconfig.RouterCategory{
			{Name: "small", Description: "trivial tasks", Model: routerSmall},
			{Name: "large", Description: "deep reasoning", Model: routerLarge},
		},
		RouterDefaultCategory: "small",
		AllowAllTools:         true,
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return routerE2EProvider(&models, &mu)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "parent done" {
		t.Fatalf("terminal text = %q, want parent done", got)
	}

	mu.Lock()
	defer mu.Unlock()
	// The shared mock cursor serialises exactly: parent (Subagent call) → classifier →
	// routed child → parent (final). Assert the request POSITIONS, not just "some request
	// carried routerLarge" — so a future classifier-on-a-slot change (which would shift
	// the classifier off the session model) can't pass spuriously.
	if len(models) != 4 {
		t.Fatalf("recorded %d requests (%v), want exactly 4 (parent→classifier→child→parent)", len(models), models)
	}
	if models[0] != "gpt-5" {
		t.Fatalf("models[0] (parent) = %q, want the session model gpt-5 (models=%v)", models[0], models)
	}
	if models[1] != "gpt-5" {
		t.Fatalf("models[1] (classifier) = %q, want the session model gpt-5 — no router slot configured (models=%v)", models[1], models)
	}
	if models[2] != routerLarge {
		t.Fatalf("models[2] (routed CHILD) = %q, want the classified model %q (models=%v)", models[2], routerLarge, models)
	}
	if models[3] != "gpt-5" {
		t.Fatalf("models[3] (final parent) = %q, want gpt-5 (models=%v)", models[3], models)
	}
}

// END-TO-END byte-identical-when-OFF: with the router OFF (no taxonomy, ADR 0042), the
// SAME script runs without a classifier turn — the child inherits the session model and
// NO request carries a routed model. Proves OFF ⇒ no classifier call.
func TestRouterOffIsByteIdenticalE2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	var (
		mu     sync.Mutex
		models []string
	)
	built, err := Build(ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		Model:     "gpt-5",
		// OFF (ADR 0042): no taxonomy ⇒ byte-identical to no router (no classifier call).
		AllowAllTools:       true,
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			// NO classifier turn: parent → child → parent. If the router fired, the
			// child turn would be consumed by the classifier and the run would desync.
			return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
				mu.Lock()
				models = append(models, r.Model)
				mu.Unlock()
			})},
				mockllm.ToolCallTurn(session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"explore"}`))),
				mockllm.TextTurn("CHILD SUMMARY"),
				mockllm.TextTurn("parent done"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "parent done" {
		t.Fatalf("terminal text = %q, want parent done (a fired classifier would desync the shared cursor)", got)
	}

	mu.Lock()
	defer mu.Unlock()
	// Exactly 3 requests (parent, child, parent) — no classifier turn was consumed.
	if len(models) != 3 {
		t.Fatalf("recorded %d requests (%v), want exactly 3 (parent→child→parent, no classifier call when OFF)", len(models), models)
	}
	for _, m := range models {
		if m != "gpt-5" {
			t.Fatalf("a request carried %q with the router OFF; every request must be the session model gpt-5 (models=%v)", m, models)
		}
	}
}

// drainRunWithSubagentStart is drainRun plus capture of every EvSubagentStart payload, so a
// test can assert what actually crossed the wire on the delegation-start event (not just the
// terminal text).
func drainRunWithSubagentStart(run interface {
	Events() <-chan session.Event
	Approve(string, session.ApprovalVerdict)
}) (final string, starts []session.SubagentPayload) {
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			starts = append(starts, *ev.Subagent)
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	return final, starts
}

// END-TO-END (JAORMX PR #408 non-blocking note): buildModelRouterTask's composition-side
// category-selector-empty miss (issue #287) — a detailed reason like
// "category-selector-empty (category=small)" — must be REDUCED to the bare static code
// before it reaches the delegation-start event (routingReasonPayload's allowlist,
// engine/agent/subagent.go). This closes the composition-to-wire seam that was previously
// only indirectly verified: TestBuildModelRouterTaskFailSoftOnEmptySelector proves the
// closure returns the detailed string, and the routingReasonPayload unit tests prove the
// reduction in isolation, but nothing drove the two together through a real Build → Run.
func TestRouterCategorySelectorEmptyReasonReducesOnWireE2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()

	built, err := Build(ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		Model:     "gpt-5",
		// The "small" category's selector is EMPTY — the classifier's pick maps to no
		// concrete model, forcing buildModelRouterTask's composition-side miss.
		RouterCategories: []permconfig.RouterCategory{
			{Name: "small", Description: "trivial tasks", Model: ""},
		},
		RouterDefaultCategory: "small",
		AllowAllTools:         true,
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.NewWith(nil,
				mockllm.ToolCallTurn(session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"explore"}`))),
				mockllm.TextTurn(`{"category":"small"}`), // the classifier turn: picks "small"
				mockllm.TextTurn("CHILD SUMMARY"),        // the child, fallen back to the session model
				mockllm.TextTurn("parent done"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	final, starts := drainRunWithSubagentStart(run)
	if final != "parent done" {
		t.Fatalf("terminal text = %q, want parent done", final)
	}

	if len(starts) != 1 {
		t.Fatalf("got %d subagent.start events, want exactly 1", len(starts))
	}
	if got := starts[0].RoutingReason; got != "category-selector-empty" {
		t.Fatalf("delegation-start RoutingReason = %q, want the reduced static code "+
			"\"category-selector-empty\" (the detailed \"category-selector-empty "+
			"(category=small)\" composition reason must never reach the wire)", got)
	}
}
