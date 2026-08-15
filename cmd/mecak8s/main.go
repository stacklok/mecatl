// Command mecak8s is the storage-free, Kubernetes-native mecatl agent binary
// (ADR 0048). See flags.go for the configuration surface and serve.go for the
// shutdown contract. This file is the thin entry point: parse flags → build the
// diagnostics sink → app.Build → serve → os.Exit.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/app"
)

func main() {
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// slog.SetDefault stays for the daemon: this is the DELIBERATE, PERMANENT
	// third-party-slog bridge — a server's operational output belongs on
	// stderr/journald. cmd/ mains are the only layer allowed to call
	// slog.SetDefault; all of internal/ flows through the injected
	// port.Diagnostics (ban-guarded). Mirrors cmd/mecated.
	slog.SetDefault(logger)
	diag := slogdiag.NewFromLogger(logger)

	ctx, stop := signalCtx()
	defer stop()

	// Observability (issue #343, ADR 0098): OPT-IN. With no --otlp-* / --metrics-addr
	// flags this is a no-op (byte-identical default). The flush defer runs BEFORE
	// built.Close() (LIFO), so the OTLP flush completes before the service tears
	// down on the SIGTERM path.
	obs, oerr := buildObservability(ctx, cfg)
	if oerr != nil {
		return fmt.Errorf("telemetry: %w", oerr)
	}

	built, err := app.Build(ctx, appConfig(cfg, diag, obs))
	if err != nil {
		flushTelemetry(os.Stderr, obs, cfg.otlpShutdownTimeout)
		return err
	}
	defer built.Close()
	defer flushTelemetry(os.Stderr, obs, cfg.otlpShutdownTimeout)

	return serve(ctx, cfg, built.Service, obs)
}
