package app

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	"github.com/stacklok/mecatl/internal/adapter/openaicompat"
	"github.com/stacklok/mecatl/internal/adapter/openrouter"
	"github.com/stacklok/mecatl/internal/adapter/providercatalog"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/provider/anthropic"
)

const liveModelRefreshTimeout = 10 * time.Second

func liveRefreshDelay(cfg Config) time.Duration {
	if cfg.liveModelRefreshDelay > 0 {
		return cfg.liveModelRefreshDelay
	}
	if d, err := time.ParseDuration(os.Getenv("MECATL_LIVE_MODEL_REFRESH_DELAY")); err == nil && d > 0 {
		return d
	}
	return 0
}

func anyProviderHasLister(reg *providerRegistry) bool {
	if reg == nil {
		return false
	}
	for _, pid := range reg.Available() {
		if entry, ok := reg.Lookup(pid); ok && entry.lister != nil {
			return true
		}
	}
	return false
}

// modelEntry is source-neutral observed metadata. Nil modalities mean unknown;
// a non-nil declaration, including an empty one, is authoritative. An omitted
// reasoning capability can be marked unknown without changing other listers'
// existing interpretation of a listed Reasoning:false row. Effort's zero value
// is unknown, so a lister that can't tell leaves it unset.
type modelEntry struct {
	ID               string
	DisplayName      string
	ContextLimit     int
	OutputLimit      int
	InputModalities  []string
	Reasoning        bool
	reasoningUnknown bool // a listed model may omit reasoning support; false preserves legacy known-row behavior
	ToolCall         bool
	Thinking         thinkingDescriptor
	Effort           effortLevels
}

type thinkingDescriptor struct{ Known, Adaptive, Enabled bool }

// modelLister is borrowed by the Build-local discovery owner. Implementations
// must honor cancellation; callers never own the shared fetch context.
type modelLister interface {
	ListModels(context.Context) ([]modelEntry, error)
}

type openRouterLister struct{ inner *openrouter.Lister }
type openRouterAnthropicLister struct{ inner openRouterLister }

func (l openRouterAnthropicLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	raw, err := l.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]modelEntry, 0, len(raw))
	for _, m := range raw {
		if isOpenRouterAnthropicModel(m.ID) {
			out = append(out, m)
		}
	}
	return out, nil
}

// Codex membership comes only from entitlements. Catalog enrichment of omitted
// context windows belongs to resolution, never to live observations.
type openAICodexLister struct{ inner *openaicodex.Lister }

func (l openAICodexLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	raw, err := l.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	metadata := make(map[string]modelEntry)
	for _, m := range embeddedModels(providerOpenAI) {
		metadata[m.ID] = m
	}
	out := make([]modelEntry, 0, len(raw))
	for _, m := range raw {
		entry := modelEntry{ID: m.ID, DisplayName: m.DisplayName, ContextLimit: m.ContextLimit, Reasoning: m.Reasoning, ToolCall: m.ToolCall}
		if m.InputModalitiesKnown {
			entry.InputModalities = append([]string{}, m.InputModalities...)
		}
		catalog, catalogued := metadata[m.ID]
		if entry.DisplayName == "" && catalogued {
			entry.DisplayName = catalog.DisplayName
		}
		if !m.InputModalitiesKnown {
			if catalogued && len(catalog.InputModalities) > 0 {
				entry.InputModalities = append([]string(nil), catalog.InputModalities...)
			} else {
				entry.InputModalities = []string{"text"}
				if openaiStaticCaps.Image {
					entry.InputModalities = append(entry.InputModalities, "image")
				}
				if openaiStaticCaps.Audio {
					entry.InputModalities = append(entry.InputModalities, "audio")
				}
			}
		}
		if !m.ReasoningKnown && catalogued {
			entry.Reasoning = catalog.Reasoning
		}
		if m.ReasoningKnown {
			entry.Effort = knownEffortLevels(m.ReasoningEfforts)
		}
		out = append(out, entry)
	}
	return out, nil
}

func (l openRouterLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	raw, err := l.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]modelEntry, 0, len(raw))
	for _, m := range raw {
		out = append(out, modelEntry{ID: m.ID, DisplayName: m.DisplayName, ContextLimit: m.ContextLimit, OutputLimit: m.OutputLimit, InputModalities: m.InputModalities, Reasoning: m.Reasoning, ToolCall: m.ToolCall,
			Effort: openRouterEffortLevels(m)})
	}
	return out, nil
}

// openRouterEffortLevels reads OpenRouter's reasoning metadata. A model that
// doesn't accept the reasoning parameter takes no effort; a reasoning model
// without supported_efforts hasn't said which levels it takes.
func openRouterEffortLevels(m openrouter.Model) effortLevels {
	switch {
	case !m.Reasoning:
		return knownEffortLevels(nil)
	case m.ReasoningEffortsListed:
		return knownEffortLevels(m.ReasoningEfforts)
	}
	return effortLevels{}
}

type anthropicLister struct{ inner *anthropic.Lister }

func (l anthropicLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	raw, err := l.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]modelEntry, 0, len(raw))
	for _, m := range raw {
		mods := []string{"text"}
		if m.Image {
			mods = append(mods, "image")
		}
		entry := modelEntry{ID: m.ID, DisplayName: m.DisplayName, ContextLimit: m.ContextLimit, OutputLimit: m.OutputLimit, InputModalities: mods,
			Reasoning: m.Thinking.Adaptive || m.Thinking.Enabled, reasoningUnknown: !m.Thinking.Known, ToolCall: true,
			Thinking: thinkingDescriptor{Known: m.Thinking.Known, Adaptive: m.Thinking.Adaptive, Enabled: m.Thinking.Enabled}}
		if m.Effort.Known {
			entry.Effort = knownEffortLevels(m.Effort.Levels)
		}
		out = append(out, entry)
	}
	return out, nil
}

