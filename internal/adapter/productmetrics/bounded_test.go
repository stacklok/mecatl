package productmetrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stacklok/mecatl/engine/session"
)

// allowedAttributeKeys is the COMPLETE set of attribute keys any instrument
// in this package may ever carry. A future change that attaches a new label
// must add it here explicitly — the same "closed set is a reviewed
// decision" discipline as internal/adapter/telemetry's attrRole.
var allowedAttributeKeys = map[string]bool{
	attrStop:     true,
	attrKind:     true,
	attrFeature:  true,
	attrProvider: true,
	attrMode:     true,
}

// sensitiveMarkers are strings injected into every field the Recorder must
// NEVER read. If any of these ever shows up in a collected metric name or
// attribute value, something started reading a field it shouldn't.
var sensitiveMarkers = []string{
	"sensitive-session-id-marker",
	"secret-tool-name-marker",
	"secret-tool-content-marker",
	"secret-error-text-marker",
}

func TestRecorderNeverAttachesUnboundedAttributesOrSensitiveContent(t *testing.T) {
	r, reader := newTestRecorder(t)

	// Drive every observation path with deliberately sensitive-looking data.
	r.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult,
		Result: &session.ResultPayload{
			Stop:  session.StopError,
			Text:  "sensitive-session-id-marker should never be read",
			Error: "secret-error-text-marker: connection to 10.0.0.5 failed",
			Usage: session.Usage{InputTokens: 1, OutputTokens: 1},
		},
	})
	r.Emit(context.Background(), session.Event{Type: session.EvSubagentStart})
	r.Emit(context.Background(), session.Event{Type: session.EvTeamStart})
	r.ToolCall(
		session.SessionID("sensitive-session-id-marker"),
		session.ToolCall{Name: "secret-tool-name-marker"},
		session.ToolResult{Content: "secret-tool-content-marker", IsError: true},
		10*time.Millisecond, 20*time.Millisecond,
	)
	r.Heartbeat(FeatureSnapshot{
		Memory: true, Guardrails: true, MCP: true, Scheduling: true,
		Provider: ProviderOther, Mode: ModeK8s,
	})

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	totalMetrics := 0
	for _, sm := range rm.ScopeMetrics {
		totalMetrics += len(sm.Metrics)
	}
	if totalMetrics < 10 {
		t.Fatalf("collected only %d metrics, want at least 10 (the full mecatl.product.* instrument set) — the walk below would otherwise pass vacuously", totalMetrics)
	}

	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			assertNoSensitiveSubstring(t, md.Name)
			sum, ok := md.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s: aggregation is %T, want Sum[int64]", md.Name, md.Data)
			}
			for _, dp := range sum.DataPoints {
				iter := dp.Attributes.Iter()
				for iter.Next() {
					kv := iter.Attribute()
					key := string(kv.Key)
					if !allowedAttributeKeys[key] {
						t.Errorf("metric %s carries attribute key %q, not in allowedAttributeKeys", md.Name, key)
					}
					assertNoSensitiveSubstring(t, kv.Value.AsString())
				}
			}
			// mecatl.product.tool_calls carries NO attributes at all — the
			// strongest form of "no tool identity ever attaches."
			if md.Name == "mecatl.product.tool_calls" {
				for _, dp := range sum.DataPoints {
					if dp.Attributes.Len() != 0 {
						t.Errorf("mecatl.product.tool_calls data point carries %d attributes, want 0: %v",
							dp.Attributes.Len(), dp.Attributes)
					}
				}
			}
		}
	}
}

func assertNoSensitiveSubstring(t *testing.T, s string) {
	t.Helper()
	for _, marker := range sensitiveMarkers {
		if strings.Contains(s, marker) {
			t.Errorf("value %q contains sensitive marker %q", s, marker)
		}
	}
}
