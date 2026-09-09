// Package cliconfig provides product-metrics opt-out precedence and a
// ToolCallRecorder fan-out helper.
package cliconfig

import (
	"os"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// ProductMetricsPrecedence carries the opt-out inputs
// ResolveProductMetricsEnabled folds, highest precedence first: an explicit
// CLI flag, then the DO_NOT_TRACK env var convention (consoledonottrack.com),
// then the operator settings.yaml value, then default-enabled.
type ProductMetricsPrecedence struct {
	// FlagSet/FlagValue report whether --product-metrics was explicitly
	// passed on the command line and its value.
	FlagSet   bool
	FlagValue bool
	// Getenv abstracts os.Getenv for DO_NOT_TRACK / testing. Defaults to
	// os.Getenv when nil.
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
	if getenv("DO_NOT_TRACK") != "" {
		return false
	}
	if p.SettingsEnabled != nil {
		return *p.SettingsEnabled
	}
	return true
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
	for _, r := range m {
		r.ToolCall(id, call, result, queued, took)
	}
}
