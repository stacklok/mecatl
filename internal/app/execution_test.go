package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type executionReadyManager struct {
	err error
}

func (m *executionReadyManager) EnsureReady(ctx context.Context, _ microvmmanager.ReadyRequest) (string, error) {
	microvmmanager.ReportReadinessStage(ctx, microvmmanager.StageDownload)
	if m.err != nil {
		return "", m.err
	}
	return "unix:///run/test-microvmd.sock", nil
}

func TestFoldExecutionConfiguresMicroVMFromOperatorSettings(t *testing.T) {
	settings := writeOperatorSettingsFile(t, `
execution:
  default_placement: microvm-local
  microvm:
    guest_egress:
      mode: deny-all
`)
	manager := &executionReadyManager{}
	cfg := Config{
		Workspace:         t.TempDir(),
		PermissionConfigs: []string{settings},
		MicroVMReadyRequest: func(selection microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
			return microvmmanager.ReadyRequest{Policy: microvmmanager.Policy{GuestEgressMode: selection.Mode}}, nil
		},
		MicroVMManagerFactory: func() (MicroVMReadyManager, string, error) {
			return manager, "unix:///run/test-microvmd.sock", nil
		},
	}
	cfg.permResolver = buildPermResolver(cfg)
	got, err := foldExecution(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlacementProvider == nil || got.EnvironmentForkers["microvm"] == nil || got.EnvironmentMergers["microvm"] == nil {
		t.Fatal("microvm-local settings did not configure the placement/fork/merge bundle")
	}
}

func TestFoldExecutionCLIOverrideWinsOnlyWhenSet(t *testing.T) {
	settings := writeOperatorSettingsFile(t, `execution: {default_placement: microvm-local}`)
	cfg := Config{PermissionConfigs: []string{settings}, DefaultPlacement: PlacementHostLocal, DefaultPlacementSet: true}
	cfg.permResolver = buildPermResolver(cfg)
	got, err := foldExecution(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlacementProvider != nil {
		t.Fatal("explicit host-local override did not outrank operator settings")
	}
}

func TestMicroVMReadinessProgressAndStableFailureCrossPlacementBoundary(t *testing.T) {
	manager := &executionReadyManager{err: errors.New("private manager failure")}
	var progress []string
	cfg, err := ConfigureExecution(Config{
		Workspace: t.TempDir(), DefaultPlacement: PlacementMicroVMLocal, DefaultPlacementSet: true,
		MicroVMReadyRequest: func(microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
			return microvmmanager.ReadyRequest{}, nil
		},
		MicroVMManagerFactory: func() (MicroVMReadyManager, string, error) {
			return manager, "unix:///run/test-microvmd.sock", nil
		},
		MicroVMReadinessObserver: func(_ microvmmanager.ReadinessStage, message string) {
			progress = append(progress, message)
		},
		MicroVMReadinessFailureHint: "next: run 'mecated microvm doctor'; diagnostics log: /state/mecatl/mecatui.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.PlacementProvider.Bind(t.Context(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "deployment"})
	if err == nil {
		t.Fatal("readiness failure did not cross the real placement bind boundary")
	}
	for _, want := range []string{"Guest IPv4 egress is permissive", "Downloading microVM components"} {
		if !strings.Contains(strings.Join(progress, "\n"), want) {
			t.Fatalf("progress %q omitted %q", progress, want)
		}
	}
	for _, want := range []string{"preparation failed during download", "mecated microvm doctor", "/state/mecatl/mecatui.log"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q omitted %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "private failure") {
		t.Fatalf("stable UI error leaked private manager detail: %v", err)
	}
}

func TestFoldExecutionSettingsErrorsNameSettingsKeys(t *testing.T) {
	settings := writeOperatorSettingsFile(t, `
execution:
  default_placement: microvm-local
  microvm:
    guest_egress:
      mode: allowlist
      allow: [127.0.0.1:443/tcp]
`)
	cfg := Config{PermissionConfigs: []string{settings}}
	cfg.permResolver = buildPermResolver(cfg)
	_, err := foldExecution(cfg)
	if err == nil || !strings.Contains(err.Error(), "execution.microvm.guest_egress.allow") || strings.Contains(err.Error(), "--microvm-guest-allow") {
		t.Fatalf("settings validation error = %v", err)
	}
}

func TestFoldExecutionRejectsMalformedDestination(t *testing.T) {
	settings := writeOperatorSettingsFile(t, `
execution:
  default_placement: microvm-local
  microvm:
    guest_egress:
      mode: allowlist
      allow: [127.0.0.1:443/tcp]
`)
	cfg := Config{PermissionConfigs: []string{settings}}
	cfg.permResolver = permconfig.New(permconfig.Options{ExplicitFiles: cfg.PermissionConfigs})
	if _, err := foldExecution(cfg); err == nil {
		t.Fatal("invalid MicroVM destination did not fail closed")
	}
}
