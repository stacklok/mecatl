package telemetry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// meterName is the instrumentation scope name for the domain meter. The
// prometheus exporter does not fold the scope into series names by default, so
// each instrument carries the "mecatl." prefix below to keep the exposed series
// recognisable (e.g. mecatl_events_total).
const meterName = "github.com/stacklok/mecatl/internal/adapter/telemetry"

// Latency-instrument names. Each is an explicit-bucket-histogram latency
// instrument (ADR 0045, superseding the base-2 exponential aggregation of ADR
// 0018 §5 decision 2). They are exported as package constants because the
// MeterProvider installs an explicit-bucket metric.View keyed on each exact
// name; the view and the instrument name MUST agree, so views target
// instruments by these constants rather than duplicated string literals. The
// explicit ladder renders as classic Prometheus le= buckets in the text
// exposition, so a plain `curl :9099/metrics` / promtool — and the perf-MCP
// reducer's classicLadder path — yield p50/p90/p99 with zero scrape config.
const (
	// toolDurationInstrument is the per-tool execution wall-clock histogram.
	toolDurationInstrument = "mecatl.tool.duration"
	// turnDurationInstrument is the per-turn model-call wall-clock histogram.
	turnDurationInstrument = "mecatl.turn.duration"
	// ttftInstrument is the time-to-first-token histogram.
	ttftInstrument = "mecatl.ttft"
	// interTokenInstrument is the per-turn MEAN inter-token gap histogram — the
	// average gap between consecutive content chunks within a turn.
	interTokenInstrument = "mecatl.inter_token" //nolint:gosec // G101 false positive: a metric instrument name, not a credential
	// interTokenMaxInstrument is the per-turn WORST inter-token gap histogram — the
	// single largest gap between consecutive content chunks within a turn. It is a
	// streaming-jitter tail signal: where mean tracks typical smoothness, max
	// captures the worst stall a user felt mid-turn.
	interTokenMaxInstrument = "mecatl.inter_token.max" //nolint:gosec // G101 false positive: a metric instrument name, not a credential
	// toolQueueInstrument is the tool queue-time histogram: the wait from a call
	// entering dispatch to its execution starting (the coordinated-omission fix).
	toolQueueInstrument = "mecatl.tool.queue"
	// scheduleFireDurationInstrument is the scheduled-task fire wall-clock
	// histogram: the elapsed time from a fire's due time (captured before
	// Store.Claim) to its terminal EvResult. It is a schedule-lifecycle latency
	// signal, distinct from the role-family run/turn latency instruments
	// (schedule metrics are NOT a role-family — issue #233): a fire mints a
	// fresh session whose OWN run already carries role="main" via its
	// EventSink; this instrument captures the schedule-level end-to-end fire
	// cost (due→terminal), not the per-turn cost the run's own metrics already
	// record.
	scheduleFireDurationInstrument = "mecatl.schedule.fire_duration"
)

// latencyInstruments is the single source of truth for which instruments are
// aggregated as explicit-bucket histograms. LatencyViews builds one view per
// entry, so adding a latency instrument here installs its explicit-bucket view
// everywhere the MeterProvider is assembled — no per-call-site duplication of the
// aggregation literal.
var latencyInstruments = []string{
	toolDurationInstrument,
	turnDurationInstrument,
	ttftInstrument,
	interTokenInstrument,
	interTokenMaxInstrument,
	toolQueueInstrument,
	scheduleFireDurationInstrument,
}

// latencyBucketBoundaries is the explicit upper-bound ladder (seconds) every
// latency instrument shares. It deliberately spans from millisecond-scale gaps —
// the .001–.005 low end gives inter_token / tool_queue real resolution — through
// minute-scale turn durations (the 30–300 high end covers slow reasoning-model
// turns, e.g. the 41s main turns observed in live monitoring). The otel→prometheus
// exporter renders an explicit-bucket histogram as classic le= buckets in the text
// exposition, so these bounds ARE the quantile resolution a plain scrape gets.
var latencyBucketBoundaries = []float64{
	0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300,
}

