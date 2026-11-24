package app

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	"github.com/stacklok/mecatl/internal/adapter/openaicompat"
	"github.com/stacklok/mecatl/internal/adapter/openrouter"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/provider/anthropic"
)

func jsonClient(body string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	})}
}

func assertEffortLevels(t *testing.T, label string, got effortLevels, known bool, levels ...string) {
	t.Helper()
	if got.known != known || !slices.Equal(got.levels, levels) {
		t.Errorf("%s: effort levels = %+v, want known=%v levels=%v", label, got, known, levels)
	}
}

func TestKnownEffortLevelsKeepsVocabularyOrder(t *testing.T) {
	assertEffortLevels(t, "mixed tokens", knownEffortLevels([]string{"xhigh", "none", " High ", "minimal", "low", "turbo"}), true, "low", "high", "xhigh")
	assertEffortLevels(t, "only tokens outside the vocabulary", knownEffortLevels([]string{"none", "minimal"}), true)
	assertEffortLevels(t, "no tokens", knownEffortLevels(nil), true)
}

// TestEffortVocabularyMatchesSetting keeps the composition vocabulary and the
// tokens models.effort_levels accepts in step.
func TestEffortVocabularyMatchesSetting(t *testing.T) {
	if !slices.Equal(effortVocabulary, permconfig.EffortLevelTokens) {
		t.Fatalf("effortVocabulary = %v, permconfig.EffortLevelTokens = %v", effortVocabulary, permconfig.EffortLevelTokens)
	}
	for _, level := range effortVocabulary {
		if got, ok := NormalizeReasoningEffort(level); !ok || got != level {
			t.Fatalf("NormalizeReasoningEffort(%q) = %q, %v; every level must be a valid effort", level, got, ok)
		}
	}
}

func TestModelEffortLevelsPrecedence(t *testing.T) {
	view := &discoverySnapshot{providers: map[string]discoveryProvider{
		providerAnthropic: {observations: []modelEntry{
			{ID: "claude-opus-4-5", Effort: knownEffortLevels([]string{"low"})},
			{ID: "claude-opus-4-7", Effort: knownEffortLevels(nil)},
			{ID: "claude-haiku-4-5"},
		}},
		providerToolhive:   {observations: []modelEntry{{ID: "gateway-only"}}},
		providerOpenRouter: {observations: []modelEntry{{ID: "anthropic/claude-sonnet-4.5", Reasoning: true}}},
	}}
	cfg := Config{effortLevels: map[string]map[string][]string{
		providerAnthropic: {"claude-opus-4-5": {"max", "high"}, "claude-sonnet-4-5": {}},
	}}
	for _, tc := range []struct {
		name, pid, model string
		source           effortLevelsSource
		known            bool
		levels           []string
	}{
		{"setting beats live", providerAnthropic, "claude-opus-4-5", effortLevelsFromConfig, true, []string{"high", "max"}},
		{"empty setting is known none", providerAnthropic, "claude-sonnet-4-5", effortLevelsFromConfig, true, nil},
		{"live known none beats catalog levels", providerAnthropic, "claude-opus-4-7", effortLevelsFromLive, true, nil},
		{"live unknown falls through to catalog", providerAnthropic, "claude-haiku-4-5", effortLevelsFromCatalog, true, nil},
		{"catalog levels without live data", providerOpenAI, "gpt-5", effortLevelsFromCatalog, true, []string{"low", "medium", "high"}},
		{"gateway Anthropic route reads the Anthropic catalog", providerToolhiveAnthropic, "claude-opus-4-5", effortLevelsFromCatalog, true, []string{"low", "medium", "high"}},
		{"gateway OpenAI route has no catalog", providerToolhive, "gateway-only", effortLevelsUnknown, false, nil},
		{"unlisted model on an unknown provider", "custom", "anything", effortLevelsUnknown, false, nil},
		// OpenRouter turns an effort level into a thinking budget for a reasoning
		// model without effort levels, so the catalog's missing option says nothing.
		{"OpenRouter live reasoning model without levels skips the catalog", providerOpenRouter, "anthropic/claude-sonnet-4.5", effortLevelsUnknown, false, nil},
		{"OpenRouter catalogued reasoning model without an effort option", providerOpenRouter, "anthropic/claude-opus-4.5", effortLevelsUnknown, false, nil},
		{"OpenRouter Anthropic route reads the same rule", providerOpenRouterAnthropic, "anthropic/claude-opus-4.5", effortLevelsUnknown, false, nil},
		{"OpenRouter catalogued effort option", providerOpenRouter, "openai/gpt-5", effortLevelsFromCatalog, true, []string{"low", "medium", "high"}},
		{"OpenRouter catalogued model that doesn't reason", providerOpenRouter, "amazon/nova-lite-v1", effortLevelsFromCatalog, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveModelEffortLevels(cfg, view, tc.pid, tc.model)
			if got.source != tc.source {
				t.Fatalf("source = %q, want %q", got.source, tc.source)
			}
			assertEffortLevels(t, tc.name, got.effortLevels, tc.known, tc.levels...)
		})
	}
	if got := resolveModelEffortLevels(Config{}, nil, providerOpenAI, "gpt-5"); got.source != effortLevelsFromCatalog {
		t.Fatalf("nil view source = %q, want catalog", got.source)
	}
}

