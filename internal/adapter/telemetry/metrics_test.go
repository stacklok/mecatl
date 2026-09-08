package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// newTestMetrics builds a Metrics adapter backed by a ManualReader so tests can
// Collect() the recorded data points directly. It installs the same explicit-bucket
// histogram views Setup uses for the latency instruments (ADR 0045), so the latency
// series collect as classic Histogram[float64] here too.
func newTestMetrics(t *testing.T) (*Metrics, *metric.ManualReader) {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(append([]metric.Option{metric.WithReader(reader)}, latencyViewOpts()...)...)
	m, err := NewMetrics(mp)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	return m, reader
}

// latencyViewOpts adapts the production LatencyViews() into MeterProvider options
// so tests install the SAME explicit-bucket views the production pipeline does —
// proving the latency series collect as classic explicit-bucket histograms here too.
func latencyViewOpts() []metric.Option {
	views := LatencyViews()
	opts := make([]metric.Option, 0, len(views))
	for _, v := range views {
		opts = append(opts, metric.WithView(v))
	}
	return opts
}

// collect gathers all metrics into a flat name→Aggregation map for assertions.
func collect(t *testing.T, reader *metric.ManualReader) map[string]metricdata.Aggregation {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	out := make(map[string]metricdata.Aggregation)
	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			out[md.Name] = md.Data
		}
	}
	return out
}

// sumPoint returns the int64 Sum value whose attribute key equals value, or
// fails if absent.
func sumPoint(t *testing.T, agg metricdata.Aggregation, key, value string) int64 {
	t.Helper()
	sum, ok := agg.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("aggregation is %T, want Sum[int64]", agg)
	}
	for _, dp := range sum.DataPoints {
		if v, present := dp.Attributes.Value(attribute.Key(key)); present && v.AsString() == value {
			return dp.Value
		}
	}
	t.Fatalf("no data point with %s=%q (points: %d)", key, value, len(sum.DataPoints))
	return 0
}

func TestMetricsLearningActivitiesUseClosedContentFreeLabelsAndTokenUnits(t *testing.T) {
	m, reader := newTestMetrics(t)
	activities := []learning.Activity{
		{Kind: learning.ActivityAdmitted, Reason: learning.ReasonHardTrigger, Sensitivity: learning.Balanced, Count: 1},
		{Kind: learning.ActivitySkipped, Reason: learning.ReasonBelowThreshold, Sensitivity: learning.Conservative, Count: 1},
		{Kind: learning.ActivityDuplicate, Reason: learning.ReasonDuplicate, Sensitivity: learning.Balanced, Count: 1},
		{Kind: learning.ActivityRateLimited, Reason: learning.ReasonRateLimit, Sensitivity: learning.Balanced, Count: 1},
		{Kind: learning.ActivityQueueFull, Reason: learning.ReasonQueueFull, Sensitivity: learning.Balanced, Count: 1},
		{Kind: learning.ActivityClosed, Reason: learning.ReasonCoordinatorClosed, Sensitivity: learning.Balanced, Count: 1},
		{Kind: learning.ActivityTimedOut, Reason: learning.ReasonTimeout, Sensitivity: learning.Balanced, Count: 1},
		{Kind: learning.ActivityReservedTokens, Reason: learning.ReasonWeightedThreshold, Sensitivity: learning.Balanced, Count: 4321},
	}
	for _, activity := range activities {
		m.EmitLearning(activity)
	}
	agg := collect(t, reader)["mecatl.learning.activity"]
	sum, ok := agg.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("learning aggregation = %T", agg)
	}
	allowed := map[attribute.Key]bool{attribute.Key(attrType): true, attribute.Key(attrReason): true, attribute.Key(attrSensitivity): true}
	for _, point := range sum.DataPoints {
		for _, attr := range point.Attributes.ToSlice() {
			if !allowed[attr.Key] {
				t.Fatalf("identity/content attribute leaked: %s", attr.Key)
			}
		}
	}
	if got := sumPoint(t, agg, attrType, string(learning.ActivityReservedTokens)); got != 4321 {
		t.Fatalf("reserved token units = %d, want 4321", got)
	}
}

