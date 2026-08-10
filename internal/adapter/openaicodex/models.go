package openaicodex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/internal/adapter/modelhttp"
)

const (
	maxModelsResponseBytes   = 1 << 20
	maxModelIDRunes          = 256
	maxModelNameRunes        = 512
	codexModelsClientVersion = "1.0.0"
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
// by inference. A separate client supplies the bounded listing timeout while
// retaining the policy's transport and redirect refusal.
func NewLister(policy RequestPolicy) *Lister {
	return &Lister{
		clientVersion: codexModelsClientVersion,
		client: &http.Client{
			Timeout:       modelhttp.DefaultTimeout,
			CheckRedirect: policy.client.CheckRedirect,
			Transport:     policyRoundTripper{policy: policy},
		},
	}
}

type policyRoundTripper struct{ policy RequestPolicy }

func (t policyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.policy.middleware(req, func(next *http.Request) (*http.Response, error) {
		return t.policy.client.Transport.RoundTrip(next)
	})
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
	body, err := modelhttp.Get(ctx, l.client, BaseURL+"/models?"+query.Encode(), "openai-codex", maxModelsResponseBytes, func(req *http.Request) {
		// net/http.NewRequest copies URL.Host into Request.Host. The immutable
		// policy forbids host overrides, so clear the optional override field;
		// the transport still routes through URL.Host.
		req.Host = ""
	})
	if err != nil {
		return nil, err
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
			DisplayName:          truncateModelField(stripModelControls(wire.DisplayName), maxModelNameRunes),
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
		stripModelControls(value) == value &&
		!strings.ContainsRune(value, unicode.ReplacementChar)
}

func stripModelControls(value string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return -1
		case r == 0x2028 || r == 0x2029 || unicode.Is(unicode.Bidi_Control, r):
			return -1
		default:
			return r
		}
	}, value)
}

func truncateModelField(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
