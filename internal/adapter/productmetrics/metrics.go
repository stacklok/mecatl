package productmetrics

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// meterName is the instrumentation scope name for this package's meter.
const meterName = "github.com/stacklok/mecatl/internal/adapter/productmetrics"

// Attribute keys. Every value ever attached under these keys is drawn from a
// bounded closed set (session.StopReason, the fixed token-kind strings, or
// this package's own Feature/ProviderFamily/DeploymentMode enums) — never a
// session id, model id, tool name, or free text.
const (
	attrStop     = "stop"
	attrKind     = "kind"
	attrFeature  = "feature"
	attrProvider = "family"
	attrMode     = "mode"
)

// Recorder is the product-metrics adapter: it implements port.EventSink
// (this file) and port.ToolCallRecorder (toolcall.go), deriving ONLY the
// bounded counts in the design's catalog. It never reads a tool name,
// session id, model id, or any free-text field.
type Recorder struct {
	heartbeat       metric.Int64Counter
	featureEnabled  metric.Int64Counter
	providerConfig  metric.Int64Counter
	deploymentMode  metric.Int64Counter
	sessionsStarted metric.Int64Counter
	runsCompleted   metric.Int64Counter
	toolCalls       metric.Int64Counter
	tokens          metric.Int64Counter
	subagentUsed    metric.Int64Counter
	teamUsed        metric.Int64Counter
}

// Compile-time interface checks.
var (
	_ port.EventSink        = (*Recorder)(nil)
	_ port.ToolCallRecorder = (*Recorder)(nil)
)

// NewRecorder constructs every instrument from the given MeterProvider. It
// returns an error if any instrument fails to construct — the OTel meter API
// is fallible.
func NewRecorder(mp metric.MeterProvider) (*Recorder, error) {
	meter := mp.Meter(meterName)
	r := &Recorder{}
	var err error

	if r.heartbeat, err = meter.Int64Counter("mecatl.adoption.heartbeat",
		metric.WithDescription("Process liveness heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: heartbeat counter: %w", err)
	}
	if r.featureEnabled, err = meter.Int64Counter("mecatl.adoption.feature_enabled",
		metric.WithDescription("Major feature enabled, by closed feature name, per heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: feature_enabled counter: %w", err)
	}
	if r.providerConfig, err = meter.Int64Counter("mecatl.adoption.provider_configured",
		metric.WithDescription("Configured LLM provider family, by closed family name, per heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: provider_configured counter: %w", err)
	}
	if r.deploymentMode, err = meter.Int64Counter("mecatl.adoption.deployment_mode",
		metric.WithDescription("Process deployment mode, by closed mode name, per heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: deployment_mode counter: %w", err)
	}
	if r.sessionsStarted, err = meter.Int64Counter("mecatl.adoption.sessions_started",
		metric.WithDescription("Total sessions started.")); err != nil {
		return nil, fmt.Errorf("productmetrics: sessions_started counter: %w", err)
	}
	if r.runsCompleted, err = meter.Int64Counter("mecatl.adoption.runs_completed",
		metric.WithDescription("Total runs completed, by bounded stop reason.")); err != nil {
		return nil, fmt.Errorf("productmetrics: runs_completed counter: %w", err)
	}
	if r.toolCalls, err = meter.Int64Counter("mecatl.adoption.tool_calls",
		metric.WithDescription("Total tool calls executed (no tool identity attached).")); err != nil {
		return nil, fmt.Errorf("productmetrics: tool_calls counter: %w", err)
	}
	if r.tokens, err = meter.Int64Counter("mecatl.adoption.tokens",
		metric.WithDescription("Total tokens accounted, by bounded kind."),
		metric.WithUnit("{token}")); err != nil {
		return nil, fmt.Errorf("productmetrics: tokens counter: %w", err)
	}
	if r.subagentUsed, err = meter.Int64Counter("mecatl.adoption.subagent_used",
		metric.WithDescription("Runs that used the Subagent delegation family at least once.")); err != nil {
		return nil, fmt.Errorf("productmetrics: subagent_used counter: %w", err)
	}
	if r.teamUsed, err = meter.Int64Counter("mecatl.adoption.team_used",
		metric.WithDescription("Runs that used the Team delegation family at least once.")); err != nil {
		return nil, fmt.Errorf("productmetrics: team_used counter: %w", err)
	}
	return r, nil
}

// Emit derives coarse, bounded counts from a single domain Event. It reads
// ONLY ev.Type, ev.Result.Stop, and ev.Result.Usage — never a session id,
// model id/alias, tool name, or any free-text field (ev.Result.Text/Error
// are never touched).
func (r *Recorder) Emit(ctx context.Context, ev session.Event) {
	switch ev.Type {
	case session.EvSessionInit:
		r.sessionsStarted.Add(ctx, 1)
	case session.EvResult:
		r.recordResult(ctx, ev.Result)
	case session.EvSubagentStart:
		r.subagentUsed.Add(ctx, 1)
	case session.EvTeamStart:
		r.teamUsed.Add(ctx, 1)
	}
}

func (r *Recorder) recordResult(ctx context.Context, res *session.ResultPayload) {
	if res == nil {
		r.runsCompleted.Add(ctx, 1, metric.WithAttributes(attribute.String(attrStop, string(session.StopNone))))
		return
	}
	r.runsCompleted.Add(ctx, 1, metric.WithAttributes(attribute.String(attrStop, string(res.Stop))))
	u := res.Usage
	r.tokens.Add(ctx, int64(u.InputTokens), metric.WithAttributes(attribute.String(attrKind, "input")))
	r.tokens.Add(ctx, int64(u.OutputTokens), metric.WithAttributes(attribute.String(attrKind, "output")))
	r.tokens.Add(ctx, int64(u.CacheReadTokens), metric.WithAttributes(attribute.String(attrKind, "cache_read")))
	r.tokens.Add(ctx, int64(u.CacheWriteTokens), metric.WithAttributes(attribute.String(attrKind, "cache_write")))
	r.tokens.Add(ctx, int64(u.ReasoningTokens), metric.WithAttributes(attribute.String(attrKind, "reasoning")))
}
