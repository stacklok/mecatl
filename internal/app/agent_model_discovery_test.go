package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type discoveryResult struct {
	Models     []discoveryModel          `json:"models"`
	Providers  []discoveryProviderResult `json:"providers"`
	Returned   int                       `json:"returned"`
	Available  int                       `json:"available"`
	Truncated  bool                      `json:"truncated"`
	NextCursor string                    `json:"next_cursor"`
}

type discoveryProviderResult struct {
	ProviderID string `json:"provider_id"`
	ModelCount int    `json:"model_count"`
}

type discoveryModel struct {
	ProviderID   string `json:"provider_id"`
	ModelID      string `json:"model_id"`
	DisplayName  string `json:"display_name"`
	Image        bool   `json:"image"`
	Reasoning    bool   `json:"reasoning"`
	ContextLimit int64  `json:"context_limit"`
}

func executeDiscovery(t *testing.T, inventory server.ModelInventory, args string) (session.ToolResult, discoveryResult) {
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
		{ProviderId: "beta", Id: "shared-model", DisplayName: "Shared B", Reasoning: true, ContextLimit: 200000, PromptCached: true},
		{ProviderId: "alpha", Id: "shared-model", DisplayName: "Shared A", Image: true, ContextLimit: 128000},
		{ProviderId: "beta", Id: "unique", DisplayName: "Unique", ContextLimit: 64000},
		nil,
		{ProviderId: "", Id: "invalid"},
		{ProviderId: "gamma", Id: ""},
	}
}

func copyDiscoveryModelInfo(model *mecatlv1.ModelInfo) mecatlv1.ModelInfo {
	return mecatlv1.ModelInfo{
		Id:           model.GetId(),
		ProviderId:   model.GetProviderId(),
		DisplayName:  model.GetDisplayName(),
		Image:        model.GetImage(),
		Reasoning:    model.GetReasoning(),
		ContextLimit: model.GetContextLimit(),
		PromptCached: model.GetPromptCached(),
	}
}

func requireSuccess(t *testing.T, result session.ToolResult) {
	t.Helper()
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", result.Content)
	}
}

func mutateDiscoveryCursor(t *testing.T, cursor string, mutate func(*agentModelDiscoveryCursor)) string {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("decode generated cursor: %v", err)
	}
	var envelope agentModelDiscoveryCursor
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		t.Fatalf("decode generated cursor envelope: %v", err)
	}
	mutate(&envelope)
	return encodeAgentModelDiscoveryCursor(envelope)
}

func TestInvariant_agent_model_discovery_Scenario1_SelectableProviderFacets(t *testing.T) {
	result, got := executeDiscovery(t, newTestModelInventory(fixtureModels()), `{}`)
	requireSuccess(t, result)
	want := []discoveryProviderResult{{ProviderID: "alpha", ModelCount: 1}, {ProviderID: "beta", ModelCount: 2}}
	if fmt.Sprint(got.Providers) != fmt.Sprint(want) {
		t.Fatalf("providers = %+v, want %+v", got.Providers, want)
	}
	if len(got.Models) != 3 || got.Models[0].ProviderID != "alpha" || got.Models[1].ProviderID != "beta" || got.Models[1].ModelID != "shared-model" {
		t.Fatalf("models are not canonical distinct handles: %+v", got.Models)
	}
}

func TestInvariant_agent_model_discovery_Scenario1_FacetsAreInitialAndSafe(t *testing.T) {
	inventory := newTestModelInventory(fixtureModels())
	for _, args := range []string{`{"provider_id":"beta"}`, `{"model_id":"shared-model"}`, `{"query":"shared"}`} {
		result, _ := executeDiscovery(t, inventory, args)
		requireSuccess(t, result)
		if strings.Contains(result.Content, `"providers"`) {
			t.Fatalf("filtered result disclosed provider facet: %s", result.Content)
		}
	}
	firstResult, first := executeDiscovery(t, inventory, `{"limit":1}`)
	requireSuccess(t, firstResult)
	continuedResult, _ := executeDiscovery(t, inventory, fmt.Sprintf(`{"cursor":%q}`, first.NextCursor))
	requireSuccess(t, continuedResult)
	if strings.Contains(continuedResult.Content, `"providers"`) {
		t.Fatalf("continuation repeated provider facet: %s", continuedResult.Content)
	}
	emptyResult, empty := executeDiscovery(t, newTestModelInventory(nil), `{}`)
	requireSuccess(t, emptyResult)
	if !strings.Contains(emptyResult.Content, `"providers":[]`) || len(empty.Providers) != 0 || len(empty.Models) != 0 {
		t.Fatalf("empty inventory result is dishonest: %s", emptyResult.Content)
	}
}

