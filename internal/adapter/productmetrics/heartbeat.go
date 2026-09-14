package productmetrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// DefaultHeartbeatInterval is the steady-state heartbeat cadence for
// long-running processes (mecated, mecatui, mecak8s). mecatequi (short-lived)
// passes 0 — a single immediate fire only, no ticker.
const DefaultHeartbeatInterval = 24 * time.Hour

// Heartbeat records the periodic liveness + feature/provider/mode signal.
// Every attribute value comes from the closed Feature/ProviderFamily/
// DeploymentMode enums — never a def/model/tool name.
func (r *Recorder) Heartbeat(snap FeatureSnapshot) {
	ctx := context.Background()
	r.heartbeat.Add(ctx, 1)
	for feature, on := range snap.enabled() {
		if on {
			r.featureEnabled.Add(ctx, 1, metric.WithAttributes(attribute.String(attrFeature, string(feature))))
		}
	}
	r.providerConfig.Add(ctx, 1, metric.WithAttributes(attribute.String(attrProvider, string(snap.Provider))))
	r.deploymentMode.Add(ctx, 1, metric.WithAttributes(attribute.String(attrMode, string(snap.Mode))))
}

// RunHeartbeat fires one heartbeat immediately, then one every interval,
// until ctx is done. interval<=0 disables the ticker (a single fire only —
// mecatequi's shape). Meant to run in its own goroutine, owned by the
// caller (composition), which cancels ctx on shutdown.
func RunHeartbeat(ctx context.Context, r *Recorder, interval time.Duration, snap FeatureSnapshot) {
	r.Heartbeat(snap)
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Heartbeat(snap)
		}
	}
}