func TestMetricsLearningActivitiesRejectValuesOutsideClosedVocabularies(t *testing.T) {
	m, reader := newTestMetrics(t)
	kinds := []learning.ActivityKind{
		learning.ActivityAdmitted, learning.ActivitySkipped, learning.ActivityRateLimited,
		learning.ActivityDuplicate, learning.ActivityQueueFull, learning.ActivityClosed,
		learning.ActivityAbstained, learning.ActivityStaged, learning.ActivityPromoted,
		learning.ActivityConflicted, learning.ActivityFailed, learning.ActivityTimedOut,
		learning.ActivityReservedTokens, learning.ActivitySkillActivatedValidated,
		learning.ActivitySkillActivatedEvaluated, learning.ActivitySkillStaged,
		learning.ActivitySkillRejected,
	}
	reasons := []learning.AdmissionReason{
		learning.ReasonBelowThreshold, learning.ReasonInvalidCurrentSpan, learning.ReasonNonMainSession,
		learning.ReasonIneligibleStop, learning.ReasonTrivialRun, learning.ReasonExplicitRemember,
		learning.ReasonExplicitLearnProcedure, learning.ReasonRepeatedCorrection,
		learning.ReasonTrustedHostContradiction, learning.ReasonFailureRecovery,
		learning.ReasonRepeatedToolSequence, learning.ReasonSubstantialSuccess,
		learning.ReasonModelTurnsModifier, learning.ReasonSuccessfulToolsModifier,
		learning.ReasonRunTokensModifier, learning.ReasonPolicyAlways, learning.ReasonPolicyNever,
		learning.ReasonHostRequested, learning.ReasonHardTrigger, learning.ReasonWeightedThreshold,
		learning.ReasonDuplicate, learning.ReasonRateLimit, learning.ReasonQueueFull,
		learning.ReasonCoordinatorClosed, learning.ReasonTimeout, learning.ReasonReflectionFailed,
		learning.ReasonAbstained, learning.ReasonStaged, learning.ReasonPromoted, learning.ReasonConflicted,
	}
	sensitivities := []learning.Sensitivity{learning.Conservative, learning.Balanced, learning.Eager}
	for _, kind := range kinds {
		m.EmitLearning(learning.Activity{Kind: kind, Reason: learning.ReasonHardTrigger, Sensitivity: learning.Balanced, Count: 1})
	}
	for _, reason := range reasons {
		m.EmitLearning(learning.Activity{Kind: learning.ActivityAdmitted, Reason: reason, Sensitivity: learning.Balanced, Count: 1})
	}
	for _, sensitivity := range sensitivities {
		m.EmitLearning(learning.Activity{Kind: learning.ActivityAdmitted, Reason: learning.ReasonHardTrigger, Sensitivity: sensitivity, Count: 1})
	}
	for _, invalid := range []learning.Activity{
		{Kind: "injected", Reason: learning.ReasonHardTrigger, Sensitivity: learning.Balanced, Count: 1000},
		{Kind: learning.ActivityAdmitted, Reason: "model-content", Sensitivity: learning.Balanced, Count: 1000},
		{Kind: learning.ActivityAdmitted, Reason: learning.ReasonHardTrigger, Sensitivity: learning.Sensitivity(255), Count: 1000},
		{Kind: learning.ActivityAdmitted, Reason: learning.ReasonHardTrigger, Sensitivity: learning.SensitivityUnset, Count: 1000},
	} {
		m.EmitLearning(invalid)
	}

	agg := collect(t, reader)["mecatl.learning.activity"]
	sum, ok := agg.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("learning aggregation = %T", agg)
	}
	allowedKinds, allowedReasons, allowedSensitivities := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, value := range kinds {
		allowedKinds[string(value)] = true
	}
	for _, value := range reasons {
		allowedReasons[string(value)] = true
	}
	for _, value := range sensitivities {
		allowedSensitivities[value.String()] = true
	}
	var total int64
	for _, point := range sum.DataPoints {
		total += point.Value
		for key, allowed := range map[attribute.Key]map[string]bool{
			attribute.Key(attrType): allowedKinds, attribute.Key(attrReason): allowedReasons, attribute.Key(attrSensitivity): allowedSensitivities,
		} {
			value, present := point.Attributes.Value(key)
			if !present || !allowed[value.AsString()] {
				t.Fatalf("point has invalid %s=%q", key, value.AsString())
			}
		}
	}
	if want := int64(len(kinds) + len(reasons) + len(sensitivities)); total != want {
		t.Fatalf("accepted activity count = %d, want %d", total, want)
	}
}

func TestMetricsEventsTotal(t *testing.T) {
	m, reader := newTestMetrics(t)

	m.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	m.Emit(context.Background(), session.Event{Type: session.EvTurnStart, Turn: 0})
	m.Emit(context.Background(), session.Event{Type: session.EvMessageDelta})
	m.Emit(context.Background(), session.Event{Type: session.EvMessageDelta})

	data := collect(t, reader)
	events := data["mecatl.events"]
	if got := sumPoint(t, events, attrType, "session.init"); got != 1 {
		t.Errorf("events{session.init} = %d, want 1", got)
	}
	if got := sumPoint(t, events, attrType, "message.delta"); got != 2 {
		t.Errorf("events{message.delta} = %d, want 2", got)
	}
}

