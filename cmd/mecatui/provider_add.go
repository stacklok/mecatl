package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

const (
	providerAuthOIDC = "oidc"
	providerAuthNone = "none"
)

var (
	readProviderAddField = readProviderAddFieldFromTerminal
	providerSettingsPath = defaultProviderSettingsPath
	updateProviderMap    = permconfig.UpdateProviderMap
)

func defaultProviderSettingsPath() string {
	return filepath.Join(xdgconfig.UserConfigDir(xdgconfig.OSEnv), permconfig.UserSettingsRelPath)
}

func readProviderAddFieldFromTerminal(prompt string) (string, error) {
	if _, err := fmt.Fprint(os.Stderr, prompt+": "); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if errors.Is(err, io.EOF) && line == "" {
		return "", context.Canceled
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func runProviderAddCommand(res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		return providerHelpResult(stderr, providerActionAdd)
	}
	definition, err := collectProviderDefinition()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return providerCredentialCancellation(stderr)
		}
		return fmt.Errorf("providers add: collect definition: %w", err)
	}

	state, err := updateProviderMap(context.Background(), providerSettingsPath(), permconfig.ProviderMapUpdate{
		Provider:            res.llmEndpoint,
		Definition:          &definition,
		OIDCCredentialStore: defaultProviderOIDCCredentialStore(definition),
	})
	if err != nil {
		return fmt.Errorf("providers add: save provider definition: %w", err)
	}
	if state == authfile.CommitNotApplied {
		return errors.New("providers add: provider definition was not saved")
	}

	if len(res.remaining) == 1 && res.remaining[0] == "--no-login" {
		if definition.Auth.Method == providerAuthNone {
			_, err = fmt.Fprintf(stdout, "Provider definition saved for %q\n", res.llmEndpoint)
		} else {
			_, err = fmt.Fprintf(stdout, "Provider definition saved for %q\nNext login command: mecatui providers login %s\n", res.llmEndpoint, res.llmEndpoint)
		}
		return err
	}

	if _, err := fmt.Fprintf(stdout, "Provider definition saved for %q\n", res.llmEndpoint); err != nil {
		return err
	}
	if definition.Auth.Method == providerAuthNone {
		return nil
	}
	login := invocationResolution{mode: modeProviderCredential, llmAction: providerActionLogin, llmEndpoint: res.llmEndpoint}
	var loginOut, loginErr bytes.Buffer
	err = runProviderCredentialCommand(login, &loginOut, &loginErr)
	if errors.Is(err, errProviderCredentialCancelled) {
		_, writeErr := fmt.Fprintf(stderr, "Login cancelled; provider definition saved for %q.\n", res.llmEndpoint)
		return writeErr
	}
	if _, writeErr := io.Copy(stdout, &loginOut); writeErr != nil {
		return writeErr
	}
	if _, writeErr := io.Copy(stderr, &loginErr); writeErr != nil {
		return writeErr
	}
	return err
}

func collectProviderDefinition() (permconfig.ProviderDefinition, error) {
	baseURL, err := readProviderAddField("Base HTTPS URL")
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}
	flavor, err := readProviderAddChoice("API flavor", []string{"openai-responses", "openai-chat-completions", "anthropic-messages"})
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}
	model, err := readProviderAddField("Default model")
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}
	method, err := readProviderAddChoice("Auth method", []string{"api_key", "oidc", providerAuthNone})
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}

	definition := permconfig.ProviderDefinition{BaseURL: baseURL, APIFlavor: flavor, DefaultModel: model, Auth: permconfig.ProviderAuth{Method: method}}
	if method != "oidc" {
		return definition, nil
	}
	oidc, err := collectProviderOIDC()
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}
	definition.Auth.OIDC = &oidc
	return definition, nil
}

func readProviderAddChoice(label string, choices []string) (string, error) {
	var prompt strings.Builder
	prompt.WriteString(label)
	prompt.WriteString(":\n")
	for i, choice := range choices {
		fmt.Fprintf(&prompt, "  %d. %s\n", i+1, choice)
	}
	prompt.WriteString("Selection")

	selected, err := readProviderAddField(prompt.String())
	if err != nil {
		return "", err
	}
	index, err := strconv.Atoi(strings.TrimSpace(selected))
	if err != nil || index < 1 || index > len(choices) {
		return "", fmt.Errorf("enter a listed %s number", strings.ToLower(label))
	}
	return choices[index-1], nil
}

func defaultProviderOIDCCredentialStore(definition permconfig.ProviderDefinition) *permconfig.OIDCCredentialStore {
	if definition.Auth.Method != providerAuthOIDC {
		return nil
	}
	base := xdgconfig.UserStateDir(xdgconfig.OSEnv)
	if !filepath.IsAbs(base) {
		return nil
	}
	return &permconfig.OIDCCredentialStore{
		Home: filepath.Join(base, "mecatl", "provider-oidc"),
		Key:  permconfig.NativeCredentialKey{Source: "keyring"},
	}
}

func collectProviderOIDC() (permconfig.ProviderOIDC, error) {
	issuer, err := readProviderAddField("OIDC issuer")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	clientID, err := readProviderAddField("OIDC client ID")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	scopes, err := readProviderAddField("OIDC scopes (space-separated)")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	audience, err := readProviderAddField("OIDC audience (optional)")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	issuerTrust, err := collectProviderTrust("Issuer")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	gatewayTrust, err := collectProviderTrust("Gateway")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	return permconfig.ProviderOIDC{Issuer: issuer, ClientID: clientID, Scopes: strings.Fields(scopes), ResourceAudience: audience, IssuerTrust: issuerTrust, GatewayTrust: gatewayTrust}, nil
}

func collectProviderTrust(name string) (permconfig.NativeTrust, error) {
	policy, err := readProviderAddChoice(name+" trust policy", []string{"public", "private-ca"})
	if err != nil {
		return permconfig.NativeTrust{}, err
	}
	trust := permconfig.NativeTrust{Policy: policy}
	if policy != "private-ca" {
		return trust, nil
	}
	bundle, err := readProviderAddField(name + " CA bundle")
	if err != nil {
		return permconfig.NativeTrust{}, err
	}
	trust.CABundle = bundle
	return trust, nil
}