// latencyBucketAggregation is the single aggregation spec shared by every
// latency instrument. Defining it once keeps the bucket ladder from drifting
// across instruments (the drift the prior tool-duration commit's single-source
// lesson guards against). The Boundaries slice is copied per call so a view never
// shares the package-level slice's backing array.
func latencyBucketAggregation() sdkmetric.AggregationExplicitBucketHistogram {
	bounds := make([]float64, len(latencyBucketBoundaries))
	copy(bounds, latencyBucketBoundaries)
	return sdkmetric.AggregationExplicitBucketHistogram{Boundaries: bounds}
}

// LatencyViews returns the explicit-bucket-histogram views for EVERY latency
// instrument (tool/turn duration, TTFT, inter-token, tool-queue; ADR 0045,
// superseding ADR 0018 §5 decision 2). It is the single source of truth for the
// latency aggregation: any MeterProvider feeding NewMetrics MUST install these
// (sdkmetric.WithView(LatencyViews()...)), or the latency series fall back to the
// SDK default explicit buckets (a coarser ladder that misses the ms low end and
// the minute-scale high end the latencyBucketBoundaries cover).
//
// The explicit ladder renders as classic Prometheus le= buckets, so the text
// /metrics exposition + promtool + the perf-MCP reducer's classicLadder path all
// yield p50/p90/p99 with zero scrape config — the issue #158 fix. (OTLP push, were
// it ever wired, would carry the same explicit buckets; the exponential-tail
// precision OTLP could have aggregated is unconsumed today since no OTLP metrics
// reader exists.)
//
// Aggregation choice is a view-on-the-provider/reader concern, NOT a
// per-instrument hint, which is why it belongs to whoever assembles the
// MeterProvider.
func LatencyViews() []sdkmetric.View {
	views := make([]sdkmetric.View, 0, len(latencyInstruments))
	for _, name := range latencyInstruments {
		views = append(views, sdkmetric.NewView(
			sdkmetric.Instrument{Name: name},
			sdkmetric.Stream{Aggregation: latencyBucketAggregation()},
		))
	}
	return views
}

// Attribute keys. These are the only label dimensions any series carries; all
// values are bounded domain enums (event/stop/tool names) or low-cardinality
// flags — never a session id or free text.
const (
	attrType    = "type"    // event type
	attrStop    = "stop"    // run stop reason
	attrTool    = "tool"    // tool name
	attrError   = "error"   // tool error outcome ("true"/"false")
	attrKind    = "kind"    // token kind (input/output/cache_read/cache_write/reasoning)
	attrRole    = "role"    // engine role family (the closed Role* set below)
	attrOutcome = "outcome" // schedule fire outcome (fired/skipped/failed)
)

// Role family values for the attrRole label. This is a CLOSED, bounded set —
// the cardinality contract of the role dimension. The composition layer
// (internal/app's roleFamily) maps every engine role onto exactly one of these
// six values before it ever reaches a metric; def names, member names, model
// ids, and session ids must NEVER appear as a role value. RoleChild is the
// fail-safe bucket for any role the mapping does not recognise. Every member
// must correspond to a role an engine actually carries — an unmatchable value
// in the model-facing filter enums is a trap (the reason there is no "fork"
// family: fork branches/judges have session-id prefixes, never a Deps.Role).
const (
	// RoleMain is the main (operator-facing) engine.
	RoleMain = "main"
	// RoleSubagent is the Subagent delegation family (explorer, per-def, model-override).
	RoleSubagent = "subagent"
	// RoleMember is the agent-team member/lead family.
	RoleMember = "member"
	// RoleParallel is the Parallel fan-out family (branches and the judge).
	RoleParallel = "parallel"
	// RoleUserModel is the user-model review child.
	RoleUserModel = "usermodel"
	// RoleChild is the safe fallback for an unrecognised child role.
	RoleChild = "child"
)

