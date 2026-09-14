package productmetrics

import (
	"context"
	"testing"
	"time"
)

func TestRecorderHeartbeatRecordsClosedLabelsOnly(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Heartbeat(FeatureSnapshot{
		Memory: true, MCP: true,
		Provider: ProviderAnthropic, Mode: ModeInteractive,
	})

	collected := collect(t, reader)
	if got := sumValue(t, collected["mecatl.product.heartbeat"]); got != 1 {
		t.Errorf("heartbeat = %d, want 1", got)
	}
	featureAgg := collected["mecatl.product.feature_enabled"]
	if got := sumPoint(t, featureAgg, "feature", "memory"); got != 1 {
		t.Errorf("feature_enabled{feature=memory} = %d, want 1", got)
	}
	if got := sumPoint(t, featureAgg, "feature", "mcp"); got != 1 {
		t.Errorf("feature_enabled{feature=mcp} = %d, want 1", got)
	}
	// guardrails/scheduling were false in the snapshot: TestRecorderNeverAttachesUnboundedAttributesOrSensitiveContent
	// (Task 6) is the exhaustive "no other data point" check; this test
	// only asserts the enabled ones are present with the right value.
	if got := sumPoint(t, collected["mecatl.product.provider_configured"], "family", "anthropic"); got != 1 {
		t.Errorf("provider_configured{family=anthropic} = %d, want 1", got)
	}
	if got := sumPoint(t, collected["mecatl.product.deployment_mode"], "mode", "interactive"); got != 1 {
		t.Errorf("deployment_mode{mode=interactive} = %d, want 1", got)
	}
}

func TestRunHeartbeatFiresImmediatelyThenStopsOnCtxDone(t *testing.T) {
	r, reader := newTestRecorder(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled BEFORE RunHeartbeat: only the immediate fire happens.

	RunHeartbeat(ctx, r, time.Hour, FeatureSnapshot{Mode: ModeHeadless})

	if got := sumValue(t, collect(t, reader)["mecatl.product.heartbeat"]); got != 1 {
		t.Errorf("heartbeat = %d, want exactly 1 (immediate fire only)", got)
	}
}
