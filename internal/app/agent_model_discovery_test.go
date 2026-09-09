package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type discoveryResult struct {
	Models    []discoveryModel `json:"models"`
	Returned  int              `json:"returned"`
	Available int              `json:"available"`
	Truncated bool             `json:"truncated"`
}

type discoveryModel struct {
	ProviderID   string `json:"provider_id"`
	ModelID      string `json:"model_id"`
	DisplayName  string `json:"display_name"`
	Image        bool   `json:"image"`
	Reasoning    bool   `json:"reasoning"`
	ContextLimit int64  `json:"context_limit"`
}

func executeDiscovery(t *testing.T, inventory *resolvedModelInventory, args string) (session.ToolResult, discoveryResult) {
	t.Helper()
	call := session.ToolCall{ID: "call-1", Name: agentModelDiscoveryToolName, Args: json.RawMessage(args)}
	result, err := newAgentModelDiscoveryTool(inventory).Execute(context.Background(), call, tool.Environment{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var decoded discoveryResult
	if !result.IsError {
		if err := json.Unmarshal([]byte(result.Content), &decoded); err != nil {
			t.Fatalf("decode discovery result: %v\n%s", err, result.Content)
		}
	}
	return result, decoded
}

func fixtureModels() []*mecatlv1.ModelInfo {
	return []*mecatlv1.ModelInfo{
		{ProviderId: "alpha", Id: "shared-model", DisplayName: "Shared A", Image: true, ContextLimit: 128000},
		{ProviderId: "beta", Id: "shared-model", DisplayName: "Shared B", Reasoning: true, ContextLimit: 200000},
		{ProviderId: "beta", Id: "unique", DisplayName: "Unique", ContextLimit: 64000},
	}
}

func TestAgentModelDiscovery_Scenario1_SharedResolvedInventory(t *testing.T) {
	inventory := newResolvedModelInventory(fixtureModels())
	svc, err := newTestServerService(server.Config{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(defaultRules(), nil)}), Store: memstore.New(), ModelInventory: inventory})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	_, got := executeDiscovery(t, inventory, `{}`)
	listed := svc.ListModels(context.Background())
	if len(got.Models) != len(listed) {
		t.Fatalf("discovery returned %d models, ListModels returned %d", len(got.Models), len(listed))
	}
	for i, model := range got.Models {
		want := listed[i]
		if model.ProviderID != want.ProviderId || model.ModelID != want.Id || model.DisplayName != want.DisplayName || model.Image != want.Image || model.Reasoning != want.Reasoning || model.ContextLimit != want.ContextLimit {
			t.Errorf("entry %d = %+v, want ListModels metadata %+v", i, model, want)
		}
	}
}

func TestAgentModelDiscovery_Scenario1_ExactProviderModelHandles(t *testing.T) {
	_, got := executeDiscovery(t, newResolvedModelInventory(fixtureModels()), `{"model_id":"shared-model"}`)
	if len(got.Models) != 2 {
		t.Fatalf("equal model ids under distinct providers merged: got %+v", got.Models)
	}
	if got.Models[0].ProviderID == got.Models[1].ProviderID || got.Models[0].ModelID != "shared-model" || got.Models[1].ModelID != "shared-model" {
		t.Fatalf("exact provider/model handles not preserved: %+v", got.Models)
	}
}

func TestAgentModelDiscovery_Scenario1_ReadOnlyBoundedOutput(t *testing.T) {
	models := make([]*mecatlv1.ModelInfo, 0, 200)
	for i := range 200 {
		models = append(models, &mecatlv1.ModelInfo{ProviderId: "p", Id: fmt.Sprintf("model-%03d", i), DisplayName: strings.Repeat("x", 400)})
	}
	inventory := newResolvedModelInventory(models)
	discovery := newAgentModelDiscoveryTool(inventory)
	if !discovery.ReadOnly() {
		t.Fatal("model discovery must be read-only")
	}
	firstResult, first := executeDiscovery(t, inventory, `{}`)
	secondResult, second := executeDiscovery(t, inventory, `{}`)
	if firstResult.Content != secondResult.Content {
		t.Fatal("stable inventory produced non-deterministic output")
	}
	if len(first.Models) != defaultAgentModelDiscoveryLimit || !first.Truncated || first.Returned != len(first.Models) || first.Available != len(models) {
		t.Fatalf("default bound metadata is dishonest: %+v", first)
	}
	if len(firstResult.Content) > maxAgentModelDiscoveryOutputBytes {
		t.Fatalf("output length %d exceeds bound %d", len(firstResult.Content), maxAgentModelDiscoveryOutputBytes)
	}
	if second.Models[0].ProviderID == "" || second.Models[0].ModelID == "" {
		t.Fatalf("bounded result omitted complete selection handle: %+v", second.Models[0])
	}
}

