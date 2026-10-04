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

// UtilityEngineUsage returns every model call made by a utility engine. Its main
// call is attributed from composition; nested utility work such as tier-4
// compaction retains the exact attribution already recorded on the utility session.
// The owning caller remaps every returned bucket to its fixed purpose.
func UtilityEngineUsage(kind session.UsageKind, identity session.ProviderModelID, sess *session.Session) session.AuxiliaryUsage {
	out := auxiliaryUsage(kind, identity, sess.UsageFor(session.UsageKindMain))
	for nestedKind, bucket := range sess.TokenUsageSnapshot() {
		if nestedKind == session.UsageKindMain {
			continue
		}
		out = out.Merge(session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{nestedKind: bucket}})
	}
	return out
}

// RemapAuxiliaryUsage confines a producer result to the caller-owned purpose while
// preserving every reported model attribution and its spend.
func RemapAuxiliaryUsage(ctx context.Context, diag port.Diagnostics, purpose session.UsageKind, in session.AuxiliaryUsage) session.AuxiliaryUsage {
	out := session.AuxiliaryUsage{}
	unexpected, empty, missingPurpose := false, false, false
	if purpose == "" {
		purpose = session.UsageKind(unknownAuxiliaryAttribution)
	}
	for kind, bucket := range in.Buckets {
		if kind == "" || kind == session.UsageKind(unknownAuxiliaryAttribution) {
			missingPurpose = true
		}
		if kind != purpose {
			unexpected = true
		}
		if len(bucket.Models) == 0 {
			empty = true
		}
		for model, usage := range bucket.Models {
			if usage == (session.Usage{}) {
				empty = true
				continue
			}
			if model == "" {
				model = unknownAuxiliaryAttribution
				empty = true
			}
			out = out.Merge(session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
				purpose: {Models: map[string]session.Usage{model: usage}},
			}})
		}
	}
	if diag != nil {
		if missingPurpose {
			diag.Log(ctx, port.LevelWarn, "auxiliary usage missing purpose remapped", "purpose", string(purpose))
		} else if unexpected || empty {
			diag.Log(ctx, port.LevelDebug, "auxiliary usage result normalized", "unexpected_bucket", unexpected, "empty_bucket", empty)
		}
	}
	return out
}
