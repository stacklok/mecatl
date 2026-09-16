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
	"time"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

const (
	providerAuthAPIKey  = "api_key"
	providerAuthOIDC    = "oidc"
	providerAuthNone    = "none"
	providerRollbackMax = 5 * time.Second
)

func defaultProviderSettingsPath() string {
	return filepath.Join(xdgconfig.UserConfigDir(xdgconfig.OSEnv), permconfig.UserSettingsRelPath)
}

func readProviderFieldFromTerminal(ctx context.Context, prompt string) (string, error) {
	return readProviderField(ctx, bufio.NewReader(os.Stdin), prompt)
}

func readProviderField(ctx context.Context, input *bufio.Reader, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := fmt.Fprint(os.Stderr, prompt+": "); err != nil {
		return "", providerTerminalError("could not write terminal prompt")
	}
	value, err := input.ReadString('\n')
	if errors.Is(err, io.EOF) {
		err = context.Canceled
	} else if err != nil {
		err = providerTerminalError("could not read terminal input")
	}
	return strings.TrimSpace(value), err
}

func (c providerCommands) runAdd(ctx context.Context, res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		return providerHelpResult(stderr, providerActionAdd)
	}
	inspection, err := c.inspectForEnrollment()
	if err != nil {
		return fmt.Errorf("providers add: inspect configured providers: %w", err)
	}
	if _, exists := inspection.definitions[res.providerName]; exists {
		return fmt.Errorf("providers add: provider %q is already configured; use `mecatui providers login %s` to replace its credential", res.providerName, res.providerName)
	}
	definition, err := c.collectDefinition(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return providerCredentialCancellation(stderr)
		}
		return fmt.Errorf("providers add: collect definition: %w", err)
	}

	store := defaultProviderOIDCCredentialStore(definition)
	state, err := c.backend.updateProviderMap(ctx, c.backend.settingsPath(), permconfig.ProviderMapUpdate{
		Provider:                          res.providerName,
		Definition:                        &definition,
		ExpectedAbsent:                    true,
		OIDCCredentialStore:               store,
		ExpectedOIDCCredentialStoreAbsent: store != nil && !inspection.oidcStoreConfigured,
	})
	if err != nil {
		if state == authfile.CommitNotApplied && errors.Is(err, context.Canceled) {
			return providerCredentialCancellation(stderr)
		}
		return fmt.Errorf("providers add: save provider definition: %w", err)
	}
	if state == authfile.CommitNotApplied {
		return errors.New("providers add: provider definition was not saved")
	}

	if len(res.remaining) == 1 && res.remaining[0] == "--no-login" {
		if definition.Auth.Method == providerAuthNone {
			_, err = fmt.Fprintf(stdout, "Provider definition saved for %q\n", res.providerName)
		} else {
			_, err = fmt.Fprintf(stdout, "Provider definition saved for %q\nNext login command: mecatui providers login %s\n", res.providerName, res.providerName)
		}
		return err
	}

	if definition.Auth.Method == providerAuthNone {
		_, err = fmt.Fprintf(stdout, "Provider definition saved for %q\n", res.providerName)
		return err
	}
	return c.finishProviderAdd(ctx, res.providerName, definition, store, inspection, stdout, stderr)
}

