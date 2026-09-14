// Package cliconfig provides product-metrics opt-out precedence and a
// ToolCallRecorder fan-out helper.
package cliconfig

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
)

// doNotTrackOptOut reports whether a DO_NOT_TRACK env value means "opt out",
// per the donottrack.sh convention: unset/empty and the conventional
// "off" spellings ("0", "false", case-insensitive) are NOT an opt-out; any
// other value is.
func doNotTrackOptOut(v string) bool {
	switch strings.ToLower(v) {
	case "", "0", "false":
		return false
	default:
		return true
	}
}

// mecatlProductMetricsOverride parses the MECATL_PRODUCT_METRICS env var: a
// mecatl-specific boolean override, distinct from the generic DO_NOT_TRACK
// convention. Named to match --product-metrics/telemetry.productMetrics.enabled
// exactly (one vocabulary word across all three surfaces) rather than a
// "track"/"telemetry"-flavored name — this codebase's OWN "telemetry" already
// means the unrelated, opt-in operator OTLP/Prometheus pipeline
// (internal/adapter/telemetry), so a same-flavored name here would misleadingly
// suggest it also touches that pipeline; it does not and never should.
// set is false when the var is empty/unset/unparseable, letting the caller
// fall through to the next precedence tier.
func mecatlProductMetricsOverride(v string) (value, set bool) {
	if v == "" {
		return false, false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, false
	}
	return b, true
}

// ProductMetricsPrecedence carries the opt-out inputs
// ResolveProductMetricsEnabled folds, highest precedence first: an explicit
// CLI flag, then the mecatl-specific MECATL_PRODUCT_METRICS env var, then the
// DO_NOT_TRACK env var convention (donottrack.sh), then the operator
// settings.yaml value, then default-enabled.
type ProductMetricsPrecedence struct {
	// FlagSet/FlagValue report whether --product-metrics was explicitly
	// passed on the command line and its value.
	FlagSet   bool
	FlagValue bool
	// Getenv abstracts os.Getenv for MECATL_PRODUCT_METRICS / DO_NOT_TRACK /
	// testing. Defaults to os.Getenv when nil.
	Getenv func(string) string
	// SettingsEnabled is permconfig.Resolver.OperatorProductMetricsEnabled()
	// — nil when the operator set no telemetry.productMetrics.enabled value.
	SettingsEnabled *bool
}

// ResolveProductMetricsEnabled applies the opt-out precedence documented on
// ProductMetricsPrecedence. Default (nothing set anywhere) is true — product
// metrics are OPT-OUT, not opt-in.
func ResolveProductMetricsEnabled(p ProductMetricsPrecedence) bool {
	if p.FlagSet {
		return p.FlagValue
	}
	getenv := p.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if v, set := mecatlProductMetricsOverride(getenv("MECATL_PRODUCT_METRICS")); set {
		return v
	}
	if doNotTrackOptOut(getenv("DO_NOT_TRACK")) {
		return false
	}
	if p.SettingsEnabled != nil {
		return *p.SettingsEnabled
	}
	return true
}

// ResolveProviderFamily derives the closed-set productmetrics.ProviderFamily
// from the same two CLI-level signals every one of the four mecatl binaries
// resolves at flag-parse time (useOpenAI is a dedicated --openai bool that
// exists on mecated/mecatequi/mecak8s; defaultProvider is --default-provider
// on all four). It NEVER returns the type's zero value — the enum has no
// zero-value member, only ProviderAnthropic/OpenAI/OpenRouter/Other — so a
// heartbeat can never emit the invalid provider_configured{family=""} that a
// hand-rolled, only-partly-populated FeatureSnapshot produced before. Kept
// here (not duplicated per binary) as the SINGLE shared oracle every
// productMetricsSnapshot in cmd/mecated, cmd/mecatui, cmd/mecatequi, and
// cmd/mecak8s calls, mirroring mecated's original Task 11 switch verbatim.
func ResolveProviderFamily(useOpenAI bool, defaultProvider string) productmetrics.ProviderFamily {
	lower := strings.ToLower(defaultProvider)
	switch {
	case useOpenAI:
		return productmetrics.ProviderOpenAI
	case strings.Contains(lower, "openrouter"):
		return productmetrics.ProviderOpenRouter
	case strings.Contains(lower, "openai"):
		return productmetrics.ProviderOpenAI
	case defaultProvider == "" || strings.Contains(lower, "anthropic"):
		return productmetrics.ProviderAnthropic
	default:
		return productmetrics.ProviderOther
	}
}

// TeeToolCallRecorder combines multiple ToolCallRecorders into one — the
// ToolCallRecorder twin of internal/adapter/telemetry.NewSink's EventSink
// fan-out (no such helper existed before product metrics, because until now
// only one ToolCallRecorder ever observed a given engine). nil entries are
// skipped, so a caller can pass an always-present operator recorder
// alongside an optional product-metrics one without a conditional slice
// build.
func TeeToolCallRecorder(recorders ...port.ToolCallRecorder) port.ToolCallRecorder {
	var non []port.ToolCallRecorder
	for _, r := range recorders {
		if r != nil {
			non = append(non, r)
		}
	}
	return multiToolCallRecorder(non)
}

type multiToolCallRecorder []port.ToolCallRecorder

func (m multiToolCallRecorder) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	m.ToolCallForRun("", id, call, result, queued, took)
}

// ToolCallForRun satisfies port.RunAwareToolCallRecorder: it forwards runID to
// any element that implements the richer interface, and falls back to that
// element's plain ToolCall otherwise. Without this method, wrapping a
// RunAwareToolCallRecorder-capable recorder (e.g. productmetrics.Recorder) in
// a multiToolCallRecorder would ERASE the optional capability — the engine's
// dispatch.go type-assertion is on Deps.ToolCallRecorder itself, and a
// composed value that only implements the base interface fails that
// assertion regardless of what it wraps. This is the general hazard with
// decorating an optional-capability interface: every decorator in the chain
// must forward it, or the capability silently stops reaching the type that
// actually needs it.
func (m multiToolCallRecorder) ToolCallForRun(runID string, id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	for _, r := range m {
		if aware, ok := r.(port.RunAwareToolCallRecorder); ok {
			aware.ToolCallForRun(runID, id, call, result, queued, took)
			continue
		}
		r.ToolCall(id, call, result, queued, took)
	}
}

// Compile-time interface check: multiToolCallRecorder must keep forwarding
// port.RunAwareToolCallRecorder, or had_tool_call/tool_calls_per_run/
// time_to_first_value silently go inert in every binary that tees a
// productmetrics.Recorder through this helper.
var _ port.RunAwareToolCallRecorder = multiToolCallRecorder(nil)