func TestFailedStepRetryRunBalancesActiveRunMetric(t *testing.T) {
	metrics, reader := newTestMetrics(t)
	eng := agent.NewEngine(agent.Deps{
		LLM:                 mockllm.New(mockllm.TextTurn("retried")),
		Catalog:             tool.NewCatalog(),
		Policy:              permpolicy.NewPolicy(nil, nil),
		Sink:                metrics,
		MaxNoProgressNudges: -1,
	})
	sess := session.New("retry-metrics", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordFailureMetadata(session.RetryDispositionRetryable, session.StreamProgressPrecommit); err != nil {
		t.Fatal(err)
	}
	if err := sess.PrepareFailedStepRetry(); err != nil {
		t.Fatal(err)
	}
	env := tool.MustEnvironment(sess.EnvironmentRef, memfs.NewWorkspace("/ws"), memledger.New(), nil)
	run := eng.RetryFailedStep(context.Background(), sess, env)
	for range run.Events() {
	}
	if got := activeRunsValue(t, reader); got != 0 {
		t.Fatalf("active_runs after failed-step retry = %d, want 0", got)
	}
	if got := sumPoint(t, collect(t, reader)["mecatl.events"], attrType, string(session.EvSessionInit)); got != 1 {
		t.Fatalf("retry session.init events = %d, want 1", got)
	}
}

func TestMetricsRunsAndActiveRuns(t *testing.T) {
	m, reader := newTestMetrics(t)

	m.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	if got := activeRunsValue(t, reader); got != 1 {
		t.Fatalf("active_runs after init = %d, want 1", got)
	}
	m.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})
	if got := activeRunsValue(t, reader); got != 0 {
		t.Fatalf("active_runs after result = %d, want 0", got)
	}

	runs := collect(t, reader)["mecatl.runs"]
	if got := sumPoint(t, runs, attrStop, "end_turn"); got != 1 {
		t.Errorf("runs{end_turn} = %d, want 1", got)
	}
}

// TestMetricsTurnEndCountsTurn asserts an EvTurnEnd increments the per-turn
// counter (mecatl.turns) — the true per-turn denominator, distinct from the
// run-level mecatl.runs. The counter carries NO stop attribute (EvTurnEnd has
// no stop reason; a non-varying label would mislead an operator).
func TestMetricsTurnEndCountsTurn(t *testing.T) {
	m, reader := newTestMetrics(t)

	m.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, TurnEnd: &session.TurnEndPayload{DurationMs: 10}})

	turns := collect(t, reader)["mecatl.turns"]
	sum, ok := turns.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("turns is %T, want Sum[int64]", turns)
	}
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Fatalf("turns = %+v, want single point 1", sum.DataPoints)
	}
	// The point must not carry a "stop" attribute (label dropped).
	if _, present := sum.DataPoints[0].Attributes.Value(attrStop); present {
		t.Errorf("turns carries a %q attribute; it must be unlabelled by stop", attrStop)
	}
}

// TestMetricsNoProgressCountsEmpty asserts that an EvNoProgress in ISOLATION
// increments the empty-turn counter (mecatl.turn_empty) and on its own does not
// touch mecatl.turns. This is a unit fact about the EvNoProgress branch alone;
// in the real loop an empty turn ALSO emits EvTurnEnd (see
// TestMetricsEmptyTurnBumpsBothCounters for the realistic relationship).
func TestMetricsNoProgressCountsEmpty(t *testing.T) {
	m, reader := newTestMetrics(t)

	m.Emit(context.Background(), session.Event{Type: session.EvNoProgress})

	data := collect(t, reader)
	empty := data["mecatl.turn_empty"]
	sum, ok := empty.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("turn_empty is %T, want Sum[int64]", empty)
	}
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Errorf("turn_empty = %+v, want single point 1", sum.DataPoints)
	}
	// A bare EvNoProgress (no accompanying EvTurnEnd) does not bump turns.
	if _, present := data["mecatl.turns"]; present {
		t.Errorf("turns recorded on a bare no-progress event; want none")
	}
}

// TestMetricsEmptyTurnBumpsBothCounters drives the REALISTIC loop sequence for
// one empty/no-progress turn: the loop emits EvTurnEnd unconditionally on the
// turn boundary, THEN EvNoProgress. Both fire, so turns_total AND turn_empty
// both increment by one — they are NOT mutually exclusive; turn_empty is the
// empty SUBSET of turns (the empty-turn share, turn_empty/turns).
func TestMetricsEmptyTurnBumpsBothCounters(t *testing.T) {
	m, reader := newTestMetrics(t)

	// Realistic per-turn ordering: turn boundary, then the no-progress nudge.
	m.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, TurnEnd: &session.TurnEndPayload{DurationMs: 5}})
	m.Emit(context.Background(), session.Event{Type: session.EvNoProgress})

	data := collect(t, reader)

	turns, ok := data["mecatl.turns"].(metricdata.Sum[int64])
	if !ok || len(turns.DataPoints) != 1 || turns.DataPoints[0].Value != 1 {
		t.Fatalf("turns = %+v, want single point 1 (the empty turn IS a completed turn)", data["mecatl.turns"])
	}
	empty, ok := data["mecatl.turn_empty"].(metricdata.Sum[int64])
	if !ok || len(empty.DataPoints) != 1 || empty.DataPoints[0].Value != 1 {
		t.Fatalf("turn_empty = %+v, want single point 1 (the empty subset)", data["mecatl.turn_empty"])
	}
}

