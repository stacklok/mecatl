package productmetrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stacklok/mecatl/engine/session"
)

// allowedAttributeKeys is the COMPLETE set of attribute keys any instrument
// in this package may ever carry. A future change that attaches a new label
// must add it here explicitly — the same "closed set is a reviewed
// decision" discipline as internal/adapter/telemetry's attrRole.
var allowedAttributeKeys = map[string]bool{
	attrStop:        true,
	attrKind:        true,
	attrFeature:     true,
	attrProvider:    true,
	attrMode:        true,
	attrHadToolCall: true,
	attrCategory:    true,
	attrOutcome:     true,
}

// sensitiveMarkers are strings injected into every field the Recorder must
// NEVER read, or (for the tool-name markers) must only ever read through a
// closed-set projection. If any of these ever shows up in a collected metric
// name or attribute value, something started emitting a field it shouldn't.
var sensitiveMarkers = []string{
	"sensitive-session-id-marker",
	"secret-tool-name-marker",
	"secret-tool-content-marker",
	"secret-error-text-marker",
	// The MCP server + remote tool names inside a namespaced mcp__ tool name:
	// operator-chosen free text, which must be bucketed under the single
	// literal "mcp" rather than emitted.
	"evilserver",
	"leak_this_name",
}

func TestRecorderNeverAttachesUnboundedAttributesOrSensitiveContent(t *testing.T) {
	r, reader := newTestRecorder(t)
	// Arm time_to_first_value so its data points are collected too — an
	// unarmed Recorder never records it, which would leave that instrument
	// outside the walk below.
	r.EnableFirstValueTracking(time.Now(), false, nil)

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
	// The run-aware path, with an MCP-namespaced name whose server and remote
	// tool halves are both operator-chosen free text. EvSessionInit is driven
	// FIRST with the SAME RunID so this run is genuinely tracked — exercising
	// run_duration and tool_calls_per_run too, not just category/outcome.
	r.Emit(context.Background(), session.Event{Type: session.EvSessionInit, RunID: "run-x"})
	r.ToolCallForRun(
		"run-x",
		session.SessionID("sensitive-session-id-marker"),
		session.ToolCall{Name: "mcp__evilserver__leak_this_name"},
		session.ToolResult{Content: "secret-tool-content-marker"},
		10*time.Millisecond, 20*time.Millisecond,
	)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult, RunID: "run-x",
		Result: &session.ResultPayload{Stop: session.StopEndTurn},
	})
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
	// wantInstrumentCount is NewRecorder's exact registered instrument count
	// (heartbeat, feature_enabled, provider_configured, deployment_mode,
	// sessions_started, runs_completed, tool_calls, tokens, subagent_used,
	// team_used, run_duration, tool_calls_per_run, time_to_first_value).
	// Asserting the EXACT count, not a floor, means a future instrument this
	// test's driving code doesn't happen to exercise fails loudly here rather
	// than silently passing the walk below vacuously.
	const wantInstrumentCount = 13
	if totalMetrics != wantInstrumentCount {
		t.Fatalf("collected %d metrics, want exactly %d (the full mecatl.product.* instrument set) — the walk below would otherwise pass vacuously on a new, unexercised instrument", totalMetrics, wantInstrumentCount)
	}

	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			assertNoSensitiveSubstring(t, md.Name)
			for _, attrs := range dataPointAttributes(t, md.Name, md.Data) {
				iter := attrs.Iter()
				for iter.Next() {
					kv := iter.Attribute()
					key := string(kv.Key)
					if !allowedAttributeKeys[key] {
						t.Errorf("metric %s carries attribute key %q, not in allowedAttributeKeys", md.Name, key)
					}
					// Every value must be a STRING: a non-string value would
					// slip past the sensitive-substring walk below with an
					// empty AsString(), making this guard vacuous for it.
					if kv.Value.Type() != attribute.STRING {
						t.Errorf("metric %s attribute %q has value type %v, want STRING", md.Name, key, kv.Value.Type())
					}
					assertNoSensitiveSubstring(t, kv.Value.AsString())
				}
			}
			// mecatl.product.tool_calls carries exactly the two bounded
			// tool-call keys — the category value can only ever be a built-in
			// tool's own name, "mcp", or "other" (see toolCategory), so no
			// tool identity beyond mecatl's own fixed catalog can attach.
			if md.Name == "mecatl.product.tool_calls" {
				for _, attrs := range dataPointAttributes(t, md.Name, md.Data) {
					if _, ok := attrs.Value(attrCategory); !ok {
						t.Errorf("mecatl.product.tool_calls data point is missing the %s attribute: %v", attrCategory, attrs)
					}
					if _, ok := attrs.Value(attrOutcome); !ok {
						t.Errorf("mecatl.product.tool_calls data point is missing the %s attribute: %v", attrOutcome, attrs)
					}
					if attrs.Len() != 2 {
						t.Errorf("mecatl.product.tool_calls data point carries %d attributes, want exactly 2: %v",
							attrs.Len(), attrs)
					}
				}
			}
		}
	}
}

// dataPointAttributes returns every collected data point's attribute set,
// whatever aggregation the instrument uses. The walk must cover histograms as
// well as sums: this package publishes run_duration, tool_calls_per_run and
// time_to_first_value as histograms, and an aggregation this helper did not
// know about would silently drop that instrument out of the no-PII guard —
// so an unrecognised shape is a hard failure, never a skip.
func dataPointAttributes(t *testing.T, name string, agg metricdata.Aggregation) []attribute.Set {
	t.Helper()
	var out []attribute.Set
	switch data := agg.(type) {
	case metricdata.Sum[int64]:
		for _, dp := range data.DataPoints {
			out = append(out, dp.Attributes)
		}
	case metricdata.Histogram[int64]:
		for _, dp := range data.DataPoints {
			out = append(out, dp.Attributes)
		}
	case metricdata.Histogram[float64]:
		for _, dp := range data.DataPoints {
			out = append(out, dp.Attributes)
		}
	default:
		t.Fatalf("metric %s: unhandled aggregation %T — add it to dataPointAttributes so the no-PII walk keeps covering it", name, agg)
	}
	return out
}

// TestToolCategoryOnlyEmitsClosedSetValues is the direct unit-level guard on
// the one projection that reads a tool name: whatever it is fed, the output
// must be a member of builtinToolCategories ∪ {"mcp", "other"}.
func TestToolCategoryOnlyEmitsClosedSetValues(t *testing.T) {
	inputs := []string{
		"", "Read", "Bash", "mcp__evilserver__leak_this_name", "mcp__", "mcp_",
		"secret-tool-name-marker", "MCP__X__Y", "read", "Read ",
		"agent-def-derived-name", strings.Repeat("x", 4096),
	}
	for _, in := range inputs {
		got := toolCategory(in)
		if got == categoryMCP || got == categoryOther {
			continue
		}
		if !builtinToolCategories[got] {
			t.Errorf("toolCategory(%q) = %q, which is outside the closed set (builtins ∪ {%q, %q})",
				in, got, categoryMCP, categoryOther)
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
