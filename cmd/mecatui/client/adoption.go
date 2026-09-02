package client

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
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

// PreflightSessionAdoption is retained as a temporary UI compatibility stub.
func (*Client) PreflightSessionAdoption(context.Context, string, AdoptionBindings) (AdoptionPreflight, error) {
	return AdoptionPreflight{}, fmt.Errorf("legacy session adoption is unsupported")
}

// AdoptSession is retained as a temporary UI compatibility stub.
func (*Client) AdoptSession(context.Context, string, string, AdoptionBindings) (AdoptionResult, error) {
	return AdoptionResult{}, fmt.Errorf("legacy session adoption is unsupported")
}