func TestInvariant_agent_model_discovery_Scenario1_SingleDiscoveryTool(t *testing.T) {
	ctx := context.Background()
	cfg := fullyLoadedCfg(t)
	provider := mockllm.New(mockllm.TextTurn("ok"))
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	shared, assets, _, _, closeAll, err := buildCatalog(ctx, isolateConfig(t, cfg), reg, provider, hookexec.New(nil), agents.NewRegistry(nil), memstore.New(), nil)
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	defer closeAll()
	selector, closeSelector, _ := assembleCatalog(ctx, cfg, reg, memstore.New(), hookexec.New(nil), &assets, catalogSession{provider: provider, providerID: providerOpenAI, model: cfg.Model})
	defer func() { _ = closeSelector() }()
	noFS, closeNoFS, _ := assembleCatalog(ctx, cfg, reg, memstore.New(), hookexec.New(nil), &assets, catalogSession{provider: provider, providerID: providerOpenAI, model: cfg.Model, noFS: true})
	defer func() { _ = closeNoFS() }()
	for name, catalog := range map[string]*tool.Catalog{"shared": shared, "selector": selector, "no-fs": noFS} {
		if _, ok := catalog.Lookup(agentModelDiscoveryToolName); !ok {
			t.Errorf("%s catalog missing %s", name, agentModelDiscoveryToolName)
		}
		for _, forbidden := range []string{"ListProviders", "DiscoverModelsV2"} {
			if _, ok := catalog.Lookup(forbidden); ok {
				t.Errorf("%s catalog registered forbidden compatibility tool %s", name, forbidden)
			}
		}
	}
}

func TestInvariant_agent_model_discovery_Scenario2_LiteralTermSearch(t *testing.T) {
	models := []*mecatlv1.ModelInfo{
		{ProviderId: "ÄLPHA", Id: "Vision-One", DisplayName: "Fast Reasoner"},
		{ProviderId: "unicode", Id: "é", DisplayName: "composed"},
		{ProviderId: "unicode", Id: "e\u0301", DisplayName: "decomposed"},
	}
	inventory := newTestModelInventory(models)
	result, got := executeDiscovery(t, inventory, `{"query":"  vision\u2003REASON  "}`)
	requireSuccess(t, result)
	if len(got.Models) != 1 || got.Models[0].ModelID != "Vision-One" {
		t.Fatalf("literal AND search across fields = %+v", got.Models)
	}
	_, composed := executeDiscovery(t, inventory, `{"query":"é"}`)
	_, decomposed := executeDiscovery(t, inventory, `{"query":"é"}`)
	if len(composed.Models) != 1 || composed.Models[0].ModelID != "é" || len(decomposed.Models) != 1 || decomposed.Models[0].ModelID != "é" {
		t.Fatalf("query unexpectedly normalized Unicode: composed=%+v decomposed=%+v", composed.Models, decomposed.Models)
	}
}

func TestInvariant_agent_model_discovery_Scenario2_NoQueryLanguage(t *testing.T) {
	models := []*mecatlv1.ModelInfo{
		{ProviderId: "literal", Id: "a.*", DisplayName: "regex"},
		{ProviderId: "literal", Id: "field:value", DisplayName: "field"},
		{ProviderId: "literal", Id: "-fast", DisplayName: "negation"},
		{ProviderId: "literal", Id: "x>10", DisplayName: "comparison"},
		{ProviderId: "literal", Id: `"quoted"`, DisplayName: "quotes"},
	}
	inventory := newTestModelInventory(models)
	for _, term := range []string{"a.*", "field:value", "-fast", "x>10", `"quoted"`} {
		result, got := executeDiscovery(t, inventory, fmt.Sprintf(`{"query":%q}`, term))
		requireSuccess(t, result)
		if len(got.Models) != 1 || got.Models[0].ModelID != term {
			t.Errorf("query %q was interpreted instead of matched literally: %+v", term, got.Models)
		}
	}
	_, noRegex := executeDiscovery(t, inventory, `{"query":".*"}`)
	if len(noRegex.Models) != 1 || noRegex.Models[0].ModelID != "a.*" {
		t.Fatalf("regex-looking term was treated as wildcard: %+v", noRegex.Models)
	}
}

