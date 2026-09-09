// Package cliconfig assembles the BuildProductMetrics composition helper —
// the full opt-out product-metrics pipeline (install id → provider →
// recorder → heartbeat goroutine) that a cmd main threads into its
// EventSink/ToolCallRecorder fan-out. Kept separate from
// productmetrics_config.go (the precedence/fan-out helpers Task 9 added):
// this file is the thing that actually constructs the pipeline, not the
// pure-function policy that decides whether to.
package cliconfig

import (
	"context"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
)

// ProductMetricsDisclosureNotice is printed ONCE — the first run after
// product metrics were enabled and this install's telemetry id did not yet
// exist — to stderr, non-blockingly, before any pipeline is built. Opt-out
// telemetry without a visible disclosure is the pattern that burns
// community trust; this is the whole of that disclosure.
const ProductMetricsDisclosureNotice = `mecatl reports anonymous product-adoption metrics (version, OS/arch, which
major features you have enabled, and coarse session/run/tool-call counts —
never a prompt, file path, tool name, or model id) to help Stacklok understand
community adoption. This is on by default. To opt out: pass
--product-metrics=false, set DO_NOT_TRACK=1, or set
telemetry.productMetrics.enabled: false in your settings.yaml. Details:
<user-docs link, filled in by Task 15>.
`

// ProductMetricsHandles bundles the handles a cmd main threads into its
// EventSink/ToolCallRecorder fan-out (via TeeToolCallRecorder /
// internal/adapter/telemetry.NewSink alongside the operator sink) and its
// shutdown defer. Every field is zero-valued when telemetry is disabled.
type ProductMetricsHandles struct {
	Sink             port.EventSink
	ToolCallRecorder port.ToolCallRecorder
	// Shutdown flushes + stops the provider. Always non-nil (a no-op when
	// disabled), so a caller can defer it unconditionally.
	Shutdown func(context.Context) error
	// FirstRun is true the first time this install's telemetry id was just
	// minted — the caller prints ProductMetricsDisclosureNotice when true.
	FirstRun bool
}

// BuildProductMetrics constructs the full opt-out product-metrics pipeline
// when enabled is true; when false it returns zero handles (the
// byte-identical disabled posture) and no error. heartbeatInterval is
// productmetrics.DefaultHeartbeatInterval for long-running processes, or 0
// for a single-fire-only short-lived process (mecatequi). heartbeatCtx is
// cancelled by the caller on shutdown to stop the periodic ticker goroutine
// this starts.
func BuildProductMetrics(
	ctx, heartbeatCtx context.Context,
	enabled bool,
	binary productmetrics.Binary,
	version string,
	heartbeatInterval time.Duration,
	snap productmetrics.FeatureSnapshot,
) (ProductMetricsHandles, error) {
	noop := func(context.Context) error { return nil }
	if !enabled {
		return ProductMetricsHandles{Shutdown: noop}, nil
	}

	installID, firstRun, err := productmetrics.LoadOrCreateInstallIDDefault()
	if err != nil {
		return ProductMetricsHandles{Shutdown: noop}, fmt.Errorf("product metrics: install id: %w", err)
	}

	provider, err := productmetrics.NewProvider(ctx, productmetrics.Config{
		Binary:    binary,
		Version:   version,
		InstallID: installID,
	})
	if err != nil {
		return ProductMetricsHandles{Shutdown: noop}, fmt.Errorf("product metrics: provider: %w", err)
	}

	recorder, err := productmetrics.NewRecorder(provider.Meter())
	if err != nil {
		_ = provider.Shutdown(ctx)
		return ProductMetricsHandles{Shutdown: noop}, fmt.Errorf("product metrics: recorder: %w", err)
	}

	go productmetrics.RunHeartbeat(heartbeatCtx, recorder, heartbeatInterval, snap)

	return ProductMetricsHandles{
		Sink:             recorder,
		ToolCallRecorder: recorder,
		Shutdown:         provider.Shutdown,
		FirstRun:         firstRun,
	}, nil
}
