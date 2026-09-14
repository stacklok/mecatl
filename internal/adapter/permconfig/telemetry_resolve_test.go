package permconfig

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

// The telemetry: block (specifically telemetry.productMetrics.enabled) is
// OPERATOR-TIER ONLY: a project repo must never be able to flip a user's own
// product-metrics opt-out in either direction. These tests pin the operator
// capture and the project-tier WARN-ignore (the fail-closed core). Mirrors
// openrouter_test.go.

const operatorTelemetryYAML = "telemetry:\n  productMetrics:\n    enabled: false\n"

// TestOperatorProductMetricsEnabledFromCLIHonoured: an OPERATOR-TIER (CLI
// explicit) telemetry: block is read and returned by
// OperatorProductMetricsEnabled(), parsed faithfully.
func TestOperatorProductMetricsEnabledFromCLIHonoured(t *testing.T) {
	env := envWithExplicit("/etc/mecatl/telemetry.yaml", operatorTelemetryYAML)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/telemetry.yaml"}}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	got := r.OperatorProductMetricsEnabled()
	if got == nil || *got != false {
		t.Fatalf("OperatorProductMetricsEnabled() = %v, want explicit false", got)
	}
}

// TestOperatorProductMetricsEnabledAbsentIsNil: no telemetry: config anywhere
// yields a nil accessor result.
func TestOperatorProductMetricsEnabledAbsentIsNil(t *testing.T) {
	r := newWithEnv(Options{Conventional: true}, fakeEnv())
	if got := r.OperatorProductMetricsEnabled(); got != nil {
		t.Fatalf("OperatorProductMetricsEnabled() = %v, want nil (absent)", got)
	}
}

// TestProjectTierTelemetryBlockIsIgnoredWithWarn is the FAIL-CLOSED CORE: a
// PROJECT-TIER telemetry: block must NEVER become the operator config, and the
// resolver WARNs naming why (a project repo cannot change the user's own
// product-metrics opt-out).
func TestProjectTierTelemetryBlockIsIgnoredWithWarn(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, operatorTelemetryYAML)

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)

	if got := r.OperatorProductMetricsEnabled(); got != nil {
		t.Fatalf("a PROJECT-tier telemetry: block must NOT become the operator config; got %v", got)
	}
	log := buf.String()
	if !strings.Contains(log, "IGNORING a project-tier telemetry") {
		t.Fatalf("expected an ignore-WARN naming the project tier; got:\n%s", log)
	}
}
