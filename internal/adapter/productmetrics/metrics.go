package productmetrics

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// meterName is the instrumentation scope name for this package's meter.
const meterName = "github.com/stacklok/mecatl/internal/adapter/productmetrics"

// Attribute keys. Every value ever attached under these keys is drawn from a
// bounded closed set (session.StopReason, the fixed token-kind strings, this
// package's own Feature/ProviderFamily/DeploymentMode enums, or "true"/"false")
// — never a session id, model id, or free text. The tool_calls instrument's
// two keys live with their closed-set projection in toolcall.go.
const (
	attrStop        = "stop"
	attrKind        = "kind"
	attrFeature     = "feature"
	attrProvider    = "family"
	attrMode        = "mode"
	attrHadToolCall = "had_tool_call"
)

// Recorder is the product-metrics adapter: it implements port.EventSink
// (this file) and port.ToolCallRecorder + port.RunAwareToolCallRecorder
// (toolcall.go), deriving ONLY the bounded counts in the design's catalog. It
// never reads a session id, model id, result content, or any free-text field;
// the one thing it reads from a tool call is its NAME, and only to map it
// through the closed-set projection in toolcall.go (toolCategory), which can
// emit nothing but a built-in tool's own name, "mcp", or "other".
type Recorder struct {
	heartbeat        metric.Int64Counter
	featureEnabled   metric.Int64Counter
	providerConfig   metric.Int64Counter
	deploymentMode   metric.Int64Counter
	sessionsStarted  metric.Int64Counter
	runsCompleted    metric.Int64Counter
	toolCalls        metric.Int64Counter
	tokens           metric.Int64Counter
	subagentUsed     metric.Int64Counter
	teamUsed         metric.Int64Counter
	runDuration      metric.Float64Histogram
	toolCallsPerRun  metric.Int64Histogram
	timeToFirstValue metric.Float64Histogram

	// perRun holds the bounded per-live-run facts this package derives across
	// the Emit/ToolCallForRun boundary. See perRunTracker.
	perRun *perRunTracker

	// firstValue holds the once-per-install time_to_first_value state. It is
	// deliberately SEPARATE from perRun (which is per-live-run, keyed by run id
	// and dropped at EvResult): this is install-scoped, single-slot state whose
	// whole lifetime is the process.
	firstValue firstValueTracker
}

// firstValueTracker guards the once-ever time_to_first_value state. Armed by
// EnableFirstValueTracking (composition), read and flipped at most once by
// recordResult.
//
// CONCURRENCY: as with perRunTracker, the "is it done" test and the "mark it
// done" write are ONE critical section — concurrent EvResult observations on a
// fan-out deployment would otherwise both pass the test and record two samples
// for a metric whose entire contract is "at most one, ever".
type firstValueTracker struct {
	mu sync.Mutex
	// armed is false until EnableFirstValueTracking is called; an unarmed
	// Recorder never records the metric at all (the default, byte-identical to
	// the pre-feature posture for every existing caller of NewRecorder).
	armed bool
	// firstSeenAt is this install's first-seen moment; the recorded duration is
	// measured from it.
	firstSeenAt time.Time
	// done is true once the sample exists — either recorded by THIS process, or
	// (per the persisted marker) by an earlier one.
	done bool
	// recordFn persists the marker so a LATER process invocation also stays
	// disabled. Called at most once, best-effort.
	recordFn func() error
}

// EnableFirstValueTracking arms mecatl.product.time_to_first_value recording.
// firstSeenAt is this install's first-seen timestamp; alreadyRecorded, when
// true, permanently disables recording for this Recorder's lifetime (this
// install already has its one sample). recordFn persists the local marker so a
// later process invocation also stays disabled; it is called at most once, and
// may be nil (in-memory-only tracking).
//
// It is a separate arming step rather than a NewRecorder parameter so that
// NewRecorder's signature — and every existing caller and test of it — stays
// unchanged; an unarmed Recorder simply never records this instrument.
func (r *Recorder) EnableFirstValueTracking(firstSeenAt time.Time, alreadyRecorded bool, recordFn func() error) {
	r.firstValue.mu.Lock()
	defer r.firstValue.mu.Unlock()
	r.firstValue.armed = true
	r.firstValue.firstSeenAt = firstSeenAt
	r.firstValue.done = alreadyRecorded
	r.firstValue.recordFn = recordFn
}