func TestInvariant_agent_model_discovery_Scenario2_QueryValidationAndNonDisclosure(t *testing.T) {
	inventory := newTestModelInventory(fixtureModels())
	for _, omitted := range []string{`{"query":""}`, `{"query":" \u2003 "}`} {
		result, got := executeDiscovery(t, inventory, omitted)
		requireSuccess(t, result)
		if len(got.Models) != 3 || len(got.Providers) != 2 {
			t.Errorf("omitted query %s changed scope: %+v", omitted, got)
		}
	}
	invalidUTF8 := "{\"query\":\"secret" + string([]byte{0xff}) + "\"}"
	cases := []string{
		`{"query":"secret\tterm"}`,
		`{"query":"secret\nterm"}`,
		`{"query":"secret\u0085term"}`,
		fmt.Sprintf(`{"query":%q}`, strings.Repeat("s", 513)),
		`{"query":"one two three four five six seven eight nine"}`,
		fmt.Sprintf(`{"query":%q}`, strings.Repeat("s", 65)),
		fmt.Sprintf(`{"query":%q}`, strings.Repeat("a ", 7)+strings.Repeat("b", 250)),
		invalidUTF8,
	}
	for _, args := range cases {
		result, _ := executeDiscovery(t, inventory, args)
		if !result.IsError || len(result.Content) > maxAgentModelDiscoveryErrorBytes || strings.Contains(result.Content, "secret") {
			t.Errorf("invalid query was not rejected with a bounded nondisclosing error: args=%q result=%+v", args, result)
		}
	}
}

func TestInvariant_agent_model_discovery_Scenario2_AllProviderAndExactProviderSearch(t *testing.T) {
	inventory := newTestModelInventory(fixtureModels())
	_, all := executeDiscovery(t, inventory, `{"query":"shared"}`)
	_, beta := executeDiscovery(t, inventory, `{"provider_id":"beta","query":"shared"}`)
	unknownResult, unknown := executeDiscovery(t, inventory, `{"provider_id":"missing","query":"shared"}`)
	requireSuccess(t, unknownResult)
	if len(all.Models) != 2 || len(beta.Models) != 1 || beta.Models[0].ProviderID != "beta" {
		t.Fatalf("all/exact provider search mismatch: all=%+v beta=%+v", all.Models, beta.Models)
	}
	if len(unknown.Models) != 0 || unknown.Available != 0 || strings.Contains(unknownResult.Content, `"providers"`) {
		t.Fatalf("unknown provider inferred or fell back: %s", unknownResult.Content)
	}
}

func TestInvariant_agent_model_discovery_Scenario2_ExactFilterValidation(t *testing.T) {
	inventory := newTestModelInventory(fixtureModels())
	for _, args := range []string{
		`{"provider_id":" beta"}`,
		`{"provider_id":"beta "}`,
		`{"model_id":"shared\u0085model"}`,
		fmt.Sprintf(`{"provider_id":%q}`, strings.Repeat("p", 513)),
		`{"Provider_Id":"beta"}`,
		`{"pRoViDeR_Id":"beta"}`,
		`{"provider_id":"alpha","provider_id":"beta"}`,
		`{"provider_id":"alpha","Provider_Id":"beta"}`,
		`{"provider_id":null}`,
	} {
		result, _ := executeDiscovery(t, inventory, args)
		if !result.IsError || len(result.Content) > maxAgentModelDiscoveryErrorBytes || strings.Contains(result.Content, "alpha") || strings.Contains(result.Content, "beta") {
			t.Errorf("invalid raw or exact filter accepted or disclosed: args=%q result=%+v", args, result)
		}
	}
	_, exact := executeDiscovery(t, inventory, `{"provider_id":"beta","model_id":"Shared-Model"}`)
	if len(exact.Models) != 0 {
		t.Fatalf("exact filter was normalized: %+v", exact.Models)
	}
	_, first := executeDiscovery(t, inventory, `{"limit":1}`)
	invalidRestoredFilter := mutateDiscoveryCursor(t, first.NextCursor, func(cursor *agentModelDiscoveryCursor) {
		cursor.ProviderID = " beta"
	})
	restored, _ := executeDiscovery(t, inventory, fmt.Sprintf(`{"cursor":%q}`, invalidRestoredFilter))
	if !restored.IsError {
		t.Fatal("cursor-restored exact filter bypassed the first-page validator")
	}
}

