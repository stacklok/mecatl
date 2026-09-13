package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestProviderSetDefaultUsesResolvedFallbackAndSafeWriter(t *testing.T) {
	oldResolve, oldUpdate, oldPath := resolveProviderDefaultForCommand, updateProviderDefaults, providerSettingsPath
	t.Cleanup(func() {
		resolveProviderDefaultForCommand, updateProviderDefaults, providerSettingsPath = oldResolve, oldUpdate, oldPath
	})
	resolveProviderDefaultForCommand = func(provider, model string) (string, string, error) {
		if provider != "openrouter" || model != "" {
			t.Fatalf("resolver input = (%q, %q)", provider, model)
		}
		return "openrouter", "openai/gpt-5", nil
	}
	providerSettingsPath = func() string { return "/safe/settings.yaml" }
	called := false
	updateProviderDefaults = func(_ context.Context, path string, update permconfig.DefaultUpdate) (authfile.CommitState, error) {
		called = true
		if path != "/safe/settings.yaml" || update != (permconfig.DefaultUpdate{Provider: "openrouter", Model: "openai/gpt-5"}) {
			t.Fatalf("safe update = (%q, %+v)", path, update)
		}
		return authfile.CommitDurable, nil
	}

	var stdout bytes.Buffer
	if err := runProviderSetDefaultCommand(invocationResolution{llmEndpoint: "openrouter"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if !called || !strings.Contains(stdout.String(), `provider "openrouter", model "openai/gpt-5"`) {
		t.Fatalf("called=%t output=%q", called, stdout.String())
	}
}

func TestProviderSetDefaultInvalidDoesNotWrite(t *testing.T) {
	oldResolve, oldUpdate := resolveProviderDefaultForCommand, updateProviderDefaults
	t.Cleanup(func() {
		resolveProviderDefaultForCommand, updateProviderDefaults = oldResolve, oldUpdate
	})
	resolveProviderDefaultForCommand = func(string, string) (string, string, error) {
		return "", "", errors.New("unknown or unavailable provider")
	}
	updateProviderDefaults = func(context.Context, string, permconfig.DefaultUpdate) (authfile.CommitState, error) {
		t.Fatal("writer called for invalid selection")
		return authfile.CommitNotApplied, nil
	}

	err := runProviderSetDefaultCommand(invocationResolution{llmEndpoint: "missing", remaining: []string{"bogus"}}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unknown or unavailable provider") {
		t.Fatalf("invalid selection error = %v", err)
	}
}

func TestProviderSetDefaultGrammar(t *testing.T) {
	got := resolveInvocation([]string{"mecatui", "providers", "set-default", "openai", "gpt-5"})
	if got.err != nil || got.mode != modeProviderSetDefault || got.llmEndpoint != "openai" || len(got.remaining) != 1 || got.remaining[0] != "gpt-5" {
		t.Fatalf("resolution = %+v", got)
	}
}