// Metrics is an OpenTelemetry-backed telemetry adapter. It implements both
// port.EventSink (deriving counters/gauges from the event stream) and
// port.ToolCallRecorder (deriving tool-call counters and a latency histogram).
//
// All series use bounded attribute sets: tool names and stop reasons are bounded
// domain values, and no series is ever labelled by session id or free text. The
// instruments are created from a metric.Meter obtained from the injected
// MeterProvider; the provider's prometheus exporter (wired in Setup) renders
// them on the /metrics registry.
type Metrics struct {
	events     metric.Int64Counter
	runs       metric.Int64Counter
	turns      metric.Int64Counter
	turnsEmpty metric.Int64Counter
	toolCalls  metric.Int64Counter
	tokens     metric.Int64Counter
	permAsks   metric.Int64Counter
	activeRuns metric.Int64UpDownCounter
	// cacheHit holds the prompt-cache hit ratio of the most recent run result.
	// It is a synchronous gauge: recordResult sets it to the latest run's ratio,
	// mirroring the single-value semantics of the old client_golang Gauge.
	cacheHit metric.Float64Gauge
	// toolDuration is the tool execution wall-clock histogram (unit "s"). Setup
	// installs an explicit-bucket-histogram view for it (toolDurationInstrument).
	toolDuration metric.Float64Histogram
	// turnDuration is the per-turn model-call wall-clock histogram (unit "s"),
	// recorded from EvTurnEnd.DurationMs.
	turnDuration metric.Float64Histogram
	// ttft is the time-to-first-token histogram (unit "s"), recorded from
	// EvTurnEnd.TTFTMs (skipped when the turn produced no observable output).
	ttft metric.Float64Histogram
	// interToken is the per-turn mean inter-token-gap histogram (unit "s"),
	// recorded from EvTurnEnd.InterTokenMeanMs (skipped for <2-content-chunk turns).
	interToken metric.Float64Histogram
	// interTokenMax is the per-turn worst inter-token-gap histogram (unit "s"),
	// recorded from EvTurnEnd.InterTokenMaxMs (skipped for <2-content-chunk turns).
	// It is the streaming-jitter tail signal beside interToken's typical-gap mean.
	interTokenMax metric.Float64Histogram
	// toolQueue is the tool queue-time histogram (unit "s"): the wait from a call
	// entering dispatch to its execution starting (coordinated-omission fix).
	toolQueue metric.Float64Histogram
	// scheduleFires counts scheduled-task fire outcomes (fired/skipped/failed),
	// labelled by outcome. Schedule metrics are a SEPARATE dimension from the
	// role-family instruments (issue #233): a fire mints a fresh session whose
	// own run already carries role="main"; these instruments capture the
	// schedule-lifecycle counts/latency the scheduler reports via the
	// composition-injected ScheduleMetrics callback.
	scheduleFires metric.Int64Counter
	// scheduleFireDuration is the schedule fire wall-clock histogram (unit "s"):
	// the elapsed time from a fire's Claim to its terminal EvResult. Only
	// recorded for fired/failed fires (a skipped fire has no run — duration 0).
	// Shares the latencyBucketBoundaries explicit-bucket ladder via
	// scheduleFireDurationInstrument in latencyInstruments.
	scheduleFireDuration metric.Float64Histogram
}

// Compile-time interface checks.
var (
	_ port.EventSink        = (*Metrics)(nil)
	_ port.ToolCallRecorder = (*Metrics)(nil)
)

