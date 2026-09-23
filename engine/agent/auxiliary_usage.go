package agent

import (
	"strings"

	"github.com/stacklok/mecatl/engine/session"
)

const unknownAuxiliaryAttribution = "unknown"

func auxiliaryUsage(kind session.UsageKind, identity session.ProviderModelID, usage session.Usage) session.AuxiliaryUsage {
	if usage == (session.Usage{}) {
		return session.AuxiliaryUsage{}
	}
	providerID := strings.Join(strings.Fields(identity.ProviderID), " ")
	modelID := strings.Join(strings.Fields(identity.ModelID), " ")
	attribution := unknownAuxiliaryAttribution
	if providerID != "" && modelID != "" {
		attribution = providerID + "/" + modelID
	}
	return session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		kind: {Total: usage, Models: map[string]session.Usage{attribution: usage}},
	}}
}