// TestMetricsTurnEmptyRoleScoped asserts a role-scoped view tags the empty-turn
// counter with its role family (a child's no-progress turn is role-attributed).
func TestMetricsTurnEmptyRoleScoped(t *testing.T) {
	m, reader := newTestMetrics(t)
	sub := m.WithRole(RoleSubagent)

	sub.Emit(context.Background(), session.Event{Type: session.EvNoProgress})

	empty := collect(t, reader)["mecatl.turn_empty"]
	if got := sumPointWith(t, empty, map[string]string{attrRole: RoleSubagent}); got != 1 {
		t.Errorf("turn_empty{role=subagent} = %d, want 1", got)
	}
}

// TestMetricsResultNil drives the distinct r == nil branch of recordResult: a
// terminal EvResult with no payload must still count the run (stop=none) and must
// not touch tokens or the cache-hit gauge, and must not panic.
func TestMetricsResultNil(t *testing.T) {
	m, reader := newTestMetrics(t)

	m.Emit(context.Background(), session.Event{Type: session.EvResult, Result: nil})

	data := collect(t, reader)
	runs := data["mecatl.runs"]
	if got := sumPoint(t, runs, attrStop, string(session.StopNone)); got != 1 {
		t.Errorf("runs{stop=none} = %d, want 1", got)
	}
	// tokens and cache_hit_ratio must be untouched: with nothing recorded, the
	// instruments produce no data points at all.
	if _, present := data["mecatl.tokens"]; present {
		t.Errorf("tokens recorded on nil result; want none")
	}
	if _, present := data["mecatl.cache_hit_ratio"]; present {
		t.Errorf("cache_hit_ratio recorded on nil result; want none")
	}
}

// TestMetricsCacheHitLastValue asserts the cache-hit gauge is last-value: two
// results with different ratios leave the gauge reflecting only the second.
func TestMetricsCacheHitLastValue(t *testing.T) {
	m, reader := newTestMetrics(t)

	// First result: 25/(100) cache-read share.
	m.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
		Stop:  session.StopEndTurn,
		Usage: session.Usage{InputTokens: 75, CacheReadTokens: 25},
	}})
	// Second result with a different ratio (50/100) — this is the value the gauge
	// must report.
	m.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
		Stop:  session.StopEndTurn,
		Usage: session.Usage{InputTokens: 50, CacheReadTokens: 50},
	}})

	want := (session.Usage{InputTokens: 50, CacheReadTokens: 50}).CacheHitRate()
	gauge, ok := collect(t, reader)["mecatl.cache_hit_ratio"].(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("cache_hit_ratio is %T, want Gauge[float64]", collect(t, reader)["mecatl.cache_hit_ratio"])
	}
	if len(gauge.DataPoints) != 1 || gauge.DataPoints[0].Value != want {
		t.Errorf("cache_hit_ratio = %+v, want single point %v (last result only)", gauge.DataPoints, want)
	}
}

// activeRunsValue collects the (single) active_runs up/down counter value. It is
// a non-monotonic Sum, so this verifies the decrement-on-result semantics.
func activeRunsValue(t *testing.T, reader *metric.ManualReader) int64 {
	t.Helper()
	agg := collect(t, reader)["mecatl.active_runs"]
	sum, ok := agg.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("active_runs is %T, want Sum[int64]", agg)
	}
	if sum.IsMonotonic {
		t.Errorf("active_runs Sum is monotonic; want non-monotonic (UpDownCounter)")
	}
	if len(sum.DataPoints) != 1 {
		t.Fatalf("active_runs points = %d, want 1", len(sum.DataPoints))
	}
	return sum.DataPoints[0].Value
}

func TestMetricsTokensAndCacheRatio(t *testing.T) {
	m, reader := newTestMetrics(t)

	m.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
		Stop: session.StopEndTurn,
		Usage: session.Usage{
			InputTokens:      100,
			OutputTokens:     40,
			CacheReadTokens:  25,
			CacheWriteTokens: 10,
		},
	}})

	data := collect(t, reader)
	tokens := data["mecatl.tokens"]
	cases := map[string]int64{"input": 100, "output": 40, "cache_read": 25, "cache_write": 10}
	for kind, want := range cases {
		if got := sumPoint(t, tokens, attrKind, kind); got != want {
			t.Errorf("tokens{%s} = %d, want %d", kind, got, want)
		}
	}

	gauge, ok := data["mecatl.cache_hit_ratio"].(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("cache_hit_ratio is %T, want Gauge[float64]", data["mecatl.cache_hit_ratio"])
	}
	if len(gauge.DataPoints) != 1 || gauge.DataPoints[0].Value != 0.25 {
		t.Errorf("cache_hit_ratio = %+v, want single point 0.25", gauge.DataPoints)
	}
}

func TestMetricsPermissionAsks(t *testing.T) {
	m, reader := newTestMetrics(t)

	m.Emit(context.Background(), session.Event{Type: session.EvPermissionAsk})
	m.Emit(context.Background(), session.Event{Type: session.EvPermissionAsk})

	agg := collect(t, reader)["mecatl.permission.asks"]
	sum, ok := agg.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("permission.asks is %T, want Sum[int64]", agg)
	}
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 2 {
		t.Errorf("permission.asks = %+v, want single point 2", sum.DataPoints)
	}
}