// NewMetrics constructs a Metrics adapter from an OTel MeterProvider. It
// implements both port.EventSink and port.ToolCallRecorder. The provider is expected to
// have a prometheus exporter reader and the latency explicit-bucket-histogram
// views installed (see Setup); NewMetrics itself only creates the instruments.
//
// It returns an error if any instrument fails to construct — the OTel meter API
// is fallible, unlike client_golang's panic-on-misuse registration.
func NewMetrics(mp metric.MeterProvider) (*Metrics, error) {
	meter := mp.Meter(meterName)
	m := &Metrics{}

	var err error
	if m.events, err = meter.Int64Counter(
		"mecatl.events",
		metric.WithDescription("Total domain events observed, by event type."),
	); err != nil {
		return nil, fmt.Errorf("telemetry: events counter: %w", err)
	}
	if m.runs, err = meter.Int64Counter(
		"mecatl.runs",
		metric.WithDescription("Total runs finished, by stop reason."),
	); err != nil {
		return nil, fmt.Errorf("telemetry: runs counter: %w", err)
	}
	if m.turns, err = meter.Int64Counter(
		"mecatl.turns",
		metric.WithDescription("Total turns completed (the per-turn denominator)."),
	); err != nil {
		return nil, fmt.Errorf("telemetry: turns counter: %w", err)
	}
	if m.turnsEmpty, err = meter.Int64Counter(
		"mecatl.turn_empty",
		metric.WithDescription("Total turns that produced no tool call and no text (empty/no-progress turns)."),
	); err != nil {
		return nil, fmt.Errorf("telemetry: turn empty counter: %w", err)
	}
	if m.toolCalls, err = meter.Int64Counter(
		"mecatl.tool.calls",
		metric.WithDescription("Total tool calls executed, by tool name and error outcome."),
	); err != nil {
		return nil, fmt.Errorf("telemetry: tool calls counter: %w", err)
	}
	if m.tokens, err = meter.Int64Counter(
		"mecatl.tokens",
		metric.WithDescription("Total tokens accounted, by kind (input/output/cache_read/cache_write/reasoning)."),
		metric.WithUnit("{token}"),
	); err != nil {
		return nil, fmt.Errorf("telemetry: tokens counter: %w", err)
	}
	if m.permAsks, err = meter.Int64Counter(
		"mecatl.permission.asks",
		metric.WithDescription("Total permission.ask events observed."),
	); err != nil {
		return nil, fmt.Errorf("telemetry: permission asks counter: %w", err)
	}
	if m.activeRuns, err = meter.Int64UpDownCounter(
		"mecatl.active_runs",
		metric.WithDescription("Number of runs currently in flight."),
	); err != nil {
		return nil, fmt.Errorf("telemetry: active runs up/down counter: %w", err)
	}
	if m.cacheHit, err = meter.Float64Gauge(
		"mecatl.cache_hit_ratio",
		metric.WithDescription("Prompt-cache hit ratio of the most recent run result."),
	); err != nil {
		return nil, fmt.Errorf("telemetry: cache hit gauge: %w", err)
	}
	if m.toolDuration, err = meter.Float64Histogram(
		toolDurationInstrument,
		metric.WithDescription("Tool execution wall-clock duration in seconds, by tool name."),
		metric.WithUnit("s"),
	); err != nil {
		return nil, fmt.Errorf("telemetry: tool duration histogram: %w", err)
	}
	if m.turnDuration, err = meter.Float64Histogram(
		turnDurationInstrument,
		metric.WithDescription("Per-turn model-call wall-clock duration in seconds."),
		metric.WithUnit("s"),
	); err != nil {
		return nil, fmt.Errorf("telemetry: turn duration histogram: %w", err)
	}
	if m.ttft, err = meter.Float64Histogram(
		ttftInstrument,
		metric.WithDescription("Time to first observable output (text, reasoning, or tool call) per turn, in seconds."),
		metric.WithUnit("s"),
	); err != nil {
		return nil, fmt.Errorf("telemetry: ttft histogram: %w", err)
	}
	if m.interToken, err = meter.Float64Histogram(
		interTokenInstrument,
		metric.WithDescription("Mean inter-token gap per turn, in seconds."),
		metric.WithUnit("s"),
	); err != nil {
		return nil, fmt.Errorf("telemetry: inter-token histogram: %w", err)
	}
	if m.interTokenMax, err = meter.Float64Histogram(
		interTokenMaxInstrument,
		metric.WithDescription("Worst (largest) inter-token gap per turn, in seconds."),
		metric.WithUnit("s"),
	); err != nil {
		return nil, fmt.Errorf("telemetry: inter-token max histogram: %w", err)
	}
	if m.toolQueue, err = meter.Float64Histogram(
		toolQueueInstrument,
		metric.WithDescription("Tool dispatch queue time in seconds (enqueue→execution start), by tool name."),
		metric.WithUnit("s"),
	); err != nil {
		return nil, fmt.Errorf("telemetry: tool queue histogram: %w", err)
	}
	if m.scheduleFires, err = meter.Int64Counter(
		"mecatl.schedule.fires",
		metric.WithDescription("Total scheduled-task fires, by outcome (fired/skipped/failed)."),
	); err != nil {
		return nil, fmt.Errorf("telemetry: schedule fires counter: %w", err)
	}
	if m.scheduleFireDuration, err = meter.Float64Histogram(
		scheduleFireDurationInstrument,
		metric.WithDescription("Scheduled-task fire wall-clock duration in seconds (due→terminal)."),
		metric.WithUnit("s"),
	); err != nil {
		return nil, fmt.Errorf("telemetry: schedule fire duration histogram: %w", err)
	}

	return m, nil
}

