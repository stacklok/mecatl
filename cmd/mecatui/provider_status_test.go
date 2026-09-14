package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestProvidersStatusIsPassiveAndDeterministic(t *testing.T) {
	commands := testProviderCommands()
	calls := 0
	commands.backend.inspect = func() (providerInspection, error) {
		calls++
		return providerInspection{definitions: permconfig.ProviderDefinitions{"custom": {ID: "custom", Auth: permconfig.ProviderAuth{Method: providerAuthNone}, DefaultModel: "model"}}}, nil
	}
	res := resolveInvocation([]string{"mecatui", "providers"})
	var first, second bytes.Buffer
	if err := commands.runStatus(context.Background(), res, &first, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := commands.runStatus(context.Background(), res, &second, io.Discard); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || first.String() != second.String() {
		t.Fatalf("status not deterministic: calls=%d", calls)
	}
	if strings.Contains(first.String(), "credential") || strings.Contains(first.String(), "fingerprint") {
		t.Fatalf("status exposed credential detail: %q", first.String())
	}
}

func TestProvidersStatusToolHiveAndUnknownProvider(t *testing.T) {
	commands := testProviderCommands()
	commands.backend.toolHiveAvailable = func() bool { return true }
	commands.backend.inspect = providerInspectionLoader(providerInspection{})
	res := resolveInvocation([]string{"mecatui", "providers", "status", "toolhive"})
	var output bytes.Buffer
	if err := commands.runStatus(context.Background(), res, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Next step: use `thv llm` tooling") {
		t.Fatalf("ToolHive handoff missing: %q", output.String())
	}
	res = resolveInvocation([]string{"mecatui", "providers", "status", "missing"})
	if err := commands.runStatus(context.Background(), res, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), `unknown provider "missing"`) {
		t.Fatalf("unknown provider error = %v", err)
	}
}

func TestLocalProviderInspectionUsesConfiguredAPIKeyFileAndReportsShadowing(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("OPENAI_API_KEY", "environment-secret")
	for _, name := range []string{"ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(name, "")
	}
	authPath := filepath.Join(t.TempDir(), "provider-keys.yaml")
	if err := os.WriteFile(authPath, []byte("providers:\n  openai:\n    api_key: file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(configHome, "mecatl", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("credential_store:\n  api_key:\n    file: "+authPath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := inspectLocalProviders()
	if err != nil {
		t.Fatal(err)
	}
	statuses := testProviderCommands().statuses(context.Background(), inspection, false)
	for _, status := range statuses {
		if status.Name == "openai" {
			if status.Auth != "configured (environment shadows credential_store.api_key.file)" {
				t.Fatalf("auth = %q", status.Auth)
			}
			return
		}
	}
	t.Fatal("OpenAI status missing")
}

func TestProviderStatusesFilterBuiltinsButKeepCustomDefinitions(t *testing.T) {
	commands := testProviderCommands()
	statuses := commands.statuses(context.Background(), providerInspection{definitions: permconfig.ProviderDefinitions{"no-auth": {ID: "no-auth", Auth: permconfig.ProviderAuth{Method: providerAuthNone}}, "needs-key": {ID: "needs-key", Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}}, false)
	if len(statuses) != 2 || statuses[0].Name != "needs-key" || statuses[1].Name != "no-auth" {
		t.Fatalf("statuses = %#v", statuses)
	}
}

func TestProviderStatusesIncludeToolHiveOnlyWhenDetected(t *testing.T) {
	commands := testProviderCommands()
	if got := commands.statuses(context.Background(), providerInspection{}, false); len(got) != 0 {
		t.Fatalf("unexpected statuses: %#v", got)
	}
	commands.backend.toolHiveAvailable = func() bool { return true }
	got := commands.statuses(context.Background(), providerInspection{}, false)
	if len(got) != 1 || got[0].Name != toolHiveEndpointID {
		t.Fatalf("ToolHive status = %#v", got)
	}
}

func TestProvidersStatusNamedStockUsesFullInventoryAndReadableBlocks(t *testing.T) {
	commands := testProviderCommands()
	commands.backend.inspect = providerInspectionLoader(providerInspection{})
	var empty bytes.Buffer
	if err := commands.runStatus(context.Background(), resolveInvocation([]string{"mecatui", "providers"}), &empty, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty.String(), "No local providers are configured.") {
		t.Fatalf("empty state = %q", empty.String())
	}
	var output bytes.Buffer
	if err := commands.runStatus(context.Background(), resolveInvocation([]string{"mecatui", "providers", "status", "openai"}), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "openai (built-in)\n  Authentication: not configured") {
		t.Fatalf("named status = %q", output.String())
	}
}

func TestProviderOIDCStatusReportsEnrollment(t *testing.T) {
	commands := testProviderCommands()
	runtime := &statusProviderOIDCRuntime{}
	commands.backend.openOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
		return runtime, nil
	}
	auth, next := commands.oidcStatus(context.Background(), permconfig.ProviderDefinition{ID: "custom"})
	if auth != "OIDC enrolled" || next != "ready to use" || !runtime.closed {
		t.Fatalf("OIDC status = (%q,%q), closed=%t", auth, next, runtime.closed)
	}
}

type statusProviderOIDCRuntime struct{ closed bool }

func (*statusProviderOIDCRuntime) Login(context.Context) error { return nil }
func (*statusProviderOIDCRuntime) Status(context.Context) llmendpoint.Status {
	return llmendpoint.StatusUsable
}
func (*statusProviderOIDCRuntime) Logout(context.Context) error { return nil }
func (r *statusProviderOIDCRuntime) Close() error               { r.closed = true; return nil }

func TestProvidersSupportedProviderCommandsAreExecutable(t *testing.T) {
	for _, args := range [][]string{{"mecatui", "providers", "setup"}, {"mecatui", "providers", "login", "custom"}, {"mecatui", "providers", "remove", "custom"}} {
		if got := resolveInvocation(args); got.err != nil {
			t.Errorf("%v resolved to %+v", args, got)
		}
	}
}