func TestAgentModelDiscovery_Scenario1_SystemPromptContainsDiscoveryContract(t *testing.T) {
	const model = "gpt-5"
	var captured prompt.Layered
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { captured = req.System })}, mockllm.TextTurn("ok"))
	reg := regForTest(provider, providerOpenAI, model)
	assets := catalogAssets{modelInventory: newResolvedModelInventory(fixtureModels())}
	factory := sessionEngineFactory(Config{Model: model}, reg, provider, memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, assets, nil)
	built, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = built.Close() }()
	if !built.Engine.HasTool(agentModelDiscoveryToolName) {
		t.Fatalf("factory-built engine is missing %s", agentModelDiscoveryToolName)
	}
	sess := session.New("discovery-prompt", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	run := built.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "find a model"})
	for range run.Events() {
	}
	for _, clause := range []string{agentModelDiscoveryToolName, "provider_id", "model_id", "exact selection handle"} {
		if !strings.Contains(captured.StablePrefix, clause) {
			t.Errorf("StablePrefix missing discovery contract clause %q", clause)
		}
	}
}

func TestAgentModelDiscovery_Scenario2_SafeFiltersOnly(t *testing.T) {
	spec := newAgentModelDiscoveryTool(newResolvedModelInventory(nil)).Spec()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(spec.Schema, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	want := map[string]bool{"provider_id": true, "model_id": true, "limit": true}
	if len(schema.Properties) != len(want) {
		t.Fatalf("filter schema = %v, want safe fields only", schema.Properties)
	}
	for name := range schema.Properties {
		if !want[name] {
			t.Errorf("unsafe/unsupported discovery filter %q", name)
		}
	}
	result, _ := executeDiscovery(t, newResolvedModelInventory(fixtureModels()), `{"endpoint":"https://internal.example","query":"route:secret","credential":"token"}`)
	if !result.IsError {
		t.Fatal("unknown endpoint/query/credential filters must be rejected, not accepted or ignored")
	}
}

func TestAgentModelDiscovery_Scenario2_EmptyFiltersAreOmitted(t *testing.T) {
	inventory := newResolvedModelInventory(fixtureModels())
	for _, tc := range []struct {
		name string
		args string
		want int
	}{
		{name: "both empty", args: `{"provider_id":"","model_id":""}`, want: 3},
		{name: "provider only", args: `{"provider_id":"alpha","model_id":""}`, want: 1},
		{name: "model only", args: `{"provider_id":"","model_id":"unique"}`, want: 1},
		{name: "exact non-empty", args: `{"provider_id":"beta","model_id":"shared-model"}`, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, got := executeDiscovery(t, inventory, tc.args)
			if result.IsError || len(got.Models) != tc.want || got.Available != tc.want {
				t.Fatalf("discovery %s = result=%+v output=%+v, want %d matches", tc.args, result, got, tc.want)
			}
		})
	}
}

func TestAgentModelDiscovery_Scenario2_EmptyFiltersRejectInvalidForms(t *testing.T) {
	inventory := newResolvedModelInventory(fixtureModels())
	invalidUTF8 := "{\"provider_id\":\"bad" + string([]byte{0xff}) + "\"}"
	for _, args := range []string{`{"provider_id":" "}`, `{"model_id":"\t"}`, `{"provider_id":null}`, `{"model_id":null}`, invalidUTF8} {
		result, _ := executeDiscovery(t, inventory, args)
		if !result.IsError {
			t.Errorf("invalid filter %s was accepted: %s", args, result.Content)
		}
	}
}

func TestAgentModelDiscovery_Scenario2_InvalidFiltersDoNotProbeOrSelect(t *testing.T) {
	inventory := newResolvedModelInventory(fixtureModels())
	unknown, got := executeDiscovery(t, inventory, `{"provider_id":"unavailable"}`)
	if unknown.IsError || len(got.Models) != 0 || got.Available != 0 {
		t.Fatalf("unknown provider must return an honest empty result without fallback: result=%+v body=%+v", unknown, got)
	}
	for _, args := range []string{`{"provider_id":" bad "}`, fmt.Sprintf(`{"limit":%d}`, maxAgentModelDiscoveryLimit+1)} {
		result, _ := executeDiscovery(t, inventory, args)
		if !result.IsError {
			t.Errorf("malformed/over-bound filter %s was accepted: %s", args, result.Content)
		}
	}
	if got := inventory.Models(); len(got) != len(fixtureModels()) {
		t.Fatalf("invalid filters changed inventory/session selection state: %+v", got)
	}
}

func TestAgentModelDiscovery_Scenario2_FilterPreservesExactHandles(t *testing.T) {
	_, got := executeDiscovery(t, newResolvedModelInventory(fixtureModels()), `{"model_id":"shared-model","limit":10}`)
	seen := map[string]bool{}
	for _, model := range got.Models {
		seen[model.ProviderID+"\x00"+model.ModelID] = true
	}
	if !seen["alpha\x00shared-model"] || !seen["beta\x00shared-model"] || len(seen) != 2 {
		t.Fatalf("filter merged or rewrote exact handles: %+v", got.Models)
	}
}