// claim reports whether THIS observation is the install's first-value moment, marking it claimed and persisting the marker as one atomic step. It
// returns the firstSeenAt to measure from; a false claim means the metric must
// not be recorded (unarmed, already recorded, or no usable firstSeenAt).
func (t *firstValueTracker) claim() (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.armed || t.done || t.firstSeenAt.IsZero() {
		return time.Time{}, false
	}
	t.done = true
	if t.recordFn != nil {
		// Best-effort: a failed write risks re-recording once on a later
		// process, which is a fidelity wobble in a coarse onboarding signal —
		// not a correctness bug worth failing anything over.
		_ = t.recordFn()
	}
	return t.firstSeenAt, true
}

// perRunState is the bounded set of facts tracked for ONE live run, keyed by
// the loop-stamped session.Event.RunID (an opaque per-run correlation id, ADR
// 0249 — never a session id, tool name, or free-text field, so this package's
// no-PII invariant holds). Every field is a count or a boolean derived from a
// closed vocabulary; nothing here is ever attached as an attribute VALUE.
type perRunState struct {
	// subagentSeen/teamSeen dedup subagentUsed/teamUsed to their documented
	// "at least once per run" semantics: a run's first EvSubagentStart or
	// EvTeamStart increments the counter, later ones in the SAME run (e.g. a
	// fan-out of concurrent Subagent calls) do not.
	subagentSeen bool
	teamSeen     bool

	// hadToolCall records whether the run made at least one SUCCESSFUL tool
	// call — the product definition of "this run took an action", read at
	// EvResult time as the runs_completed had_tool_call attribute.
	hadToolCall bool

	// toolCallCount is the run's total tool calls (successful or not),
	// published at EvResult as the tool_calls_per_run distribution.
	toolCallCount int64

	// startedAt is the wall-clock time this run's EvSessionInit was observed,
	// used at EvResult time to compute run_duration. It stays the zero Time
	// for a run whose EvSessionInit this Recorder never saw (e.g. a process
	// restart mid-run) — recordResult must never record a duration from a
	// zero startedAt.
	startedAt time.Time
}

// perRunTracker guards the live-run state map. Bounded to concurrently-live
// runs: a run's entry is created lazily on its first observed fact and dropped
// on its EvResult, so the map never grows across a process's lifetime.
//
// CONCURRENCY: every method below performs its map lookup AND its field
// mutation as ONE critical section under mu, and no *perRunState pointer ever
// escapes a locked region. Callers therefore cannot race on a state's fields:
// Emit (per event) and ToolCallForRun (per tool call) run concurrently on a
// fan-out run, and the only shape that is provably safe is "the lock covers
// both halves".
type perRunTracker struct {
	mu     sync.Mutex
	states map[string]*perRunState
}

func newPerRunTracker() *perRunTracker {
	return &perRunTracker{states: make(map[string]*perRunState)}
}

// stateLocked returns runID's live state, creating it if absent. The caller
// MUST hold t.mu, and must not retain the pointer past the critical section.
func (t *perRunTracker) stateLocked(runID string) *perRunState {
	st, ok := t.states[runID]
	if !ok {
		st = &perRunState{}
		t.states[runID] = st
	}
	return st
}