// rssZeroLogOnce ensures the "RSS read returned 0 on a supported platform"
// diagnostic is logged at most once for the process lifetime, so a persistent
// /proc failure surfaces a single actionable line rather than one per scrape.
var rssZeroLogOnce sync.Once

// RegisterProcessGauges registers the process-level observable gauges on the
// given MeterProvider's meter. Currently it registers mecatl.process.rss (the
// resident set size in bytes), read lock-free via readRSS on each collection.
//
// The gauge is registered ONLY where RSS is actually readable (Linux): off
// Linux rssSupported() is false and the series is simply absent (decision 9 —
// the RSS gauge ships for general long-session memory visibility, no
// leak-specific alarm). It is a separate registration from NewMetrics because
// the domain instruments derive from the event/log stream, whereas this is an
// async observation of the OS process; keeping it apart lets a caller opt out.
//
// It returns an error if the instrument fails to construct.
func RegisterProcessGauges(mp metric.MeterProvider, diag port.Diagnostics) error {
	if !rssSupported() {
		return nil
	}
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	meter := mp.Meter(meterName)
	rss, err := meter.Int64ObservableGauge(
		"mecatl.process.rss",
		metric.WithDescription("Process resident set size in bytes (read from /proc on Linux)."),
		metric.WithUnit("By"),
		metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
			if v := readRSS(); v > 0 {
				o.Observe(int64(v)) //nolint:gosec // RSS bytes fits an int64 for any real process
			} else {
				// 0 on a platform that claims RSS support means a persistent /proc
				// read failure: the gauge series silently goes missing. Log it ONCE
				// at debug so the gap is diagnosable without flooding every scrape.
				rssZeroLogOnce.Do(func() {
					diag.Log(ctx, port.LevelDebug, "process RSS read returned 0 on a supported platform; mecatl_process_rss series will be absent until /proc reads succeed")
				})
			}
			return nil
		}),
	)
	if err != nil {
		return fmt.Errorf("telemetry: process rss gauge: %w", err)
	}
	_ = rss // the instrument is driven by its callback; the handle is not used directly.
	return nil
}

// Emit records OTel metrics derived from a single domain Event, attributing
// every series to the MAIN engine (role="main"). A child engine's events must
// flow through WithRole instead, so each series carries the role label
// uniformly. The ctx is the run's context (threaded from port.EventSink.Emit)
// and is passed to every instrument operation so the SDK can attach exemplars
// from an active span.
func (m *Metrics) Emit(ctx context.Context, ev session.Event) {
	m.emit(ctx, ev, roleAttr(RoleMain))
}

// emit is the role-prefixed record path behind both Emit (role="main") and the
// WithRole wrapper. prefix is appended to every instrument operation's
// attribute set; it must contain only bounded values (the role family).
func (m *Metrics) emit(ctx context.Context, ev session.Event, prefix []attribute.KeyValue) {
	m.events.Add(ctx, 1, withAttrs(prefix, attribute.String(attrType, string(ev.Type))))

	switch ev.Type {
	case session.EvSessionInit:
		// active_runs is role-tagged too: a bounded per-role in-flight gauge.
		m.activeRuns.Add(ctx, 1, withAttrs(prefix))
	case session.EvPermissionAsk:
		m.permAsks.Add(ctx, 1, withAttrs(prefix))
	case session.EvResult:
		m.activeRuns.Add(ctx, -1, withAttrs(prefix))
		m.recordResult(ctx, ev.Result, prefix)
	case session.EvTurnEnd:
		// turns_total counts ALL completed turns — every model exchange that ran
		// to a turn boundary and produced an EvTurnEnd, including empty/no-progress
		// ones (EvTurnEnd fires unconditionally before the no-progress branch in
		// the loop). It is the per-turn denominator. Abnormal RUN terminals
		// (error/cancelled/budget/no-progress) are counted at the run level by
		// runs_total{stop}; EvTurnEnd carries no stop reason, so this counter is
		// deliberately NOT labelled by stop (only by role).
		m.turns.Add(ctx, 1, withAttrs(prefix))
		m.recordTurnEnd(ctx, ev.TurnEnd, prefix)
	case session.EvNoProgress:
		// The EMPTY SUBSET of completed turns: a turn that produced NEITHER a tool
		// call NOR text (#82). NOT mutually exclusive with turns_total — the same
		// turn already bumped turns_total via EvTurnEnd above, so
		// turn_empty_total/turns_total is the empty-turn SHARE. EvNoProgress fires
		// on every advisory nudge AND the give-up, so this counts up to
		// MaxNoProgressNudges+1 emissions per stuck sequence. prefix carries role.
		m.turnsEmpty.Add(ctx, 1, withAttrs(prefix))
	case session.EvTurnStart,
		session.EvMessageDelta,
		session.EvToolCall,
		session.EvToolResult,
		session.EvToolProgress,
		session.EvHook,
		session.EvCompaction:
		// Counted by mecatl.events above; no further metric. EvToolProgress is a
		// transient advisory line — the events bump is all it warrants.
	}
}

