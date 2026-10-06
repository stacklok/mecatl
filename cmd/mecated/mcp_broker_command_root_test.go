package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
)

func TestMecatedExplicitBrokerAuthorityCannotConstructBundledHost(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte("mcp:\n  mode: broker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseFlagsMode(modeServe, []string{"--permission-config", settings})
	if err != nil {
		t.Fatal(err)
	}
	ac := appConfig(cfg, nil, nil, nil, nil, port.NopDiagnostics{})
	if ac.MCPAuthorityLoader == nil || !ac.MCPBrokerSupported {
		t.Fatal("canonical authority loader missing")
	}
	ac.Workspace = t.TempDir()
	ac.UseMock = true
	ac.NoSoul = true
	ac.NoUserModel = true
	ac.PermissionsConventional = false
	ac.AgentsConventional = false
	_, err = buildIsolated(t, t.Context(), ac)
	if err == nil || !strings.Contains(err.Error(), "bundled broker has been retired") {
		t.Fatalf("bundled broker not rejected: %v", err)
	}
}

func TestMecatedOmittedMCPModeDefaultsGlobalWithoutBroker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := parseFlagsMode(modeServe, nil)
	if err != nil {
		t.Fatal(err)
	}
	ac := appConfig(cfg, nil, nil, nil, nil, port.NopDiagnostics{})
	if ac.MCPAuthorityDefault != mcpauthority.Global || ac.SessionBrokerFactory != nil {
		t.Fatal("omitted mode enabled broker")
	}
	ac.Workspace = t.TempDir()
	ac.UseMock = true
	ac.NoSoul = true
	ac.NoUserModel = true
	ac.PermissionsConventional = false
	ac.AgentsConventional = false
	ac.ToolHiveEnabled = false
	built, err := buildIsolated(t, context.Background(), ac)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
}
