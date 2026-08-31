package client

import (
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// TestCapabilitiesFrom covers the proto→plain translation, including the nil
// (older-server) case that MUST degrade to the all-false zero value rather than
// panic — the backward-compat guarantee.
func TestCapabilitiesFrom(t *testing.T) {
	tests := []struct {
		name string
		in   *mecatlv1.ServerCapabilities
		want Capabilities
	}{
		{
			name: "nil (older server) → all false",
			in:   nil,
			want: Capabilities{},
		},
		{
			name: "empty proto → all false",
			in:   &mecatlv1.ServerCapabilities{},
			want: Capabilities{},
		},
		{
			name: "all on (incl. media + model-selection caps)",
			in: &mecatlv1.ServerCapabilities{
				Mcp:              true,
				SlashCommands:    true,
				Memory:           true,
				Skills:           true,
				Teams:            true,
				Agents:           true,
				Bash:             true,
				Soul:             true,
				UserModel:        true,
				ModelSelection:   true,
				Image:            true,
				Audio:            true,
				Steer:            true,
				ManualCompaction: true,
			},
			want: Capabilities{
				MCP:              true,
				SlashCommands:    true,
				Memory:           true,
				Skills:           true,
				Teams:            true,
				Agents:           true,
				Bash:             true,
				Soul:             true,
				UserModel:        true,
				ModelSelection:   true,
				Image:            true,
				Audio:            true,
				Steer:            true,
				ManualCompaction: true,
			},
		},
		{
			name: "manual compaction maps independently",
			in:   &mecatlv1.ServerCapabilities{ManualCompaction: true},
			want: Capabilities{ManualCompaction: true},
		},
		{
			name: "model_selection maps independently",
			in:   &mecatlv1.ServerCapabilities{ModelSelection: true},
			want: Capabilities{ModelSelection: true},
		},
		{
			name: "media caps map independently (image on, audio off)",
			in: &mecatlv1.ServerCapabilities{
				Image: true,
			},
			want: Capabilities{
				Image: true,
			},
		},
		{
			name: "mixed (each field maps independently)",
			in: &mecatlv1.ServerCapabilities{
				Mcp:    true,
				Memory: true,
				Bash:   true,
				Audio:  true,
			},
			want: Capabilities{
				MCP:    true,
				Memory: true,
				Bash:   true,
				Audio:  true,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := capabilitiesFrom(tc.in); got != tc.want {
				t.Fatalf("capabilitiesFrom(%v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestCapabilitiesFromManualDreamPresenceAndTargets(t *testing.T) {
	if got := capabilitiesFrom(&mecatlv1.ServerCapabilities{}); got.ManualDream != nil {
		t.Fatalf("absent manual dream = %+v", got.ManualDream)
	}
	got := capabilitiesFrom(&mecatlv1.ServerCapabilities{ManualDream: &mecatlv1.ManualDreamCapabilities{
		ProjectMemory: &mecatlv1.DreamTargetCapability{Generate: true, Decide: true},
		UserModel:     &mecatlv1.DreamTargetCapability{UnavailableReason: "disabled\xff"},
	}})
	if got.ManualDream == nil || !got.ManualDream.ProjectMemory.Generate || !got.ManualDream.ProjectMemory.Decide || got.ManualDream.UserModel.UnavailableReason != "disabled�" {
		t.Fatalf("manual dream mapping = %+v", got.ManualDream)
	}
}

// TestResolvedModelFrom covers the proto→plain translation of the effective model,
// including the nil (older-server) case that MUST degrade to the zero value rather
// than panic — the backward-compat guarantee that drives the UI to show no model
// segment.
func TestResolvedModelFrom(t *testing.T) {
	tests := []struct {
		name string
		in   *mecatlv1.ResolvedModel
		want ResolvedModel
	}{
		{
			name: "nil (older server) → zero value",
			in:   nil,
			want: ResolvedModel{},
		},
		{
			name: "empty proto → zero value",
			in:   &mecatlv1.ResolvedModel{},
			want: ResolvedModel{},
		},
		{
			name: "populated maps each field",
			in:   &mecatlv1.ResolvedModel{ProviderId: "openai", ModelId: "gpt-5", ContextWindow: 400000},
			want: ResolvedModel{ProviderID: "openai", ModelID: "gpt-5", ContextWindow: 400000},
		},
		{
			name: "passthrough model id with no provider still maps",
			in:   &mecatlv1.ResolvedModel{ModelId: "some/passthrough"},
			want: ResolvedModel{ModelID: "some/passthrough"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvedModelFrom(tc.in); got != tc.want {
				t.Fatalf("resolvedModelFrom(%v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