func (c providerCommands) finishProviderAdd(ctx context.Context, provider string, definition permconfig.ProviderDefinition, insertedStore *permconfig.OIDCCredentialStore, inspection providerInspection, stdout, stderr io.Writer) error {
	login := invocationResolution{mode: modeProviderCredential, providerAction: providerActionLogin, providerName: provider}
	var loginOut bytes.Buffer
	c.deferCredentialCancellation = true
	err := c.runCredential(ctx, login, &loginOut, stderr)
	if errors.Is(err, errProviderCredentialCancelled) {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerRollbackMax)
		defer cancel()
		var removeStore *permconfig.OIDCCredentialStore
		if definition.Auth.Method == providerAuthOIDC && !inspection.oidcStoreConfigured {
			removeStore = insertedStore
		}
		rollbackState, rollbackErr := c.backend.updateProviderMap(rollbackCtx, c.backend.settingsPath(), permconfig.ProviderMapUpdate{
			Provider:                  provider,
			ExpectedDefinition:        &definition,
			RemoveOIDCCredentialStore: removeStore,
		})
		if rollbackErr != nil || rollbackState == authfile.CommitNotApplied {
			return fmt.Errorf("providers add: login cancelled; provider definition %q may remain because rollback failed: %w", provider, errors.Join(rollbackErr, errors.New("definition retention is uncertain")))
		}
		if definition.Auth.Method == providerAuthOIDC {
			if _, writeErr := fmt.Fprintln(stderr, "Provider definition restored; the OIDC state described above was not rolled back."); writeErr != nil {
				return writeErr
			}
			return errProviderCredentialCancelled
		}
		return providerCredentialCancellation(stderr)
	}
	if _, writeErr := fmt.Fprintf(stdout, "Provider definition saved for %q\n", provider); writeErr != nil {
		return writeErr
	}
	if _, writeErr := io.Copy(stdout, &loginOut); writeErr != nil {
		return writeErr
	}
	return err
}

func (c providerCommands) collectDefinition(ctx context.Context) (permconfig.ProviderDefinition, error) {
	baseURL, err := c.terminal.readField(ctx, "Base HTTPS URL")
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}
	flavor, err := c.readChoice(ctx, "API flavor", []string{"openai-responses", "openai-chat-completions", "anthropic-messages"})
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}
	model, err := c.terminal.readField(ctx, "Default model")
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}
	method, err := c.readChoice(ctx, "Auth method", []string{providerAuthAPIKey, "oidc", providerAuthNone})
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}

	definition := permconfig.ProviderDefinition{BaseURL: baseURL, APIFlavor: flavor, DefaultModel: model, Auth: permconfig.ProviderAuth{Method: method}}
	if method != "oidc" {
		return definition, nil
	}
	oidc, err := c.collectOIDC(ctx)
	if err != nil {
		return permconfig.ProviderDefinition{}, err
	}
	definition.Auth.OIDC = &oidc
	return definition, nil
}

func (c providerCommands) readChoice(ctx context.Context, label string, choices []string) (string, error) {
	var prompt strings.Builder
	prompt.WriteString(label)
	prompt.WriteString(":\n")
	for i, choice := range choices {
		fmt.Fprintf(&prompt, "  %d. %s\n", i+1, choice)
	}
	prompt.WriteString("Selection")

	selected, err := c.terminal.readField(ctx, prompt.String())
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

func (c providerCommands) collectOIDC(ctx context.Context) (permconfig.ProviderOIDC, error) {
	issuer, err := c.terminal.readField(ctx, "OIDC issuer")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	clientID, err := c.terminal.readField(ctx, "OIDC client ID")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	scopes, err := c.terminal.readField(ctx, "OIDC scopes (space-separated)")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	audience, err := c.terminal.readField(ctx, "OIDC audience (optional)")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	issuerTrust, err := c.collectTrust(ctx, "Issuer")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	gatewayTrust, err := c.collectTrust(ctx, "Gateway")
	if err != nil {
		return permconfig.ProviderOIDC{}, err
	}
	return permconfig.ProviderOIDC{Issuer: issuer, ClientID: clientID, Scopes: strings.Fields(scopes), ResourceAudience: audience, IssuerTrust: issuerTrust, GatewayTrust: gatewayTrust}, nil
}

func (c providerCommands) collectTrust(ctx context.Context, name string) (permconfig.NativeTrust, error) {
	policy, err := c.readChoice(ctx, name+" trust policy", []string{"public", "private-ca"})
	if err != nil {
		return permconfig.NativeTrust{}, err
	}
	trust := permconfig.NativeTrust{Policy: policy}
	if policy != "private-ca" {
		return trust, nil
	}
	bundle, err := c.terminal.readField(ctx, name+" CA bundle")
	if err != nil {
		return permconfig.NativeTrust{}, err
	}
	trust.CABundle = bundle
	return trust, nil
}
