package agent

import (
	"context"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
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

// RemapAuxiliaryUsage confines a producer result to the caller-owned purpose while
// preserving every non-empty model attribution and its reported totals.
func RemapAuxiliaryUsage(ctx context.Context, diag port.Diagnostics, purpose session.UsageKind, in session.AuxiliaryUsage) session.AuxiliaryUsage {
	out := session.AuxiliaryUsage{}
	unexpected, empty := false, false
	for kind, bucket := range in.Buckets {
		if kind == "" || kind != purpose {
			unexpected = true
		}
		if len(bucket.Models) == 0 {
			empty = true
		}
		for model, usage := range bucket.Models {
			if model == "" || usage == (session.Usage{}) {
				empty = true
				continue
			}
			out = out.Merge(session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
				purpose: {Models: map[string]session.Usage{model: usage}},
			}})
		}
	}
	if (unexpected || empty) && diag != nil {
		diag.Log(ctx, port.LevelDebug, "auxiliary usage result normalized", "unexpected_bucket", unexpected, "empty_bucket", empty)
	}
	return out
}

func remapAuxiliaryUsage(ctx context.Context, diag port.Diagnostics, purpose session.UsageKind, in session.AuxiliaryUsage) session.AuxiliaryUsage {
	return RemapAuxiliaryUsage(ctx, diag, purpose, in)
}
