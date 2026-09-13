package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
)

func TestProviderUnification_Scenario2_ProviderCommandGrammar(t *testing.T) {
	for _, args := range [][]string{
		{"mecatui", "providers"}, {"mecatui", "providers", "status"},
	} {
		if got := resolveInvocation(args); got.err != nil {
			t.Fatalf("resolve %v: %v", args, got.err)
		}
	}
	if got := resolveInvocation([]string{"mecatui", "llm", "status"}); got.err == nil {
		t.Fatal("legacy llm command was accepted")
	}
}

func TestProviderUnification_Scenario3_ProviderHelpHierarchy(t *testing.T) {
	got := resolveInvocation([]string{"mecatui", "providers", "status", "--help"})
	if got.err != nil || got.mode != modeProviderStatus {
		t.Fatalf("status help = %+v", got)
	}
}

func TestProviderUnification_Scenario4_APIKeyFileFlagIsTheOnlySpelling(t *testing.T) {
	if got := resolveInvocation([]string{"mecatui", "--api-key-file", "auth.yaml"}); got.err != nil {
		t.Fatalf("--api-key-file did not remain a bare invocation: %v", got.err)
	}
}

func TestProviderUnification_Scenario5_ChangedTargetIsTruthful(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := authfile.UpdateAPIKey(ctx, t.TempDir()+"/auth.yaml", authfile.APIKeyUpdate{Provider: "openai", APIKey: stringPtr("secret")})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write = %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("write error disclosed secret")
	}
}

func stringPtr(v string) *string { return &v }
