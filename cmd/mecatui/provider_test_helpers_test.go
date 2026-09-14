package main

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func testProviderCommands() providerCommands {
	commands := newProviderCommands()
	commands.backend.prepareOIDCRoot = nil
	commands.backend.toolHiveAvailable = func() bool { return false }
	commands.backend.inspect = providerInspectionLoader(providerInspection{})
	return commands
}

func providerInput(t *testing.T, values ...string) func(context.Context, string) (string, error) {
	t.Helper()
	return func(context.Context, string) (string, error) {
		if len(values) == 0 {
			t.Fatal("unexpected provider prompt")
		}
		value := values[0]
		values = values[1:]
		return value, nil
	}
}

func providerMapWriter(path string, writer func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error)) providerBackend {
	backend := testProviderCommands().backend
	backend.settingsPath = func() string { return path }
	backend.updateProviderMap = writer
	return backend
}

func providerCredentialConfigLoader(cfg providerCredentialConfig) func() (providerCredentialConfig, error) {
	return func() (providerCredentialConfig, error) { return cfg, nil }
}

func providerInspectionLoader(inspection providerInspection) func() (providerInspection, error) {
	return func() (providerInspection, error) { return inspection, nil }
}
