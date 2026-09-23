// Package providercatalog is a stdlib-only LEAF DATA adapter exposing a pinned,
// embed-vendored subset of the models.dev model catalog as a typed, read-only Go
// API. It carries no behaviour beyond parsing the embedded JSON once and handing
// out immutable-by-convention value types; nothing here knows about the agent,
// providers, ports, or the domain.
//
// # Source, provenance, and license
//
// The embedded data in models.dev.curated.json is a subset of the community model
// catalog published at:
//
//	https://models.dev/api.json
//
// canonical repository https://github.com/anomalyco/models.dev, distributed
// under the MIT License ("Copyright (c) 2025 models.dev"). The full MIT license
// text + attribution is vendored alongside this file in MODELS_DEV_LICENSE, as
// the MIT license requires when redistributing portions of the work.
//
// The catalog API exposes NO version field (the upstream endpoint has none), so
// the fetch date IS the pin. Pinned 2026-07-13.
//
// # Curation policy (full vendor for in-scope providers)
//
// The full catalog has 145+ providers and thousands of models. We vendor ONLY the
// three providers in scope for multi-provider Phase 0/1 — but for each of them we
// vendor ALL models (no hand-pinned allowlist). The DROPPED surface, stated plainly:
//
//   - 142+ providers dropped wholesale (out of P0/P1 scope: Chat-Completions
//     providers like Gemini-native / Together are P2 and need their own adapter,
//     so their catalog entries are not useful yet).
//   - openai: ALL models vendored (P0 native via the Responses adapter).
//   - anthropic: ALL models vendored (P1 native Messages adapter).
//   - openrouter: ALL models vendored (the OpenRouter flagship+long-tail set —
//     hundreds of routes across dozens of upstream providers).
//
// The catalog is kept fresh by a weekly CI job
// (.github/workflows/catalog-refresh.yml) that re-fetches models.dev/api.json,
// regenerates the curated JSON, and opens a PR if it changed. An unknown
// provider/model id is an honest (_, false) lookup miss, never a silent
// substitution. The Go structs deliberately do NOT parse cost / release_date /
// knowledge etc.; the curated JSON KEEPS those fields (the jq projection copies
// whole model objects) so a future slice can surface them without re-pinning
// (encoding/json ignores unmapped fields).
//
// # Deterministic regeneration (reproducible re-pin)
//
// The curated file is generated from the full source by ONE deterministic jq
// filter; -S (sort keys) makes the embedded bytes stable across re-pins so a diff
// shows only real model changes. To re-pin:
//
//	# 1. Re-fetch the pinned source (record the date in the comment above):
//	curl -s https://models.dev/api.json > .scratch/models.dev.full.json
//
//	# 2. Regenerate the curated subset deterministically (ALL models for the 3
//	#    in-scope providers — no allowlist):
//	jq -S '{
//	  openai:     ( .openai     | {id, env, npm, api, name, doc, models} ),
//	  anthropic:  ( .anthropic  | {id, env, npm, api, name, doc, models} ),
//	  openrouter: ( .openrouter | {id, env, npm, api, name, doc, models} )
//	}' .scratch/models.dev.full.json > internal/adapter/providercatalog/models.dev.curated.json
//
// # Layering
//
// LEAF adapter: stdlib + embed + encoding/json ONLY. It imports NO domain
// (session/prompt/tool/governance), NO port, NO internal/app, and NO other
// adapter. Its Catalog/Provider/Model are package-own value types and must never
// leak into the domain or port. Only the composition layer (internal/app) reads
// it.
package providercatalog

import (
	_ "embed"
	"encoding/json"
	"sort"
	"sync"
)

//go:embed models.dev.curated.json
var rawCatalogBytes []byte

// rawProvider mirrors one provider entry's wire shape in models.dev.curated.json.
// Its field set is a 1:1 projection of the upstream per-provider schema kept by
// the regen jq, so re-pinning needs no reshaping. Unmapped fields (cost etc. on
// models) are ignored by encoding/json.
type rawProvider struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Env    []string            `json:"env"`
	API    string              `json:"api"` // null in source for SDK-default providers ⇒ ""
	NPM    string              `json:"npm"`
	Doc    string              `json:"doc"`
	Models map[string]rawModel `json:"models"`
}

