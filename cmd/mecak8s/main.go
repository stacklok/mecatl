// Command mecak8s is the storage-free, Kubernetes-native mecatl agent binary
// (ADR 0048). See flags.go for the configuration surface and serve.go for the
// shutdown contract. This file is the thin entry point: parse flags → build the
// diagnostics sink → app.Build → serve → os.Exit.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/executionclient"
	"github.com/stacklok/mecatl/internal/adapter/mockscript"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

func boundedClose(closeFn func(), timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		closeFn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn("application cleanup timed out; process exit will end remaining cleanup", "timeout", timeout)
	}
}

func main() {
	if buildinfo.IsVersion(os.Args) {
		buildinfo.PrintVersion(os.Stdout, "mecak8s")
		return
	}
	if err := run(); err != nil {
		slog.Error("mecak8s exited with error", "err", err)
		os.Exit(1)
	}
}

// run is the testable entry point: parse flags, install the slog default +
// Diagnostics sink, build the engine/service via app.Build, and serve until a
// signal drives the bounded shutdown. It returns nil on a clean shutdown
// (exit 0) — the honest contract is that in-flight runs are CANCELLED, not
// drained (see serve.go's doc comment).
func run() error {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		return err
	}

	if cfg.mockScript != "" {
		cfg.mockProvider, err = mockscript.Load(cfg.mockScript)
		if err != nil {
			return err
		}
	}

	logger := cliconfig.NewTextLogger(os.Stderr, cfg.logLevel, cfg.logLevelWarning)
	// slog.SetDefault stays for the daemon: this is the DELIBERATE, PERMANENT
	// third-party-slog bridge — a server's operational output belongs on
	// stderr/journald. cmd/ mains are the only layer allowed to call
	// slog.SetDefault; all of internal/ flows through the injected
	// port.Diagnostics (ban-guarded). Mirrors cmd/mecated.
	slog.SetDefault(logger)
	diag := slogdiag.NewFromLogger(logger)
	cfg.diagnostics = diag

	ctx, stop := signalCtx()
	defer stop()

	// Observability (issue #343, ADR 0098): OPT-IN. With no --otlp-* / --metrics-addr
	// flags this is a no-op (byte-identical default). The flush defer runs BEFORE
	// built.Close() (LIFO), so the OTLP flush completes before the service tears
	// down on the SIGTERM path.
	obs, oerr := buildObservability(ctx, cfg, diag)
	if oerr != nil {
		return fmt.Errorf("telemetry: %w", oerr)
	}

	composition := appConfig(cfg, diag, obs)
	if cfg.executionEnabled {
		tlsConfig, tlsErr := executionclient.LoadTLSConfig(executionclient.TLSFiles{CA: cfg.executionTLSCA, Cert: cfg.executionTLSCert, Key: cfg.executionTLSKey})
		if tlsErr != nil {
			flushTelemetry(os.Stderr, obs, cfg.otlpShutdownTimeout)
			return tlsErr
		}
		client, clientErr := executionclient.New(cfg.executionEndpoint, tlsConfig)
		if clientErr != nil {
			flushTelemetry(os.Stderr, obs, cfg.otlpShutdownTimeout)
			return clientErr
		}
		defer client.Close()
		placement, placementErr := executionclient.NewProvider(client, cfg.executionProfile)
		if placementErr != nil {
			flushTelemetry(os.Stderr, obs, cfg.otlpShutdownTimeout)
			return placementErr
		}
		composition.PlacementProvider = placement
		composition.PlacementScope = "remote-execution"
	}
	built, err := app.Build(ctx, composition)
	if err != nil {
		flushTelemetry(os.Stderr, obs, cfg.otlpShutdownTimeout)
		return err
	}
	defer boundedClose(built.Close, cfg.closeTimeout)
	defer flushTelemetry(os.Stderr, obs, cfg.otlpShutdownTimeout)

	return serve(ctx, cfg, built.Service, obs, built.MCPBrokerHandlers, built.MCPBrokerCallbackPath)
}