func TestMetricsToolCallExplicitBucketHistogram(t *testing.T) {
	m, reader := newTestMetrics(t)

	m.ToolCall("sess-1", session.NewToolCall("c1", "bash", nil),
		session.NewToolResult("c1", "ok"), 0, 250*time.Millisecond)
	m.ToolCall("sess-1", session.NewToolCall("c2", "bash", nil),
		session.NewToolError("c2", "boom"), 0, 10*time.Millisecond)

	data := collect(t, reader)
	calls := data["mecatl.tool.calls"]
	if got := sumPoint(t, calls, attrError, "false"); got != 1 {
		t.Errorf("tool.calls{error=false} = %d, want 1", got)
	}
	if got := sumPoint(t, calls, attrError, "true"); got != 1 {
		t.Errorf("tool.calls{error=true} = %d, want 1", got)
	}

	// The view must turn the tool-duration instrument into an explicit-bucket
	// histogram (ADR 0045), with both observations on the single bash series.
	dp := classicHist(t, data, toolDurationInstrument)
	if dp.Count != 2 {
		t.Errorf("tool.duration count = %d, want 2", dp.Count)
	}
	// Sum must be ~0.26s (250ms + 10ms recorded via took.Seconds()). This catches
	// a unit regression (e.g. recording nanoseconds or milliseconds) that Count
	// alone would not.
	if got := dp.Sum; got < 0.259 || got > 0.261 {
		t.Errorf("tool.duration sum = %v, want ≈0.26", got)
	}
	// The explicit ladder must carry our boundaries (ADR 0045), so the classic
	// le= exposition yields quantiles. Spot-check the low and high ends are present.
	if len(dp.Bounds) != len(latencyBucketBoundaries) {
		t.Errorf("tool.duration bounds = %d, want %d (latencyBucketBoundaries)", len(dp.Bounds), len(latencyBucketBoundaries))
	}
	if len(dp.Bounds) > 0 && (dp.Bounds[0] != latencyBucketBoundaries[0] || dp.Bounds[len(dp.Bounds)-1] != latencyBucketBoundaries[len(latencyBucketBoundaries)-1]) {
		t.Errorf("tool.duration bounds endpoints = [%v..%v], want [%v..%v]",
			dp.Bounds[0], dp.Bounds[len(dp.Bounds)-1], latencyBucketBoundaries[0], latencyBucketBoundaries[len(latencyBucketBoundaries)-1])
	}
}

// classicHist asserts the named instrument collected as a classic explicit-bucket
// Histogram (ADR 0045) and returns its single data point, failing otherwise.
func classicHist(t *testing.T, data map[string]metricdata.Aggregation, name string) metricdata.HistogramDataPoint[float64] {
	t.Helper()
	h, ok := data[name].(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("%s is %T, want Histogram[float64]", name, data[name])
	}
	if len(h.DataPoints) != 1 {
		t.Fatalf("%s series = %d, want 1", name, len(h.DataPoints))
	}
	return h.DataPoints[0]
}

// TestMetricsLatencyInstruments asserts the five latency instruments — turn
// duration, TTFT, inter-token (mean), inter-token (max), and tool queue — all
// collect as classic explicit-bucket histograms (ADR 0045) with sane unit-converted
// values, and that the "not measured" zero-guards hold (a turn with no content
// records turn duration only; a zero queue time still records on the queue
// histogram since 0 is a real, immediate-dispatch observation there).
func TestMetricsLatencyInstruments(t *testing.T) {
	m, reader := newTestMetrics(t)

	// A fully-measured turn: duration 195ms, TTFT 30ms, mean inter-token 25ms.
	m.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, TurnEnd: &session.TurnEndPayload{
		DurationMs: 195, TTFTMs: 30, InterTokenMeanMs: 25, InterTokenMaxMs: 40,
	}})
	// A Clock-less / no-output turn: duration measured, TTFT/inter-token "not
	// measured" (0) → must NOT add a TTFT/inter-token observation.
	m.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, TurnEnd: &session.TurnEndPayload{
		DurationMs: 5, TTFTMs: 0, InterTokenMeanMs: 0, InterTokenMaxMs: 0,
	}})
	// A queued mutate tool: 120ms queue, 30ms execution.
	m.ToolCall("s", session.NewToolCall("c", "edit", nil),
		session.NewToolResult("c", "ok"), 120*time.Millisecond, 30*time.Millisecond)

	data := collect(t, reader)

	turn := classicHist(t, data, turnDurationInstrument)
	if turn.Count != 2 { // both turns record a duration
		t.Errorf("turn.duration count = %d, want 2", turn.Count)
	}
	if got := turn.Sum; got < 0.199 || got > 0.201 { // 195ms + 5ms = 0.2s
		t.Errorf("turn.duration sum = %v, want ≈0.2", got)
	}

	ttft := classicHist(t, data, ttftInstrument)
	if ttft.Count != 1 { // only the measured turn
		t.Errorf("ttft count = %d, want 1 (the no-content turn must not record)", ttft.Count)
	}
	if got := ttft.Sum; got < 0.029 || got > 0.031 {
		t.Errorf("ttft sum = %v, want ≈0.03", got)
	}

	inter := classicHist(t, data, interTokenInstrument)
	if inter.Count != 1 {
		t.Errorf("inter_token count = %d, want 1", inter.Count)
	}
	if got := inter.Sum; got < 0.0249 || got > 0.0251 {
		t.Errorf("inter_token sum = %v, want ≈0.025", got)
	}

	interMax := classicHist(t, data, interTokenMaxInstrument)
	if interMax.Count != 1 { // only the measured turn (max 40ms)
		t.Errorf("inter_token.max count = %d, want 1", interMax.Count)
	}
	if got := interMax.Sum; got < 0.0399 || got > 0.0401 {
		t.Errorf("inter_token.max sum = %v, want ≈0.04", got)
	}

	queue := classicHist(t, data, toolQueueInstrument)
	if queue.Count != 1 {
		t.Errorf("tool.queue count = %d, want 1", queue.Count)
	}
	if got := queue.Sum; got < 0.119 || got > 0.121 { // 120ms
		t.Errorf("tool.queue sum = %v, want ≈0.12", got)
	}
}

