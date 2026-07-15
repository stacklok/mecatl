package client

import (
	"context"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The model-selection surface: plain client-owned structs mirroring the proto
// ModelInfo + ListModelsResponse, the unary RPC wrapper that maps proto → the
// structs, and the tea.Cmd constructor the ui's /models picker calls. As with the
// soul/usermodel/skills surfaces, NO proto type leaks past this file — the ui
// renders the picker purely from these plain structs.

// ModelInfo is one selectable model's listing metadata — the proto-free mirror of
// mecatlv1.ModelInfo. The ui renders the /models picker purely from these. The
// (ProviderID, ID) pair is what CreateSession ultimately carries.
type ModelInfo struct {
	ID           string // the opaque model_id sent on CreateSession
	ProviderID   string // the provider_id sent on CreateSession
	DisplayName  string // human label; falls back to ID server-side already
	Image        bool   // accepts image input
	Reasoning    bool   // emits reasoning
	ContextLimit int64  // total context window in tokens; 0 = unknown
}

// ModelSelection is the chosen (provider, model) the client sends on
// CreateSession. Proto-free; the zero value means "server default" (no
// provider_id/model_id set on the request).
type ModelSelection struct {
	ProviderID string
	ModelID    string
	// ReasoningEffort is the chosen reasoning-effort tier (ADR 0055): "" / "auto"
	// (unset — operator/provider default) or low/medium/high/xhigh/max. It is sent
	// on CreateSession.reasoning_effort and is meaningful WITHOUT a provider/model
	// (it rides the server-default provider), so an effort-only selection is NOT
	// zero (IsZero counts it).
	ReasoningEffort string
}

// IsZero reports whether the selection is empty (⇒ the server picks its default).
// An effort-only selection is NOT zero (the client must still send it).
func (s ModelSelection) IsZero() bool {
	return s.ProviderID == "" && s.ModelID == "" && s.ReasoningEffort == ""
}

// Matches reports whether m is the model this selection names (by provider + id).
func (s ModelSelection) Matches(m ModelInfo) bool {
	return s.ProviderID == m.ProviderID && s.ModelID == m.ID
}

// ProviderStatus is one provider's last live-listing outcome (issue #262: the
// ToolHive LLM gateway), the proto-free mirror of mecatlv1.ProviderStatus.
// State is "ok" | "unreachable" | "unauthorized" | "empty"; Hint is a short
// human remediation string, empty for "ok".
type ProviderStatus struct {
	ProviderID string
	State      string
	Hint       string
	// DefaultModelAutoSelected is true ONLY when this row's provider is the
	// DEFAULT provider AND the server AUTO-selected its default model (a
	// first-listed heal/probe pick, issue #262 review finding 7) — never true
	// when an operator configured --model/--default-model. Named to match the
	// wire field (default_model_auto_selected) and the server-side accessor
	// (providerRegistry.DefaultModelAutoSelected) verbatim.
	DefaultModelAutoSelected bool
}

// ModelsMsg carries a ListModels result for the /models picker. Err is set on
// failure; the picker surfaces it rather than silently degrading. Statuses is
// the (possibly empty) per-provider live-listing outcome list — empty for
// every deployment without an intent-driven provider (byte-identical to
// before issue #262).
type ModelsMsg struct {
	Models   []ModelInfo
	Statuses []ProviderStatus
	Err      error
}

// ModelLister is the subset of *Client the ui's /models picker needs. Splitting
// it out keeps the ui injectable with a fake for offline tests; *Client satisfies
// it.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, []ProviderStatus, error)
}

// ListModels fetches the selectable-model inventory across AVAILABLE providers,
// (provider_id, id)-sorted (the server sorts; the client preserves that order),
// plus (issue #262) the per-provider live-listing status.
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, []ProviderStatus, error) {
	resp, err := c.svc.ListModels(ctx, &mecatlv1.ListModelsRequest{})
	if err != nil {
		return nil, nil, err
	}
	return mapModels(resp), mapProviderStatuses(resp), nil
}

// mapModels maps a proto ListModelsResponse (nil-safe) to the plain structs.
func mapModels(in *mecatlv1.ListModelsResponse) []ModelInfo {
	if in == nil {
		return nil
	}
	models := in.GetModels()
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		out = append(out, mapModelInfo(m))
	}
	return out
}

// mapProviderStatuses maps a proto ListModelsResponse's provider_status
// (nil-safe) to the plain structs.
func mapProviderStatuses(in *mecatlv1.ListModelsResponse) []ProviderStatus {
	if in == nil {
		return nil
	}
	rows := in.GetProviderStatus()
	out := make([]ProviderStatus, 0, len(rows))
	for _, r := range rows {
		if r == nil {
			continue
		}
		out = append(out, ProviderStatus{
			ProviderID:               r.GetProviderId(),
			State:                    r.GetState(),
			Hint:                     r.GetHint(),
			DefaultModelAutoSelected: r.GetDefaultModelAutoSelected(),
		})
	}
	return out
}

// mapModelInfo maps one proto ModelInfo (nil-safe) to the plain struct.
func mapModelInfo(m *mecatlv1.ModelInfo) ModelInfo {
	if m == nil {
		return ModelInfo{}
	}
	return ModelInfo{
		ID:           m.GetId(),
		ProviderID:   m.GetProviderId(),
		DisplayName:  m.GetDisplayName(),
		Image:        m.GetImage(),
		Reasoning:    m.GetReasoning(),
		ContextLimit: m.GetContextLimit(),
	}
}

// ListModelsCmd fetches the model inventory off the update goroutine; the result
// (success or error) arrives as a ModelsMsg.
func ListModelsCmd(ctx context.Context, l ModelLister) tea.Cmd {
	return func() tea.Msg {
		ms, statuses, err := l.ListModels(ctx)
		if err != nil {
			return ModelsMsg{Err: err}
		}
		return ModelsMsg{Models: ms, Statuses: statuses}
	}
}
