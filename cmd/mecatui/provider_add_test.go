package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
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
			restoreProviderAddInput(t, "https://gateway.example/v1", "2", "model-1", map[string]string{"api_key": "1", "none": "3"}[tc.method])
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

func TestProviderAddChoiceUsesNumberedValidatedSelections(t *testing.T) {
	old := readProviderAddField
	t.Cleanup(func() { readProviderAddField = old })

	for _, tc := range []struct {
		label   string
		choices []string
		input   string
		want    string
	}{
		{"API flavor", []string{"openai-responses", "openai-chat-completions", "anthropic-messages"}, "2", "openai-chat-completions"},
		{"Auth method", []string{"api_key", "oidc", "none"}, "3", "none"},
		{"Issuer trust policy", []string{"public", "private-ca"}, "2", "private-ca"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			var prompt string
			readProviderAddField = func(got string) (string, error) {
				prompt = got
				return tc.input, nil
			}
			choice, err := readProviderAddChoice(tc.label, tc.choices)
			if err != nil || choice != tc.want {
				t.Fatalf("choice = %q, %v", choice, err)
			}
			for i, choice := range tc.choices {
				wantLine := fmt.Sprintf("  %d. %s", i+1, choice)
				if !strings.Contains(prompt, wantLine) {
					t.Errorf("prompt missing %q: %q", wantLine, prompt)
				}
			}
		})
	}

	readProviderAddField = func(string) (string, error) { return "openai-chat-completions", nil }
	if _, err := readProviderAddChoice("API flavor", []string{"openai-responses", "openai-chat-completions"}); err == nil || err.Error() != "enter a listed api flavor number" {
		t.Fatalf("invalid selection error = %v", err)
	}

	readProviderAddField = func(string) (string, error) { return "", context.Canceled }
	if _, err := readProviderAddChoice("Auth method", []string{"api_key", "oidc", "none"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestCollectProviderDefinitionBuildsOIDC(t *testing.T) {
	restoreProviderAddInput(t,
		"https://gateway.example/v1", "1", "model-1", "2",
		"https://issuer.example", "client-id", "openid profile", "audience",
		"2", "/issuer-ca.pem", "1",
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
	restoreProviderAddInput(t, "https://gateway.example", "1", "model-1", "1")
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
	restoreProviderAddInput(t, "https://gateway.example", "1", "model-1", "1")
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

func TestProviderAddChainsOIDCLoginAfterSavingCompleteConfiguration(t *testing.T) {
	restoreProviderAddInput(t,
		"https://gateway.example", "1", "model-1", "2",
		"https://issuer.example", "client-id", "openid profile", "audience", "1", "1",
	)
	var saved permconfig.ProviderMapUpdate
	restoreProviderAddWriter(t, func(_ context.Context, _ string, update permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		saved = update
		return authfile.CommitDurable, nil
	})
	oldLoad, oldRuntime := loadProviderCredentialConfig, openProviderOIDCRuntime
	t.Cleanup(func() { loadProviderCredentialConfig, openProviderOIDCRuntime = oldLoad, oldRuntime })
	definition := permconfig.ProviderDefinition{Auth: permconfig.ProviderAuth{Method: "oidc", OIDC: &permconfig.ProviderOIDC{CredentialStore: &permconfig.OIDCCredentialStore{Home: "/safe/oidc", Key: permconfig.NativeCredentialKey{Source: "keyring"}}}}}
	loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
		return providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": definition}}, nil
	}
	runtime := &providerAddOIDCRuntime{}
	openProviderOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
		return runtime, nil
	}
	var stdout, stderr bytes.Buffer
	if err := runProviderAddCommand(invocationResolution{mode: modeProviderAdd, llmEndpoint: "custom"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if saved.OIDCCredentialStore == nil || saved.OIDCCredentialStore.Key.Source != "keyring" || !runtime.loggedIn {
		t.Fatalf("saved update = %#v, OIDC login=%v", saved, runtime.loggedIn)
	}
	if stdout.String() != "Provider definition saved for \"custom\"\nOIDC login successful for provider \"custom\"\n" || stderr.Len() != 0 {
		t.Fatalf("output = stdout %q stderr %q", stdout.String(), stderr.String())
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

type providerAddOIDCRuntime struct{ loggedIn bool }

func (r *providerAddOIDCRuntime) Login(context.Context) error { r.loggedIn = true; return nil }
func (*providerAddOIDCRuntime) Status(context.Context) llmendpoint.Status {
	return llmendpoint.StatusUsable
}
func (*providerAddOIDCRuntime) Logout(context.Context) error { return nil }
func (*providerAddOIDCRuntime) Close() error                 { return nil }