func TestAgentModelDiscovery_Scenario3_PerSessionCatalogParity(t *testing.T) {
	ctx := context.Background()
	cfg := fullyLoadedCfg(t)
	provider := mockllm.New(mockllm.TextTurn("ok"))
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	shared, assets, _, _, closeAll, err := buildCatalog(ctx, cfg, reg, provider, hookexec.New(nil), agents.NewRegistry(nil), memstore.New(), nil)
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	defer closeAll()
	selector, closeSelector := assembleCatalog(ctx, cfg, reg, memstore.New(), hookexec.New(nil), &assets, catalogSession{provider: provider, providerID: providerOpenAI, model: cfg.Model})
	defer func() { _ = closeSelector() }()
	for label, catalog := range map[string]*tool.Catalog{"shared": shared, "selector": selector} {
		if _, ok := catalog.Lookup(agentModelDiscoveryToolName); !ok {
			t.Errorf("%s catalog missing %s", label, agentModelDiscoveryToolName)
		}
	}
}

func TestAgentModelDiscovery_Scenario3_NoFSCatalogParity(t *testing.T) {
	ctx := context.Background()
	cfg := fullyLoadedCfg(t)
	provider := mockllm.New(mockllm.TextTurn("ok"))
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	_, assets, _, _, closeAll, err := buildCatalog(ctx, cfg, reg, provider, hookexec.New(nil), agents.NewRegistry(nil), memstore.New(), nil)
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	defer closeAll()
	catalog, closeCatalog := assembleCatalog(ctx, cfg, reg, memstore.New(), hookexec.New(nil), &assets, catalogSession{provider: provider, providerID: providerOpenAI, model: cfg.Model, noFS: true})
	defer func() { _ = closeCatalog() }()
	discovery, ok := catalog.Lookup(agentModelDiscoveryToolName)
	if !ok || !discovery.ReadOnly() {
		t.Fatalf("no-FS catalog must retain read-only %s", agentModelDiscoveryToolName)
	}
	result, _ := discovery.Execute(ctx, session.ToolCall{ID: "no-fs", Name: agentModelDiscoveryToolName, Args: json.RawMessage(`{}`)}, tool.Environment{})
	if result.IsError {
		t.Fatalf("discovery unexpectedly required a filesystem environment: %s", result.Content)
	}
}

func TestAgentModelDiscovery_Scenario3_RefreshFallbackConsistency(t *testing.T) {
	floor := fixtureModels()
	inventory := newResolvedModelInventory(floor)
	svc, err := newTestServerService(server.Config{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(defaultRules(), nil)}), Store: memstore.New(), ModelInventory: inventory})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	assertSame := func(stage string) {
		t.Helper()
		_, got := executeDiscovery(t, inventory, `{}`)
		listed := svc.ListModels(context.Background())
		if len(got.Models) != len(listed) {
			t.Fatalf("%s: discovery=%d ListModels=%d", stage, len(got.Models), len(listed))
		}
	}
	assertSame("before refresh")
	assertSame("during refresh before publish")
	live := []*mecatlv1.ModelInfo{{ProviderId: "alpha", Id: "live", DisplayName: "Live", ContextLimit: 300000}}
	svc.SetModels(live)
	assertSame("after successful refresh")
	inventory.SetModels(floor)
	assertSame("failed refresh retains floor")
	svc.SetModels(nil)
	assertSame("honest empty inventory")
	_, empty := executeDiscovery(t, inventory, `{}`)
	if len(empty.Models) != 0 || empty.Available != 0 || empty.Truncated {
		t.Fatalf("empty inventory invented availability: %+v", empty)
	}
}

func TestInvariant_agent_model_discovery_non_disclosure(t *testing.T) {
	const secret = "sk-super-secret"
	const endpoint = "https://user:pass@internal.example/v1"
	inventory := newResolvedModelInventory(fixtureModels())
	result, _ := executeDiscovery(t, inventory, `{"endpoint":"`+endpoint+`","credential":"`+secret+`","topology":"private-route"}`)
	if !result.IsError {
		t.Fatal("unsafe fields must produce a bounded validation error")
	}
	for _, forbidden := range []string{secret, endpoint, "user:pass", "internal.example", "private-route"} {
		if strings.Contains(result.Content, forbidden) {
			t.Errorf("model-visible validation error disclosed forbidden input %q: %s", forbidden, result.Content)
		}
	}
	if len(result.Content) > maxAgentModelDiscoveryErrorBytes {
		t.Fatalf("validation error exceeds bound: %d > %d", len(result.Content), maxAgentModelDiscoveryErrorBytes)
	}
	okResult, _ := executeDiscovery(t, inventory, `{}`)
	var payload map[string]any
	if err := json.Unmarshal([]byte(okResult.Content), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	for _, forbiddenKey := range []string{"endpoint", "url", "credential", "token", "route", "topology", "error", "error_body"} {
		if _, exists := payload[forbiddenKey]; exists {
			t.Errorf("discovery output exposed forbidden top-level field %q", forbiddenKey)
		}
	}
	allowedModelFields := map[string]bool{"provider_id": true, "model_id": true, "display_name": true, "image": true, "reasoning": true, "context_limit": true}
	for i, rawModel := range payload["models"].([]any) {
		for field := range rawModel.(map[string]any) {
			if !allowedModelFields[field] {
				t.Errorf("discovery model %d exposed non-safe field %q", i, field)
			}
		}
	}
}