func TestInvariant_agent_model_discovery_Scenario3_CursorTraversal(t *testing.T) {
	models := make([]*mecatlv1.ModelInfo, 0, 7)
	for i := 6; i >= 0; i-- {
		models = append(models, &mecatlv1.ModelInfo{ProviderId: "p", Id: fmt.Sprintf("m-%d", i)})
	}
	inventory := newTestModelInventory(models)
	args := `{"limit":2}`
	var ids []string
	for page := 0; page < 10; page++ {
		result, got := executeDiscovery(t, inventory, args)
		requireSuccess(t, result)
		for _, model := range got.Models {
			ids = append(ids, model.ModelID)
		}
		if got.NextCursor == "" {
			if got.Truncated {
				t.Fatal("final page is truncated without a cursor")
			}
			break
		}
		if !got.Truncated {
			t.Fatal("cursor is present while truncated is false")
		}
		args = fmt.Sprintf(`{"cursor":%q}`, got.NextCursor)
	}
	if got, want := strings.Join(ids, ","), "m-0,m-1,m-2,m-3,m-4,m-5,m-6"; got != want {
		t.Fatalf("cursor traversal = %q, want %q", got, want)
	}
}

func TestInvariant_agent_model_discovery_Scenario3_InventoryBoundCursor(t *testing.T) {
	original := fixtureModels()[:3]
	inventory := newTestModelInventory(original)
	_, first := executeDiscovery(t, inventory, `{"limit":1}`)
	if first.NextCursor == "" {
		t.Fatal("first page did not return cursor")
	}
	inventory.publish([]*mecatlv1.ModelInfo{original[2], original[0], original[1]})
	sameResult, same := executeDiscovery(t, inventory, fmt.Sprintf(`{"cursor":%q}`, first.NextCursor))
	requireSuccess(t, sameResult)
	if len(same.Models) != 1 {
		t.Fatalf("reordered identical inventory invalidated cursor: %+v", same)
	}
	variants := map[string][]*mecatlv1.ModelInfo{
		"addition": append(append([]*mecatlv1.ModelInfo(nil), original...), &mecatlv1.ModelInfo{ProviderId: "zeta", Id: "added"}),
		"removal":  original[:2],
	}
	for _, field := range []string{"provider_id", "model_id", "display_name", "image", "reasoning", "context_limit"} {
		changed := append([]*mecatlv1.ModelInfo(nil), original...)
		modelCopy := copyDiscoveryModelInfo(original[0])
		changed[0] = &modelCopy
		switch field {
		case "provider_id":
			changed[0].ProviderId = "changed-provider"
		case "model_id":
			changed[0].Id = "changed-model"
		case "display_name":
			changed[0].DisplayName = "changed-display"
		case "image":
			changed[0].Image = !changed[0].Image
		case "reasoning":
			changed[0].Reasoning = !changed[0].Reasoning
		case "context_limit":
			changed[0].ContextLimit++
		}
		variants[field] = changed
	}
	for name, changed := range variants {
		inventory.publish(changed)
		stale, _ := executeDiscovery(t, inventory, fmt.Sprintf(`{"cursor":%q}`, first.NextCursor))
		if !stale.IsError || !strings.Contains(stale.Content, "restart without a cursor") || strings.Contains(stale.Content, first.NextCursor) {
			t.Errorf("%s did not produce fixed restart error: %+v", name, stale)
		}
	}
}

