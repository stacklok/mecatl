package openaicodex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/internal/adapter/modeltext"
)

const (
	maxModelsResponseBytes   = 1 << 20
	maxModelIDRunes          = 256
	maxModelNameRunes        = 512
	codexModelsClientVersion = "1.0.0"
	modelsTimeout            = 5 * time.Second
)

// Model is the package-owned projection of one picker-visible Codex
// entitlement. It carries no credential or endpoint data.
type Model struct {
	ID                   string
	DisplayName          string
	ContextLimit         int
	InputModalities      []string
	InputModalitiesKnown bool
	Reasoning            bool
	ReasoningKnown       bool
	ToolCall             bool
}

// Lister enumerates only the models granted to the current ChatGPT account.
type Lister struct {
	client        *http.Client
	clientVersion string
}

// NewLister derives its HTTP path from the same immutable RequestPolicy used
// by inference. A separate client supplies the bounded listing timeout over
// the exact same final transport; inference itself has no blanket timeout.
func NewLister(policy RequestPolicy) *Lister {
	return &Lister{
		clientVersion: codexModelsClientVersion,
		client: &http.Client{
			Timeout:       modelsTimeout,
			CheckRedirect: policy.HTTPClient().CheckRedirect,
			Transport:     policy,
		},
	}
}

type modelsEnvelope struct {
	Models []modelsWireModel `json:"models"`
}

// modelsWireModel is the accepted, sanitized Step 1 fixture subset. Unknown
// provider-private fields are deliberately ignored.
type modelsWireModel struct {
	Slug                     string             `json:"slug"`
	DisplayName              string             `json:"display_name"`
	Visibility               string             `json:"visibility"`
	ContextWindow            int                `json:"context_window"`
	MaxContextWindow         int                `json:"max_context_window"`
	InputModalities          *[]string          `json:"input_modalities"`
	SupportedReasoningLevels *[]json.RawMessage `json:"supported_reasoning_levels"`
}

// ListModels returns picker-visible entitlements in server order.
func (l *Lister) ListModels(ctx context.Context) ([]Model, error) {
	query := url.Values{"client_version": []string{l.clientVersion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/models?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("openai-codex: build models request: %w", err)
	}
	// The immutable policy forbids host overrides. Keep the optional override
	// field empty; the transport still routes through URL.Host.
	req.Host = ""
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai-codex: fetch models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("openai-codex: unexpected models status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("openai-codex: read models body: %w", err)
	}
	if len(body) > maxModelsResponseBytes {
		return nil, fmt.Errorf("openai-codex: models response exceeds %d-byte cap", maxModelsResponseBytes)
	}
	var envelope modelsEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("openai-codex: decode models: %w", err)
	}
	models := make([]Model, 0, len(envelope.Models))
	for _, wire := range envelope.Models {
		if wire.Visibility != "list" {
			continue
		}
		if !validModelID(wire.Slug) {
			continue
		}
		contextLimit := wire.ContextWindow
		if contextLimit <= 0 {
			contextLimit = wire.MaxContextWindow
		}
		var modalities []string
		if wire.InputModalities != nil {
			modalities = append([]string(nil), (*wire.InputModalities)...)
		}
		models = append(models, Model{
			ID:                   wire.Slug,
			DisplayName:          modeltext.TruncateRunes(modeltext.StripControls(wire.DisplayName), maxModelNameRunes),
			ContextLimit:         contextLimit,
			InputModalities:      modalities,
			InputModalitiesKnown: wire.InputModalities != nil,
			Reasoning:            wire.SupportedReasoningLevels != nil && len(*wire.SupportedReasoningLevels) > 0,
			ReasoningKnown:       wire.SupportedReasoningLevels != nil,
			ToolCall:             true,
		})
	}
	return models, nil
}

// validModelID treats the entitlement slug as an opaque identifier. A valid
// slug is returned byte-for-byte; a slug that would require trimming,
// sanitization, or truncation is rejected instead of silently becoming a
// different model id.
func validModelID(value string) bool {
	return value != "" &&
		utf8.RuneCountInString(value) <= maxModelIDRunes &&
		strings.TrimSpace(value) == value &&
		modeltext.StripControls(value) == value &&
		!strings.ContainsRune(value, unicode.ReplacementChar)
}
