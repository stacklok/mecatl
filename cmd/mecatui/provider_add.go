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
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
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
	definition, err := collectProviderDefinition()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return providerCredentialCancellation(stderr)
		}
		return fmt.Errorf("providers add: collect definition: %w", err)
	}

	state, err := updateProviderMap(context.Background(), providerSettingsPath(), permconfig.ProviderMapUpdate{
		Provider:   res.llmEndpoint,
		Definition: &definition,
	})
	if err != nil {
		return fmt.Errorf("providers add: save provider definition: %w", err)
	}
	if state == authfile.CommitNotApplied {
		return errors.New("providers add: provider definition was not saved")
	}

	if len(res.remaining) == 1 && res.remaining[0] == "--no-login" {
		if definition.Auth.Method == "none" {
			_, err = fmt.Fprintf(stdout, "Provider definition saved for %q\n", res.llmEndpoint)
		} else {
			_, err = fmt.Fprintf(stdout, "Provider definition saved for %q\nNext login command: mecatui providers login %s\n", res.llmEndpoint, res.llmEndpoint)
		}
		return err
	}

	if _, err := fmt.Fprintf(stdout, "Provider definition saved for %q\n", res.llmEndpoint); err != nil {
		return err
	}
	if definition.Auth.Method == "none" {
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
	flavor, err := readProviderAddField("API flavor (openai-responses, openai-chat-completions, anthropic-messages)")
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}
	model, err := readProviderAddField("Default model")
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}
	method, err := readProviderAddField("Auth method (api_key, oidc, none)")
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
	policy, err := readProviderAddField(name + " trust policy (public, private-ca)")
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