func TestInvariant_agent_model_discovery_Scenario3_CursorValidation(t *testing.T) {
	inventory := newTestModelInventory(fixtureModels())
	_, first := executeDiscovery(t, inventory, `{"provider_id":"beta","query":"beta","limit":1}`)
	cursor := first.NextCursor
	if cursor == "" {
		t.Fatal("fixture did not produce cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("decode generated cursor: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		t.Fatalf("decode generated envelope: %v", err)
	}
	envelope["unknown"] = true
	unknownJSON, _ := json.Marshal(envelope)
	unknown := base64.RawURLEncoding.EncodeToString(unknownJSON)
	invalidCursors := []string{
		cursor + "=",
		unknown,
		strings.Repeat("a", 4097),
		"%%%",
		mutateDiscoveryCursor(t, cursor, func(value *agentModelDiscoveryCursor) { value.Version++ }),
		mutateDiscoveryCursor(t, cursor, func(value *agentModelDiscoveryCursor) { value.Terms = []string{"UPPER"} }),
		mutateDiscoveryCursor(t, cursor, func(value *agentModelDiscoveryCursor) { value.Terms = []string{"two terms"} }),
		mutateDiscoveryCursor(t, cursor, func(value *agentModelDiscoveryCursor) { value.Terms = make([]string, 9) }),
		mutateDiscoveryCursor(t, cursor, func(value *agentModelDiscoveryCursor) { value.Terms = []string{strings.Repeat("x", 65)} }),
		mutateDiscoveryCursor(t, cursor, func(value *agentModelDiscoveryCursor) {
			value.Terms = []string{strings.Repeat("a", 60), strings.Repeat("b", 60), strings.Repeat("c", 60), strings.Repeat("d", 60), strings.Repeat("e", 20)}
		}),
		mutateDiscoveryCursor(t, cursor, func(value *agentModelDiscoveryCursor) { value.Limit = 0 }),
		mutateDiscoveryCursor(t, cursor, func(value *agentModelDiscoveryCursor) { value.Limit = 51 }),
		mutateDiscoveryCursor(t, cursor, func(value *agentModelDiscoveryCursor) { value.Offset = 0 }),
		mutateDiscoveryCursor(t, cursor, func(value *agentModelDiscoveryCursor) { value.Offset = 100 }),
		mutateDiscoveryCursor(t, cursor, func(value *agentModelDiscoveryCursor) { value.Digest = "BAD" }),
	}
	invalidArgs := []string{
		fmt.Sprintf(`{"cursor":%q,"provider_id":"beta"}`, cursor),
		fmt.Sprintf(`{"cursor":%q,"query":"shared"}`, cursor),
		fmt.Sprintf(`{"cursor":%q,"limit":1}`, cursor),
	}
	for _, invalidCursor := range invalidCursors {
		invalidArgs = append(invalidArgs, fmt.Sprintf(`{"cursor":%q}`, invalidCursor))
	}
	for _, invalidCursor := range invalidCursors {
		args := fmt.Sprintf(`{"cursor":%q}`, invalidCursor)
		result, _ := executeDiscovery(t, inventory, args)
		if !result.IsError || len(result.Content) > maxAgentModelDiscoveryErrorBytes || strings.Contains(result.Content, invalidCursor) {
			t.Errorf("invalid cursor accepted or disclosed: args length=%d result=%+v", len(args), result)
		}
	}
	for _, args := range invalidArgs[:3] {
		result, _ := executeDiscovery(t, inventory, args)
		if !result.IsError || len(result.Content) > maxAgentModelDiscoveryErrorBytes || strings.Contains(result.Content, cursor) {
			t.Errorf("invalid cursor arguments accepted or disclosed: args length=%d result=%+v", len(args), result)
		}
	}
	oversizedFilter := strings.Repeat("<", maxAgentModelDiscoveryFilterBytes)
	oversizedInventory := newTestModelInventory([]*mecatlv1.ModelInfo{{ProviderId: oversizedFilter, Id: "one"}, {ProviderId: oversizedFilter, Id: "two"}})
	unrepresentableCursor, _ := executeDiscovery(t, oversizedInventory, fmt.Sprintf(`{"provider_id":%q,"limit":1}`, oversizedFilter))
	if !unrepresentableCursor.IsError || unrepresentableCursor.Content != agentModelDiscoveryOutputError {
		t.Fatalf("overlong emitted cursor was usable instead of rejected: %+v", unrepresentableCursor)
	}
	result, got := executeDiscovery(t, inventory, fmt.Sprintf(`{"cursor":%q}`, cursor))
	requireSuccess(t, result)
	if len(got.Models) != 1 || got.Models[0].ProviderID != "beta" {
		t.Fatalf("cursor did not restore complete scope: %+v", got)
	}
}

func TestInvariant_agent_model_discovery_Scenario3_ByteBoundMakesProgress(t *testing.T) {
	models := make([]*mecatlv1.ModelInfo, 0, 50)
	for i := range 50 {
		models = append(models, &mecatlv1.ModelInfo{ProviderId: "p", Id: fmt.Sprintf("m-%02d", i), DisplayName: strings.Repeat("x", 1200)})
	}
	inventory := newTestModelInventory(models)
	result, first := executeDiscovery(t, inventory, `{"limit":50}`)
	requireSuccess(t, result)
	if len(result.Content) > maxAgentModelDiscoveryOutputBytes || first.Returned == 0 || first.Returned >= 50 || first.NextCursor == "" || !first.Truncated {
		t.Fatalf("byte-bounded first page made no progress or lied: bytes=%d result=%+v", len(result.Content), first)
	}
	continuedResult, continued := executeDiscovery(t, inventory, fmt.Sprintf(`{"cursor":%q}`, first.NextCursor))
	requireSuccess(t, continuedResult)
	if continued.Returned == 0 || continued.Models[0].ModelID != fmt.Sprintf("m-%02d", first.Returned) || strings.Contains(continuedResult.Content, `"providers"`) {
		t.Fatalf("continuation skipped first unreturned row or repeated facet: %+v", continued)
	}
	facetModels := make([]*mecatlv1.ModelInfo, 0, 1000)
	for i := range 1000 {
		facetModels = append(facetModels, &mecatlv1.ModelInfo{ProviderId: fmt.Sprintf("p%04d", i), Id: "same"})
	}
	facetInventory := newTestModelInventory(facetModels)
	facetOnly, _ := executeDiscovery(t, facetInventory, `{"limit":1}`)
	if !facetOnly.IsError || facetOnly.Content != agentModelDiscoveryOutputError {
		t.Fatalf("unrepresentable provider facet did not return bounded error: %+v", facetOnly)
	}
	filteredFacet, filtered := executeDiscovery(t, facetInventory, `{"query":"same","limit":1}`)
	requireSuccess(t, filteredFacet)
	if filtered.NextCursor == "" || strings.Contains(filteredFacet.Content, `"providers"`) {
		t.Fatalf("filtered page did not remain usable without facets: %+v", filtered)
	}
	continuedFacet, _ := executeDiscovery(t, facetInventory, fmt.Sprintf(`{"cursor":%q}`, filtered.NextCursor))
	if continuedFacet.IsError || strings.Contains(continuedFacet.Content, `"providers"`) {
		t.Fatalf("cursor page did not remain usable without facets: %+v", continuedFacet)
	}
	unrepresentable := newTestModelInventory([]*mecatlv1.ModelInfo{{ProviderId: strings.Repeat("p", maxAgentModelDiscoveryOutputBytes), Id: "m"}})
	tooLarge, _ := executeDiscovery(t, unrepresentable, `{}`)
	if !tooLarge.IsError || len(tooLarge.Content) > maxAgentModelDiscoveryErrorBytes {
		t.Fatalf("unrepresentable page did not return bounded error: %+v", tooLarge)
	}
}

func TestInvariant_agent_model_discovery_Scenario4_SystemPromptContainsWorkflowNotInventory(t *testing.T) {
	const model = "gpt-5"
	const inventoryMarker = "private-live-model-marker"
	const inventoryCount = 10007
	liveInventory := make([]*mecatlv1.ModelInfo, 0, inventoryCount)
	for i := range inventoryCount {
		liveInventory = append(liveInventory, &mecatlv1.ModelInfo{ProviderId: "live-provider-marker", Id: fmt.Sprintf("%s-%d", inventoryMarker, i)})
	}
	var captured prompt.Layered
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { captured = req.System })}, mockllm.TextTurn("ok"))
	reg := regForTest(provider, providerOpenAI, model)
	assets := catalogAssets{modelInventory: newTestModelInventory(liveInventory)}
	factory := sessionEngineFactory(Config{Model: model}, reg, provider, memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, assets, nil)
	built, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = built.Close() }()
	sess := session.New("discovery-prompt", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	run := built.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "find a model"})
	for range run.Events() {
	}
	for _, clause := range []string{"DiscoverModels", "without provider_id", "all selectable providers", "exact (provider_id, model_id)", "cannot switch the session", "return it to the caller"} {
		if !strings.Contains(captured.StablePrefix, clause) {
			t.Errorf("StablePrefix missing workflow clause %q", clause)
		}
	}
	for _, forbidden := range []string{inventoryMarker, "live-provider-marker", strconv.Itoa(len(liveInventory))} {
		if strings.Contains(captured.StablePrefix, forbidden) {
			t.Errorf("StablePrefix embedded live inventory value %q", forbidden)
		}
	}
}

