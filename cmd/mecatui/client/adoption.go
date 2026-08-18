package client

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// AdoptionBindings is the proto-free explicit execution/model binding reviewed
// before a legacy session is copied into a new main chat.
type AdoptionBindings struct {
	Workspace       string
	EnvironmentKind string
	EnvironmentID   string
	ProviderID      string
	ModelID         string
	Profile         string
}

// AdoptionPreflight is the server-authoritative eligibility result and resolved binding display.
type AdoptionPreflight struct {
	Eligible bool
	Reason   CapabilityReason
	Bindings AdoptionBindings
}

// AdoptionResult identifies the new main session and its source/capability echoes.
type AdoptionResult struct {
	SessionID       string
	SourceSessionID string
	Capabilities    Capabilities
	ResolvedModel   ResolvedModel
}

// SessionAdopter is the proto-free legacy-adoption client seam used by the UI.
type SessionAdopter interface {
	PreflightSessionAdoption(ctx context.Context, sourceID string, bindings AdoptionBindings) (AdoptionPreflight, error)
	AdoptSession(ctx context.Context, sourceID, idempotencyKey string, bindings AdoptionBindings) (AdoptionResult, error)
}

// SessionAdoptionPreflightMsg carries one source-correlated server preflight.
type SessionAdoptionPreflightMsg struct {
	SourceID  string
	Bindings  AdoptionBindings
	Preflight AdoptionPreflight
	Err       error
}

// SessionAdoptedMsg carries adoption plus authoritative target refetches.
type SessionAdoptedMsg struct {
	SourceID   string
	Result     AdoptionResult
	Snapshot   SessionSnapshot
	Transcript SessionTranscript
	Err        error
}

// PreflightSessionAdoptionCmd performs preflight outside the reducer.
func PreflightSessionAdoptionCmd(ctx context.Context, adopter SessionAdopter, sourceID string, bindings AdoptionBindings) tea.Cmd {
	return func() tea.Msg {
		preflight, err := adopter.PreflightSessionAdoption(ctx, sourceID, bindings)
		return SessionAdoptionPreflightMsg{SourceID: sourceID, Bindings: bindings, Preflight: preflight, Err: err}
	}
}

func adoptionBindingsToProto(binding AdoptionBindings) *mecatlv1.AdoptionBindings {
	return &mecatlv1.AdoptionBindings{Workspace: binding.Workspace, EnvironmentKind: binding.EnvironmentKind, EnvironmentId: binding.EnvironmentID, ProviderId: binding.ProviderID, ModelId: binding.ModelID, Profile: binding.Profile}
}

func adoptionBindingsFromProto(binding *mecatlv1.AdoptionBindings) AdoptionBindings {
	if binding == nil {
		return AdoptionBindings{}
	}
	return AdoptionBindings{Workspace: binding.GetWorkspace(), EnvironmentKind: binding.GetEnvironmentKind(), EnvironmentID: binding.GetEnvironmentId(), ProviderID: binding.GetProviderId(), ModelID: binding.GetModelId(), Profile: binding.GetProfile()}
}

// PreflightSessionAdoption asks the server to validate one legacy source and explicit bindings.
func (c *Client) PreflightSessionAdoption(ctx context.Context, sourceID string, bindings AdoptionBindings) (AdoptionPreflight, error) {
	response, err := c.svc.PreflightSessionAdoption(ctx, &mecatlv1.PreflightSessionAdoptionRequest{SourceSessionId: sourceID, Bindings: adoptionBindingsToProto(bindings)})
	if err != nil {
		return AdoptionPreflight{}, fmt.Errorf("preflight session adoption: %w", err)
	}
	return AdoptionPreflight{Eligible: response.GetEligible(), Reason: CapabilityReason(response.GetReasonCode()), Bindings: adoptionBindingsFromProto(response.GetBindings())}, nil
}

// AdoptSession publishes the caller/source-bound idempotent main-session copy.
func (c *Client) AdoptSession(ctx context.Context, sourceID, idempotencyKey string, bindings AdoptionBindings) (AdoptionResult, error) {
	response, err := c.svc.AdoptSession(ctx, &mecatlv1.AdoptSessionRequest{SourceSessionId: sourceID, IdempotencyKey: idempotencyKey, Bindings: adoptionBindingsToProto(bindings)})
	if err != nil {
		return AdoptionResult{}, fmt.Errorf("adopt session: %w", err)
	}
	return AdoptionResult{SessionID: response.GetSessionId(), SourceSessionID: response.GetSourceSessionId(), Capabilities: capabilitiesFrom(response.GetCapabilities()), ResolvedModel: resolvedModelFrom(response.GetResolvedModel())}, nil
}