// TestMetricsLatencyGuardsIndependent proves the >0 skip-guards in recordTurnEnd
// are INDEPENDENT, not coupled: a turn that measured TTFT but produced fewer than
// two content chunks (a single content chunk) carries TTFTMs>0 with
// InterTokenMeanMs==0 and InterTokenMaxMs==0. The TTFT histogram must increment
// while BOTH inter-token histograms (mean and max) must NOT — a regression that
// gated them on a shared condition would wrongly record (or wrongly drop) one.
func TestMetricsLatencyGuardsIndependent(t *testing.T) {
	m, reader := newTestMetrics(t)

	// Single-content-chunk-style payload: TTFT measured, both gaps "not measured".
	m.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, TurnEnd: &session.TurnEndPayload{
		DurationMs: 50, TTFTMs: 20, InterTokenMeanMs: 0, InterTokenMaxMs: 0,
	}})

	data := collect(t, reader)

	ttft := classicHist(t, data, ttftInstrument)
	if ttft.Count != 1 {
		t.Errorf("ttft count = %d, want 1 (TTFT measured)", ttft.Count)
	}
	// Neither inter-token histogram should have any data point at all: with nothing
	// recorded, the instrument produces no series.
	if _, present := data[interTokenInstrument]; present {
		t.Errorf("inter_token (mean) recorded on a zero-gap turn; guards are coupled to TTFT")
	}
	if _, present := data[interTokenMaxInstrument]; present {
		t.Errorf("inter_token.max recorded on a zero-gap turn; guards are coupled to TTFT")
	}
}

// TestMetricsScrapeThroughPrometheusExporter wires the adapter through the OTel
// prometheus exporter (as Setup does) and asserts the expected series names
// appear in the /metrics text. These pinned names are mecatl's choice — there is
// no legacy byte-for-byte contract — so a future rename is caught here.
func TestMetricsScrapeThroughPrometheusExporter(t *testing.T) {
	reg := prometheus.NewRegistry()
	exp, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		t.Fatalf("prometheus exporter: %v", err)
	}
	mp := metric.NewMeterProvider(append([]metric.Option{metric.WithReader(exp)}, latencyViewOpts()...)...)
	m, err := NewMetrics(mp)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}

	m.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	m.Emit(context.Background(), session.Event{Type: session.EvPermissionAsk})
	m.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
		Stop:  session.StopEndTurn,
		Usage: session.Usage{InputTokens: 10, CacheReadTokens: 5},
	}})
	m.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, TurnEnd: &session.TurnEndPayload{
		DurationMs: 100, TTFTMs: 20, InterTokenMeanMs: 10, InterTokenMaxMs: 15,
	}})
	m.Emit(context.Background(), session.Event{Type: session.EvNoProgress})
	m.ToolCall("s", session.NewToolCall("c", "bash", nil), session.NewToolResult("c", "ok"), 3*time.Millisecond, 5*time.Millisecond)
	// Schedule-fire metrics (issue #233): a fired fire with a duration, a
	// skipped fire (no duration), and a failed fire. EmitSchedule is the
	// composition-injected callback target; the outcome label ("fired" /
	// "skipped" / "failed") is the schedule-lifecycle dimension.
	m.EmitSchedule(session.SchedulePayload{ScheduleName: "s", Kind: "fired"}, 250*time.Millisecond)
	m.EmitSchedule(session.SchedulePayload{ScheduleName: "s", Kind: "skipped"}, 0)
	m.EmitSchedule(session.SchedulePayload{ScheduleName: "s", Kind: "failed"}, 0)

	body := scrape(t, MetricsHandler(reg))
	// The prometheus exporter applies its own _total / unit suffixes; these are
	// the names it naturally produces for our instruments.
	for _, name := range []string{
		"mecatl_events_total",
		"mecatl_runs_total",
		"mecatl_turns_total",
		"mecatl_turn_empty_total",
		"mecatl_tool_calls_total",
		"mecatl_tokens_total",
		"mecatl_permission_asks_total",
		"mecatl_active_runs",
		"mecatl_cache_hit_ratio",
		"mecatl_tool_duration_seconds",
		"mecatl_turn_duration_seconds",
		"mecatl_ttft_seconds",
		"mecatl_inter_token_seconds",
		"mecatl_inter_token_max_seconds",
		"mecatl_tool_queue_seconds",
		"mecatl_schedule_fires_total",
		"mecatl_schedule_fire_duration_seconds",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics missing series %q", name)
		}
	}
}

