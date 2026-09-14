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
major features you have enabled, an anonymous per-install identifier, and
coarse session/run/tool-call counts by bounded category — never a prompt,
file path, raw tool name, or model id) to help Stacklok understand community
adoption. This is on by default. To opt out: pass
--product-metrics=false, set MECATL_PRODUCT_METRICS=false, set DO_NOT_TRACK=1,
or set telemetry.productMetrics.enabled: false in your settings.yaml. Details:
see docs/adr/0329-product-metrics.md.
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
//
// When dryRun is true (and enabled is also true), this builds a
// productmetrics.DryRunRecorder over diag instead of the real OTLP
// pipeline — the --product-metrics-dry-run audit path: no install-id
// read/write, no real provider, no real heartbeat ticker. It fires exactly
// ONE representative Heartbeat call SYNCHRONOUSLY, before returning (a dry
// run only needs to show one sample, not simulate the full cadence, and a
// short-lived process like mecatequi can exit before an unawaited goroutine
// ever runs) and returns handles wrapping the DryRunRecorder as both Sink
// and ToolCallRecorder.
//
// installIDOverride, when non-empty, is used verbatim as the install id and
// the local-file mechanism (LoadOrCreateInstallIDDefault) is skipped entirely.
// It exists for mecak8s, which runs storage-free with no PVC (ADR 0048): its
// Helm chart provisions ONE stable id per release in a ConfigMap and threads
// it in via MECATL_PRODUCT_METRICS_INSTALL_ID, because a local file would mint
// a fresh, never-reused id on every pod restart. An override never reports
// FirstRun (nothing was minted here, and the chart — not this process — owns
// the id's lifecycle). Every other binary passes "" and keeps the local-file
// behaviour unchanged.
//
// notify, when firstRun is true, is called EXACTLY ONCE, SYNCHRONOUSLY,
// BEFORE this function starts the heartbeat goroutine (whose first
// Heartbeat call fires immediately — see RunHeartbeat) and before it
// returns. ADR 0329 makes visible advance disclosure load-bearing for
// opt-out collection: printing the notice only after the caller later
// notices ProductMetricsHandles.FirstRun — e.g. after its own startup work,
// or worse, only at shutdown/flush time — leaves a window where the
// pipeline can record and export data before a human ever sees the notice.
// Calling notify here, before ANY export-capable state exists, closes that
// window regardless of what the caller does afterward (including an error
// path that flushes and discards the handles without ever consulting
// FirstRun). notify is nil-safe: a nil notify simply skips the call (kept
// for the disabled/dry-run/override paths and existing test callers that
// don't exercise disclosure). FirstRun is still returned on the handles for
// callers/tests that want to observe it, but it must never be the sole
// trigger for actually showing the notice.
func BuildProductMetrics(
	ctx, heartbeatCtx context.Context,
	enabled, dryRun bool,
	binary productmetrics.Binary,
	version string,
	heartbeatInterval time.Duration,
	snap productmetrics.FeatureSnapshot,
	installIDOverride string,
	diag port.Diagnostics,
	notify func(string),
) (ProductMetricsHandles, error) {
	noop := func(context.Context) error { return nil }
	if !enabled {
		return ProductMetricsHandles{Shutdown: noop}, nil
	}
	if dryRun {
		rec := productmetrics.NewDryRunRecorder(diag)
		rec.Heartbeat(snap)
		return ProductMetricsHandles{Sink: rec, ToolCallRecorder: rec, Shutdown: noop}, nil
	}

	// LoadOrCreateInstallIDDefault persists (or reads back) this process's
	// local install-id file and reports firstRun for the disclosure notice
	// below. Reinstated as a real, exported resource attribute (see
	// provider.go's doc comment) after its cardinality cost was sized and
	// accepted. An externally provisioned id (see installIDOverride) bypasses
	// it entirely: there is no file to read, write, or report a first run
	// from, so Available() gates ONLY this local-file branch — a keyless
	// build (every local/dev/CI-test build) must not mint and persist an id
	// file that a later release build's LoadOrCreateInstallIDDefault would
	// then read back as "already exists", silently reporting firstRun=false
	// for that build's genuine first export (see Available's doc comment).
	// The override path still proceeds and fails later, at provider
	// construction, exactly as before.
	installID, firstRun := installIDOverride, false
	if installID == "" {
		if !productmetrics.Available() {
			return ProductMetricsHandles{Shutdown: noop},
				fmt.Errorf("product metrics: no ingest key baked into this build (see BUILD_LDFLAGS in Taskfile.yml)")
		}
		var err error
		installID, firstRun, err = productmetrics.LoadOrCreateInstallIDDefault()
		if err != nil {
			return ProductMetricsHandles{Shutdown: noop}, fmt.Errorf("product metrics: install id: %w", err)
		}
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

	// Disclosure BEFORE the pipeline goes live: notify runs synchronously,
	// here, before the heartbeat goroutine below is even started — so no
	// export-capable state exists yet when a human is expected to have seen
	// the notice.
	if firstRun && notify != nil {
		notify(ProductMetricsDisclosureNotice)
	}

	go productmetrics.RunHeartbeat(heartbeatCtx, recorder, heartbeatInterval, snap)

	armFirstValueTracking(ctx, recorder, installIDOverride, diag)

	return ProductMetricsHandles{
		Sink:             recorder,
		ToolCallRecorder: recorder,
		Shutdown:         provider.Shutdown,
		FirstRun:         firstRun,
	}, nil
}

// armFirstValueTracking enables mecatl.product.time_to_first_value on
// recorder — EXCEPT when installIDOverride is non-empty (mecak8s), where it
// deliberately does nothing.
//
// mecak8s runs storage-free with no PVC (ADR 0048) — the SAME reason its
// install-id comes from a Helm ConfigMap rather than a local file (see
// provider.go's doc comment). The once-ever contract of time_to_first_value
// depends on the SAME kind of durable local marker
// (LoadOrCreateFirstValueMarkerDefault, under $XDG_STATE_HOME) the install-id
// mechanism does, and mecak8s's Helm chart provisions no equivalent for it.
// Arming anyway would make every pod restart/replica rearm with
// alreadyRecorded=false, so a continuously-rolling deployment would emit a
// steady stream of "time to first value" samples that are really
// "time from this pod's start to its first qualifying run" — indistinguishable
// in the backend, under one stable mecatl.install.id, from a stream of
// brand-new installs onboarding continuously. Silently shipping that under a
// "once per install, ever" label would be worse than not shipping the metric
// at all for this one binary; a future durable marker (the ConfigMap, or
// Redis, since mecak8s already depends on it — ADR 0048) can lift this
// restriction later.
//
// firstSeenAt is time.Now(): this process's start, not the install-id file's
// mtime. The approximation is deliberate and sound for the signal's purpose (a
// coarse "how long did onboarding take", not a billing-grade timer) — the
// marker read below means the metric can only ever fire on an install that has
// not yet had a qualifying run, and for a genuinely new install this process IS
// the first one, so "now" is that install's first-seen moment to within the
// process's own startup. It also keeps the install-id file's path private to
// the productmetrics package.
//
// A marker-read failure degrades to "track it anyway" rather than disabling
// anything: time_to_first_value is a nice-to-have signal, not load-bearing
// enough to fail the whole product-metrics pipeline over. The worst case is one
// duplicate sample from a later process.
func armFirstValueTracking(ctx context.Context, recorder *productmetrics.Recorder, installIDOverride string, diag port.Diagnostics) {
	if installIDOverride != "" {
		return
	}
	already, err := productmetrics.FirstValueRecordedDefault()
	if err != nil && diag != nil {
		diag.Log(ctx, port.LevelDebug,
			"product metrics: could not read the first-value marker; time_to_first_value may be re-recorded once",
			"error", err)
		already = false
	}
	recorder.EnableFirstValueTracking(time.Now(), already, func() (bool, error) {
		alreadyExisted, werr := productmetrics.LoadOrCreateFirstValueMarkerDefault()
		return !alreadyExisted, werr
	})
}
