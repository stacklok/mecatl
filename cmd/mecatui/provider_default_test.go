package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestProviderUnification_Scenario4_DefaultUsesStartupResolver(t *testing.T) {
	commands := testProviderCommands()
	commands.backend.resolveDefault = func(_ context.Context, provider, model string) (string, string, error) {
		if provider != "openrouter" || model != "" {
			t.Fatalf("resolver input = (%q, %q)", provider, model)
		}
		return "openrouter", "openai/gpt-5", nil
	}
	commands.backend.settingsPath = func() string { return "/safe/settings.yaml" }
	called := false
	commands.backend.updateDefaults = func(_ context.Context, path string, update permconfig.DefaultUpdate) (authfile.CommitState, error) {
		called = true
		if path != "/safe/settings.yaml" || update != (permconfig.DefaultUpdate{Provider: "openrouter", Model: "openai/gpt-5"}) {
			t.Fatalf("safe update = (%q, %+v)", path, update)
		}
		return authfile.CommitDurable, nil
	}
	var stdout bytes.Buffer
	if err := commands.runSetDefault(context.Background(), invocationResolution{providerName: "openrouter"}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !called || !strings.Contains(stdout.String(), `provider "openrouter", model "openai/gpt-5"`) {
		t.Fatalf("called=%t output=%q", called, stdout.String())
	}
}

func TestProviderSetDefaultInvalidDoesNotWrite(t *testing.T) {
	commands := testProviderCommands()
	commands.backend.resolveDefault = func(context.Context, string, string) (string, string, error) {
		return "", "", errors.New("unknown or unavailable provider")
	}
	commands.backend.updateDefaults = func(context.Context, string, permconfig.DefaultUpdate) (authfile.CommitState, error) {
		t.Fatal("writer called")
		return authfile.CommitNotApplied, nil
	}
	err := commands.runSetDefault(context.Background(), invocationResolution{providerName: "missing", remaining: []string{"bogus"}}, &bytes.Buffer{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unknown or unavailable provider") {
		t.Fatalf("invalid selection error = %v", err)
	}
}

func TestProviderSetDefaultGrammar(t *testing.T) {
	got := resolveInvocation([]string{"mecatui", "providers", "set-default", "openai", "gpt-5"})
	if got.err != nil || got.mode != modeProviderSetDefault || got.providerName != "openai" || len(got.remaining) != 1 || got.remaining[0] != "gpt-5" {
		t.Fatalf("resolution = %+v", got)
	}
}