// TestGatewayListerEffortLevels pins how an OpenAI-compatible listing's
// reasoning.supported_efforts is read: a listing that publishes levels makes an
// omitted model known to take none; a listing that publishes none leaves every
// model unknown, as a gateway that predates the field does.
func TestGatewayListerEffortLevels(t *testing.T) {
	const publishing = `{"object":"list","data":[` +
		`{"id":"listed","reasoning":{"supported_efforts":["xhigh","minimal","high","low"]}},` +
		`{"id":"omitted"},` +
		`{"id":"only-none","reasoning":{"supported_efforts":["none"]}}]}`
	rows, err := gatewayLister{inner: openaicompat.NewLister("https://gateway.example/v1", "k", jsonClient(publishing))}.ListModels(context.Background())
	if err != nil || len(rows) != 3 {
		t.Fatalf("ListModels: %v, %v", rows, err)
	}
	assertEffortLevels(t, "listed", rows[0].Effort, true, "low", "high", "xhigh")
	assertEffortLevels(t, "omitted from a publishing listing", rows[1].Effort, true)
	assertEffortLevels(t, "only none", rows[2].Effort, true)

	const silent = `{"object":"list","data":[{"id":"a"},{"id":"b","reasoning":{"mandatory":true}}]}`
	for _, lister := range []modelLister{
		gatewayLister{inner: openaicompat.NewLister("https://gateway.example/v1", "k", jsonClient(silent))},
		openCodeLister{inner: openaicompat.NewLister("https://opencode.example/v1", "k", jsonClient(silent))},
	} {
		rows, err := lister.ListModels(context.Background())
		if err != nil || len(rows) != 2 {
			t.Fatalf("ListModels: %v, %v", rows, err)
		}
		for _, row := range rows {
			assertEffortLevels(t, row.ID+" on a listing without levels", row.Effort, false)
		}
	}
}

func TestOpenRouterListerEffortLevels(t *testing.T) {
	const body = `{"data":[` +
		`{"id":"vendor/listed","supported_parameters":["reasoning","tools"],"reasoning":{"supported_efforts":["max","high","none"]}},` +
		`{"id":"vendor/reasoning-only","supported_parameters":["reasoning"],"reasoning":{"mandatory":false}},` +
		`{"id":"vendor/no-reasoning","supported_parameters":["tools"]}]}`
	rows, err := openRouterLister{inner: openrouter.NewLister(jsonClient(body))}.ListModels(context.Background())
	if err != nil || len(rows) != 3 {
		t.Fatalf("ListModels: %v, %v", rows, err)
	}
	assertEffortLevels(t, "listed", rows[0].Effort, true, "high", "max")
	assertEffortLevels(t, "reasoning without levels", rows[1].Effort, false)
	assertEffortLevels(t, "no reasoning parameter", rows[2].Effort, true)
}

func TestAnthropicListerEffortLevels(t *testing.T) {
	const body = `{"data":[` +
		`{"id":"three","capabilities":{"effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},"xhigh":{"supported":false},"max":{"supported":false}}}},` +
		`{"id":"none","capabilities":{"effort":{"supported":false}}},` +
		`{"id":"silent"}],"has_more":false}`
	rows, err := anthropicLister{inner: anthropic.NewLister("sk-test", "", jsonClient(body))}.ListModels(context.Background())
	if err != nil || len(rows) != 3 {
		t.Fatalf("ListModels: %v, %v", rows, err)
	}
	assertEffortLevels(t, "three", rows[0].Effort, true, "low", "medium", "high")
	assertEffortLevels(t, "none", rows[1].Effort, true)
	assertEffortLevels(t, "silent", rows[2].Effort, false)
}

func TestEmbeddedModelsCarryEffortLevels(t *testing.T) {
	byID := map[string]modelEntry{}
	for _, m := range embeddedModels(providerAnthropic) {
		byID[m.ID] = m
	}
	assertEffortLevels(t, "claude-opus-4-5", byID["claude-opus-4-5"].Effort, true, "low", "medium", "high")
	assertEffortLevels(t, "claude-haiku-4-5", byID["claude-haiku-4-5"].Effort, true)
}

func TestDiscoveryObservationsCopyEffortLevels(t *testing.T) {
	source := []modelEntry{{ID: "m", Effort: knownEffortLevels([]string{"low", "high"})}}
	clone := cloneModelEntries(source)
	clone[0].Effort.levels[0] = "mutated"
	if source[0].Effort.levels[0] != "low" {
		t.Fatalf("cloneModelEntries shares effort levels with its source")
	}
}

func TestCodexListerEffortLevels(t *testing.T) {
	const body = `{"models":[` +
		`{"slug":"listed","visibility":"list","supported_reasoning_levels":[{"effort":"xhigh"},{"effort":"minimal"},{"effort":"low"}]},` +
		`{"slug":"no-levels","visibility":"list","supported_reasoning_levels":[]},` +
		`{"slug":"silent","visibility":"list"}]}`
	policy, err := openaicodex.NewRequestPolicy(codexRegistryCredential(t), func() time.Time { return codexRegistryNow },
		roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}, Request: req}, nil
		}))
	if err != nil {
		t.Fatalf("NewRequestPolicy: %v", err)
	}
	rows, err := openAICodexLister{inner: openaicodex.NewLister(policy)}.ListModels(context.Background())
	if err != nil || len(rows) != 3 {
		t.Fatalf("ListModels: %v, %v", rows, err)
	}
	assertEffortLevels(t, "listed", rows[0].Effort, true, "low", "xhigh")
	assertEffortLevels(t, "no levels", rows[1].Effort, true)
	assertEffortLevels(t, "silent", rows[2].Effort, false)
}