// TestEmitSchedule asserts the schedule-fire counter (labelled by outcome) and
// the fire-duration histogram record correctly, and that a skipped fire (no
// duration) bumps the counter WITHOUT recording a histogram observation. It also
// covers the nil-safe path (a nil Metrics must not panic). Schedule metrics are
// NOT a role-family (issue #233): the series carry NO role label.
func TestEmitSchedule(t *testing.T) {
	m, reader := newTestMetrics(t)

	// A fired fire with a duration; a failed fire with a duration; a skipped
	// fire (no duration). The counter is bumped per outcome; the histogram only
	// records when duration > 0.
	m.EmitSchedule(session.SchedulePayload{ScheduleName: "a", Kind: "fired"}, 250*time.Millisecond)
	m.EmitSchedule(session.SchedulePayload{ScheduleName: "b", Kind: "failed"}, 120*time.Millisecond)
	m.EmitSchedule(session.SchedulePayload{ScheduleName: "c", Kind: "skipped"}, 0)

	data := collect(t, reader)
	fires := data["mecatl.schedule.fires"]
	if got := sumPoint(t, fires, attrOutcome, "fired"); got != 1 {
		t.Errorf("schedule.fires{fired} = %d, want 1", got)
	}
	if got := sumPoint(t, fires, attrOutcome, "failed"); got != 1 {
		t.Errorf("schedule.fires{failed} = %d, want 1", got)
	}
	if got := sumPoint(t, fires, attrOutcome, "skipped"); got != 1 {
		t.Errorf("schedule.fires{skipped} = %d, want 1", got)
	}

	// The fire-duration histogram: two observations (fired + failed); the
	// skipped fire (duration 0) records NOTHING. The instrument shares the
	// latencyBucketBoundaries explicit-bucket ladder (it is in latencyInstruments).
	// It is labelled by outcome, so there are TWO data points (fired, failed).
	hist, ok := data["mecatl.schedule.fire_duration"].(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("schedule.fire_duration is %T, want Histogram[float64]", data["mecatl.schedule.fire_duration"])
	}
	var count uint64
	var boundsLen int
	for _, dp := range hist.DataPoints {
		count += dp.Count
		boundsLen = len(dp.Bounds)
	}
	if count != 2 {
		t.Errorf("schedule.fire_duration count = %d, want 2 (fired+failed; skipped must not record)", count)
	}
	if boundsLen != len(latencyBucketBoundaries) {
		t.Errorf("schedule.fire_duration bounds = %d, want %d (latencyBucketBoundaries)", boundsLen, len(latencyBucketBoundaries))
	}

	// Nil-safe: a nil Metrics must not panic (the byte-identical no-metrics path).
	var nilM *Metrics
	nilM.EmitSchedule(session.SchedulePayload{Kind: "fired"}, time.Second)
}

