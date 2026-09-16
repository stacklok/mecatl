package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestProviderTerminalReadsPropagateAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readProviderFieldFromTerminal(ctx, "field"); !errors.Is(err, context.Canceled) {
		t.Fatalf("field read error = %v", err)
	}
	if _, err := readHiddenProviderAPIKey(ctx, "provider"); !errors.Is(err, context.Canceled) {
		t.Fatalf("API-key read error = %v", err)
	}
}

func TestProviderFieldReaderRetainsBufferedLines(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("first\nsecond\n"))
	for _, want := range []string{"first", "second"} {
		got, err := readProviderField(context.Background(), input, "field")
		if err != nil || got != want {
			t.Fatalf("read field = %q, %v; want %q, nil", got, err, want)
		}
	}
}

func TestProviderCommandsOIDCLoginPreparesAbsentPrivateRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing", "oidc")
	definition := providerOIDCTestDefinition(root)
	commands := newProviderCommands()
	commands.backend.loadCredentials = providerCredentialConfigLoader(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": definition}})
	opened := false
	commands.backend.openOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("root not prepared: info=%v err=%v", info, err)
		}
		opened = true
		return &statusProviderOIDCRuntime{}, nil
	}
	if err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogin, "custom"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("OIDC runtime not opened")
	}
}

func TestProviderCommandsPassiveOIDCStatusDoesNotCreateRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing", "oidc")
	commands := newProviderCommands()
	commands.backend.openOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
		return &statusProviderOIDCRuntime{}, nil
	}
	commands.backend.prepareOIDCRoot = func(string) error { t.Fatal("passive status prepared root"); return nil }
	commands.oidcStatus(context.Background(), providerOIDCTestDefinition(root))
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("passive status created root: %v", err)
	}
}

func TestProviderAddCancelledLoginRollbackSuccess(t *testing.T) {
	commands := cancelledAddCommands(t)
	writes := 0
	commands.backend.updateProviderMap = func(_ context.Context, _ string, update permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		writes++
		if writes == 2 && (update.Provider != "custom" || update.Definition != nil || update.ExpectedDefinition == nil || update.ExpectedDefinition.Auth.Method != providerAuthAPIKey) {
			t.Fatalf("rollback update = %#v, want custom-provider removal", update)
		}
		return authfile.CommitDurable, nil
	}
	var stdout, stderr bytes.Buffer
	err := commands.runAdd(context.Background(), invocationResolution{mode: modeProviderAdd, providerName: "custom"}, &stdout, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || writes != 2 || stdout.Len() != 0 || !strings.HasSuffix(stderr.String(), "Cancelled; no changes made.\n") {
		t.Fatalf("err=%v writes=%d stdout=%q stderr=%q", err, writes, stdout.String(), stderr.String())
	}
}

func TestProviderAddCancelledLoginRollbackFailureIsTruthful(t *testing.T) {
	commands := cancelledAddCommands(t)
	writes := 0
	commands.backend.updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		writes++
		if writes == 1 {
			return authfile.CommitDurable, nil
		}
		return authfile.CommitNotApplied, errors.New("rollback unavailable")
	}
	var stdout, stderr bytes.Buffer
	err := commands.runAdd(context.Background(), invocationResolution{mode: modeProviderAdd, providerName: "custom"}, &stdout, &stderr)
	if err == nil || errors.Is(err, errProviderCredentialCancelled) || !strings.Contains(err.Error(), `provider definition "custom" may remain`) {
		t.Fatalf("rollback failure = %v", err)
	}
	if stdout.Len() != 0 || strings.Contains(stderr.String(), "Cancelled; no changes made.") {
		t.Fatalf("false cancellation claim: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func cancelledAddCommands(t *testing.T) providerCommands {
	commands := testProviderCommands()
	commands.terminal.readField = providerInput(t, "https://gateway.example", "1", "model", "1")
	commands.terminal.readAPIKey = func(context.Context, string) (string, error) { return "", context.Canceled }
	commands.backend.settingsPath = func() string { return "/settings.yaml" }
	commands.backend.loadCredentials = providerCredentialConfigLoader(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}})
	return commands
}

func TestProviderAddRejectsExistingDefinitionBeforePromptOrWrite(t *testing.T) {
	commands := testProviderCommands()
	commands.backend.inspect = providerInspectionLoader(providerInspection{definitions: permconfig.ProviderDefinitions{
		"custom": {ID: "custom", Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}},
	}})
	commands.terminal.readField = func(context.Context, string) (string, error) {
		t.Fatal("existing provider must not prompt")
		return "", nil
	}
	commands.backend.updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		t.Fatal("existing provider must not be overwritten")
		return authfile.CommitNotApplied, nil
	}
	err := commands.runAdd(context.Background(), invocationResolution{mode: modeProviderAdd, providerName: "custom"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already configured") {
		t.Fatalf("existing provider error = %v", err)
	}
}

func providerOIDCTestDefinition(root string) permconfig.ProviderDefinition {
	return permconfig.ProviderDefinition{ID: "custom", BaseURL: "https://gateway.example", Auth: permconfig.ProviderAuth{Method: providerAuthOIDC, OIDC: &permconfig.ProviderOIDC{Issuer: "https://issuer.example", ClientID: "client", Scopes: []string{"openid"}, IssuerTrust: permconfig.NativeTrust{Policy: "public"}, GatewayTrust: permconfig.NativeTrust{Policy: "public"}, CredentialStore: &permconfig.OIDCCredentialStore{Home: root, Key: permconfig.NativeCredentialKey{Source: "keyring"}}}}}
}