// markFamilyUsed reports whether this is the first time, within the run
// identified by runID, that family has been observed — marking it seen as a
// side effect. An empty runID (no run context to dedup against) always counts,
// matching the pre-dedup behavior.
func (t *perRunTracker) markFamilyUsed(runID string, family delegationFamily) bool {
	if runID == "" {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.stateLocked(runID)
	seen := &st.subagentSeen
	if family == familyTeam {
		seen = &st.teamSeen
	}
	if *seen {
		return false
	}
	*seen = true
	return true
}

// markStarted stamps runID's start time as now. Called once, from Emit's
// EvSessionInit case; a run with no observed EvSessionInit never has this
// called and its startedAt stays the zero Time.
func (t *perRunTracker) markStarted(runID string) {
	if runID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.stateLocked(runID)
	st.startedAt = time.Now()
}

// markToolCall tallies one tool call against runID. An empty runID (a caller
// on the base port.ToolCallRecorder path, with no run to correlate against) is
// tracked nowhere — its call is still counted on the tool_calls instrument,
// but it can contribute to no run's had_tool_call.
func (t *perRunTracker) markToolCall(runID string, errored bool) {
	if runID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.stateLocked(runID)
	st.toolCallCount++
	if !errored {
		st.hadToolCall = true
	}
}

// finish drops runID's live state at EvResult and returns a COPY of what was
// there (the zero value if the run recorded nothing — e.g. a run with no tool
// calls and no delegation-family use). Returning a copy, not the pointer,
// keeps every field read outside the lock race-free.
func (t *perRunTracker) finish(runID string) perRunState {
	if runID == "" {
		return perRunState{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.states[runID]
	if !ok {
		return perRunState{}
	}
	delete(t.states, runID)
	return *st
}

// Compile-time interface checks.
var (
	_ port.EventSink                = (*Recorder)(nil)
	_ port.ToolCallRecorder         = (*Recorder)(nil)
	_ port.RunAwareToolCallRecorder = (*Recorder)(nil)
)

// NewRecorder constructs every instrument from the given MeterProvider. It
// returns an error if any instrument fails to construct — the OTel meter API
// is fallible.
func NewRecorder(mp metric.MeterProvider) (*Recorder, error) {
	meter := mp.Meter(meterName)
	r := &Recorder{perRun: newPerRunTracker()}
	var err error

	if r.heartbeat, err = meter.Int64Counter("mecatl.product.heartbeat",
		metric.WithDescription("Process liveness heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: heartbeat counter: %w", err)
	}
	if r.featureEnabled, err = meter.Int64Counter("mecatl.product.feature_enabled",
		metric.WithDescription("Major feature enabled, by closed feature name, per heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: feature_enabled counter: %w", err)
	}
	if r.providerConfig, err = meter.Int64Counter("mecatl.product.provider_configured",
		metric.WithDescription("Configured LLM provider family, by closed family name, per heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: provider_configured counter: %w", err)
	}
	if r.deploymentMode, err = meter.Int64Counter("mecatl.product.deployment_mode",
		metric.WithDescription("Process deployment mode, by closed mode name, per heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: deployment_mode counter: %w", err)
	}
	if r.sessionsStarted, err = meter.Int64Counter("mecatl.product.sessions_started",
		metric.WithDescription("Total sessions started.")); err != nil {
		return nil, fmt.Errorf("productmetrics: sessions_started counter: %w", err)
	}
	if r.runsCompleted, err = meter.Int64Counter("mecatl.product.runs_completed",
		metric.WithDescription("Total runs completed, by bounded stop reason and whether the run made at least one successful tool call.")); err != nil {
		return nil, fmt.Errorf("productmetrics: runs_completed counter: %w", err)
	}
	if r.toolCalls, err = meter.Int64Counter("mecatl.product.tool_calls",
		metric.WithDescription(`Total tool calls executed, by bounded category (a built-in tool's own name, the single value "mcp" for any MCP-server tool, or "other") and outcome.`)); err != nil {
		return nil, fmt.Errorf("productmetrics: tool_calls counter: %w", err)
	}
	if r.tokens, err = meter.Int64Counter("mecatl.product.tokens",
		metric.WithDescription("Total tokens accounted, by bounded kind."),
		metric.WithUnit("{token}")); err != nil {
		return nil, fmt.Errorf("productmetrics: tokens counter: %w", err)
	}
	if r.subagentUsed, err = meter.Int64Counter("mecatl.product.subagent_used",
		metric.WithDescription("Runs that used the Subagent delegation family at least once.")); err != nil {
		return nil, fmt.Errorf("productmetrics: subagent_used counter: %w", err)
	}
	if r.teamUsed, err = meter.Int64Counter("mecatl.product.team_used",
		metric.WithDescription("Runs that used the Team delegation family at least once.")); err != nil {
		return nil, fmt.Errorf("productmetrics: team_used counter: %w", err)
	}
	if r.runDuration, err = meter.Float64Histogram("mecatl.product.run_duration",
		metric.WithDescription("Wall-clock duration of a run, from session init to result, in seconds."),
		metric.WithUnit("s")); err != nil {
		return nil, fmt.Errorf("productmetrics: run_duration histogram: %w", err)
	}
	if r.toolCallsPerRun, err = meter.Int64Histogram("mecatl.product.tool_calls_per_run",
		metric.WithDescription("Total tool calls made within a single run."),
		metric.WithUnit("{tool_call}")); err != nil {
		return nil, fmt.Errorf("productmetrics: tool_calls_per_run histogram: %w", err)
	}
	if r.timeToFirstValue, err = meter.Float64Histogram("mecatl.product.time_to_first_value",
		metric.WithDescription("One-time-per-install duration, in seconds, from the start of the process that observed this install's first run that both made a successful tool call and ended cleanly (an approximation of onboarding time, not install age: a process started days after install and reaching that run in minutes reports minutes, not days)."),
		metric.WithUnit("s")); err != nil {
		return nil, fmt.Errorf("productmetrics: time_to_first_value histogram: %w", err)
	}
	return r, nil
}

// Emit derives coarse, bounded counts from a single domain Event. It reads
// ONLY ev.Type, ev.RunID, ev.Result.Stop, and ev.Result.Usage — never a
// session id, model id/alias, tool name, or any free-text field
// (ev.Result.Text/Error are never touched). ev.RunID is an opaque per-run
// correlation id (ADR 0249), not a session id, and is used ONLY as the key of
// the per-run tracker (dedup'ing subagentUsed/teamUsed and resolving
// had_tool_call at EvResult); it never becomes an attribute value.
func (r *Recorder) Emit(ctx context.Context, ev session.Event) {
	switch ev.Type {
	case session.EvSessionInit:
		r.sessionsStarted.Add(ctx, 1)
		r.perRun.markStarted(ev.RunID)
	case session.EvResult:
		// finish both reads and clears the run's state, so the had_tool_call
		// resolution and the bounded-map cleanup are one step.
		r.recordResult(ctx, ev.Result, r.perRun.finish(ev.RunID))
	case session.EvSubagentStart:
		if r.perRun.markFamilyUsed(ev.RunID, familySubagent) {
			r.subagentUsed.Add(ctx, 1)
		}
	case session.EvTeamStart:
		if r.perRun.markFamilyUsed(ev.RunID, familyTeam) {
			r.teamUsed.Add(ctx, 1)
		}
	}
}

// delegationFamily is the closed set of families dedup'd per run.
type delegationFamily int

const (
	familySubagent delegationFamily = iota
	familyTeam
)

// recordResult counts the completed run against its bounded stop reason and
// the had_tool_call fact carried by the run's just-finished state.
func (r *Recorder) recordResult(ctx context.Context, res *session.ResultPayload, st perRunState) {
	stop := session.StopNone
	if res != nil {
		stop = res.Stop
	}
	r.runsCompleted.Add(ctx, 1, metric.WithAttributes(
		attribute.String(attrStop, string(stop)),
		attribute.String(attrHadToolCall, strconv.FormatBool(st.hadToolCall))))
	r.toolCallsPerRun.Record(ctx, st.toolCallCount)
	if !st.startedAt.IsZero() {
		r.runDuration.Record(ctx, time.Since(st.startedAt).Seconds())
	}
	// The install's first-value moment: the first run that BOTH took an action
	// (a successful tool call) and ended cleanly. Recorded at most once ever,
	// across process restarts — see firstValueTracker.
	if stop == session.StopEndTurn && st.hadToolCall {
		if firstSeenAt, claimed := r.firstValue.claim(); claimed {
			r.timeToFirstValue.Record(ctx, time.Since(firstSeenAt).Seconds())
		}
	}
	if res == nil {
		return
	}
	u := res.Usage
	r.tokens.Add(ctx, int64(u.InputTokens), metric.WithAttributes(attribute.String(attrKind, "input")))
	r.tokens.Add(ctx, int64(u.OutputTokens), metric.WithAttributes(attribute.String(attrKind, "output")))
	r.tokens.Add(ctx, int64(u.CacheReadTokens), metric.WithAttributes(attribute.String(attrKind, "cache_read")))
	r.tokens.Add(ctx, int64(u.CacheWriteTokens), metric.WithAttributes(attribute.String(attrKind, "cache_write")))
	r.tokens.Add(ctx, int64(u.ReasoningTokens), metric.WithAttributes(attribute.String(attrKind, "reasoning")))
}
