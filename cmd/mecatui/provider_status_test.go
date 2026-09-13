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
	old := loadProviderStatuses
	t.Cleanup(func() { loadProviderStatuses = old })
	calls := 0
	loadProviderStatuses = func() ([]providerStatus, error) {
		calls++
		return []providerStatus{
			{Name: "openai", Class: "built-in", Auth: "configured", DefaultModel: "gpt-5", Next: "ready to use"},
			{Name: "toolhive", Class: "external", Auth: "managed externally", DefaultModel: "ToolHive managed", Next: "use `thv llm` tooling"},
		}, nil
	}

	res := resolveInvocation([]string{"mecatui", "providers"})
	if res.err != nil || res.mode != modeProviderStatus {
		t.Fatalf("bare providers resolution = %+v", res)
	}
	var first, second bytes.Buffer
	if err := runProviderStatusCommand(res, &first, &bytes.Buffer{}); err != nil {
		t.Fatalf("first status: %v", err)
	}
	if err := runProviderStatusCommand(res, &second, &bytes.Buffer{}); err != nil {
		t.Fatalf("second status: %v", err)
	}
	if calls != 2 || first.String() != second.String() {
		t.Fatalf("status output must be deterministic and passive: calls=%d first=%q second=%q", calls, first.String(), second.String())
	}
	if strings.Contains(first.String(), "credential") || strings.Contains(first.String(), "fingerprint") {
		t.Fatalf("status exposed credential detail: %q", first.String())
	}
}

func TestProvidersStatusToolHiveAndUnknownProvider(t *testing.T) {
	old := loadProviderStatuses
	t.Cleanup(func() { loadProviderStatuses = old })
	loadProviderStatuses = func() ([]providerStatus, error) {
		return []providerStatus{{Name: "toolhive", Class: "external", Auth: "managed externally", DefaultModel: "ToolHive managed", Next: "use `thv llm` tooling"}}, nil
	}

	res := resolveInvocation([]string{"mecatui", "providers", "status", "toolhive"})
	var output bytes.Buffer
	if err := runProviderStatusCommand(res, &output, &bytes.Buffer{}); err != nil {
		t.Fatalf("ToolHive status: %v", err)
	}
	if !strings.Contains(output.String(), "next=use `thv llm` tooling") {
		t.Fatalf("ToolHive handoff missing: %q", output.String())
	}

	res = resolveInvocation([]string{"mecatui", "providers", "status", "missing"})
	if err := runProviderStatusCommand(res, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `unknown provider "missing"`) {
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

	statuses, err := currentProviderStatuses()
	if err != nil {
		t.Fatalf("inspect providers: %v", err)
	}
	provider, model, err := resolveProviderDefault("openai", "gpt-5")
	if err != nil || provider != "openai" || model != "gpt-5" {
		t.Fatalf("default validation through configured credential file = (%q, %q, %v)", provider, model, err)
	}
	for _, status := range statuses {
		if status.Name == "openai" {
			if status.Auth != "configured (environment shadows credential_store.api_key.file)" {
				t.Fatalf("OpenAI status auth = %q", status.Auth)
			}
			var output bytes.Buffer
			if err := writeProviderStatus(&output, status); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "environment-secret") || strings.Contains(output.String(), "file-secret") {
				t.Fatalf("provider status exposed a credential value: %q", output.String())
			}
			return
		}
	}
	t.Fatal("OpenAI status missing")
}

func TestProviderOIDCStatusReportsEnrollment(t *testing.T) {
	oldOpen := openProviderOIDCRuntime
	t.Cleanup(func() { openProviderOIDCRuntime = oldOpen })
	runtime := &statusProviderOIDCRuntime{}
	openProviderOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
		return runtime, nil
	}

	auth, next := providerOIDCStatus(permconfig.ProviderDefinition{ID: "custom"})
	if auth != "OIDC enrolled" || next != "ready to use" || !runtime.closed {
		t.Fatalf("OIDC status = (%q, %q), closed=%t", auth, next, runtime.closed)
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
	for _, args := range [][]string{
		{"mecatui", "providers", "setup"},
		{"mecatui", "providers", "setup", "openai"},
	} {
		if got := resolveInvocation(args); got.err != nil || got.mode != modeProviderSetup {
			t.Errorf("%v resolved to %+v, want provider setup", args, got)
		}
	}
	for _, args := range [][]string{
		{"mecatui", "providers", "login", "custom"},
		{"mecatui", "providers", "logout", "custom"},
		{"mecatui", "providers", "remove", "custom"},
	} {
		got := resolveInvocation(args)
		if got.err != nil {
			t.Errorf("%v resolution = %+v", args, got)
			continue
		}
		if args[2] == "remove" && got.mode != modeProviderRemove {
			t.Errorf("%v mode = %q, want %q", args, got.mode, modeProviderRemove)
		}
		if args[2] != "remove" && got.mode != modeProviderCredential {
			t.Errorf("%v mode = %q, want %q", args, got.mode, modeProviderCredential)
		}
	}
}