func TestInvariant_agent_model_discovery_Scenario4_ToolSpecificationContract(t *testing.T) {
	spec := newAgentModelDiscoveryTool(newTestModelInventory(nil)).Spec()
	for _, clause := range []string{"strings.Fields", "strings.ToLower", "all selectable providers", "exact", "cursor", "restart without a cursor", "providers", "32 KiB", "never probes", "never selects"} {
		if !strings.Contains(spec.Description, clause) {
			t.Errorf("tool description missing actionable contract clause %q: %s", clause, spec.Description)
		}
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(spec.Schema, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	for _, field := range []string{"provider_id", "model_id", "query", "cursor", "limit"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Errorf("replacement schema missing %s", field)
		}
	}
	if len(schema.Properties) != 5 {
		t.Fatalf("replacement schema has unexpected properties: %v", schema.Properties)
	}
}

func TestInvariant_agent_model_discovery_Scenario4_SharedLiveInventory(t *testing.T) {
	published := &mecatlv1.ModelInfo{ProviderId: "p", Id: "m", DisplayName: "original", Image: true, Reasoning: true, ContextLimit: 42, PromptCached: true}
	inventory := newTestModelInventory([]*mecatlv1.ModelInfo{published})
	published.ProviderId, published.Id, published.DisplayName, published.Image, published.Reasoning, published.ContextLimit, published.PromptCached = "mutated", "mutated", "mutated", false, false, 0, false
	first := inventory.CurrentModelSnapshot().Models
	if len(first) != 1 || first[0].ProviderId != "p" || first[0].Id != "m" || first[0].DisplayName != "original" || !first[0].Image || !first[0].Reasoning || first[0].ContextLimit != 42 || !first[0].PromptCached {
		t.Fatalf("publisher mutation changed inventory: %+v", first)
	}
	first[0].ProviderId, first[0].DisplayName = "reader-mutated", "reader-mutated"
	second := inventory.CurrentModelSnapshot().Models
	if second[0].ProviderId != "p" || second[0].DisplayName != "original" {
		t.Fatalf("reader mutation changed inventory: %+v", second)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				rows := inventory.CurrentModelSnapshot().Models
				rows[0].DisplayName = "concurrent-reader-mutation"
				result, got := executeDiscovery(t, inventory, `{}`)
				if result.IsError || len(got.Models) != 1 || got.Models[0].DisplayName != "original" {
					t.Errorf("discovery observed mutable inventory: result=%+v got=%+v", result, got)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestInvariant_agent_model_discovery_Scenario4_PermissionPostureUnchanged(t *testing.T) {
	discovery := newAgentModelDiscoveryTool(newTestModelInventory(nil))
	if !discovery.ReadOnly() {
		t.Fatal("DiscoverModels must remain read-only")
	}
	call := session.NewToolCall("id", agentModelDiscoveryToolName, json.RawMessage(`{}`))
	for name, rules := range map[string][]governance.Rule{"default": defaultRules(), "production": mainRules(Config{})} {
		decision := permpolicy.NewPolicy(rules, nil).Evaluate(context.Background(), "s1", session.ModeDefault, call, nil)
		if decision.Effect != governance.Ask {
			t.Errorf("%s permission posture = %v, want unchanged Ask", name, decision.Effect)
		}
	}
	for _, rule := range mainRules(Config{}) {
		if rule.Tool == agentModelDiscoveryToolName {
			t.Fatalf("DiscoverModels gained a special permission rule: %+v", rule)
		}
	}
}