// recordResult records run-terminal metrics: the stop reason, token totals, and
// the cache hit ratio.
func (m *Metrics) recordResult(ctx context.Context, r *session.ResultPayload, prefix []attribute.KeyValue) {
	if r == nil {
		m.runs.Add(ctx, 1, withAttrs(prefix, attribute.String(attrStop, string(session.StopNone))))
		return
	}
	m.runs.Add(ctx, 1, withAttrs(prefix, attribute.String(attrStop, string(r.Stop))))
	u := r.Usage
	m.tokens.Add(ctx, int64(u.InputTokens), withAttrs(prefix, attribute.String(attrKind, "input")))
	m.tokens.Add(ctx, int64(u.OutputTokens), withAttrs(prefix, attribute.String(attrKind, "output")))
	m.tokens.Add(ctx, int64(u.CacheReadTokens), withAttrs(prefix, attribute.String(attrKind, "cache_read")))
	m.tokens.Add(ctx, int64(u.CacheWriteTokens), withAttrs(prefix, attribute.String(attrKind, "cache_write")))
	m.tokens.Add(ctx, int64(u.ReasoningTokens), withAttrs(prefix, attribute.String(attrKind, "reasoning")))
	m.cacheHit.Record(ctx, u.CacheHitRate(), withAttrs(prefix))
}

// recordTurnEnd records the per-turn latency histograms from a TurnEndPayload:
// turn duration always, plus TTFT and the inter-token gaps when they were
// actually measured. The inter-token signal is split into two instruments: the
// per-turn MEAN gap (interTokenInstrument, typical smoothness) and the per-turn
// MAX gap (interTokenMaxInstrument, the worst mid-turn stall — a streaming-jitter
// tail signal). A 0 on TTFTMs / InterTokenMeanMs / InterTokenMaxMs means "not
// measured" (no Clock, no content chunk, or fewer than two content chunks) —
// never a real observation — so each is skipped under its OWN independent >0
// guard to avoid recording bogus zeros. All instruments are unit "s", so ms is
// converted to seconds.
func (m *Metrics) recordTurnEnd(ctx context.Context, p *session.TurnEndPayload, prefix []attribute.KeyValue) {
	if p == nil {
		return
	}
	m.turnDuration.Record(ctx, msToSeconds(p.DurationMs), withAttrs(prefix))
	if p.TTFTMs > 0 {
		m.ttft.Record(ctx, msToSeconds(p.TTFTMs), withAttrs(prefix))
	}
	if p.InterTokenMeanMs > 0 {
		m.interToken.Record(ctx, msToSeconds(p.InterTokenMeanMs), withAttrs(prefix))
	}
	if p.InterTokenMaxMs > 0 {
		m.interTokenMax.Record(ctx, msToSeconds(p.InterTokenMaxMs), withAttrs(prefix))
	}
}

// msToSeconds converts a millisecond count to seconds for the "s"-unit latency
// instruments.
func msToSeconds(ms int64) float64 { return float64(ms) / 1000.0 }

// ToolCall records the per-tool call counter and the duration/queue-time latency
// histograms, attributed to the MAIN engine (role="main"); a child engine's
// calls flow through WithRole instead. It satisfies port.ToolCallRecorder.
// ToolCallRecorder carries no ctx, so the recordings use a background context —
// exemplar correlation is best-effort here. queued is the dispatch wait
// (enqueue→execution start); took is the execution wall time. Both are recorded
// with the same tool attribute.
func (m *Metrics) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	m.toolCall(id, call, result, queued, took, roleAttr(RoleMain))
}

