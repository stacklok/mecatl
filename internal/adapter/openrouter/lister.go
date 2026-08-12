// Package openrouter is a stdlib-only LEAF adapter that fetches the LIVE
// OpenRouter model catalog from the public, UNAUTHENTICATED models endpoint and
// maps it into a neutral, package-own result type. It carries no behaviour beyond
// the HTTP GET + JSON→neutral mapping; nothing here knows about the agent,
// providers, ports, the composition layer, or any other adapter.
//
// # Provenance and wire shape
//
// The endpoint is:
//
//	GET https://openrouter.ai/api/v1/models
//
// It is UNAUTHENTICATED — no API key is read or sent (CWE-200: the key lives only
// in the resilience-wrapped port.LLMProvider, never in this lister). The response
// envelope is {"data":[ {id, name, context_length, architecture:{input_modalities},
// supported_parameters:[...]}, ... ]}. Mapping:
//
//	id                                       → ID
//	name                                     → DisplayName
//	context_length                           → ContextLimit
//	top_provider.max_completion_tokens       → OutputLimit (the output ceiling)
//	architecture.input_modalities ∋ "image"  → InputModalities (carries "image")
//	supported_parameters ∋ "reasoning"       → Reasoning
//	supported_parameters ∋ "tools"           → ToolCall
//
// Wire shape verified against a live capture pinned 2026-07-13 (345 models). A
// trimmed sample lives in testdata/ for the offline tests; the live endpoint is
// NEVER contacted in a test.
//
// # Layering
//
// LEAF adapter: net/http + encoding/json + stdlib ONLY. It imports NO domain
// (session/prompt/tool/governance), NO port, NO internal/app, NO providercatalog,
// and NO other adapter. Its Model type is package-own and must never leak into the
// domain or port — only the composition layer (internal/app) reads it and maps it
// to its own neutral liveModel type (so there is no import cycle: the adapter does
// not import internal/app).
package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/modeltext"
)

const (
	// openRouterModelsURL is the FIXED, package-internal endpoint. It is never
	// caller/catalog/registry-supplied — there is no interpolation and no
	// host-from-config path, so there is no SSRF surface (CWE-918).
	openRouterModelsURL = "https://openrouter.ai/api/v1/models"

	// maxResponseBytes caps the response body read so a hostile or pathological
	// body cannot OOM the process (CWE-770). 4 MiB comfortably holds the live
	// catalog (336 models, well under 1 MiB) with ample headroom.
	maxResponseBytes = 4 << 20

	// defaultTimeout bounds the whole fetch when no client timeout is otherwise
	// configured (the injected client may carry its own; the ctx deadline also
	// applies). A live catalog fetch that hangs must not stall the caller.
	defaultTimeout = 5 * time.Second

	// maxIDRunes / maxNameRunes bound a SINGLE model's id and display name. The
	// 4 MiB whole-response cap above does not stop ONE hostile entry with a
	// multi-megabyte id/name from surviving into a picker row, so each field is
	// rune-truncated in the map loop (a picker label is short by nature; these are
	// generous). Rune-aware truncation never splits a multi-byte boundary.
	maxIDRunes   = 256
	maxNameRunes = 512
)

// Model is the adapter's OWN neutral result type. The composition layer maps it to
// its composition-local modelEntry; it never leaves this package's caller as-is and
// carries nothing provider-private (no key, no URL).
type Model struct {
	ID           string
	DisplayName  string
	ContextLimit int
	// OutputLimit is top_provider.max_completion_tokens (the output ceiling; 0 =
	// unknown). It is CAPTURED for the meta store but is currently OFF the request
	// path for OpenRouter: OpenRouter rides the openai Responses adapter, which takes
	// NO per-model max_tokens resolver today (unlike the native anthropic adapter). So
	// this value reaches the resolvers' store but no OpenRouter request consumes it
	// yet. If a future change wires a max_tokens resolver for the openai/OpenRouter
	// adapter, the composition UPPER clamp (internal/app/livemeta.go clampLive) MUST
	// already gate this live value before it can flow into max_tokens — do not wire
	// the consumer ahead of the clamp.
	OutputLimit     int
	InputModalities []string
	Reasoning       bool
	ToolCall        bool
}

// Lister fetches the OpenRouter live model catalog over an INJECTED *http.Client
// (tests pass a mock transport; production gets a default client with a sane
// timeout). It is KEYLESS — it sends no Authorization header and never sees the
// OpenRouter API key.
type Lister struct {
	httpClient *http.Client
}

// NewLister constructs a Lister over the given client. A nil client yields a
// default client with defaultTimeout (so production wiring may pass nil and tests
// inject a mock transport).
func NewLister(client *http.Client) *Lister {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Lister{httpClient: client}
}

// wireResponse mirrors the subset of the OpenRouter /models envelope this adapter
// consumes. Unmapped fields (pricing, created, description, …) are ignored by
// encoding/json.
type wireResponse struct {
	Data []wireModel `json:"data"`
}

type wireModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	Architecture  struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	// TopProvider carries the upstream provider's per-model limits. Its
	// max_completion_tokens is the output ceiling — NEW field captured for the
	// resolvers (it previously never reached the wire struct). Absent/null ⇒ 0 ⇒ the
	// composition helper falls back to the catalog floor.
	TopProvider struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	SupportedParameters []string `json:"supported_parameters"`
}

// ListModels GETs the live OpenRouter catalog and maps it to []Model. It is
// read-only and fail-safe to the caller: any transport, status, size, or parse
// error returns a non-nil error (the composition layer then falls back to the
// embedded catalog). It NEVER panics.
func (l *Lister) ListModels(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openRouterModelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("openrouter: build request: %w", err)
	}
	// KEYLESS: no Authorization header (CWE-200). Accept JSON explicitly.
	req.Header.Set("Accept", "application/json")

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter: fetch models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("openrouter: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("openrouter: read body: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("openrouter: response exceeds %d-byte cap", maxResponseBytes)
	}

	var wire wireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("openrouter: decode models: %w", err)
	}

	out := make([]Model, 0, len(wire.Data))
	for _, w := range wire.Data {
		if w.ID == "" {
			continue // defensive: skip a malformed entry with no id
		}
		out = append(out, Model{
			ID:              modeltext.TruncateRunes(w.ID, maxIDRunes),
			DisplayName:     modeltext.TruncateRunes(w.Name, maxNameRunes),
			ContextLimit:    w.ContextLength,
			OutputLimit:     w.TopProvider.MaxCompletionTokens,
			InputModalities: append([]string(nil), w.Architecture.InputModalities...),
			Reasoning:       slices.Contains(w.SupportedParameters, "reasoning"),
			ToolCall:        slices.Contains(w.SupportedParameters, "tools"),
		})
	}
	return out, nil
}