type gatewayLister struct{ inner *openaicompat.Lister }

func (l gatewayLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	raw, err := l.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	publishes := publishesEffortLevels(raw)
	out := make([]modelEntry, 0, len(raw))
	for _, m := range raw {
		out = append(out, modelEntry{ID: m.ID, DisplayName: m.DisplayName, ContextLimit: m.ContextLimit, InputModalities: m.InputModalities, ToolCall: true,
			Effort: gatewayEffortLevels(m, publishes)})
	}
	return out, nil
}

// publishesEffortLevels reports whether an OpenAI-compatible listing carries
// reasoning.supported_efforts for any model.
func publishesEffortLevels(raw []openaicompat.Model) bool {
	return slices.ContainsFunc(raw, func(m openaicompat.Model) bool { return m.ReasoningEffortsListed })
}

// gatewayEffortLevels reads one model of an OpenAI-compatible listing. A listing
// that publishes levels omits them for models that take no effort, so an omitted
// model is known to take none. A listing that publishes no levels at all, such as
// a gateway that predates the field, says nothing, and its models stay unknown.
func gatewayEffortLevels(m openaicompat.Model, listingPublishesLevels bool) effortLevels {
	switch {
	case m.ReasoningEffortsListed:
		return knownEffortLevels(m.ReasoningEfforts)
	case listingPublishesLevels:
		return knownEffortLevels(nil)
	}
	return effortLevels{}
}

type openCodeLister struct{ inner *openaicompat.Lister }

func (l openCodeLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	raw, err := l.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	publishes := publishesEffortLevels(raw)
	out := make([]modelEntry, 0, len(raw))
	for _, m := range raw {
		out = append(out, modelEntry{ID: m.ID, DisplayName: m.DisplayName, ContextLimit: m.ContextLimit, ToolCall: true, InputModalities: []string{"text", "image"},
			Effort: gatewayEffortLevels(m, publishes)})
	}
	return out, nil
}

func providerInventoryFloor(reg *providerRegistry, pid string) []modelEntry {
	if embedded := embeddedModels(pid); len(embedded) > 0 {
		return embedded
	}
	return customProviderInventoryFloor(reg, pid)
}

func customProviderInventoryFloor(reg *providerRegistry, pid string) []modelEntry {
	if reg != nil {
		if entry, ok := reg.Lookup(pid); ok && entry.defaultModel != "" {
			return []modelEntry{{ID: entry.defaultModel}}
		}
	}
	return nil
}

func embeddedModels(pid string) []modelEntry {
	p, ok := providercatalog.Default().Provider(pid)
	if !ok {
		return nil
	}
	out := make([]modelEntry, 0, len(p.Models()))
	for _, m := range p.Models() {
		out = append(out, modelEntry{ID: m.ID(), DisplayName: m.Name(), ContextLimit: m.ContextLimit(), OutputLimit: m.OutputLimit(), InputModalities: append([]string{}, m.InputModalities()...), Reasoning: m.SupportsReasoning(), ToolCall: m.SupportsToolCall(),
			Effort: knownEffortLevels(m.EffortLevels())})
	}
	return out
}

// This is a metadata namespace, not an inventory/entitlement namespace.
func metadataCatalogProviderID(pid string) string {
	switch pid {
	case providerOpenAICodex:
		return providerOpenAI
	case providerOpenRouterAnthropic:
		return providerOpenRouter
	case providerToolhiveAnthropic:
		return providerAnthropic
	}
	return pid
}

func sortModelInfos(out []*mecatlv1.ModelInfo) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetProviderId() != out[j].GetProviderId() {
			return out[i].GetProviderId() < out[j].GetProviderId()
		}
		return out[i].GetId() < out[j].GetId()
	})
}

func mergeCustomProviderFloor(reg *providerRegistry, pid string, live []modelEntry) []modelEntry {
	floor := customProviderInventoryFloor(reg, pid)
	if len(floor) == 0 {
		return live
	}
	for _, m := range live {
		if m.ID == floor[0].ID {
			return live
		}
	}
	return append(floor, live...)
}

type providerStatus struct{ State, Hint string }

func awaitContextWindow(ctx context.Context, reg *providerRegistry, pid, model string) error {
	return awaitContextWindowWithin(ctx, reg, pid, model, liveModelRefreshTimeout)
}

func awaitContextWindowWithin(ctx context.Context, reg *providerRegistry, pid, model string, wait time.Duration) error {
	if reg == nil {
		return nil
	}
	cfg := Config{ContextWindowOverride: reg.contextWindowOverride, contextWindows: reg.contextWindows}
	if resolveModelWindow(cfg, reg.discovery.snapshot(), pid, model).admissible {
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	view, err := reg.discovery.request(bounded, pid, discoveryAdmission)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && reg.discovery.ctx.Err() != nil {
		return err
	}
	if resolveModelWindow(cfg, view, pid, model).admissible {
		return nil
	}
	return contextWindowUnavailable(pid, model)
}

func contextWindowUnavailable(pid, model string) error {
	return fmt.Errorf("%w: context window discovery unavailable for provider %q model %q; restore model discovery or set an exact override (e.g. models.context_windows.%s.%s: 128000) in your settings, then retry", server.ErrContextWindowUnavailable, pid, model, pid, model)
}