// toolCall is the role-prefixed record path behind both ToolCall (role="main")
// and the WithRole wrapper.
func (m *Metrics) toolCall(_ session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration, prefix []attribute.KeyValue) {
	ctx := context.Background()
	errLabel := "false"
	if result.IsError {
		errLabel = "true"
	}
	m.toolCalls.Add(ctx, 1, withAttrs(prefix,
		attribute.String(attrTool, call.Name),
		attribute.String(attrError, errLabel),
	))
	toolAttr := withAttrs(prefix, attribute.String(attrTool, call.Name))
	m.toolDuration.Record(ctx, took.Seconds(), toolAttr)
	m.toolQueue.Record(ctx, queued.Seconds(), toolAttr)
}

// EmitSchedule records scheduled-task fire metrics. It is the
// composition-injected callback target the scheduler invokes (via
// Config.ScheduleMetrics) for every fired/skipped/failed fire. Schedule metrics
// are NOT a role-family (issue #233): a fire mints a fresh session whose OWN
// run already carries role="main" via its EventSink, so these instruments carry
// NO role label — they are a separate schedule-lifecycle dimension.
//
// duration is the fire's wall-clock cost (due→terminal — measured from the
// tick/due time captured before Store.Claim, not from the Claim op itself).
// The scheduler passes it only for a fired/failed fire (the value of
// time.Since(now) captured in fireClaimed); a SKIPPED fire (no run) passes
// duration 0 and the fire-duration histogram is skipped. The fires counter is
// ALWAYS bumped (labelled by outcome). Nil-safe: a nil Metrics is a no-op (the
// byte-identical no-metrics path).
func (m *Metrics) EmitSchedule(payload session.SchedulePayload, duration time.Duration) {
	if m == nil {
		return
	}
	outcomeAttr := withAttrs(nil, attribute.String(attrOutcome, payload.Kind))
	m.scheduleFires.Add(context.Background(), 1, outcomeAttr)
	if duration > 0 {
		m.scheduleFireDuration.Record(context.Background(), duration.Seconds(), outcomeAttr)
	}
}

// roleAttr builds the one-element attribute prefix carrying the role label.
func roleAttr(role string) []attribute.KeyValue {
	return []attribute.KeyValue{attribute.String(attrRole, role)}
}

// withAttrs combines a (bounded) attribute prefix with an operation's own
// attributes into a single measurement option, copying so the shared prefix
// slice is never appended to in place.
func withAttrs(prefix []attribute.KeyValue, own ...attribute.KeyValue) metric.MeasurementOption {
	combined := make([]attribute.KeyValue, 0, len(prefix)+len(own))
	combined = append(combined, prefix...)
	combined = append(combined, own...)
	return metric.WithAttributes(combined...)
}

// RoleMetrics is a role-scoped view over a *Metrics: it implements BOTH
// port.EventSink and port.ToolCallRecorder, delegating every Add/Record to the
// shared instruments with the role attribute appended. The role MUST be one of
// the bounded Role* family constants — the composition layer maps engine roles
// onto that closed set BEFORE constructing a RoleMetrics, so no def/member name
// or session id can ever become a label value. It holds no state of its own and
// spawns no goroutine.
type RoleMetrics struct {
	m     *Metrics
	attrs []attribute.KeyValue
}

// Compile-time interface checks: the role-scoped view is a drop-in for both
// engine observability seams.
var (
	_ port.EventSink        = (*RoleMetrics)(nil)
	_ port.ToolCallRecorder = (*RoleMetrics)(nil)
)

// WithRole returns a role-scoped dual-interface view (port.EventSink +
// port.ToolCallRecorder) over the SAME underlying instruments, tagging every
// series with role. Pass one of the bounded Role* constants (the composition
// layer's roleFamily mapping guarantees this for child engines).
func (m *Metrics) WithRole(role string) *RoleMetrics {
	return &RoleMetrics{m: m, attrs: roleAttr(role)}
}

// Emit relays the event to the shared instruments with the role attribute.
func (r *RoleMetrics) Emit(ctx context.Context, ev session.Event) {
	r.m.emit(ctx, ev, r.attrs)
}

// ToolCall relays the tool record to the shared instruments with the role
// attribute.
func (r *RoleMetrics) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	r.m.toolCall(id, call, result, queued, took, r.attrs)
}
