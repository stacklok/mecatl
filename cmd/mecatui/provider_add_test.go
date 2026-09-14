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
	for _, tc := range []struct{ name, method, selection string }{{"api key", providerAuthAPIKey, "1"}, {"no auth", providerAuthNone, "3"}} {
		t.Run(tc.name, func(t *testing.T) {
			commands := testProviderCommands()
			commands.terminal.readField = providerInput(t, "https://gateway.example/v1", "2", "model-1", tc.selection)
			got, err := commands.collectDefinition(context.Background())
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
	for _, tc := range []struct {
		label       string
		choices     []string
		input, want string
	}{
		{"API flavor", []string{"openai-responses", "openai-chat-completions", "anthropic-messages"}, "2", "openai-chat-completions"},
		{"Auth method", []string{"api_key", "oidc", "none"}, "3", "none"},
		{"Issuer trust policy", []string{"public", "private-ca"}, "2", "private-ca"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			commands := testProviderCommands()
			var prompt string
			commands.terminal.readField = func(_ context.Context, got string) (string, error) { prompt = got; return tc.input, nil }
			choice, err := commands.readChoice(context.Background(), tc.label, tc.choices)
			if err != nil || choice != tc.want {
				t.Fatalf("choice = %q, %v", choice, err)
			}
			for i, choice := range tc.choices {
				if want := fmt.Sprintf("  %d. %s", i+1, choice); !strings.Contains(prompt, want) {
					t.Errorf("prompt missing %q: %q", want, prompt)
				}
			}
		})
	}
	commands := testProviderCommands()
	commands.terminal.readField = func(context.Context, string) (string, error) { return "openai-chat-completions", nil }
	if _, err := commands.readChoice(context.Background(), "API flavor", []string{"openai-responses", "openai-chat-completions"}); err == nil || err.Error() != "enter a listed api flavor number" {
		t.Fatalf("invalid selection error = %v", err)
	}
	commands.terminal.readField = func(context.Context, string) (string, error) { return "", context.Canceled }
	if _, err := commands.readChoice(context.Background(), "Auth method", []string{"api_key", "oidc", "none"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestCollectProviderDefinitionBuildsOIDC(t *testing.T) {
	commands := testProviderCommands()
	commands.terminal.readField = providerInput(t, "https://gateway.example/v1", "1", "model-1", "2", "https://issuer.example", "client-id", "openid profile", "audience", "2", "/issuer-ca.pem", "1")
	got, err := commands.collectDefinition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := &permconfig.ProviderOIDC{Issuer: "https://issuer.example", ClientID: "client-id", Scopes: []string{"openid", "profile"}, ResourceAudience: "audience", IssuerTrust: permconfig.NativeTrust{Policy: "private-ca", CABundle: "/issuer-ca.pem"}, GatewayTrust: permconfig.NativeTrust{Policy: "public"}}
	if got.Auth.Method != providerAuthOIDC || !reflect.DeepEqual(got.Auth.OIDC, want) {
		t.Fatalf("OIDC definition = %#v, want %#v", got.Auth.OIDC, want)
	}
}

func TestProviderAddNoLoginSavesOnlyDefinitionAndPrintsCommand(t *testing.T) {
	commands := testProviderCommands()
	commands.terminal.readField = providerInput(t, "https://gateway.example", "1", "model-1", "1")
	var got permconfig.ProviderMapUpdate
	commands.backend = providerMapWriter("/safe/settings.yaml", func(_ context.Context, path string, update permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		if path != "/safe/settings.yaml" {
			t.Fatalf("writer path = %q", path)
		}
		got = update
		return authfile.CommitDurable, nil
	})
	var stdout, stderr bytes.Buffer
	res := invocationResolution{mode: modeProviderAdd, providerName: "custom", remaining: []string{"--no-login"}}
	if err := commands.runAdd(context.Background(), res, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "custom" || got.Definition == nil || got.Definition.Auth.Method != providerAuthAPIKey || !got.ExpectedAbsent {
		t.Fatalf("writer update = %#v", got)
	}
	const want = "Provider definition saved for \"custom\"\nNext login command: mecatui providers login custom\n"
	if stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("output = stdout %q stderr %q, want %q", stdout.String(), stderr.String(), want)
	}
}

func TestProviderAddRejectsInvalidProviderNameBeforeCollectingDefinition(t *testing.T) {
	commands := testProviderCommands()
	commands.terminal.readField = func(context.Context, string) (string, error) {
		t.Fatal("readField should not be called for an invalid provider name")
		return "", nil
	}
	wrote := false
	commands.backend.updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		wrote = true
		return authfile.CommitDurable, nil
	}
	var stdout, stderr bytes.Buffer
	res := invocationResolution{mode: modeProviderAdd, providerName: "MYPROVIDER", remaining: []string{"--no-login"}}
	err := commands.runAdd(context.Background(), res, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "lowercase letter") {
		t.Fatalf("runAdd error = %v, want a lowercase-letter validation error", err)
	}
	if wrote {
		t.Fatal("runAdd wrote a provider definition for an invalid name")
	}
}

func TestProviderAddChainsToSelectedLogin(t *testing.T) {
	commands := testProviderCommands()
	commands.terminal.readField = providerInput(t, "https://gateway.example", "1", "model-1", "1", "yes")
	commands.terminal.readAPIKey = func(context.Context, string) (string, error) { return "secret", nil }
	commands.backend.settingsPath = func() string { return "/safe/settings.yaml" }
	commands.backend.updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		return authfile.CommitDurable, nil
	}
	commands.backend.loadCredentials = providerCredentialConfigLoader(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}, authPath: "/safe/auth.yaml"})
	calls := 0
	commands.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		calls++
		return authfile.CommitDurable, nil
	}
	var stdout, stderr bytes.Buffer
	if err := commands.runAdd(context.Background(), invocationResolution{mode: modeProviderAdd, providerName: "custom"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || stdout.String() != "Provider definition saved for \"custom\"\nAPI key saved for provider \"custom\"\n" || !strings.Contains(stderr.String(), "plaintext") {
		t.Fatalf("login chain calls=%d stdout=%q stderr=%q", calls, stdout.String(), stderr.String())
	}
}

func TestProviderAddChainsOIDCLoginAfterSavingCompleteConfiguration(t *testing.T) {
	commands := testProviderCommands()
	commands.terminal.readField = providerInput(t, "https://gateway.example", "1", "model-1", "2", "https://issuer.example", "client-id", "openid profile", "audience", "1", "1")
	var saved permconfig.ProviderMapUpdate
	commands.backend.settingsPath = func() string { return "/safe/settings.yaml" }
	commands.backend.updateProviderMap = func(_ context.Context, _ string, update permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		saved = update
		return authfile.CommitDurable, nil
	}
	definition := permconfig.ProviderDefinition{Auth: permconfig.ProviderAuth{Method: providerAuthOIDC, OIDC: &permconfig.ProviderOIDC{CredentialStore: &permconfig.OIDCCredentialStore{Home: "/safe/oidc", Key: permconfig.NativeCredentialKey{Source: "keyring"}}}}}
	commands.backend.loadCredentials = providerCredentialConfigLoader(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": definition}})
	runtime := &providerAddOIDCRuntime{}
	commands.backend.openOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
		return runtime, nil
	}
	var stdout, stderr bytes.Buffer
	if err := commands.runAdd(context.Background(), invocationResolution{mode: modeProviderAdd, providerName: "custom"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if saved.OIDCCredentialStore == nil || saved.OIDCCredentialStore.Key.Source != "keyring" || !saved.ExpectedAbsent || !saved.ExpectedOIDCCredentialStoreAbsent || !runtime.loggedIn {
		t.Fatalf("saved update = %#v, OIDC login=%v", saved, runtime.loggedIn)
	}
}

func TestProviderAddCancellationDoesNotWrite(t *testing.T) {
	commands := testProviderCommands()
	commands.terminal.readField = func(context.Context, string) (string, error) { return "", context.Canceled }
	commands.backend.updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		t.Fatal("cancelled add must not write")
		return authfile.CommitNotApplied, nil
	}
	var stdout, stderr bytes.Buffer
	err := commands.runAdd(context.Background(), invocationResolution{mode: modeProviderAdd, providerName: "custom"}, &stdout, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || stdout.Len() != 0 || stderr.String() != "Cancelled; no changes made.\n" {
		t.Fatalf("cancellation err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

type providerAddOIDCRuntime struct{ loggedIn bool }

func (r *providerAddOIDCRuntime) Login(context.Context) error { r.loggedIn = true; return nil }
func (*providerAddOIDCRuntime) Status(context.Context) llmendpoint.Status {
	return llmendpoint.StatusUsable
}
func (*providerAddOIDCRuntime) Logout(context.Context) error { return nil }
func (*providerAddOIDCRuntime) Close() error                 { return nil }
