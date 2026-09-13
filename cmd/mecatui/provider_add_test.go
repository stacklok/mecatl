package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestCollectProviderDefinitionBuildsAPIKeyAndNoAuth(t *testing.T) {
	for _, tc := range []struct {
		name, method string
	}{
		{name: "api key", method: "api_key"},
		{name: "no auth", method: "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreProviderAddInput(t, "https://gateway.example/v1", "openai-chat-completions", "model-1", tc.method)
			got, err := collectProviderDefinition()
			if err != nil {
				t.Fatal(err)
			}
			want := permconfig.ProviderDefinition{BaseURL: "https://gateway.example/v1", APIFlavor: "openai-chat-completions", DefaultModel: "model-1", Auth: permconfig.ProviderAuth{Method: tc.method}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("definition = %#v, want %#v", got, want)
			}
		})
	}
}

func TestCollectProviderDefinitionBuildsOIDC(t *testing.T) {
	restoreProviderAddInput(t,
		"https://gateway.example/v1", "openai-responses", "model-1", "oidc",
		"https://issuer.example", "client-id", "openid profile", "audience",
		"private-ca", "/issuer-ca.pem", "public",
	)
	got, err := collectProviderDefinition()
	if err != nil {
		t.Fatal(err)
	}
	want := &permconfig.ProviderOIDC{
		Issuer: "https://issuer.example", ClientID: "client-id", Scopes: []string{"openid", "profile"}, ResourceAudience: "audience",
		IssuerTrust: permconfig.NativeTrust{Policy: "private-ca", CABundle: "/issuer-ca.pem"}, GatewayTrust: permconfig.NativeTrust{Policy: "public"},
	}
	if got.Auth.Method != "oidc" || !reflect.DeepEqual(got.Auth.OIDC, want) {
		t.Fatalf("OIDC definition = %#v, want %#v", got.Auth.OIDC, want)
	}
}

func TestProviderAddNoLoginSavesOnlyDefinitionAndPrintsCommand(t *testing.T) {
	restoreProviderAddInput(t, "https://gateway.example", "openai-responses", "model-1", "api_key")
	var got permconfig.ProviderMapUpdate
	restoreProviderAddWriter(t, func(_ context.Context, path string, update permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		if path != "/safe/settings.yaml" {
			t.Fatalf("writer path = %q", path)
		}
		got = update
		return authfile.CommitDurable, nil
	})
	var stdout, stderr bytes.Buffer
	res := invocationResolution{mode: modeProviderAdd, llmEndpoint: "custom", remaining: []string{"--no-login"}}
	if err := runProviderAddCommand(res, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "custom" || got.Definition == nil || got.Definition.Auth.Method != "api_key" {
		t.Fatalf("writer update = %#v", got)
	}
	const want = "Provider definition saved for \"custom\"\nNext login command: mecatui providers login custom\n"
	if stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("output = stdout %q stderr %q, want %q", stdout.String(), stderr.String(), want)
	}
}

func TestProviderAddChainsToSelectedLogin(t *testing.T) {
	restoreProviderAddInput(t, "https://gateway.example", "openai-responses", "model-1", "api_key")
	restoreProviderAddWriter(t, func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		return authfile.CommitDurable, nil
	})
	oldLoad, oldRead, oldUpdate := loadProviderCredentialConfig, readProviderAPIKey, updateProviderAPIKey
	t.Cleanup(func() {
		loadProviderCredentialConfig, readProviderAPIKey, updateProviderAPIKey = oldLoad, oldRead, oldUpdate
	})
	loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
		return providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}}}, authPath: "/safe/auth.yaml"}, nil
	}
	readProviderAPIKey = func(string) (string, error) { return "secret", nil }
	calls := 0
	updateProviderAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		calls++
		return authfile.CommitDurable, nil
	}
	var stdout, stderr bytes.Buffer
	if err := runProviderAddCommand(invocationResolution{mode: modeProviderAdd, llmEndpoint: "custom"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || stdout.String() != "Provider definition saved for \"custom\"\nAPI key saved for provider \"custom\"\n" || stderr.Len() != 0 {
		t.Fatalf("login chain calls=%d stdout=%q stderr=%q", calls, stdout.String(), stderr.String())
	}
}

func TestProviderAddCancellationDoesNotWrite(t *testing.T) {
	oldRead, oldUpdate := readProviderAddField, updateProviderMap
	t.Cleanup(func() { readProviderAddField, updateProviderMap = oldRead, oldUpdate })
	readProviderAddField = func(string) (string, error) { return "", context.Canceled }
	updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		t.Fatal("cancelled add must not write")
		return authfile.CommitNotApplied, nil
	}
	var stdout, stderr bytes.Buffer
	err := runProviderAddCommand(invocationResolution{mode: modeProviderAdd, llmEndpoint: "custom"}, &stdout, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || stdout.Len() != 0 || stderr.String() != "Cancelled; no changes made.\n" {
		t.Fatalf("cancellation err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func restoreProviderAddInput(t *testing.T, values ...string) {
	t.Helper()
	old := readProviderAddField
	t.Cleanup(func() { readProviderAddField = old })
	readProviderAddField = func(string) (string, error) {
		if len(values) == 0 {
			t.Fatal("unexpected provider-add prompt")
		}
		value := values[0]
		values = values[1:]
		return value, nil
	}
}

func restoreProviderAddWriter(t *testing.T, writer func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error)) {
	t.Helper()
	oldPath, oldWriter := providerSettingsPath, updateProviderMap
	t.Cleanup(func() { providerSettingsPath, updateProviderMap = oldPath, oldWriter })
	providerSettingsPath = func() string { return "/safe/settings.yaml" }
	updateProviderMap = writer
}