// rawModel mirrors one model entry's wire shape. Only the fields S2 surfaces are
// declared; the rest of the (retained) JSON is ignored on parse.
type rawModel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Family     string `json:"family"`
	Reasoning  bool   `json:"reasoning"`
	ToolCall   bool   `json:"tool_call"`
	Attachment bool   `json:"attachment"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Limit struct {
		Context int `json:"context"`
		Input   int `json:"input"`
		Output  int `json:"output"`
	} `json:"limit"`
}

// Catalog is the parsed, read-only curated catalog. It is constructed once from
// the embedded JSON and is immutable; accessors return defensive copies so a
// caller can never corrupt the singleton.
type Catalog struct {
	providers map[string]Provider // keyed by provider id
}

// Provider is one vendored provider's metadata plus its curated model set. It is
// handed out BY VALUE with unexported fields, so a returned Provider cannot
// mutate the catalog.
type Provider struct {
	id     string
	name   string
	env    []string
	api    string // "" ⇒ SDK default (openai/anthropic); base URL for openrouter
	npm    string // documentary only
	doc    string // documentary only
	models []Model
}

// Model is one curated model's metadata. Fields are chosen for S3 (the
// ListModels projection) and S5 (capability intersection) — nothing is computed
// at parse time, only data is carried.
type Model struct {
	id              string
	name            string
	family          string
	contextLimit    int
	inputLimit      int
	outputLimit     int
	inputModalities []string
	reasoning       bool
	toolCall        bool
	attachment      bool
}

var (
	defaultOnce    sync.Once
	defaultCatalog *Catalog
)

// Default returns the process-wide parsed catalog, parsed exactly once.
//
// Parse-failure posture: the JSON is compiled into the binary via go:embed, so a
// parse failure is a build/test-time defect (a corrupted embed), NOT a runtime
// fail-safe condition. Default therefore PANICS on a parse error rather than
// threading an impossible error through every S3/S5 call site — the same idiom as
// regexp.MustCompile / template.Must. The package guard test makes shipping a
// broken embed impossible.
func Default() *Catalog {
	defaultOnce.Do(func() {
		c, err := parse(rawCatalogBytes)
		if err != nil {
			panic("providercatalog: embedded models.dev.curated.json failed to parse: " + err.Error())
		}
		defaultCatalog = c
	})
	return defaultCatalog
}

// parse folds the embedded wire shape into the public Catalog with stable-sorted
// model + provider slices for determinism. It is a pure function (no embed
// dependency) so tests can exercise the fold directly.
func parse(data []byte) (*Catalog, error) {
	var raw map[string]rawProvider
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	providers := make(map[string]Provider, len(raw))
	for id, rp := range raw {
		models := make([]Model, 0, len(rp.Models))
		for _, rm := range rp.Models {
			models = append(models, Model{
				id:              rm.ID,
				name:            rm.Name,
				family:          rm.Family,
				contextLimit:    rm.Limit.Context,
				inputLimit:      rm.Limit.Input,
				outputLimit:     rm.Limit.Output,
				inputModalities: append([]string(nil), rm.Modalities.Input...),
				reasoning:       rm.Reasoning,
				toolCall:        rm.ToolCall,
				attachment:      rm.Attachment,
			})
		}
		sort.Slice(models, func(i, j int) bool { return models[i].id < models[j].id })
		providers[id] = Provider{
			id:     rp.ID,
			name:   rp.Name,
			env:    append([]string(nil), rp.Env...),
			api:    rp.API,
			npm:    rp.NPM,
			doc:    rp.Doc,
			models: models,
		}
	}
	return &Catalog{providers: providers}, nil
}

// Provider returns the provider entry for id and whether it is in the catalog.
// An unknown id is an honest (_, false) miss, never a silent substitution.
func (c *Catalog) Provider(id string) (Provider, bool) {
	p, ok := c.providers[id]
	return p, ok
}

// Providers returns all vendored providers, sorted by id (deterministic).
func (c *Catalog) Providers() []Provider {
	out := make([]Provider, 0, len(c.providers))
	for _, p := range c.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// ID returns the provider's stable, lowercase id (matches the registry's
// provider id constants and the wire provider_id).
func (p Provider) ID() string { return p.id }

// Name returns the provider's display name.
func (p Provider) Name() string { return p.name }

// EnvVars returns the credential env-var names whose non-empty value makes this
// provider AVAILABLE (any one suffices). This is the array internal/app's
// registry reads to replace its former inline env map. It stays HONEST to
// upstream — e.g. openrouter is ["OPENROUTER_API_KEY"] only; the mecatl
// OPENAI_API_KEY fallback is a COMPOSITION augmentation, not catalog data.
// A fresh copy is returned so a caller cannot mutate the singleton.
func (p Provider) EnvVars() []string { return append([]string(nil), p.env...) }

// APIBaseURL returns the provider's API base URL; "" means use the SDK default
// (openai/anthropic). For openrouter it is the OpenRouter base URL.
func (p Provider) APIBaseURL() string { return p.api }

// Models returns the curated model set, sorted by id. A fresh slice is returned
// so a caller cannot corrupt the singleton.
func (p Provider) Models() []Model { return append([]Model(nil), p.models...) }

// Model returns the curated model with id and whether it is in this provider.
func (p Provider) Model(id string) (Model, bool) {
	i := sort.Search(len(p.models), func(i int) bool { return p.models[i].id >= id })
	if i < len(p.models) && p.models[i].id == id {
		return p.models[i], true
	}
	return Model{}, false
}

// ID returns the model's catalog id (e.g. "gpt-5", or "anthropic/claude-opus-4.5"
// for an openrouter route).
func (m Model) ID() string { return m.id }

// Name returns the model's display name.
func (m Model) Name() string { return m.name }

// Family returns the model's family (documentary).
func (m Model) Family() string { return m.family }

// ContextLimit returns the total context window (limit.context).
func (m Model) ContextLimit() int { return m.contextLimit }

// InputLimit returns the max input tokens (limit.input).
func (m Model) InputLimit() int { return m.inputLimit }

// OutputLimit returns the max output tokens (limit.output).
func (m Model) OutputLimit() int { return m.outputLimit }

// InputModalities returns the raw input-modality list ("text","image","audio").
// S5 intersects this against the adapter's port.ProviderCapabilities; a fresh
// copy is returned so the singleton cannot be mutated.
func (m Model) InputModalities() []string { return append([]string(nil), m.inputModalities...) }

// SupportsImageInput reports whether "image" is among the input modalities
// (a convenience derivation for S5's capability intersection).
func (m Model) SupportsImageInput() bool {
	for _, mod := range m.inputModalities {
		if mod == "image" {
			return true
		}
	}
	return false
}

// SupportsReasoning reports whether the model exposes reasoning (for S5).
func (m Model) SupportsReasoning() bool { return m.reasoning }

// SupportsToolCall reports whether the model supports tool/function calls.
func (m Model) SupportsToolCall() bool { return m.toolCall }

// SupportsAttachment reports whether the model accepts file attachments.
func (m Model) SupportsAttachment() bool { return m.attachment }