// TestMetricsThroughRealSetup drives the production wiring end to end: it calls
// the real Setup (metrics-on, no OTLP endpoint), builds NewMetrics from
// providers.Meter, records events, then scrapes MetricsHandler(providers.Registry).
// This proves (a) the Meter and the Registry returned by Setup are the SAME
// pipeline — a mecatl_* domain series only appears if NewMetrics's meter feeds the
// registry's exporter — and (b) Setup installs the explicit-bucket views (ADR
// 0045), so the tool-duration series AND the newest latency series (inter_token.max)
// both render as classic histograms with MULTIPLE finite le= buckets (the zero-config
// quantile fix for issue #158) rather than collapsing to a single le="+Inf" bucket.
// Proving inter_token.max here confirms the latencyInstruments-slice → LatencyViews()
// → Setup path covers a new instrument automatically. Deleting LatencyViews from
// Setup would regress (b) back to the SDK default ladder (a different, coarser set
// of finite buckets), which the boundary-endpoint check below also catches.
func TestMetricsThroughRealSetup(t *testing.T) {
	providers, err := Setup(context.Background(), OTLPConfig{}) // metrics only
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	// Shutdown before any (future) leak assertion: the runtime collector and the
	// SDK reader own goroutines that Shutdown stops.
	defer func() { _ = providers.Shutdown(context.Background()) }()

	m, err := NewMetrics(providers.Meter)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}

	m.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	m.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
		Stop:  session.StopEndTurn,
		Usage: session.Usage{InputTokens: 10, CacheReadTokens: 5},
	}})
	m.ToolCall("s", session.NewToolCall("c", "bash", nil),
		session.NewToolResult("c", "ok"), 0, 250*time.Millisecond)
	// A turn with a measured worst inter-token gap so the newest latency series is
	// present and exercised through the real Setup pipeline.
	m.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, TurnEnd: &session.TurnEndPayload{
		DurationMs: 100, TTFTMs: 20, InterTokenMeanMs: 10, InterTokenMaxMs: 40,
	}})

	body := scrape(t, MetricsHandler(providers.Registry))

	// (a) A domain series proves Meter and Registry are the same pipeline.
	if !strings.Contains(body, "mecatl_events_total") {
		t.Errorf("/metrics from real Setup missing mecatl_events_total: Meter/Registry not the same pipeline")
	}
	if !strings.Contains(body, "mecatl_tool_duration_seconds_count") {
		t.Errorf("/metrics missing mecatl_tool_duration_seconds_count")
	}

	// (b) The explicit-bucket view (ADR 0045) emits one le= bucket line per
	// configured boundary PLUS the le="+Inf" overflow line — so a finite le=
	// boundary (e.g. le="0.25") is present and the quantile-bearing classic ladder
	// is exposed. The base-2 exponential view we superseded would instead collapse
	// to a single le="+Inf" line (issue #158). Asserting our specific boundaries
	// (the low-end 0.001 and a mid-ladder 0.25) also distinguishes our ladder from
	// the SDK default explicit buckets, so dropping LatencyViews from Setup regresses
	// this too. Checked for inter_token.max (driven only by the latencyInstruments
	// slice) to prove a new instrument inherits the view through Setup with no
	// per-instrument wiring.
	assertExplicitBuckets := func(prefix string) {
		t.Helper()
		var bucketLines, finiteLines int
		var hasInf, hasLow, hasMid bool
		for _, line := range strings.Split(body, "\n") {
			if !strings.HasPrefix(line, prefix+"_bucket{") {
				continue
			}
			bucketLines++
			switch {
			case strings.Contains(line, `le="+Inf"`):
				hasInf = true
			default:
				finiteLines++
			}
			if strings.Contains(line, `le="0.001"`) {
				hasLow = true
			}
			if strings.Contains(line, `le="0.25"`) {
				hasMid = true
			}
		}
		if finiteLines < 2 {
			t.Errorf("%s finite-le _bucket lines = %d, want ≥2 (explicit-bucket view; a single +Inf bucket means the exponential view regressed)", prefix, finiteLines)
		}
		if !hasInf {
			t.Errorf("%s missing the le=+Inf overflow bucket", prefix)
		}
		if !hasLow || !hasMid {
			t.Errorf("%s missing our explicit boundaries (le=0.001 present=%v, le=0.25 present=%v); ladder not the latencyBucketBoundaries", prefix, hasLow, hasMid)
		}
		_ = bucketLines
	}
	assertExplicitBuckets("mecatl_inter_token_max_seconds")
	assertExplicitBuckets("mecatl_tool_duration_seconds")
}

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// TestSetupRuntimeCollectorSeries verifies Setup starts the contrib runtime
// collector against its meter provider — go_goroutine_count is the literal series
// the collector emits (from go.goroutine.count via the prometheus exporter).
func TestSetupRuntimeCollectorSeries(t *testing.T) {
	providers, err := Setup(context.Background(), OTLPConfig{}) // no endpoint: metrics only
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() { _ = providers.Shutdown(context.Background()) }()

	if providers.Registry == nil || providers.Meter == nil {
		t.Fatal("Setup returned nil Meter/Registry with metrics always-on")
	}

	body := scrape(t, MetricsHandler(providers.Registry))
	if !strings.Contains(body, "go_goroutine_count") {
		t.Errorf("/metrics missing runtime collector series go_goroutine_count")
	}
}

func TestNewSinkFansOut(t *testing.T) {
	m, _ := newTestMetrics(t)
	c1 := &countingSink{}
	c2 := &countingSink{}

	// A ctx with a recognisable value so we can assert the fan-out forwards the
	// SAME ctx to EVERY wrapped sink, not a fresh background one.
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")

	sink := NewSink(m, c1, c2)
	sink.Emit(ctx, session.Event{Type: session.EvSessionInit})

	if c1.n != 1 || c2.n != 1 {
		t.Errorf("fan-out sink counts = %d,%d, want 1,1", c1.n, c2.n)
	}
	for i, c := range []*countingSink{c1, c2} {
		if c.lastCtx == nil || c.lastCtx.Value(ctxKey{}) != "marker" {
			t.Errorf("sink %d did not receive the forwarded ctx (got %v)", i, c.lastCtx)
		}
	}
}

type countingSink struct {
	n       int
	lastCtx context.Context //nolint:containedctx // test double records the forwarded ctx for assertion
}

func (c *countingSink) Emit(ctx context.Context, _ session.Event) {
	c.n++
	c.lastCtx = ctx
}
